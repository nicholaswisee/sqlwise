package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	username := flag.String("username", "", "Username for the first platform administrator (3-64 letters, digits, _, . or -)")
	stdin := flag.Bool("password-stdin", false, "Read the password from standard input instead of a hidden terminal prompt")
	ifNotExists := flag.Bool("if-not-exists", false, "Succeed without changing credentials if a platform administrator already exists")
	schemaOnly := flag.Bool("schema-only", false, "Initialize authentication, access, migration, and connection schemas without creating an administrator")
	flag.Parse()
	var usernameSet, stdinSet, ifNotExistsSet bool
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "username":
			usernameSet = true
		case "password-stdin":
			stdinSet = true
		case "if-not-exists":
			ifNotExistsSet = true
		}
	})
	if *schemaOnly {
		if usernameSet || stdinSet || ifNotExistsSet || flag.NArg() != 0 {
			return errors.New("-schema-only cannot be combined with -username, -password-stdin, -if-not-exists, or positional arguments")
		}
	} else if *username == "" || flag.NArg() != 0 {
		return errors.New("use -username NAME; use -help for usage")
	}
	var credentials auth.Credentials
	if !*schemaOnly {
		var password []byte
		var err error
		if *stdin {
			password, err = io.ReadAll(io.LimitReader(os.Stdin, 131))
			password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
		} else {
			fmt.Fprint(os.Stderr, "Password (12+ characters, at most 128 bytes): ")
			password, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
		}
		if err != nil {
			return errors.New("could not read password; use a terminal or -password-stdin")
		}
		defer clear(password)
		credentials, err = auth.ParseCredentials(*username, string(password))
		if err != nil {
			return auth.ErrInvalidInput
		}
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	store := auth.NewStore(pool)
	if err := store.ApplySchema(ctx); err != nil {
		return errors.New("could not initialize authentication schema; check database connectivity and permissions")
	}
	if err := access.NewStore(pool).ApplySchema(ctx); err != nil {
		return errors.New("could not initialize access schema; check database connectivity and permissions")
	}
	if err := migration.NewStore(pool).ApplySchema(ctx); err != nil {
		return errors.New("could not initialize migration schema; check database connectivity and permissions")
	}
	if err := database.NewStore(pool, nil).ApplySchema(ctx); err != nil {
		return errors.New("could not initialize connection schema; check database connectivity and permissions")
	}
	if *schemaOnly {
		fmt.Fprintln(os.Stdout, "Authentication, access, migration, and connection schemas initialized.")
		return nil
	}
	if err := store.BootstrapAdmin(ctx, credentials); err != nil {
		if *ifNotExists && errors.Is(err, auth.ErrAdminExists) {
			fmt.Fprintln(os.Stdout, "A platform administrator already exists; credentials unchanged.")
			return nil
		}
		if errors.Is(err, auth.ErrAdminExists) || errors.Is(err, auth.ErrInvalidInput) {
			return err
		}
		return errors.New("could not create administrator; check database availability and username uniqueness")
	}
	fmt.Fprintln(os.Stdout, "Platform administrator created.")
	return nil
}
