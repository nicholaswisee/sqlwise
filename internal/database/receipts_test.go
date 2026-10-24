package database_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type receiptTarget struct {
	exec  database.Executor
	begin database.TxBeginner
}

func (r receiptTarget) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	return r.exec.Exec(ctx, sql, args...)
}

func (r receiptTarget) QueryRow(ctx context.Context, sql string, args ...any) migration.Row {
	return r.exec.QueryRow(ctx, sql, args...)
}

func (r receiptTarget) Begin(ctx context.Context) (migration.TargetTx, error) {
	return r.begin.Begin(ctx)
}

func (r receiptTarget) Close() error { return nil }

func loginRole(t *testing.T) (string, string) {
	t.Helper()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	role, password := "sqlwise_receipt_"+hex.EncodeToString(suffix[:]), "receipt-"+hex.EncodeToString(suffix[:])
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("create role (needs CREATEROLE): %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanup, os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Error("could not reconnect to drop role")
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(cleanup, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Errorf("drop role: %v", err)
		}
	})
	return role, password
}

func permissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func TestReceiptSetupLimitsMigrationRole(t *testing.T) {
	f := newStoreFixture(t)
	role, password := loginRole(t)
	owner := migrationtest.TargetDatabase(t)
	cc := owner.Config().ConnConfig
	registration := f.register(t, "Restricted", database.Connection{DBMS: database.PostgreSQL, Host: cc.Host, Port: int(cc.Port), Database: cc.Database, Username: role, Password: password, TLS: database.TLSDisable})
	pools := database.NewPools(f.store)
	t.Cleanup(pools.Close)
	h, err := pools.Open(t.Context(), registration.ID)
	if err != nil {
		t.Fatal(err)
	}
	exec := must[database.Executor](t, h)
	target := receiptTarget{exec: exec, begin: must[database.TxBeginner](t, h)}

	if err := migration.ProvisionReceipts(t.Context(), target); !permissionDenied(err) {
		t.Fatalf("lazy provisioning without CREATE = %v", err)
	}
	if _, err := owner.Exec(t.Context(), database.ReceiptSetupSQL(role)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), database.ReceiptSetupSQL(role)); err != nil {
		t.Fatalf("setup is not repeatable: %v", err)
	}
	if err := migration.ProvisionReceipts(t.Context(), target); err != nil {
		t.Fatalf("provisioning after setup = %v", err)
	}

	tx, err := target.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO sqlwise_internal.migration_receipts (attempt_id, database_id, version, sql_sha256, outcome) VALUES ($1, $2, 1, $3, 'applied')`, "a1", registration.ID, migration.HashSQL("SELECT 1")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, err := exec.Exec(t.Context(), `INSERT INTO sqlwise_internal.migration_receipts (attempt_id, database_id, version, sql_sha256, outcome) VALUES ($1, $2, 1, $3, 'fenced') ON CONFLICT (attempt_id) DO NOTHING`, "a1", registration.ID, migration.HashSQL("SELECT 1")); err != nil || n != 0 {
		t.Fatalf("fence over applied receipt = %d, %v", n, err)
	}
	var outcome string
	if err := exec.QueryRow(t.Context(), `SELECT outcome FROM sqlwise_internal.migration_receipts WHERE attempt_id = $1`, "a1").Scan(&outcome); err != nil || outcome != "applied" {
		t.Fatalf("read receipt = %q, %v", outcome, err)
	}

	for _, sql := range []string{
		`UPDATE sqlwise_internal.migration_receipts SET outcome = 'fenced'`,
		`DELETE FROM sqlwise_internal.migration_receipts`,
		`TRUNCATE sqlwise_internal.migration_receipts`,
		`ALTER TABLE sqlwise_internal.migration_receipts ADD COLUMN forged int`,
		`DROP TABLE sqlwise_internal.migration_receipts`,
		`CREATE TABLE sqlwise_internal.forged (id int)`,
		`DROP SCHEMA sqlwise_internal CASCADE`,
	} {
		if _, err := exec.Exec(t.Context(), sql); !permissionDenied(err) {
			t.Fatalf("%s = %v", sql, err)
		}
	}
	var count int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM sqlwise_internal.migration_receipts WHERE outcome = 'applied'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipts after denied writes = %d, %v", count, err)
	}
}
