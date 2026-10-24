package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
	"sqlwise/internal/schemadiff"
)

type fakeResolver struct {
	dbms   database.DBMS
	handle database.Handle
	err    error
}

func (f fakeResolver) DBMS(context.Context, string) (database.DBMS, error) { return f.dbms, f.err }

func (f fakeResolver) Open(context.Context, string) (database.Handle, error) { return f.handle, f.err }

type bareHandle struct{}

func (bareHandle) DBMS() database.DBMS { return database.PostgreSQL }

func (bareHandle) Capabilities() database.Capabilities { return database.Capabilities{} }

func (bareHandle) Ping(context.Context) error { return nil }

func (bareHandle) Close() error { return nil }

type fakeTx struct{ commit error }

func (fakeTx) Exec(context.Context, string, ...any) (int64, error) { return 0, nil }

func (f fakeTx) Commit(context.Context) error { return f.commit }

func (fakeTx) Rollback(context.Context) error { return nil }

type fakeBeginner struct {
	tx  database.Tx
	err error
}

func (f fakeBeginner) Begin(context.Context) (database.Tx, error) { return f.tx, f.err }

func TestMigrationTargetsMapAdapterErrors(t *testing.T) {
	ctx := context.Background()
	unavailable := migrationTargets{resolver: fakeResolver{err: database.Unavailable()}}
	if _, err := unavailable.Dialect(ctx, "db"); !errors.Is(err, migration.ErrTargetUnavailable) || migration.Message(err) == "" {
		t.Fatalf("Dialect = %v", err)
	}
	if _, err := unavailable.Open(ctx, "db"); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Fatalf("Open = %v", err)
	}
	_, missing := database.ParseTableID("!!")
	if _, err := (migrationTargets{resolver: fakeResolver{err: missing}}).Snapshot(ctx, "db", "public"); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Fatalf("Snapshot = %v", err)
	}
	_, unsupported := database.ParseDBMS("oracle")
	if _, err := (migrationTargets{resolver: fakeResolver{err: unsupported}}).Open(ctx, "db"); !errors.Is(err, migration.ErrUnsupportedDialect) {
		t.Fatalf("unsupported Open = %v", err)
	}
	if d, err := (migrationTargets{resolver: fakeResolver{dbms: database.MySQL}}).Dialect(ctx, "db"); err != nil || d != migration.MySQL {
		t.Fatalf("MySQL Dialect = %q, %v", d, err)
	}
	if _, err := (migrationTargets{resolver: fakeResolver{handle: bareHandle{}}}).Open(ctx, "db"); !errors.Is(err, migration.ErrUnsupportedDialect) {
		t.Fatalf("handle without executor = %v", err)
	}

	beginFailure := migrationTarget{handle: bareHandle{}, begin: fakeBeginner{err: database.Unavailable()}}
	if _, err := beginFailure.Begin(ctx); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Fatalf("Begin = %v", err)
	}
	unknown := migrationTarget{handle: bareHandle{}, begin: fakeBeginner{tx: fakeTx{commit: fmt.Errorf("%w: connection reset", database.ErrOutcomeUnknown)}}}
	tx, err := unknown.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); !errors.Is(err, migration.ErrCommitUnknown) {
		t.Fatalf("unknown Commit = %v", err)
	}
	rejected := migrationTarget{handle: bareHandle{}, begin: fakeBeginner{tx: fakeTx{commit: errors.New("deferred constraint violated")}}}
	tx, _ = rejected.Begin(ctx)
	if err := tx.Commit(ctx); err == nil || errors.Is(err, migration.ErrCommitUnknown) {
		t.Fatalf("rejected Commit = %v", err)
	}
}

func TestDiffSnapshotKeepsComparedFields(t *testing.T) {
	def := "0"
	got := diffSnapshot(database.SchemaSnapshot{DBMS: database.PostgreSQL, Schema: "sales", Tables: []database.Table{{
		Schema: "sales", Name: "orders", PrimaryKey: []string{"id"},
		Columns:     []database.Column{{Name: "id", Type: "integer"}, {Name: "total", Type: "numeric(10,2)", Nullable: true, Default: &def}},
		Constraints: []database.Constraint{{Name: "orders_total_check", Kind: database.Check, Columns: []string{"total"}, Definition: "CHECK ((total >= (0)::numeric))"}},
		Indexes:     []database.Index{{Name: "orders_total_idx", Definition: "CREATE INDEX orders_total_idx ON sales.orders USING btree (total)"}},
	}}})
	if got.Dialect != schemadiff.PostgreSQL || got.Schema != "sales" || len(got.Tables) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	table := got.Tables[0]
	if table.Name != "orders" || table.PrimaryKey[0] != "id" || len(table.Columns) != 2 || table.Columns[1].Default != &def || !table.Columns[1].Nullable {
		t.Fatalf("table = %+v", table)
	}
	if table.Constraints[0] != (schemadiff.Definition{Name: "orders_total_check", Definition: "CHECK ((total >= (0)::numeric))"}) || table.Indexes[0].Name != "orders_total_idx" {
		t.Fatalf("definitions = %+v %+v", table.Constraints, table.Indexes)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("converted snapshot is invalid: %v", err)
	}
}

