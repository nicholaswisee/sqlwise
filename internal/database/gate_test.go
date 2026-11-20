package database_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/database"
	"sqlwise/internal/migration/migrationtest"
)

func acquire(t *testing.T, target *pgxpool.Pool, wait time.Duration) (*database.Gate, error) {
	t.Helper()
	g, err := database.AcquireGate(t.Context(), target.Config().ConnConfig.Copy(), "db", wait)
	if err == nil {
		t.Cleanup(func() { g.Close() })
	}
	return g, err
}

func mustGate(t *testing.T, target *pgxpool.Pool) *database.Gate {
	t.Helper()
	g, err := acquire(t, target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGateSerializesSessionsOnOneTarget(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	first := mustGate(t, target)
	if err := first.Held(t.Context()); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := acquire(t, target, 300*time.Millisecond); !errors.Is(err, database.ErrBusy) {
		t.Fatalf("second gate = %v, want ErrBusy", err)
	}
	if time.Since(started) < 300*time.Millisecond {
		t.Fatal("second gate gave up before its wait elapsed")
	}
	done := make(chan *database.Gate)
	go func() {
		g, err := database.AcquireGate(context.Background(), target.Config().ConnConfig.Copy(), "db", 5*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- g
	}()
	time.Sleep(200 * time.Millisecond)
	first.Close()
	g := <-done
	if g == nil {
		t.Fatal("waiting gate was not granted after release")
	}
	defer g.Close()
	if g.Waited() < 100*time.Millisecond {
		t.Fatalf("waited %v, want the measured lock wait", g.Waited())
	}
}

func TestGatesOnDifferentTargetsRunConcurrently(t *testing.T) {
	a, b := migrationtest.TargetDatabase(t), migrationtest.TargetDatabase(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, target := range []*pgxpool.Pool{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := database.AcquireGate(context.Background(), target.Config().ConnConfig.Copy(), "db", 200*time.Millisecond)
			if err == nil {
				// Both are held at the same time.
				time.Sleep(300 * time.Millisecond)
				g.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("different targets must not share a gate: %v", err)
		}
	}
}

func TestLostGateSessionStopsWorkWithoutReconnecting(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	g := mustGate(t, target)
	if _, err := target.Exec(t.Context(), `SELECT pg_terminate_backend($1)`, g.PID()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec(t.Context(), `SELECT 1`); err == nil {
		t.Fatal("a lost gate session must not run more SQL")
	}
	if !g.Lost() {
		t.Fatal("gate must report the lost session")
	}
	if _, err := g.Exec(t.Context(), `SELECT 1`); err == nil {
		t.Fatal("the gate reconnected transparently")
	}
	// The server dropped the lock with the session, so a successor may proceed.
	mustGate(t, target)
}

func TestGateTransactionsRunOnTheLockingSession(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	g := mustGate(t, target)
	tx, err := g.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var pid uint32
	if err := tx.(database.BatchTx).QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if pid != g.PID() {
		t.Fatalf("transaction ran on backend %d, gate holds %d", pid, g.PID())
	}
}

func TestPoolsGateRefusesMySQLTargets(t *testing.T) {
	f := newStoreFixture(t)
	registration := f.register(t, "MySQL", database.Connection{DBMS: database.MySQL, Host: "localhost", Port: 3306,
		Database: "app", Username: "app", Password: "app", TLS: database.TLSDisable})
	pools := database.NewPools(f.store)
	t.Cleanup(pools.Close)
	_, err := pools.Gate(t.Context(), registration.ID, time.Second)
	if !errors.Is(err, database.ErrUnsupportedDialect) {
		t.Fatalf("MySQL gate = %v, want ErrUnsupportedDialect", err)
	}
}

func TestPoolsGateUsesRegisteredTarget(t *testing.T) {
	f := newTargetFixture(t)
	g, err := f.pools.Gate(t.Context(), f.id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if s := f.pools.Stat(); s.Gates < 1 {
		t.Fatalf("stat = %+v, want an open gate", s)
	}
	var name string
	if err := g.QueryRow(t.Context(), `SELECT current_database()`).Scan(&name); err != nil || !strings.HasPrefix(name, "sqlwise_target_") {
		t.Fatalf("gate database = %q, %v", name, err)
	}
}

func seed(t *testing.T, target *pgxpool.Pool, rows int) {
	t.Helper()
	for _, sql := range []string{
		`CREATE TABLE accounts (id bigint PRIMARY KEY, status text NOT NULL, note text)`,
		fmt.Sprintf(`INSERT INTO accounts SELECT g, 'active', 'n' || g FROM generate_series(1, %d) g`, rows),
	} {
		if _, err := target.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

var archive = database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}

func count(t *testing.T, target *pgxpool.Pool, sql string) int64 {
	t.Helper()
	var n int64
	if err := target.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBatchUpdateCommitsEveryRowInBoundedBatches(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 1000)
	g := mustGate(t, target)
	plan, err := g.PrepareBatch(t.Context(), "exec-1", archive, 128)
	if err != nil {
		t.Fatal(err)
	}
	var seen []int64
	p, err := g.RunBatches(t.Context(), plan, database.BatchHooks{Committed: func(p database.BatchProgress) { seen = append(seen, p.Rows) }})
	if err != nil {
		t.Fatal(err)
	}
	if p.Batches != 8 || p.Rows != 1000 || p.TotalRows != 1000 || !p.Complete {
		t.Fatalf("progress = %+v", p)
	}
	if len(seen) != 8 || seen[0] != 128 || seen[7] != 1000 {
		t.Fatalf("committed progress = %v", seen)
	}
	if n := count(t, target, `SELECT count(*) FROM accounts WHERE status <> 'archived'`); n != 0 {
		t.Fatalf("%d rows not updated", n)
	}
	if n := count(t, target, `SELECT count(*) FROM sqlwise_internal.batch_rows WHERE execution_id='exec-1' AND before_value='active' AND after_value='archived'`); n != 1000 {
		t.Fatalf("%d before/after images, want 1000", n)
	}
	cps, err := g.Checkpoints(t.Context(), "exec-1", database.Forward)
	if err != nil || len(cps) != 8 || cps[0].Batch != 8 || cps[7].FirstKey != 1 || cps[0].LastKey != 1000 {
		t.Fatalf("checkpoints = %+v, %v", cps, err)
	}
}

func TestBatchFailureKeepsEarlierBatchesAndResumeSkipsThem(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 500)
	g := mustGate(t, target)
	plan, err := g.PrepareBatch(t.Context(), "exec-2", archive, 100)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected failure")
	p, err := g.RunBatches(t.Context(), plan, database.BatchHooks{BeforeCommit: func(batch int) error {
		if batch == 3 {
			return boom
		}
		return nil
	}})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if p.Batches != 2 || p.Rows != 200 || p.Complete {
		t.Fatalf("progress after failure = %+v", p)
	}
	if n := count(t, target, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 200 {
		t.Fatalf("%d rows archived after failure, want the 200 committed", n)
	}
	g.Close()
	// A restarted worker takes a new gate and resumes from the ledger.
	resumed := mustGate(t, target)
	plan, err = resumed.PrepareBatch(t.Context(), "exec-2", archive, 100)
	if err != nil {
		t.Fatal(err)
	}
	var batches []int
	p, err = resumed.RunBatches(t.Context(), plan, database.BatchHooks{BeforeCommit: func(batch int) error {
		batches = append(batches, batch)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(batches) != "[3 4 5 6]" || p.Batches != 5 || p.Rows != 500 || !p.Complete {
		t.Fatalf("resume ran batches %v, progress %+v", batches, p)
	}
}

func TestBatchScopeIsCapturedAtPrepare(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 50)
	g := mustGate(t, target)
	plan, err := g.PrepareBatch(t.Context(), "exec-3", archive, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(t.Context(), `INSERT INTO accounts SELECT g, 'active', '' FROM generate_series(51, 60) g`); err != nil {
		t.Fatal(err)
	}
	p, err := g.RunBatches(t.Context(), plan, database.BatchHooks{})
	if err != nil || p.Rows != 50 {
		t.Fatalf("progress = %+v, %v", p, err)
	}
	if n := count(t, target, `SELECT count(*) FROM accounts WHERE id > 50 AND status='active'`); n != 10 {
		t.Fatal("rows added after the scope was captured were updated")
	}
	if _, err := g.PrepareBatch(t.Context(), "exec-3", database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "other"}, 20); !errors.Is(err, database.ErrConflict) {
		t.Fatalf("re-prepare with another template = %v, want ErrConflict", err)
	}
}

func TestBatchOnEmptyTableCompletes(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 0)
	g := mustGate(t, target)
	plan, err := g.PrepareBatch(t.Context(), "exec-empty", archive, 10)
	if err != nil {
		t.Fatal(err)
	}
	p, err := g.RunBatches(t.Context(), plan, database.BatchHooks{})
	if err != nil || p.Batches != 0 || !p.Complete {
		t.Fatalf("progress = %+v, %v", p, err)
	}
}

func TestBatchTemplateValidation(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 3)
	for _, sql := range []string{
		`CREATE TABLE loose (id bigint, status text)`,
		`CREATE TABLE texty (code text PRIMARY KEY, status text)`,
		`CREATE TABLE typed (id int PRIMARY KEY, amount integer)`,
	} {
		if _, err := target.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	g := mustGate(t, target)
	cases := []struct {
		name string
		spec database.BatchUpdate
		size int
		want error
	}{
		{"zero size", archive, 0, database.ErrValidation},
		{"oversized", archive, database.MaxBatchSize + 1, database.ErrValidation},
		{"injected identifier", database.BatchUpdate{Schema: "public", Table: "accounts; DROP TABLE accounts", Key: "id", Column: "status"}, 10, database.ErrValidation},
		{"internal schema", database.BatchUpdate{Schema: "sqlwise_internal", Table: "batch_rows", Key: "row_key", Column: "after_value"}, 10, database.ErrValidation},
		{"key is column", database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "id"}, 10, database.ErrValidation},
		{"missing table", database.BatchUpdate{Schema: "public", Table: "nope", Key: "id", Column: "status"}, 10, database.ErrNotFound},
		{"non-unique key", database.BatchUpdate{Schema: "public", Table: "loose", Key: "id", Column: "status"}, 10, database.ErrValidation},
		{"text key", database.BatchUpdate{Schema: "public", Table: "texty", Key: "code", Column: "status"}, 10, database.ErrValidation},
		{"invalid value", database.BatchUpdate{Schema: "public", Table: "typed", Key: "id", Column: "amount", Value: "1; DROP TABLE typed"}, 10, database.ErrValidation},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := g.PrepareBatch(t.Context(), fmt.Sprintf("bad-%d", i), c.spec, c.size)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if n := count(t, target, `SELECT count(*) FROM accounts`); n != 3 {
		t.Fatal("validation changed the target")
	}
}

func TestCommitBatchNeverCommitsACheckpointTwice(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 10)
	g := mustGate(t, target)
	plan, err := g.PrepareBatch(t.Context(), "exec-4", archive, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.RunBatches(t.Context(), plan, database.BatchHooks{}); err != nil {
		t.Fatal(err)
	}
	// A compensating callback restores rows and checkpoints them atomically.
	restore := func(ctx context.Context, tx database.BatchTx) (database.Checkpoint, error) {
		n, err := tx.Exec(ctx, `UPDATE accounts a SET status=r.before_value FROM sqlwise_internal.batch_rows r
			WHERE r.execution_id='exec-4' AND r.batch_no=1 AND a.id=r.row_key AND a.status IS NOT DISTINCT FROM r.after_value`)
		return database.Checkpoint{Batch: 1, FirstKey: 1, LastKey: 10, Rows: n}, err
	}
	if _, err := g.CommitBatch(t.Context(), "exec-4", database.Compensate, restore, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(t.Context(), `UPDATE accounts SET status='archived' WHERE id <= 5`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CommitBatch(t.Context(), "exec-4", database.Compensate, restore, nil); !errors.Is(err, database.ErrConflict) {
		t.Fatalf("second compensation = %v, want ErrConflict", err)
	}
	// The duplicate's own writes rolled back with its rejected checkpoint.
	if n := count(t, target, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 5 {
		t.Fatalf("%d archived rows, duplicate compensation leaked writes", n)
	}
	p, err := g.BatchProgress(t.Context(), "exec-4")
	if err != nil || p.Compensated != 1 {
		t.Fatalf("progress = %+v, %v", p, err)
	}
}

func TestLostGateDuringBatchCommitIsReconciledFromTheLedger(t *testing.T) {
	target := migrationtest.TargetDatabase(t)
	seed(t, target, 30)
	g := mustGate(t, target)
	plan, err := g.PrepareBatch(t.Context(), "exec-5", archive, 10)
	if err != nil {
		t.Fatal(err)
	}
	p, err := g.RunBatches(t.Context(), plan, database.BatchHooks{BeforeCommit: func(batch int) error {
		if batch == 2 {
			_, err := target.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, g.PID())
			return err
		}
		return nil
	}})
	if err == nil {
		t.Fatal("a batch committed after its gate session was terminated")
	}
	if !errors.Is(err, database.ErrOutcomeUnknown) && !errors.Is(err, database.ErrConnectionUnavailable) {
		t.Fatalf("err = %v, want unknown or unavailable", err)
	}
	if p != (database.BatchProgress{}) {
		t.Fatalf("a lost gate reported progress %+v from memory", p)
	}
	next := mustGate(t, target)
	got, err := next.BatchProgress(t.Context(), "exec-5")
	if err != nil || got.Batches != 1 || got.Rows != 10 {
		t.Fatalf("ledger = %+v, %v; want only batch 1", got, err)
	}
	if n := count(t, target, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 10 {
		t.Fatalf("%d rows archived, want exactly the committed batch", n)
	}
}
