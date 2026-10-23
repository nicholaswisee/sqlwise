package schemadiff_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"sqlwise/internal/access"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
	"sqlwise/internal/schemadiff"
)

const desired = `{
  "dialect": "postgresql",
  "schema": "public",
  "tables": [
    {"name": "customers", "primaryKey": ["id"], "columns": [
      {"name": "id", "type": "bigint", "nullable": false},
      {"name": "email", "type": "varchar(320)", "nullable": false},
      {"name": "vip", "type": "boolean", "nullable": false, "default": "false"},
      {"name": "joined_at", "type": "timestamptz", "nullable": false, "default": "now()"}
    ]},
    {"name": "orders", "primaryKey": ["id"], "columns": [
      {"name": "id", "type": "uuid", "nullable": false, "default": "gen_random_uuid()"},
      {"name": "total", "type": "numeric(12,2)", "nullable": false, "default": "0"},
      {"name": "note", "type": "text", "nullable": true, "default": "'none; yet'"}
    ]}
  ]
}`

func TestLiveSchemaDiffToExecutedDraft(t *testing.T) {
	p := migrationtest.NewPlatform(t)
	p.Database(t, "db", "Shop")
	targets := migrationtest.NewTargets()
	target := migrationtest.TargetDatabase(t)
	targets.Add("db", target)
	drafts := migration.NewService(migration.NewStore(p.Pool), p.Access, targets, time.Now)
	service := schemadiff.NewService(p.Access, migrationtest.Snapshots{Targets: targets}, drafts)
	editor := p.User(t, "editor", map[string]string{"db": "edit"})
	viewer := p.User(t, "viewer", map[string]string{"db": "view"})
	outsider := p.User(t, "outsider", nil)
	if _, err := target.Exec(t.Context(), `CREATE TABLE customers (id bigint PRIMARY KEY, email varchar(320) NOT NULL, legacy text);
		CREATE TABLE audit_log (id int)`); err != nil {
		t.Fatal(err)
	}
	catalog := func() string {
		var state string
		if err := target.QueryRow(t.Context(), `SELECT string_agg(table_name || '.' || column_name, ',' ORDER BY table_name, ordinal_position) FROM information_schema.columns WHERE table_schema='public'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	platformRows := func() int {
		var n int
		if err := p.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM migration_drafts) + (SELECT count(*) FROM migration_attempts)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := catalog()

	proposal, err := service.Preview(viewer.Ctx, "db", desired)
	if err != nil {
		t.Fatal(err)
	}
	if catalog() != before || platformRows() != 0 {
		t.Fatal("preview wrote to the target or migration history")
	}
	for _, fragment := range []string{`ADD COLUMN "vip" boolean DEFAULT false NOT NULL`, `ADD COLUMN "joined_at" timestamp with time zone DEFAULT now() NOT NULL`, `CREATE TABLE "public"."orders"`} {
		if !strings.Contains(proposal.SQL, fragment) {
			t.Errorf("SQL lacks %q:\n%s", fragment, proposal.SQL)
		}
	}
	for _, fragment := range []string{`table "audit_log"`, `column "customers"."legacy"`} {
		if !strings.Contains(strings.Join(proposal.Warnings, "\n"), fragment) {
			t.Errorf("missing destructive warning for %s: %q", fragment, proposal.Warnings)
		}
	}
	if strings.Contains(proposal.SQL, "DROP") || !proposal.RequiresReview {
		t.Fatal("removed objects must warn, never generate DROP")
	}

	// Viewers preview; only editors create drafts, and no one without access sees anything.
	_, err = service.UseAsDraft(viewer.Ctx, "db", desired, proposal.SourceFingerprint, "")
	if !errors.Is(err, access.ErrPermissionDenied) {
		t.Fatalf("viewer draft = %v", err)
	}
	if _, err := service.Preview(outsider.Ctx, "db", desired); !errors.Is(err, access.ErrPermissionDenied) {
		t.Fatalf("outsider preview = %v", err)
	}
	if platformRows() != 0 {
		t.Fatal("denied request wrote a draft")
	}

	draft, err := service.UseAsDraft(editor.Ctx, "db", desired, proposal.SourceFingerprint, "Shop schema")
	if err != nil {
		t.Fatal(err)
	}
	if draft.SQL != proposal.SQL || draft.Title != "Shop schema" || draft.Applied() {
		t.Fatalf("draft = %+v", draft)
	}
	if catalog() != before {
		t.Fatal("creating a draft auto-executed it")
	}
	// The generated draft is editable like any other before execution.
	draft, err = drafts.Update(editor.Ctx, "db", draft.ID, migration.UpdateInput{ExpectedRevision: 1,
		DraftInput: migration.DraftInput{Title: draft.Title, SQL: draft.SQL + "COMMENT ON TABLE public.orders IS 'reviewed';\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drafts.Execute(editor.Ctx, "db", draft.ID, migration.ExecuteInput{ExpectedRevision: draft.Revision, ExpectedSQLHash: draft.SQLHash()}); err != nil {
		t.Fatal(err)
	}
	after, err := service.Preview(viewer.Ctx, "db", desired)
	if err != nil {
		t.Fatal(err)
	}
	if after.SQL != "" {
		t.Fatalf("schema does not match desired after execute; remaining SQL:\n%s", after.SQL)
	}
	for _, c := range after.Changes {
		if c.Kind != schemadiff.RemoveTable && c.Kind != schemadiff.RemoveColumn {
			t.Errorf("unexpected remaining change: %s", c)
		}
	}
	if strings.Contains(strings.Join(after.Warnings, "\n"), "sqlwise_internal") || strings.Contains(strings.Join(after.Warnings, "\n"), "migration_receipts") {
		t.Fatal("receipt schema leaked into the user diff")
	}

	// A live change after preview makes the proposal stale.
	stale, err := service.Preview(viewer.Ctx, "db", desired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(t.Context(), `ALTER TABLE customers ADD COLUMN drifted int`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UseAsDraft(editor.Ctx, "db", desired, stale.SourceFingerprint, ""); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("stale proposal = %v", err)
	}
	// No generated SQL means nothing to draft.
	fresh, err := service.Preview(viewer.Ctx, "db", desired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UseAsDraft(editor.Ctx, "db", desired, fresh.SourceFingerprint, ""); !errors.Is(err, migration.ErrValidation) {
		t.Fatalf("empty proposal draft = %v", err)
	}
}

func TestSchemaDiffInputAndSourceErrors(t *testing.T) {
	p := migrationtest.NewPlatform(t)
	p.Database(t, "db", "Shop")
	viewer := p.User(t, "viewer", map[string]string{"db": "view"})
	targets := migrationtest.NewTargets()
	drafts := migration.NewService(migration.NewStore(p.Pool), p.Access, targets, time.Now)
	service := schemadiff.NewService(p.Access, migrationtest.Snapshots{Targets: targets}, drafts)
	if _, err := service.Preview(viewer.Ctx, "db", `{"dialect":"mysql","schema":"app","tables":[]}`); !errors.Is(err, migration.ErrUnsupportedDialect) {
		t.Fatalf("mysql = %v", err)
	}
	if _, err := service.Preview(viewer.Ctx, "db", `not json`); !errors.Is(err, migration.ErrValidation) {
		t.Fatalf("invalid = %v", err)
	}
	if _, err := service.Preview(viewer.Ctx, "db", `{"dialect":"postgresql","schema":"public","tables":[]}`); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Fatalf("unreachable = %v", err)
	}
	unconfigured := schemadiff.NewService(p.Access, nil, drafts)
	if _, err := unconfigured.Preview(viewer.Ctx, "db", `{"dialect":"postgresql","schema":"public","tables":[]}`); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Fatalf("unconfigured = %v", err)
	}
}