func TestMigrationsRunThroughDatabaseAdapter(t *testing.T) {
	p := migrationtest.NewPlatform(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := database.NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	store := database.NewStore(p.Pool, cipher)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	target := migrationtest.TargetDatabase(t)
	cc := target.Config().ConnConfig
	p.Database(t, "db-a", "Alpha")
	if err := pgx.BeginFunc(t.Context(), p.Pool, func(tx pgx.Tx) error {
		return store.Insert(t.Context(), tx, "db-a", database.Connection{DBMS: database.PostgreSQL, Host: cc.Host, Port: int(cc.Port), Database: cc.Database, Username: cc.User, Password: cc.Password, TLS: database.TLSDisable})
	}); err != nil {
		t.Fatal(err)
	}
	pools := database.NewPools(store)
	t.Cleanup(pools.Close)
	adapter := migrationTargets{resolver: pools}
	editor := p.User(t, "editor", map[string]string{"db-a": "edit"})
	migrations := migration.NewService(migration.NewStore(p.Pool), p.Access, adapter, time.Now)

	draft, err := migrations.Create(editor.Ctx, "db-a", migration.DraftInput{Title: "Create items", SQL: "CREATE TABLE items (id integer PRIMARY KEY);\nINSERT INTO items VALUES (1);\n"})
	if err != nil || draft.Dialect != migration.PostgreSQL {
		t.Fatalf("Create = %+v, %v", draft, err)
	}
	attempt, err := migrations.Execute(editor.Ctx, "db-a", draft.ID, migration.ExecuteInput{ExpectedRevision: draft.Revision, ExpectedSQLHash: draft.SQLHash()})
	if err != nil || attempt.Status != migration.StatusSucceeded || attempt.AppliedVersion != 1 {
		t.Fatalf("Execute = %+v, %v", attempt, err)
	}
	var items int
	var outcome string
	if err := target.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM items), (SELECT outcome FROM sqlwise_internal.migration_receipts WHERE attempt_id=$1)`, attempt.ID).Scan(&items, &outcome); err != nil || items != 1 || outcome != "applied" {
		t.Fatalf("target items=%d receipt=%q err=%v", items, outcome, err)
	}

	failing, err := migrations.Create(editor.Ctx, "db-a", migration.DraftInput{Title: "Half done", SQL: "CREATE TABLE extra (id integer);\nINSERT INTO items VALUES (1);\n"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = migrations.Execute(editor.Ctx, "db-a", failing.ID, migration.ExecuteInput{ExpectedRevision: failing.Revision, ExpectedVersion: 1, ExpectedSQLHash: failing.SQLHash()})
	if !errors.Is(err, migration.ErrExecutionFailed) || attempt.Status != migration.StatusFailed || attempt.FailedStatement != 2 {
		t.Fatalf("failing Execute = %+v, %v", attempt, err)
	}
	var extra *string
	if err := target.QueryRow(t.Context(), `SELECT to_regclass('public.extra')::text`).Scan(&extra); err != nil || extra != nil {
		t.Fatalf("first statement survived rollback: %v %v", extra, err)
	}

	diff := schemadiff.NewService(p.Access, adapter, migrations)
	proposal, err := diff.Preview(editor.Ctx, "db-a", `{"dialect":"postgresql","schema":"public","tables":[
		{"name":"items","columns":[{"name":"id","type":"integer","nullable":false}],"primaryKey":["id"]},
		{"name":"extra","columns":[{"name":"id","type":"integer","nullable":false}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Changes) != 1 || !strings.Contains(proposal.SQL, `"extra"`) || strings.Contains(proposal.SQL, `"items"`) || strings.Contains(proposal.SQL, "sqlwise_internal") {
		t.Fatalf("proposal changes=%v sql=%s", proposal.Changes, proposal.SQL)
	}
}
