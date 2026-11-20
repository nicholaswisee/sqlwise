package migration_test

import (
	_ "embed"
	"reflect"
	"testing"
	"time"

	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/migration"
)

// m1Schema is the Milestone 1 migration schema, before rollback support.
//
//go:embed testdata/m1_schema.sql
var m1Schema string

func TestUpgradeFromMilestoneOneKeepsHistory(t *testing.T) {
	f := testfixture.New(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(m1Schema)
	exec(`INSERT INTO database_registrations (id, name, active) VALUES ('legacy', 'Legacy', true)`)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, d := range []struct{ id, sql string }{{"applied", "CREATE TABLE a (id int)"}, {"failed", "CREATE TABLE b (id int"}, {"drafted", "CREATE TABLE c (id int)"}} {
		exec(`INSERT INTO migration_drafts (id, database_id, title, sql_text, dialect, revision, created_by, updated_by, created_at, updated_at)
			VALUES ($1, 'legacy', $1, $2, 'postgresql', 1, $3, $3, $4, $4)`, d.id, d.sql, f.Admin.UserID, at)
	}
	exec(`INSERT INTO migration_attempts (id, database_id, migration_id, draft_revision, candidate_version, applied_version, sql_text, sql_sha256,
		dialect, statement_count, executor_id, status, started_at, finished_at)
		VALUES ('e1', 'legacy', 'applied', 1, 1, 1, 'CREATE TABLE a (id int)', $1, 'postgresql', 1, $2, 'succeeded', $3, $3),
		       ('e2', 'legacy', 'failed', 1, 2, NULL, 'CREATE TABLE b (id int', $1, 'postgresql', 1, $2, 'failed', $3, $3)`,
		migration.HashSQL("x"), f.Admin.UserID, at)

	store := migration.NewStore(f.Pool)
	for range 2 { // upgrades are idempotent
		if err := store.ApplySchema(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ($1, 'legacy', 'view')`, f.ViewGroup)
	service := migration.NewService(store, f.Access, nil, time.Now)
	h, err := service.History(f.Viewer.Context, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	drafts := map[string]migration.Draft{}
	for _, d := range h.Drafts {
		drafts[d.ID] = d
	}
	applied, failed, drafted := drafts["applied"], drafts["failed"], drafts["drafted"]
	// Only the applied migration becomes irreversible; nothing guesses SQL down.
	if !applied.Irreversible() || applied.SQLDown != "" || applied.SQL != "CREATE TABLE a (id int)" || applied.Revision != 1 || applied.AppliedVersion != 1 {
		t.Fatalf("legacy applied = %+v", applied)
	}
	if failed.Irreversible() || !failed.Executed() || drafted.Irreversible() || drafted.Executed() {
		t.Fatalf("legacy unapplied = %+v / %+v", failed, drafted)
	}
	if got := executionIDs(h.Committed); !reflect.DeepEqual(got, []string{"e1"}) || h.CurrentVersion != 1 {
		t.Fatalf("committed history = %v at v%d", got, h.CurrentVersion)
	}
	if top, _ := h.Head.Top(); top.ExecutionID != "e1" || len(h.Attempts) != 2 || h.Attempts[0].Operation != migration.OpUp {
		t.Fatalf("head %+v, attempts %+v", top, h.Attempts)
	}
	if _, err := f.Pool.Exec(t.Context(), `UPDATE migration_drafts SET irreversible_reason=NULL WHERE id='applied'`); err == nil {
		t.Fatal("upgraded applied migration is still mutable")
	}
}
