package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
)

func TestConnectAuthRejectsPreW2AuthSchema(t *testing.T) {
	pool := testdb.Open(t)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE auth_sessions (token_hash bytea)`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", isolatedDatabaseURL(t, pool))

	service, connectedPool, err := connectAuth()
	if service != nil || connectedPool != nil || err == nil {
		if connectedPool != nil {
			connectedPool.Close()
		}
		t.Fatalf("connectAuth() = (%v, %v, %v), want schema rejection without service or pool", service, connectedPool, err)
	}
	if !strings.Contains(err.Error(), "go run ./cmd/admin -schema-only") {
		t.Fatalf("schema error does not explain repair command: %v", err)
	}
}

func TestConnectAuthAcceptsCurrentAuthAndAccessSchema(t *testing.T) {
	pool := testdb.Open(t)
	if err := auth.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := access.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", isolatedDatabaseURL(t, pool))

	service, connectedPool, err := connectAuth()
	if err != nil {
		t.Fatal("connectAuth:", err)
	}
	if service == nil || connectedPool == nil {
		t.Fatal("connectAuth returned nil service or pool")
	}
	connectedPool.Close()
}

func TestConnectAuthRejectsMissingAccessSchema(t *testing.T) {
	pool := testdb.Open(t)
	if err := auth.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", isolatedDatabaseURL(t, pool))
	service, connectedPool, err := connectAuth()
	if service != nil || connectedPool != nil || err == nil {
		if connectedPool != nil {
			connectedPool.Close()
		}
		t.Fatalf("connectAuth() = (%v, %v, %v), want access schema rejection", service, connectedPool, err)
	}
	if !strings.Contains(err.Error(), "access schema unavailable") {
		t.Fatalf("missing access schema error: %v", err)
	}
}

func isolatedDatabaseURL(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	parsed, err := url.Parse(pool.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal("parse test database URL")
	}
	query := parsed.Query()
	query.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
