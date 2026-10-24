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
	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/auth"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/pgtarget"
)

// Platform is Wise's access fixture (auth and access schemas, a platform
// admin) plus the migration schema. Users, groups and grants go through the
// access service, the same path as the admin UI.
type Platform struct {
	Pool     *pgxpool.Pool
	Sessions *auth.Service
	Access   *access.Service
	admin    context.Context
}

const password = "migration fixture passphrase"

func NewPlatform(t *testing.T) *Platform {
	t.Helper()
	f := testfixture.New(t)
	if err := migration.NewStore(f.Pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &Platform{Pool: f.Pool, Sessions: f.Auth, Access: f.Access, admin: f.Admin.Context}
}

func (p *Platform) Exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := p.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Database registers an active target identity without connection details;
// tests attach target pools through Targets.
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
	principal, err := p.Sessions.CreateUser(p.admin, credentials(t, username))
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) > 0 {
		group, err := p.Access.CreateGroup(p.admin, "Group "+username)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Access.AddMembership(p.admin, principal.UserID, group.ID); err != nil {
			t.Fatal(err)
		}
		for database, name := range grants {
			permission, err := access.ParsePermission(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Access.SetGrant(p.admin, group.ID, database, permission); err != nil {
				t.Fatal(err)
			}
		}
	}
	return p.Login(t, username)
}

// Login signs in again, issuing proof for the user's current policy epoch.
func (p *Platform) Login(t *testing.T, username string) User {
	t.Helper()
	challenge, err := p.Sessions.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := p.Sessions.Login(t.Context(), auth.LoginRequest{Credentials: credentials(t, username), ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
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

func credentials(t *testing.T, username string) auth.Credentials {
	t.Helper()
	value, err := auth.ParseCredentials(username, password)
	if err != nil {
		t.Fatal(err)
	}
	return value
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
