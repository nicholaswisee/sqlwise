package jobs_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/database"
	"sqlwise/internal/jobs"
	"sqlwise/internal/jobs/jobstest"
	"sqlwise/internal/migration/migrationtest"
)

// consumer stands in for the migration domain: it returns the execution of
// an already-queued request row.
type consumer struct{}

func (consumer) Consume(ctx context.Context, tx pgx.Tx, databaseID, requestID string) (jobs.Consumed, error) {
	var c jobs.Consumed
	err := tx.QueryRow(ctx, `SELECT execution_id, migration_id, requested_by FROM migration_requests WHERE id=$1 AND database_id=$2`,
		requestID, databaseID).Scan(&c.ExecutionID, &c.MigrationID, &c.ActorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, &jobs.Error{Kind: jobs.ErrNotFound, Message: "Request not found for this database."}
	}
	c.Operation = "up"
	return c, err
}

type runner func(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome

func (r runner) Run(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
	return r(ctx, gate, job, progress)
}

type fixture struct {
	p       *migrationtest.Platform
	store   *jobs.Store
	queue   *jobs.Queue
	gates   *jobstest.Gates
	targets map[string]*pgxpool.Pool
	actor   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	p := migrationtest.NewPlatform(t)
	p.Database(t, "a", "A")
	p.Database(t, "b", "B")
	actor := p.User(t, "jobs-actor", nil)
	store := jobstest.Store(t, p)
	f := &fixture{p: p, store: store, queue: jobs.NewQueue(store, consumer{}), gates: jobstest.NewGates(),
		targets: map[string]*pgxpool.Pool{"a": migrationtest.TargetDatabase(t), "b": migrationtest.TargetDatabase(t)}, actor: actor.ID}
	for id, pool := range f.targets {
		f.gates.Add(id, pool)
	}
	return f
}

func (f *fixture) enqueue(t *testing.T, databaseID string) jobs.Job {
	t.Helper()
	request, _, _ := jobstest.Request(t, f.p, databaseID, f.actor)
	j, created, err := f.queue.Enqueue(t.Context(), databaseID, request, "key-"+request)
	if err != nil || !created {
		t.Fatalf("enqueue = %+v, %v, %v", j, created, err)
	}
	return j
}

func (f *fixture) worker(t *testing.T, id string, r jobs.Runner, tune ...func(*jobs.Config)) *jobs.Worker {
	t.Helper()
	cfg := jobstest.Config(id)
	for _, fn := range tune {
		fn(&cfg)
	}
	w, err := jobs.NewWorker(f.store, f.gates, r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (f *fixture) wait(t *testing.T, j jobs.Job) jobs.Job {
	t.Helper()
	return jobstest.Wait(t, f.store, j.DatabaseID, j.ID, 15*time.Second, jobstest.Settled)
}

func succeed(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
	return jobs.Success()
}

func TestEnqueueIsDurableIdempotentAndScopedToItsDatabase(t *testing.T) {
	f := newFixture(t)
	request, execution, _ := jobstest.Request(t, f.p, "a", f.actor)
	j, created, err := f.queue.Enqueue(t.Context(), "a", request, "key-1")
	if err != nil || !created || j.Status != jobs.Queued || j.Effect != jobs.EffectNone || j.ExecutionID != execution || j.Attempts != 0 {
		t.Fatalf("enqueue = %+v, %v, %v", j, created, err)
	}
	again, created, err := f.queue.Enqueue(t.Context(), "a", request, "key-1")
	if err != nil || created || again.ID != j.ID {
		t.Fatalf("repeat = %+v, %v, %v; want the same job", again, created, err)
	}
	other, created, err := f.queue.Enqueue(t.Context(), "a", request, "key-2")
	if err != nil || created || other.ID != j.ID {
		t.Fatalf("new key for the same request = %+v, %v, %v; want the same job", other, created, err)
	}
	second, _, _ := jobstest.Request(t, f.p, "a", f.actor)
	if _, _, err := f.queue.Enqueue(t.Context(), "a", second, "key-1"); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("key reused for another request = %v, want ErrConflict", err)
	}
	if _, _, err := f.queue.Enqueue(t.Context(), "a", second, ""); !errors.Is(err, jobs.ErrValidation) {
		t.Fatalf("missing key = %v, want ErrValidation", err)
	}
	if _, _, err := f.queue.Enqueue(t.Context(), "b", request, "key-3"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("request of another database = %v, want ErrNotFound", err)
	}
	if _, err := f.queue.Job(t.Context(), "b", j.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("job read through another database = %v, want ErrNotFound", err)
	}
	var count int
	if err := f.p.Pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%d jobs published, want 1", count)
	}
	if f.gates.Opens() != 0 {
		t.Fatal("enqueue touched a target")
	}
}

func TestWorkerRunsJobOnTheGateAndRecordsSuccess(t *testing.T) {
	f := newFixture(t)
	if _, err := f.targets["a"].Exec(t.Context(), `CREATE TABLE marks (job text)`); err != nil {
		t.Fatal(err)
	}
	j := f.enqueue(t, "a")
	var holder atomic.Bool
	jobstest.Start(t, f.worker(t, "w", runner(func(ctx context.Context, gate *database.Gate, job jobs.Job, _ jobs.Progress) jobs.Outcome {
		holder.Store(gate.Held(ctx) == nil)
		if _, err := gate.Exec(ctx, `INSERT INTO marks VALUES ($1)`, job.ID); err != nil {
			return jobs.Failure(err.Error())
		}
		return jobs.Success()
	})))
	done := f.wait(t, j)
	if done.Status != jobs.Succeeded || done.Effect != jobs.EffectCommitted || done.Attempts != 1 || done.StartedAt == nil || done.FinishedAt == nil {
		t.Fatalf("job = %+v", done)
	}
	if !holder.Load() {
		t.Fatal("runner did not hold the target gate")
	}
	var n int
	if err := f.targets["a"].QueryRow(t.Context(), `SELECT count(*) FROM marks WHERE job=$1`, j.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("target marks = %d, %v", n, err)
	}
}

func TestTransientFailuresRetryWithFrozenBackoffThenStop(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	var mu sync.Mutex
	var at []time.Time
	jobstest.Start(t, f.worker(t, "w", runner(func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		return jobs.Transient("Target briefly unavailable.")
	})))
	done := f.wait(t, j)
	if done.Status != jobs.Failed || done.Attempts != jobs.MaxAttempts || done.Effect != jobs.EffectNone {
		t.Fatalf("job = %+v", done)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 3 {
		t.Fatalf("%d deliveries, want 3", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < time.Second {
		t.Fatalf("first retry after %v, want at least 1s", gap)
	}
	if gap := at[2].Sub(at[1]); gap < 2*time.Second {
		t.Fatalf("second retry after %v, want at least 2s", gap)
	}
}

func TestTransientFailureThenSuccessKeepsTheAttemptCount(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	var calls atomic.Int32
	jobstest.Start(t, f.worker(t, "w", runner(func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
		if calls.Add(1) == 1 {
			return jobs.Transient("Target briefly unavailable.")
		}
		return jobs.Success()
	})))
	if done := f.wait(t, j); done.Status != jobs.Succeeded || done.Attempts != 2 {
		t.Fatalf("job = %+v", done)
	}
	// The retry is a new delivery of the same job and logical execution.
	rows, err := f.p.Pool.Query(t.Context(), `SELECT id, worker_id, outcome FROM job_deliveries WHERE job_id=$1 ORDER BY claimed_at`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids, outcomes []string
	for rows.Next() {
		var id, worker, outcome string
		if err := rows.Scan(&id, &worker, &outcome); err != nil {
			t.Fatal(err)
		}
		ids, outcomes = append(ids, id), append(outcomes, worker+":"+outcome)
	}
	if rows.Err() != nil || len(ids) != 2 || ids[0] == ids[1] || ids[0] == j.ID || ids[0] == j.ExecutionID ||
		strings.Join(outcomes, ",") != "w:requeued,w:succeeded" {
		t.Fatalf("deliveries %v %v, want two distinct IDs", ids, outcomes)
	}
}

func TestNonTransientFailuresAreNeverRetried(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	var calls atomic.Int32
	jobstest.Start(t, f.worker(t, "w", runner(func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
		calls.Add(1)
		return jobs.Failure("The requester no longer has edit access to this database. Nothing was executed.")
	})))
	done := f.wait(t, j)
	time.Sleep(300 * time.Millisecond)
	if done.Status != jobs.Failed || done.Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("job = %+v after %d deliveries", done, calls.Load())
	}
}

func TestUnreachableTargetIsRetriedThenFailsWithoutRunning(t *testing.T) {
	f := newFixture(t)
	p := f.p
	p.Database(t, "gone", "Gone")
	request, _, _ := jobstest.Request(t, p, "gone", f.actor)
	j, _, err := f.queue.Enqueue(t.Context(), "gone", request, "k")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	jobstest.Start(t, f.worker(t, "w", runner(func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
		calls.Add(1)
		return jobs.Success()
	})))
	done := f.wait(t, j)
	if done.Status != jobs.Failed || done.Attempts != 3 || calls.Load() != 0 {
		t.Fatalf("job = %+v, runner calls %d", done, calls.Load())
	}
}

