package migration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
)

type fixture struct {
	platform *migrationtest.Platform
	targets  *migrationtest.Targets
	service  *migration.Service
	target   *pgxpool.Pool
	editor   migrationtest.User
	viewer   migrationtest.User
	outsider migrationtest.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	p := migrationtest.NewPlatform(t)
	p.Database(t, "db-a", "Alpha")
	p.Database(t, "db-b", "Beta")
	f := &fixture{platform: p, targets: migrationtest.NewTargets()}
	f.target = migrationtest.TargetDatabase(t)
	f.targets.Add("db-a", f.target)
	f.service = migration.NewService(migration.NewStore(p.Pool), p.Access, f.targets, time.Now)
	f.editor = p.User(t, "editor", map[string]string{"db-a": "edit", "db-b": "edit"})
	f.viewer = p.User(t, "viewer", map[string]string{"db-a": "view"})
	f.outsider = p.User(t, "outsider", nil)
	return f
}

func (f *fixture) create(t *testing.T, title, sql string) migration.Draft {
	t.Helper()
	d, err := f.service.Create(f.editor.Ctx, "db-a", migration.DraftInput{Title: title, SQL: sql})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *fixture) execute(ctx context.Context, d migration.Draft, version int64) (migration.Attempt, error) {
	return f.service.Execute(ctx, d.DatabaseID, d.ID, migration.ExecuteInput{ExpectedRevision: d.Revision, ExpectedVersion: version, ExpectedSQLHash: d.SQLHash()})
}

