// Package migrationtest provides disposable PostgreSQL platform and target
// fixtures for migration and schema diff integration tests.
package migrationtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/pgtarget"
	"sqlwise/internal/testdb"
)

const Password = "migration fixture passphrase"

type Platform struct {
	Pool     *pgxpool.Pool
	Sessions *auth.Service
	Access   *access.Service
}

// NewPlatform applies the auth, access and migration schemas in an isolated schema.
func NewPlatform(t *testing.T) *Platform {
	t.Helper()
	pool := testdb.Open(t)
	store := auth.NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := access.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := migration.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return &Platform{Pool: pool, Sessions: sessions, Access: access.NewService(access.NewStore(pool), sessions)}
}

func (p *Platform) Exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := p.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Database registers an active target identity.
func (p *Platform) Database(t *testing.T, id, name string) {
	t.Helper()
	p.Exec(t, `INSERT INTO database_registrations (id, name, active) VALUES ($1, $2, true)`, id, name)
}

type User struct {
	ID       string
	Username string
	Token    string
	CSRF     string
	Ctx      context.Context
}

// User creates a regular user whose own group holds grants ("view"/"edit" per
// database), then signs in and binds the session to a context.
func (p *Platform) User(t *testing.T, username string, grants map[string]string) User {
	t.Helper()
	hash, err := auth.HashPassword(Password)
	if err != nil {
		t.Fatal(err)
	}
	id := "user-" + username
	p.Exec(t, `INSERT INTO users (id, username, password_hash) VALUES ($1, $2, $3)`, id, username, hash)
	if len(grants) > 0 {
		p.Exec(t, `INSERT INTO groups (id, name) VALUES ($1, $1)`, "group-"+username)
		p.Exec(t, `INSERT INTO group_memberships (user_id, group_id) VALUES ($1, $2)`, id, "group-"+username)
		for database, permission := range grants {
			p.Exec(t, `INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ($1, $2, $3)`, "group-"+username, database, permission)
		}
	}
	return p.Login(t, username)
}

func (p *Platform) Login(t *testing.T, username string) User {
	t.Helper()
	credentials, err := auth.ParseCredentials(username, Password)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := p.Sessions.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := p.Sessions.Login(t.Context(), auth.LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	session, err := p.Sessions.Session(t.Context(), issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := p.Sessions.BindSession(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	return User{ID: session.Principal.UserID, Username: username, Token: issued.Token, CSRF: session.CSRFToken, Ctx: ctx}
}

// TargetDatabase creates a fresh database on the TEST_DATABASE_URL server and
// drops it after the test, so receipts and user schemas never collide.
func TargetDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to test server")
	}
	defer admin.Close(context.Background())
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "sqlwise_target_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create target database (needs CREATEDB): %v", err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	config.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanup, dsn)
		if err != nil {
			t.Error("could not reconnect to drop target database")
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop target database: %v", err)
		}
	})
	return pool
}

// Targets maps registration IDs to target pools; it is the pgtarget stand-in
// for the database adapter, with a Commit hook and an Open counter.
type Targets = pgtarget.Registry

func NewTargets() *Targets { return pgtarget.NewRegistry() }
