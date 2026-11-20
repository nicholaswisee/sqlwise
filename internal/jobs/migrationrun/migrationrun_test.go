package migrationrun_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/database"
	"sqlwise/internal/jobs"
	"sqlwise/internal/jobs/jobstest"
	"sqlwise/internal/jobs/migrationrun"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
)

// flow is a reviewed database "db" (Editors edit, Auditors view) with the
// durable queue in front of the real migration service.
type flow struct {
	p                 *migrationtest.Platform
	target            *pgxpool.Pool
	service           *migration.Service
	store             *jobs.Store
	queue             migrationrun.Queue
	gates             *jobstest.Gates
	editor, auditor   migrationtest.User
	editors, auditors string
}

func newFlow(t *testing.T) *flow {
	t.Helper()
	p := migrationtest.NewPlatform(t)
	p.Database(t, "db", "Shop")
	f := &flow{p: p, target: migrationtest.TargetDatabase(t), gates: jobstest.NewGates()}
	targets := migrationtest.NewTargets()
	targets.Add("db", f.target)
	f.gates.Add("db", f.target)
	editor, auditor := p.User(t, "editor", nil), p.User(t, "auditor", nil)
	f.editors = p.Group(t, "Editors", map[string]string{"db": "edit"}, editor.ID, editor.ID)
	f.auditors = p.Group(t, "Auditors", map[string]string{"db": "view"}, auditor.ID, auditor.ID)
	f.login(t)
	f.service = migration.NewService(migration.NewStore(p.Pool), p.Access, targets, time.Now)
	f.store = jobstest.Store(t, p)
	f.queue = migrationrun.Queue{Jobs: jobs.NewQueue(f.store, migrationrun.Consumer{Service: f.service})}
	return f
}

func (f *flow) login(t *testing.T) {
	t.Helper()
	f.editor, f.auditor = f.p.Login(t, "editor"), f.p.Login(t, "auditor")
}

func (f *flow) worker(t *testing.T, r jobs.Runner) func() {
	t.Helper()
	w, err := jobs.NewWorker(f.store, f.gates, r, jobstest.Config("w-"+jobstest.ID(t)[:6]))
	if err != nil {
		t.Fatal(err)
	}
	return jobstest.Start(t, w)
}

func (f *flow) runner() migrationrun.Runner { return migrationrun.Runner{Service: f.service} }

