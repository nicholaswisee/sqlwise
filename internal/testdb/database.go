package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const packagePrefix = "sqlwise_test_"

var shared struct {
	once sync.Once
	dsn  string
	err  error
	keep *pgx.Conn
}

// packageDatabase creates one database per test binary, so advisory locks and
// server-wide waits of concurrently running packages cannot interfere. An
// open connection marks it in use until the binary exits; later runs drop
// idle databases created more than two minutes earlier.
func packageDatabase(dsn string) (string, error) {
	shared.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			shared.err = fmt.Errorf("connect to TEST_DATABASE_URL: %w", err)
			return
		}
		defer admin.Close(context.Background())
		dropStale(ctx, admin)
		var suffix [6]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			shared.err = err
			return
		}
		name := packagePrefix + strconv.FormatInt(time.Now().Unix(), 10) + "_" + hex.EncodeToString(suffix[:])
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			shared.err = fmt.Errorf("create package test database (needs CREATEDB): %w", err)
			return
		}
		if shared.dsn, shared.err = withDatabase(dsn, name); shared.err != nil {
			return
		}
		shared.keep, shared.err = pgx.Connect(ctx, shared.dsn)
	})
	return shared.dsn, shared.err
}

func dropStale(ctx context.Context, admin *pgx.Conn) {
	rows, err := admin.Query(ctx, `SELECT d.datname FROM pg_database d
		WHERE d.datname LIKE 'sqlwise\_test\_%' AND NOT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datname = d.datname)`)
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-2 * time.Minute).Unix()
	for _, name := range names {
		created, _, ok := strings.Cut(strings.TrimPrefix(name, packagePrefix), "_")
		if seconds, err := strconv.ParseInt(created, 10, 64); ok && err == nil && seconds < cutoff {
			_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize())
		}
	}
}

func withDatabase(dsn, name string) (string, error) {
	if parsed, err := url.Parse(dsn); err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		parsed.Path = "/" + name
		return parsed.String(), nil
	}
	if strings.Contains(dsn, "://") {
		return "", fmt.Errorf("TEST_DATABASE_URL is not a valid PostgreSQL URL")
	}
	return dsn + " dbname=" + name, nil
}

// Open isolates each test in its own schema of the package test database on
// an explicitly selected test server.
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	dsn, err := packageDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal("could not create test database pool")
	}
	t.Cleanup(admin.Close)
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	schema := "auth_test_" + hex.EncodeToString(id[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("could not create test schema")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("could not drop isolated test schema")
		}
	})
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("could not open isolated test pool")
	}
	t.Cleanup(pool.Close)
	return pool
}