type span struct{ start, end time.Time }

func TestSameDatabaseIsSerializedAcrossWorkersAndOthersRunInParallel(t *testing.T) {
	f := newFixture(t)
	a1, a2, b1 := f.enqueue(t, "a"), f.enqueue(t, "a"), f.enqueue(t, "b")
	var mu sync.Mutex
	spans := map[string]span{}
	slow := runner(func(ctx context.Context, gate *database.Gate, job jobs.Job, _ jobs.Progress) jobs.Outcome {
		start := time.Now()
		if _, err := gate.Exec(ctx, `SELECT pg_sleep(0.6)`); err != nil {
			return jobs.Failure(err.Error())
		}
		mu.Lock()
		spans[job.ID] = span{start, time.Now()}
		mu.Unlock()
		return jobs.Success()
	})
	// Two workers race for the same database; only the gate decides.
	for _, id := range []string{"w1", "w2"} {
		jobstest.Start(t, f.worker(t, id, slow, func(c *jobs.Config) { c.LockWait = 2 * time.Second }))
	}
	for _, j := range []jobs.Job{a1, a2, b1} {
		if done := f.wait(t, j); done.Status != jobs.Succeeded {
			t.Fatalf("job = %+v", done)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	overlap := func(x, y span) bool { return x.start.Before(y.end) && y.start.Before(x.end) }
	if overlap(spans[a1.ID], spans[a2.ID]) {
		t.Fatal("two jobs ran on the same database at once")
	}
	if !overlap(spans[b1.ID], spans[a1.ID]) && !overlap(spans[b1.ID], spans[a2.ID]) {
		t.Fatal("a job on another database waited for this database")
	}
}

func TestPartialEffectBlocksTheDatabaseUntilApprovedRecovery(t *testing.T) {
	f := newFixture(t)
	first := f.enqueue(t, "a")
	var resumed atomic.Bool
	r := runner(func(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
		switch {
		case job.ID == first.ID:
			progress(database.Forward, database.BatchProgress{Batches: 2, Rows: 200, TotalRows: 500})
			return jobs.Partial("Batch 3 failed; 2 batches stay committed.")
		case job.Operation == jobs.OpResume:
			resumed.Store(true)
		}
		return jobs.Success()
	})
	stop := jobstest.Start(t, f.worker(t, "w", r))
	blocked := f.wait(t, first)
	if blocked.Status != jobs.Blocked || blocked.Effect != jobs.EffectPartial || blocked.CommittedBatches != 2 || blocked.CommittedRows != 200 ||
		blocked.TotalRows == nil || *blocked.TotalRows != 500 {
		t.Fatalf("partial job = %+v", blocked)
	}
	if got := blocked.Recovery(); len(got) != 2 {
		t.Fatalf("recovery = %v, want resume and compensate", got)
	}
	later, elsewhere := f.enqueue(t, "a"), f.enqueue(t, "b")
	if done := f.wait(t, elsewhere); done.Status != jobs.Succeeded {
		t.Fatalf("other database = %+v", done)
	}
	time.Sleep(500 * time.Millisecond)
	waiting, err := f.store.Get(t.Context(), "a", later.ID)
	if err != nil || waiting.Status != jobs.Queued || waiting.BlockedBy != first.ID || waiting.Attempts != 0 {
		t.Fatalf("later job on the blocked database = %+v, %v", waiting, err)
	}
	stop()
	// The approved recovery request publishes its own job referencing the execution.
	request, execution, migration := jobstest.Request(t, f.p, "a", f.actor)
	recovery := jobstest.Insert(t, f.p, jobs.Job{DatabaseID: "a", MigrationID: migration, RequestID: request, ExecutionID: execution,
		RecoversExecutionID: first.ExecutionID, Operation: jobs.OpResume, ActorID: f.actor})
	jobstest.Start(t, f.worker(t, "w2", r))
	if done := f.wait(t, recovery); done.Status != jobs.Succeeded || !resumed.Load() {
		t.Fatalf("recovery = %+v", done)
	}
	if done := f.wait(t, later); done.Status != jobs.Succeeded {
		t.Fatalf("later job after recovery = %+v", done)
	}
	if j, _ := f.store.Get(t.Context(), "a", first.ID); j.ResolvedBy != recovery.ID || len(j.Recovery()) != 0 {
		t.Fatalf("blocked job after recovery = %+v", j)
	}
}

func TestUnknownOutcomeIsReconciledNotRetried(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	var calls atomic.Int32
	jobstest.Start(t, f.worker(t, "w", runner(func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
		if calls.Add(1) == 1 {
			return jobs.Unknown("The connection failed while committing.")
		}
		// Redelivery reconciles the recorded execution from its receipt.
		return jobs.Success()
	})))
	blocked := jobstest.Wait(t, f.store, "a", j.ID, 5*time.Second, func(j jobs.Job) bool { return j.Status != jobs.Queued && j.Status != jobs.Running })
	if blocked.Status != jobs.Blocked || blocked.Effect != jobs.EffectUnknown {
		t.Fatalf("job = %+v, want blocked unknown first", blocked)
	}
	done := f.wait(t, j)
	if done.Status != jobs.Succeeded || done.Attempts != 1 || calls.Load() != 2 {
		t.Fatalf("job = %+v after %d deliveries; reconciliation must not spend attempts", done, calls.Load())
	}
}

func TestExpiredClaimWaitsForTheLiveGateBeforeRunning(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	// A stalled worker still holds the target gate after its lease expired.
	stalled, err := f.gates.Gate(t.Context(), "a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.p.Exec(t, `UPDATE jobs SET status='running', claimed_by='stalled', lease_until=now() - interval '1 second', attempts=1 WHERE id=$1`, j.ID)
	var ran atomic.Int32
	var released atomic.Bool
	jobstest.Start(t, f.worker(t, "w", runner(func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome {
		if !released.Load() {
			t.Error("job ran while the stalled worker still held the gate")
		}
		ran.Add(1)
		return jobs.Success()
	})))
	time.Sleep(800 * time.Millisecond)
	if got, _ := f.store.Get(t.Context(), "a", j.ID); got.Status == jobs.Succeeded || got.Attempts != 1 {
		t.Fatalf("job while gate held = %+v; deferrals must not spend attempts", got)
	}
	released.Store(true)
	stalled.Close()
	if done := f.wait(t, j); done.Status != jobs.Succeeded || done.Attempts != 2 || ran.Load() != 1 {
		t.Fatalf("job = %+v, runs %d", done, ran.Load())
	}
}

func TestRestartWithStableIDReclaimsItsOwnClaimsAtOnce(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	f.p.Exec(t, `UPDATE jobs SET status='running', claimed_by='w-stable', lease_until=now() + interval '1 hour' WHERE id=$1`, j.ID)
	jobstest.Start(t, f.worker(t, "w-stable", runner(succeed)))
	if done := jobstest.Wait(t, f.store, "a", j.ID, 3*time.Second, jobstest.Settled); done.Status != jobs.Succeeded {
		t.Fatalf("job = %+v", done)
	}
}

func TestShutdownFinishesInFlightDeliveries(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	started := make(chan struct{})
	stop := jobstest.Start(t, f.worker(t, "w", runner(func(ctx context.Context, gate *database.Gate, _ jobs.Job, _ jobs.Progress) jobs.Outcome {
		close(started)
		if _, err := gate.Exec(ctx, `SELECT pg_sleep(0.5)`); err != nil {
			return jobs.Failure(err.Error())
		}
		return jobs.Success()
	})))
	<-started
	stop()
	got, err := f.store.Get(t.Context(), "a", j.ID)
	if err != nil || got.Status != jobs.Succeeded {
		t.Fatalf("job after graceful stop = %+v, %v", got, err)
	}
}

func TestWorkerRejectsUnsafeConfiguration(t *testing.T) {
	f := newFixture(t)
	for _, tune := range []func(*jobs.Config){
		func(c *jobs.Config) { c.Concurrency = 0 },
		func(c *jobs.Config) { c.Lease = 0 },
		func(c *jobs.Config) { c.JobTimeout = 0 },
		func(c *jobs.Config) { c.Backoff = nil },
	} {
		cfg := jobstest.Config("w")
		tune(&cfg)
		if _, err := jobs.NewWorker(f.store, f.gates, runner(succeed), cfg); err == nil {
			t.Fatalf("config %+v accepted", cfg)
		}
	}
}

func TestFinishedJobsAreImmutable(t *testing.T) {
	f := newFixture(t)
	j := f.enqueue(t, "a")
	jobstest.Start(t, f.worker(t, "w", runner(succeed)))
	f.wait(t, j)
	if _, err := f.p.Pool.Exec(t.Context(), `UPDATE jobs SET status='queued', finished_at=NULL WHERE id=$1`, j.ID); err == nil {
		t.Fatal("a finished job was requeued")
	}
	if _, err := f.p.Pool.Exec(t.Context(), `DELETE FROM jobs WHERE id=$1`, j.ID); err == nil {
		t.Fatal("a job was deleted")
	}
}
