// Command k6fixture prepares approved migration requests for k6 runs through
// the public API, and resets the tables a run created on a disposable target.
//
//	K6_EDITOR=editor:pass K6_LEADERS=lead1:pass,lead2:pass \
//	  go run ./cmd/k6fixture prepare -url http://localhost:8080 -databases ID1,ID2 -per-database 1 -run r1 > fixtures.json
//	go run ./cmd/k6fixture reset -target-url postgres://... -run r1
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sqlwise/internal/jobs/k6fixture"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "k6fixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: k6fixture prepare|reset [flags]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	switch args[0] {
	case "prepare":
		fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
		base := fs.String("url", "http://localhost:8080", "sqlwise base URL")
		databases := fs.String("databases", "", "comma-separated registered database IDs")
		per := fs.Int("per-database", 1, "approved requests per database at the current head")
		runID := fs.String("run", "", "unique run name (lowercase letters and digits)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		// Credentials come from the environment so they never appear in process listings.
		editor, err := account(os.Getenv("K6_EDITOR"))
		if err != nil {
			return fmt.Errorf("K6_EDITOR: %w", err)
		}
		var leaders []k6fixture.Account
		for _, raw := range strings.Split(os.Getenv("K6_LEADERS"), ",") {
			a, err := account(raw)
			if err != nil {
				return fmt.Errorf("K6_LEADERS: %w", err)
			}
			leaders = append(leaders, a)
		}
		fixtures, err := k6fixture.Prepare(ctx, k6fixture.Config{BaseURL: *base, Editor: editor, Leaders: leaders,
			Databases: split(*databases), PerDatabase: *per, Run: *runID})
		if err != nil {
			return err
		}
		out := json.NewEncoder(os.Stdout)
		out.SetIndent("", "  ")
		return out.Encode(fixtures)
	case "reset":
		fs := flag.NewFlagSet("reset", flag.ContinueOnError)
		target := fs.String("target-url", os.Getenv("K6_TARGET_URL"), "disposable target database URL")
		runID := fs.String("run", "", "run to clean up; empty drops every k6_ table")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *target == "" {
			return errors.New("-target-url or K6_TARGET_URL is required")
		}
		n, err := k6fixture.Reset(ctx, *target, *runID)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "dropped %d k6 tables\n", n)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func account(raw string) (k6fixture.Account, error) {
	user, pass, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || user == "" || pass == "" {
		return k6fixture.Account{}, errors.New("expected username:password")
	}
	return k6fixture.Account{Username: user, Password: pass}, nil
}

func split(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