// approved defines a migration and takes op through review.
func (f *flow) approved(t *testing.T, d migration.Draft, op migration.Operation) migration.Request {
	t.Helper()
	detail, err := f.service.Detail(f.editor.Ctx, "db", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := f.service.CreateRequest(f.editor.Ctx, "db", d.ID, migration.RequestInput{Operation: op,
		ExpectedRevision: detail.Draft.Revision, ExpectedDefinition: detail.Draft.DefinitionHash(), ExpectedVersion: detail.CurrentVersion})
	if err != nil {
		t.Fatal(err)
	}
	if req, err = f.service.SubmitRequest(f.editor.Ctx, "db", req.ID); err != nil {
		t.Fatal(err)
	}
	for _, leader := range []struct {
		user  migrationtest.User
		group string
	}{{f.editor, f.editors}, {f.auditor, f.auditors}} {
		if req, err = f.service.Decide(leader.user.Ctx, "db", req.ID, leader.group, true); err != nil {
			t.Fatal(err)
		}
	}
	return req
}

func (f *flow) define(t *testing.T, title, up, down string) migration.Draft {
	t.Helper()
	d, err := f.service.Create(f.editor.Ctx, "db", migration.DraftInput{Title: title, SQL: up, SQLDown: down})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *flow) enqueue(t *testing.T, req migration.Request) migration.Job {
	t.Helper()
	job, err := f.queue.Enqueue(f.editor.Ctx, "db", req.ID, "key-"+req.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *flow) settled(t *testing.T, jobID string) jobs.Job {
	t.Helper()
	return jobstest.Wait(t, f.store, "db", jobID, 20*time.Second, jobstest.Settled)
}

func (f *flow) has(t *testing.T, table string) bool {
	t.Helper()
	var exists bool
	if err := f.target.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func (f *flow) count(t *testing.T, sql string) int64 {
	t.Helper()
	var n int64
	if err := f.target.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApprovedRequestWaitsForTheWorkerAndNeverRunsInline(t *testing.T) {
	f := newFlow(t)
	d := f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets")
	job := f.enqueue(t, f.approved(t, d, migration.OpUp))
	if job.Status != "queued" || job.ExecutionID == "" {
		t.Fatalf("job = %+v", job)
	}
	time.Sleep(500 * time.Millisecond)
	if f.has(t, "widgets") || f.gates.Opens() != 0 {
		t.Fatal("the target changed before any worker ran")
	}
	if got, err := f.queue.Job(f.auditor.Ctx, "db", job.ID); err != nil || got.Status != "queued" {
		t.Fatalf("polled job = %+v, %v", got, err)
	}
	f.worker(t, f.runner())
	done := f.settled(t, job.ID)
	if done.Status != jobs.Succeeded || done.Effect != jobs.EffectCommitted || done.Attempts != 1 || !f.has(t, "widgets") {
		t.Fatalf("job = %+v", done)
	}
	h, err := f.service.History(f.auditor.Ctx, "db")
	if err != nil || len(h.Committed) != 1 || h.Committed[0].ID != job.ExecutionID {
		t.Fatalf("committed history = %+v, %v", h.Committed, err)
	}
}

func TestEveryOperationIsQueuedWithItsOwnExecution(t *testing.T) {
	f := newFlow(t)
	f.worker(t, f.runner())
	d := f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets")
	var executions []string
	for _, step := range []struct {
		op     migration.Operation
		exists bool
	}{{migration.OpUp, true}, {migration.OpRollback, false}, {migration.OpForward, true}} {
		job := f.enqueue(t, f.approved(t, d, step.op))
		if done := f.settled(t, job.ID); done.Status != jobs.Succeeded || done.Operation != string(step.op) {
			t.Fatalf("%s job = %+v", step.op, done)
		}
		if f.has(t, "widgets") != step.exists {
			t.Fatalf("after %s widgets exists = %v", step.op, !step.exists)
		}
		executions = append(executions, job.ExecutionID)
	}
	if executions[0] == executions[1] || executions[1] == executions[2] || executions[0] == executions[2] {
		t.Fatalf("executions %v must be distinct", executions)
	}
}

func TestEnqueueRefusesUnapprovedRequestsAndViewers(t *testing.T) {
	f := newFlow(t)
	d := f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets")
	detail, _ := f.service.Detail(f.editor.Ctx, "db", d.ID)
	draft, err := f.service.CreateRequest(f.editor.Ctx, "db", d.ID, migration.RequestInput{Operation: migration.OpUp,
		ExpectedRevision: detail.Draft.Revision, ExpectedDefinition: detail.Draft.DefinitionHash()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.Enqueue(f.editor.Ctx, "db", draft.ID, "k1"); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("unapproved enqueue = %v", err)
	}
	approved := f.approved(t, f.define(t, "Gadgets", "CREATE TABLE gadgets (id int)", "DROP TABLE gadgets"), migration.OpUp)
	if _, err := f.queue.Enqueue(f.auditor.Ctx, "db", approved.ID, "k2"); err == nil {
		t.Fatal("a view-only leader enqueued an execution")
	}
	if _, err := f.queue.Job(f.editor.Ctx, "db", "0123456789abcdef0123456789abcdef"); !errors.Is(err, migration.ErrNotFound) {
		t.Fatalf("unknown job = %v", err)
	}
	var n int
	if err := f.p.Pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d jobs published by refused requests", n)
	}
}

func TestRequesterRevokedAfterEnqueueWritesNothing(t *testing.T) {
	f := newFlow(t)
	d := f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets")
	job := f.enqueue(t, f.approved(t, d, migration.OpUp))
	if err := f.p.Access.RemoveMembership(f.p.Admin, f.editor.ID, f.editors); err != nil {
		t.Fatal(err)
	}
	f.worker(t, f.runner())
	done := f.settled(t, job.ID)
	if done.Status != jobs.Failed || done.Effect != jobs.EffectNone || done.Attempts != 1 || f.has(t, "widgets") {
		t.Fatalf("job for a revoked requester = %+v", done)
	}
	if done.Error == "" || strings.Contains(done.Error, "SQLSTATE") {
		t.Fatalf("job error %q must be a safe explanation", done.Error)
	}
}

// ackLost runs the real execution, then reports a lost acknowledgement once.
type ackLost struct {
	migrationrun.Runner
	lost atomic.Bool
}

func (r *ackLost) Run(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
	out := r.Runner.Run(ctx, gate, job, progress)
	if out.Status == jobs.Succeeded && r.lost.CompareAndSwap(false, true) {
		return jobs.Unknown("The worker lost the result after the target committed.")
	}
	return out
}

func TestLostAcknowledgementIsReconciledWithoutReExecuting(t *testing.T) {
	f := newFlow(t)
	if _, err := f.target.Exec(t.Context(), `CREATE TABLE ledger (id serial PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	d := f.define(t, "Ledger entry", "INSERT INTO ledger DEFAULT VALUES", "DELETE FROM ledger")
	job := f.enqueue(t, f.approved(t, d, migration.OpUp))
	f.worker(t, &ackLost{Runner: f.runner()})
	done := f.settled(t, job.ID)
	if done.Status != jobs.Succeeded || done.Attempts != 1 {
		t.Fatalf("job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM ledger`); n != 1 {
		t.Fatalf("ledger has %d rows; redelivery re-executed the migration", n)
	}
}

func TestWorkerDeathAfterTargetCommitIsRecoveredOnce(t *testing.T) {
	f := newFlow(t)
	if _, err := f.target.Exec(t.Context(), `CREATE TABLE ledger (id serial PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	d := f.define(t, "Ledger entry", "INSERT INTO ledger DEFAULT VALUES", "DELETE FROM ledger")
	req := f.approved(t, d, migration.OpUp)
	job := f.enqueue(t, req)
	// The dead worker claimed the job, committed on the target, then vanished.
	f.p.Exec(t, `UPDATE jobs SET status='running', claimed_by='dead', lease_until=now() + interval '1 second', attempts=1 WHERE id=$1`, job.ID)
	gate, err := f.gates.Gate(t.Context(), "db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Run(t.Context(), migrationrun.Target(gate), req.ID); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	f.worker(t, f.runner())
	done := f.settled(t, job.ID)
	if done.Status != jobs.Succeeded || done.Attempts != 2 {
		t.Fatalf("recovered job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM ledger`); n != 1 {
		t.Fatalf("ledger has %d rows; recovery re-executed the migration", n)
	}
}

// batches serves one approved batch template per job ID.
type batches map[string]database.BatchUpdate

func (b batches) Batch(_ context.Context, job jobs.Job) (database.BatchUpdate, int, bool, error) {
	id := job.ID
	if job.RecoversExecutionID != "" {
		id = job.RecoversExecutionID
	}
	spec, ok := b[id]
	return spec, 100, ok, nil
}

func (f *flow) seedAccounts(t *testing.T, rows int) {
	t.Helper()
	for _, sql := range []string{
		`CREATE TABLE accounts (id bigint PRIMARY KEY, status text NOT NULL, tier text NOT NULL DEFAULT 'basic')`,
		`INSERT INTO accounts SELECT g, 'active' FROM generate_series(1, 500) g`,
		// A real SQL error in the middle of batch 3 while the switch row exists.
		`CREATE TABLE fail_switch (on_row bigint)`,
		`CREATE FUNCTION fail_on_switch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF EXISTS (SELECT 1 FROM fail_switch WHERE on_row = NEW.id) THEN RAISE EXCEPTION 'injected failure on row %', NEW.id; END IF;
			RETURN NEW; END $$`,
		`CREATE TRIGGER accounts_fail BEFORE UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION fail_on_switch()`,
	} {
		if _, err := f.target.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *flow) batchJob(t *testing.T, op, recovers string) jobs.Job {
	t.Helper()
	request, execution, migrationID := jobstest.Request(t, f.p, "db", f.editor.ID)
	return jobstest.Insert(t, f.p, jobs.Job{DatabaseID: "db", MigrationID: migrationID, RequestID: request, ExecutionID: execution,
		RecoversExecutionID: recovers, Operation: op, ActorID: f.editor.ID})
}

func TestBatchFailureStaysPartialBlocksTheTargetAndResumes(t *testing.T) {
	f := newFlow(t)
	f.seedAccounts(t, 500)
	if _, err := f.target.Exec(t.Context(), `INSERT INTO fail_switch VALUES (250)`); err != nil {
		t.Fatal(err)
	}
	archive := database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}
	src := batches{}
	r := migrationrun.Runner{Service: f.service, Batches: src}
	first := f.batchJob(t, "up", "")
	src[first.ID] = archive
	stop := f.worker(t, r)
	partial := f.settled(t, first.ID)
	if partial.Status != jobs.Blocked || partial.Effect != jobs.EffectPartial || partial.CommittedBatches != 2 || partial.CommittedRows != 200 {
		t.Fatalf("failed batch job = %+v", partial)
	}
	if !strings.Contains(partial.Error, "2 batches (200 rows) stay committed") {
		t.Fatalf("error %q must state the committed work", partial.Error)
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 200 {
		t.Fatalf("%d rows archived after failure, want the 200 committed", n)
	}
	// A later migration on the same target waits for recovery.
	later := f.batchJob(t, "up", "")
	src[later.ID] = database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "tier", Value: "gold"}
	time.Sleep(600 * time.Millisecond)
	if j, _ := f.store.Get(t.Context(), "db", later.ID); j.Status != jobs.Queued || j.BlockedBy != first.ID {
		t.Fatalf("later job = %+v, want queued behind %s", j, first.ID)
	}
	stop()
	if _, err := f.target.Exec(t.Context(), `DELETE FROM fail_switch`); err != nil {
		t.Fatal(err)
	}
	resume := f.batchJob(t, jobs.OpResume, first.ExecutionID)
	src[first.ExecutionID] = archive
	f.worker(t, r)
	done := f.settled(t, resume.ID)
	if done.Status != jobs.Succeeded || done.CommittedBatches != 5 || done.CommittedRows != 500 {
		t.Fatalf("resume job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 500 {
		t.Fatalf("%d rows archived after resume", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sqlwise_internal.batch_rows WHERE execution_id='`+first.ExecutionID+`'`); n != 500 {
		t.Fatalf("%d before-images; a batch was redone or lost", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sqlwise_internal.batch_checkpoints WHERE execution_id='`+first.ExecutionID+`'`); n != 5 {
		t.Fatalf("%d checkpoints, want 5", n)
	}
	if j := f.settled(t, later.ID); j.Status != jobs.Succeeded {
		t.Fatalf("later job after recovery = %+v", j)
	}
}

func TestSameTargetBatchJobsNeverInterleaveCommits(t *testing.T) {
	f := newFlow(t)
	f.seedAccounts(t, 500)
	src := batches{}
	r := migrationrun.Runner{Service: f.service, Batches: src}
	a, b := f.batchJob(t, "up", ""), f.batchJob(t, "up", "")
	src[a.ID] = database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}
	src[b.ID] = database.BatchUpdate{Schema: "public", Table: "accounts", Key: "id", Column: "tier", Value: "gold"}
	f.worker(t, r)
	f.worker(t, r)
	for _, j := range []jobs.Job{a, b} {
		if done := f.settled(t, j.ID); done.Status != jobs.Succeeded {
			t.Fatalf("job = %+v", done)
		}
	}
	// Every batch of one execution committed before the other's first batch.
	if n := f.count(t, `WITH s AS (SELECT execution_id, min(committed_at) lo, max(committed_at) hi FROM sqlwise_internal.batch_checkpoints GROUP BY execution_id)
		SELECT count(*) FROM s x JOIN s y ON x.execution_id < y.execution_id WHERE x.lo <= y.hi AND y.lo <= x.hi`); n != 0 {
		t.Fatal("batch commits of two jobs on one target interleaved")
	}
}