func (f *fixture) targetHas(t *testing.T, table string) bool {
	t.Helper()
	var exists bool
	if err := f.target.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// history reads db-a's drafts and attempts the way the history page does.
func (f *fixture) history(t *testing.T) migration.History {
	t.Helper()
	h, err := f.service.History(f.editor.Ctx, "db-a")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// attempt finds one attempt in db-a's history by ID.
func (f *fixture) attempt(t *testing.T, id string) migration.Attempt {
	t.Helper()
	for _, a := range f.history(t).Attempts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("attempt %s not in history", id)
	return migration.Attempt{}
}

// attemptsOf returns the attempts recorded for one draft, newest first.
func (f *fixture) attemptsOf(t *testing.T, draftID string) []migration.Attempt {
	t.Helper()
	var out []migration.Attempt
	for _, a := range f.history(t).Attempts {
		if a.MigrationID == draftID {
			out = append(out, a)
		}
	}
	return out
}

func wantKind(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
}

func TestDraftLifecycleAndAccess(t *testing.T) {
	f := newFixture(t)
	d, err := f.service.Create(f.editor.Ctx, "db-a", migration.DraftInput{Title: "  Add users  ", SQL: "CREATE TABLE users (id bigint);\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Add users" || d.SQL != "CREATE TABLE users (id bigint);\n" || d.Revision != 1 || d.Dialect != migration.PostgreSQL ||
		d.CreatedBy != f.editor.ID || d.UpdatedBy != f.editor.ID || d.CreatedByName != "editor" || d.Applied() || d.CreatedAt.IsZero() {
		t.Fatalf("created draft = %+v", d)
	}

	detail, err := f.service.Detail(f.viewer.Ctx, "db-a", d.ID)
	if err != nil || detail.Draft.SQL != d.SQL || detail.Database.Name != "Alpha" || detail.Database.CanEdit || detail.CurrentVersion != 0 {
		t.Fatalf("viewer detail = %+v, %v", detail, err)
	}
	if detail, err := f.service.Detail(f.editor.Ctx, "db-a", d.ID); err != nil || !detail.Database.CanEdit {
		t.Fatalf("editor detail = %+v, %v", detail.Database, err)
	}
	history, err := f.service.History(f.viewer.Ctx, "db-a")
	if err != nil || len(history.Drafts) != 1 || history.Drafts[0].ID != d.ID {
		t.Fatalf("viewer history = %+v, %v", history, err)
	}

	updated, err := f.service.Update(f.editor.Ctx, "db-a", d.ID, migration.UpdateInput{ExpectedRevision: 1,
		DraftInput: migration.DraftInput{Title: "Add users table", SQL: "CREATE TABLE users (id bigint PRIMARY KEY);"}})
	if err != nil || updated.Revision != 2 || updated.SQL != "CREATE TABLE users (id bigint PRIMARY KEY);" || updated.Title != "Add users table" || updated.UpdatedAt.Before(d.UpdatedAt) {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	_, err = f.service.Update(f.editor.Ctx, "db-a", d.ID, migration.UpdateInput{ExpectedRevision: 1, DraftInput: migration.DraftInput{Title: "stale", SQL: "SELECT 1"}})
	wantKind(t, err, migration.ErrConflict)
	if detail, _ := f.service.Detail(f.viewer.Ctx, "db-a", d.ID); detail.Draft.SQL != updated.SQL || detail.Draft.Revision != 2 {
		t.Fatalf("stale update overwrote draft: %+v", detail.Draft)
	}

	for name, in := range map[string]migration.DraftInput{
		"empty-title": {Title: " ", SQL: "SELECT 1"}, "long-title": {Title: strings.Repeat("x", 201), SQL: "SELECT 1"},
		"empty-sql": {Title: "x", SQL: " \n"}, "nul": {Title: "x", SQL: "SELECT 1\x00"}, "large": {Title: "x", SQL: strings.Repeat("x", migration.MaxSQLBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.service.Create(f.editor.Ctx, "db-a", in)
			wantKind(t, err, migration.ErrValidation)
		})
	}

	// View may read but never mutate; no grant may not even read.
	_, err = f.service.Create(f.viewer.Ctx, "db-a", migration.DraftInput{Title: "x", SQL: "SELECT 1"})
	wantKind(t, err, access.ErrPermissionDenied)
	_, err = f.service.Update(f.viewer.Ctx, "db-a", d.ID, migration.UpdateInput{ExpectedRevision: 2, DraftInput: migration.DraftInput{Title: "x", SQL: "SELECT 1"}})
	wantKind(t, err, access.ErrPermissionDenied)
	_, err = f.execute(f.viewer.Ctx, updated, 0)
	wantKind(t, err, access.ErrPermissionDenied)
	for _, call := range []func() error{
		func() error { _, err := f.service.History(f.outsider.Ctx, "db-a"); return err },
		func() error { _, err := f.service.Detail(f.outsider.Ctx, "db-a", d.ID); return err },
		func() error { _, err := f.service.History(f.viewer.Ctx, "db-b"); return err },
		func() error { _, err := f.service.History(f.editor.Ctx, "missing"); return err },
	} {
		wantKind(t, call(), access.ErrPermissionDenied)
	}
	_, err = f.service.History(context.Background(), "db-a")
	wantKind(t, err, auth.ErrSession)
	if h := f.history(t); f.targets.Opens() != 0 || len(h.Attempts) != 0 {
		t.Fatal("denied execution reached the target or history")
	} else if len(h.Drafts) != 1 {
		t.Fatalf("denied or invalid create wrote a draft: %d drafts", len(h.Drafts))
	}

	// A migration ID is only valid under its own database.
	_, err = f.service.Detail(f.editor.Ctx, "db-b", d.ID)
	wantKind(t, err, migration.ErrNotFound)
	_, err = f.service.Update(f.editor.Ctx, "db-b", d.ID, migration.UpdateInput{ExpectedRevision: 2, DraftInput: migration.DraftInput{Title: "x", SQL: "SELECT 1"}})
	wantKind(t, err, migration.ErrNotFound)
	_, err = f.service.Execute(f.editor.Ctx, "db-b", d.ID, migration.ExecuteInput{ExpectedRevision: 2, ExpectedSQLHash: updated.SQLHash()})
	wantKind(t, err, migration.ErrNotFound)
	_, err = f.service.Detail(f.editor.Ctx, "db-a", "missing")
	wantKind(t, err, migration.ErrNotFound)
}

func TestLinearHistoryCommitRollbackAndImmutability(t *testing.T) {
	f := newFixture(t)
	first := f.create(t, "Create items", "CREATE TABLE items (id int PRIMARY KEY, name text);\nINSERT INTO items VALUES (1, 'one; still one');")
	a1, err := f.execute(f.editor.Ctx, first, 0)
	if err != nil || a1.Status != migration.StatusSucceeded || a1.AppliedVersion != 1 || a1.StatementCount != 2 || a1.ExecutorID != f.editor.ID {
		t.Fatalf("v1 = %+v, %v", a1, err)
	}
	var name string
	if err := f.target.QueryRow(t.Context(), `SELECT name FROM items WHERE id=1`).Scan(&name); err != nil || name != "one; still one" {
		t.Fatalf("v1 effect = %q, %v", name, err)
	}

	// Statement 2 fails: statement 1's row must not survive the rollback.
	second := f.create(t, "Add rows", "INSERT INTO items VALUES (2, 'two');\nINSERT INTO items VALUES (1, 'duplicate');")
	a2, err := f.execute(f.editor.Ctx, second, 1)
	wantKind(t, err, migration.ErrExecutionFailed)
	if a2.Status != migration.StatusFailed || a2.CandidateVersion != 2 || a2.AppliedVersion != 0 || a2.FailedStatement != 2 ||
		!strings.Contains(a2.Error, "SQLSTATE 23505") || strings.Contains(a2.Error, "(id)=(1)") {
		t.Fatalf("failed attempt = %+v", a2)
	}
	var rows int
	if err := f.target.QueryRow(t.Context(), `SELECT count(*) FROM items`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rollback left %d rows, %v", rows, err)
	}

	// Retrying the corrected draft reuses candidate 2 without a second applied v2.
	second, err = f.service.Update(f.editor.Ctx, "db-a", second.ID, migration.UpdateInput{ExpectedRevision: 1,
		DraftInput: migration.DraftInput{Title: "Add rows", SQL: "INSERT INTO items VALUES (2, 'two');\nINSERT INTO items VALUES (3, 'three');"}})
	if err != nil {
		t.Fatal(err)
	}
	a3, err := f.execute(f.editor.Ctx, second, 1)
	if err != nil || a3.AppliedVersion != 2 || a3.DraftRevision != 2 {
		t.Fatalf("retry v2 = %+v, %v", a3, err)
	}
	third := f.create(t, "Index", "CREATE INDEX items_name_idx ON items (name);")
	if a4, err := f.execute(f.editor.Ctx, third, 2); err != nil || a4.AppliedVersion != 3 {
		t.Fatalf("v3 = %+v, %v", a4, err)
	}

	history, err := f.service.History(f.viewer.Ctx, "db-a")
	if err != nil || history.CurrentVersion != 3 || len(history.Attempts) != 4 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	want := []struct {
		status             migration.Status
		candidate, applied int64
	}{{migration.StatusSucceeded, 3, 3}, {migration.StatusSucceeded, 2, 2}, {migration.StatusFailed, 2, 0}, {migration.StatusSucceeded, 1, 1}}
	for i, w := range want {
		a := history.Attempts[i]
		if a.Status != w.status || a.CandidateVersion != w.candidate || a.AppliedVersion != w.applied || a.ExecutorName != "editor" || a.FinishedAt == nil || a.SQL == "" {
			t.Errorf("attempt %d = %+v, want %+v", i, a, w)
		}
	}
	if history.Attempts[2].SQL != "INSERT INTO items VALUES (2, 'two');\nINSERT INTO items VALUES (1, 'duplicate');" {
		t.Error("failed attempt lost its SQL snapshot after the draft was edited")
	}
	if _, a, err := f.service.Attempt(f.viewer.Ctx, "db-a", second.ID, a2.ID); err != nil || a.ID != a2.ID || a.Status != migration.StatusFailed {
		t.Fatalf("attempt detail = %+v, %v", a, err)
	}
	if _, _, err := f.service.Attempt(f.viewer.Ctx, "db-a", first.ID, a2.ID); !errors.Is(err, migration.ErrNotFound) {
		t.Fatalf("attempt under another migration = %v", err)
	}

	// Applied drafts are never executed or edited again.
	detail, err := f.service.Detail(f.editor.Ctx, "db-a", first.ID)
	if err != nil || detail.Draft.AppliedVersion != 1 {
		t.Fatalf("applied detail = %+v, %v", detail.Draft, err)
	}
	first = detail.Draft
	_, err = f.execute(f.editor.Ctx, first, 3)
	wantKind(t, err, migration.ErrConflict)
	_, err = f.service.Update(f.editor.Ctx, "db-a", first.ID, migration.UpdateInput{ExpectedRevision: first.Revision, DraftInput: migration.DraftInput{Title: "x", SQL: "DROP TABLE items"}})
	wantKind(t, err, migration.ErrConflict)
	if n := len(f.history(t).Attempts); n != 4 {
		t.Fatalf("re-submitting an applied migration changed history to %d attempts", n)
	}
	for _, sql := range []string{
		`UPDATE migration_attempts SET sql_text='DROP TABLE items' WHERE id='` + a1.ID + `'`,
		`UPDATE migration_attempts SET status='failed', applied_version=NULL WHERE id='` + a1.ID + `'`,
		`UPDATE migration_attempts SET error_message='rewritten' WHERE id='` + a2.ID + `'`,
		`DELETE FROM migration_attempts WHERE id='` + a2.ID + `'`,
		`UPDATE migration_drafts SET sql_text='DROP TABLE items' WHERE id='` + first.ID + `'`,
		`DELETE FROM migration_drafts WHERE id='` + first.ID + `'`,
		`INSERT INTO migration_attempts (id, database_id, migration_id, draft_revision, candidate_version, applied_version, sql_text, sql_sha256, dialect, statement_count, executor_id, status, started_at, finished_at)
			SELECT 'dup', database_id, migration_id, 1, 3, 3, sql_text, sql_sha256, dialect, 1, executor_id, 'succeeded', now(), now() FROM migration_attempts WHERE id='` + a2.ID + `'`,
	} {
		if _, err := f.platform.Pool.Exec(t.Context(), sql); err == nil {
			t.Errorf("history mutation succeeded: %s", sql)
		}
	}
}

func TestExecuteRejectsStaleConfirmation(t *testing.T) {
	f := newFixture(t)
	d := f.create(t, "One", "CREATE TABLE one (id int);")
	for name, in := range map[string]migration.ExecuteInput{
		"revision": {ExpectedRevision: 2, ExpectedSQLHash: d.SQLHash()},
		"hash":     {ExpectedRevision: 1, ExpectedSQLHash: migration.HashSQL("SELECT 1")},
		"version":  {ExpectedRevision: 1, ExpectedVersion: 4, ExpectedSQLHash: d.SQLHash()},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.service.Execute(f.editor.Ctx, "db-a", d.ID, in)
			wantKind(t, err, migration.ErrConflict)
		})
	}
	if f.targetHas(t, "one") || len(f.history(t).Attempts) != 0 {
		t.Fatal("stale confirmation executed")
	}
}

func TestExecuteRejectsUnsupportedSQLBeforeTarget(t *testing.T) {
	f := newFixture(t)
	for _, sql := range []string{
		"CREATE TABLE x (id int); COMMIT;", "BEGIN; CREATE TABLE x (id int);", "VACUUM;", "CREATE DATABASE other;",
		"CREATE INDEX CONCURRENTLY i ON x (id);", "DROP SCHEMA sqlwise_internal CASCADE;", "SET search_path = other;",
		"SELECT 'unterminated", "COPY x FROM STDIN;", "CREATE TABLE ok (id int);\nCREAT TABLE broken (id int);",
	} {
		t.Run(sql, func(t *testing.T) {
			d := f.create(t, "Invalid", sql)
			_, err := f.execute(f.editor.Ctx, d, 0)
			wantKind(t, err, migration.ErrValidation)
		})
	}
	if f.targets.Opens() != 0 || len(f.history(t).Attempts) != 0 {
		t.Fatal("invalid SQL reached the target")
	}
	// Errors only the server can detect (here a duplicate table) do reach the
	// target, which also shows Opens observes target access, then roll back.
	d := f.create(t, "Duplicate", "CREATE TABLE ok_before (id int);\nCREATE TABLE ok_before (id int);")
	a, err := f.execute(f.editor.Ctx, d, 0)
	wantKind(t, err, migration.ErrExecutionFailed)
	if f.targets.Opens() == 0 || a.FailedStatement != 2 || !strings.Contains(a.Error, "42P07") || f.targetHas(t, "ok_before") {
		t.Fatalf("server-side failure = %+v, opens %d", a, f.targets.Opens())
	}
}

func TestExecuteTimeoutAndCancellationRollBack(t *testing.T) {
	f := newFixture(t)
	f.service.WithTimeouts(migration.LockWait, 500*time.Millisecond, migration.CleanupTimeout)
	d := f.create(t, "Slow", "CREATE TABLE slow_effect (id int);\nSELECT pg_sleep(30);")
	a, err := f.execute(f.editor.Ctx, d, 0)
	wantKind(t, err, migration.ErrTimeout)
	if a.Status != migration.StatusFailed || a.FailedStatement != 2 || f.targetHas(t, "slow_effect") {
		t.Fatalf("timeout attempt = %+v", a)
	}

	f.service.WithTimeouts(migration.LockWait, time.Minute, migration.CleanupTimeout)
	d, err = f.service.Update(f.editor.Ctx, "db-a", d.ID, migration.UpdateInput{ExpectedRevision: 1, DraftInput: migration.DraftInput{Title: "Slow", SQL: "CREATE TABLE cancel_effect (id int);\nSELECT pg_sleep(29);"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.editor.Ctx)
	done := make(chan error, 1)
	go func() { _, err := f.execute(ctx, d, 0); done <- err }()
	waitFor(t, f.target, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE query LIKE 'SELECT pg_sleep(29)%' AND state='active')`)
	cancel()
	wantKind(t, <-done, migration.ErrExecutionFailed)
	if f.targetHas(t, "cancel_effect") {
		t.Fatal("cancelled migration left changes")
	}
	attempts := f.history(t).Attempts
	if len(attempts) != 2 || attempts[0].Status != migration.StatusFailed || attempts[1].Status != migration.StatusFailed {
		t.Fatalf("timeout/cancel attempts = %+v, want two failed", attempts)
	}
}

// startSlow executes a draft whose last statement sleeps on the target and
// returns once that statement is running, so the execution is mid-flight.
func (f *fixture) startSlow(t *testing.T, d migration.Draft, version int64, sleep string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := f.execute(f.editor.Ctx, d, version); done <- err }()
	waitFor(t, f.target, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE query LIKE 'SELECT pg_sleep(`+sleep+`)%' AND state='active')`)
	return done
}

func TestConcurrentExecutionsKeepHistoryLinear(t *testing.T) {
	f := newFixture(t)
	f.service.WithTimeouts(30*time.Second, migration.ExecuteTimeout, migration.CleanupTimeout)
	slow := f.create(t, "Slow", "CREATE TABLE slow_side (id int);\nSELECT pg_sleep(2);")
	racer := f.create(t, "Racer", "CREATE TABLE racer_side (id int);")
	f.targets.Add("db-b", migrationtest.TargetDatabase(t))
	other, err := f.service.Create(f.editor.Ctx, "db-b", migration.DraftInput{Title: "Other", SQL: "CREATE TABLE other (id int);"})
	if err != nil {
		t.Fatal(err)
	}

	// Both were confirmed against version 0. The racer starts while the slow
	// execution is mid-flight, so it must wait and then see v1 already taken.
	slowDone := f.startSlow(t, slow, 0, "2")
	racerDone := make(chan error, 1)
	go func() { _, err := f.execute(f.editor.Ctx, racer, 0); racerDone <- err }()

	// Another target is not serialized behind db-a.
	if a, err := f.execute(f.editor.Ctx, other, 0); err != nil || a.AppliedVersion != 1 {
		t.Fatalf("parallel target = %+v, %v", a, err)
	}
	select {
	case <-slowDone:
		t.Fatal("db-b waited for db-a's execution")
	default:
	}

	if err := <-slowDone; err != nil {
		t.Fatalf("slow execute: %v", err)
	}
	wantKind(t, <-racerDone, migration.ErrConflict)
	h := f.history(t)
	if h.CurrentVersion != 1 || len(h.Attempts) != 1 || h.Attempts[0].MigrationID != slow.ID || f.targetHas(t, "racer_side") {
		t.Fatalf("concurrent executions duplicated or skipped a version: %+v", h.Attempts)
	}

	// A lock held beyond the wait bound is reported busy, not queued forever.
	f.service.WithTimeouts(200*time.Millisecond, migration.ExecuteTimeout, migration.CleanupTimeout)
	slower := f.create(t, "Slower", "SELECT pg_sleep(2.5);")
	slowerDone := f.startSlow(t, slower, 1, "2.5")
	_, err = f.execute(f.editor.Ctx, racer, 1)
	wantKind(t, err, migration.ErrBusy)
	if err := <-slowerDone; err != nil {
		t.Fatalf("slower execute: %v", err)
	}
	if len(f.attemptsOf(t, racer.ID)) != 0 || f.history(t).CurrentVersion != 2 {
		t.Fatal("busy execution created an attempt or disturbed the version sequence")
	}
}

func TestUnknownCommitOutcomesAreReconciledByReceipt(t *testing.T) {
	t.Run("acknowledgement-lost", func(t *testing.T) {
		f := newFixture(t)
		f.targets.Commit = func(ctx context.Context, commit, _ func(context.Context) error) error {
			if err := commit(ctx); err != nil {
				return err
			}
			return errors.Join(migration.ErrCommitUnknown, errors.New("connection reset after commit"))
		}
		a, err := f.execute(f.editor.Ctx, f.create(t, "Lost ack", "CREATE TABLE lost_ack (id int);"), 0)
		if err != nil || a.Status != migration.StatusSucceeded || a.AppliedVersion != 1 || a.ReconciledAt == nil || !f.targetHas(t, "lost_ack") {
			t.Fatalf("lost ack = %+v, %v", a, err)
		}
	})
	t.Run("commit-request-lost", func(t *testing.T) {
		f := newFixture(t)
		f.targets.Commit = func(ctx context.Context, _, rollback func(context.Context) error) error {
			_ = rollback(ctx)
			return errors.Join(migration.ErrCommitUnknown, errors.New("connection reset before commit"))
		}
		a, err := f.execute(f.editor.Ctx, f.create(t, "Lost commit", "CREATE TABLE lost_commit (id int);"), 0)
		wantKind(t, err, migration.ErrExecutionFailed)
		if a.Status != migration.StatusFailed || a.ReconciledAt == nil || f.targetHas(t, "lost_commit") {
			t.Fatalf("lost commit = %+v", a)
		}
	})
	t.Run("undetermined-blocks-until-resolved", func(t *testing.T) {
		f := newFixture(t)
		f.service.WithTimeouts(migration.LockWait, migration.ExecuteTimeout, 300*time.Millisecond)
		// The commit request "hangs": the transaction still holds its receipt claim.
		var abandoned func(context.Context) error
		f.targets.Commit = func(_ context.Context, _, rollback func(context.Context) error) error {
			abandoned = rollback
			return migration.ErrCommitUnknown
		}
		first := f.create(t, "Hanging", "CREATE TABLE hanging (id int);")
		a, err := f.execute(f.editor.Ctx, first, 0)
		wantKind(t, err, migration.ErrOutcomeUnknown)
		if a.Status != migration.StatusUnknown || f.attempt(t, a.ID).Status != migration.StatusUnknown {
			t.Fatalf("unknown attempt = %+v", a)
		}
		unknownID := a.ID
		f.targets.Commit = nil
		next := f.create(t, "Next", "CREATE TABLE next_one (id int);")
		_, err = f.execute(f.editor.Ctx, next, 0)
		wantKind(t, err, migration.ErrOutcomeUnknown)
		if detail, _ := f.service.Detail(f.viewer.Ctx, "db-a", next.ID); detail.Blocked == "" {
			t.Fatal("detail does not show the execution block")
		}
		if _, err := f.service.Update(f.editor.Ctx, "db-a", first.ID, migration.UpdateInput{ExpectedRevision: 1, DraftInput: migration.DraftInput{Title: "x", SQL: "SELECT 1"}}); !errors.Is(err, migration.ErrConflict) {
			t.Fatalf("edited unresolved draft: %v", err)
		}
		if err := abandoned(t.Context()); err != nil {
			t.Fatal(err)
		}
		a, err = f.execute(f.editor.Ctx, next, 0)
		if err != nil || a.AppliedVersion != 1 || f.targetHas(t, "hanging") || !f.targetHas(t, "next_one") {
			t.Fatalf("after resolution = %+v, %v", a, err)
		}
		if resolved := f.attempt(t, unknownID); resolved.Status != migration.StatusFailed || resolved.ReconciledAt == nil {
			t.Fatalf("unknown attempt was not reconciled as failed: %+v", resolved)
		}
	})
	t.Run("platform-record-failure-after-commit", func(t *testing.T) {
		f := newFixture(t)
		f.platform.Exec(t, `CREATE FUNCTION reject_success() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF NEW.status = 'succeeded' THEN RAISE EXCEPTION 'platform unavailable'; END IF; RETURN NEW; END $$`)
		f.platform.Exec(t, `CREATE TRIGGER reject_success BEFORE UPDATE ON migration_attempts FOR EACH ROW EXECUTE FUNCTION reject_success()`)
		first := f.create(t, "Committed", "CREATE TABLE committed (id int);")
		a, err := f.execute(f.editor.Ctx, first, 0)
		wantKind(t, err, migration.ErrOutcomeUnknown)
		if !f.targetHas(t, "committed") || f.attempt(t, a.ID).Status != migration.StatusRunning {
			t.Fatal("expected committed target with unrecorded platform outcome")
		}
		f.platform.Exec(t, `DROP TRIGGER reject_success ON migration_attempts`)
		next := f.create(t, "Next", "CREATE TABLE after_recovery (id int);")
		// Recovery applies v1 from the receipt, so the stale editor sees a conflict.
		_, err = f.execute(f.editor.Ctx, next, 0)
		wantKind(t, err, migration.ErrConflict)
		if recovered := f.attempt(t, a.ID); recovered.Status != migration.StatusSucceeded || recovered.AppliedVersion != 1 || recovered.ReconciledAt == nil {
			t.Fatalf("receipt recovery did not record v1: %+v", recovered)
		}
		if a, err := f.execute(f.editor.Ctx, next, 1); err != nil || a.AppliedVersion != 2 {
			t.Fatalf("after recovery = %+v, %v", a, err)
		}
	})
	t.Run("crashed-executor", func(t *testing.T) {
		f := newFixture(t)
		crashed := f.create(t, "Crashed", "CREATE TABLE crashed (id int);")
		f.platform.Exec(t, `INSERT INTO migration_attempts (id, database_id, migration_id, draft_revision, candidate_version, sql_text, sql_sha256, dialect, statement_count, executor_id, status, started_at)
			VALUES ('crashed-attempt', 'db-a', $1, 1, 1, $2, $3, 'postgresql', 1, $4, 'running', now())`, crashed.ID, crashed.SQL, crashed.SQLHash(), f.editor.ID)
		next := f.create(t, "Next", "CREATE TABLE survivor (id int);")
		if a, err := f.execute(f.editor.Ctx, next, 0); err != nil || a.AppliedVersion != 1 {
			t.Fatalf("after crash = %+v, %v", a, err)
		}
		if crashed := f.attempt(t, "crashed-attempt"); crashed.Status != migration.StatusFailed || f.targetHas(t, "crashed") {
			t.Fatalf("crashed attempt not resolved as failed: %+v", crashed)
		}
	})
}

func TestExecuteWithoutReachableTarget(t *testing.T) {
	f := newFixture(t)
	f.targets.Remove("db-a")
	d := f.create(t, "Unreachable", "CREATE TABLE unreachable (id int);")
	_, err := f.execute(f.editor.Ctx, d, 0)
	wantKind(t, err, migration.ErrTargetUnavailable)
	if len(f.history(t).Attempts) != 0 {
		t.Fatal("unreachable target created an attempt")
	}
	unconfigured := migration.NewService(migration.NewStore(f.platform.Pool), f.platform.Access, nil, time.Now)
	_, err = unconfigured.Execute(f.editor.Ctx, "db-a", d.ID, migration.ExecuteInput{ExpectedRevision: 1, ExpectedSQLHash: d.SQLHash()})
	wantKind(t, err, migration.ErrTargetUnavailable)
}

// waitFor polls a database condition; it synchronizes on real server state
// (lock queues, running queries) instead of sleeping for a guessed duration.
func waitFor(t *testing.T, pool *pgxpool.Pool, condition string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		var ok bool
		if err := pool.QueryRow(ctx, condition).Scan(&ok); err != nil {
			t.Fatalf("waiting for %q: %v", condition, err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q", condition)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
