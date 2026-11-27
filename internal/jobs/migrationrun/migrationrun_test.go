package migrationrun_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
	return f.review(t, req)
}

// review submits a request and approves it as both group leaders.
func (f *flow) review(t *testing.T, req migration.Request) migration.Request {
	t.Helper()
	req, err := f.service.SubmitRequest(f.editor.Ctx, "db", req.ID)
	if err != nil {
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

// Enqueue authorizes inside its own platform transaction. Taking a second pool
// connection for that check deadlocked once every connection held an enqueue.
func TestConcurrentEnqueuesDoNotExhaustThePlatformPool(t *testing.T) {
	f := newFlow(t)
	n := int(f.p.Pool.Config().MaxConns) * 3
	requests := make([]migration.Request, n)
	for i := range requests {
		table := fmt.Sprintf("w%d", i)
		requests[i] = f.approved(t, f.define(t, table, "CREATE TABLE "+table+" (id int)", "DROP TABLE "+table), migration.OpUp)
	}
	ctx, cancel := context.WithTimeout(f.editor.Ctx, 15*time.Second)
	defer cancel()
	errs := make(chan error, n)
	start := make(chan struct{})
	for _, req := range requests {
		go func() {
			<-start
			_, err := f.queue.Enqueue(ctx, "db", req.ID, "key-"+req.ID)
			errs <- err
		}()
	}
	close(start)
	for range n {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent enqueue: %v", err)
		}
	}
	var published int
	if err := f.p.Pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs`).Scan(&published); err != nil || published != n {
		t.Fatalf("%d of %d jobs published, %v", published, n, err)
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

func (f *flow) seedAccounts(t *testing.T) {
	t.Helper()
	for _, sql := range []string{
		`CREATE TABLE accounts (id bigint PRIMARY KEY, status text NOT NULL)`,
		`INSERT INTO accounts SELECT g, 'active' FROM generate_series(1, 500) g`,
		// A real SQL error in the middle of batch 3 while the switch row exists.
		`CREATE TABLE fail_switch (on_row bigint)`,
		`INSERT INTO fail_switch VALUES (250)`,
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

// partialBatch runs an approved batch migration that stops in batch 3.
func (f *flow) partialBatch(t *testing.T) (migration.Draft, jobs.Job) {
	t.Helper()
	f.seedAccounts(t)
	d, err := f.service.Create(f.editor.Ctx, "db", migration.DraftInput{Title: "Archive accounts", SQLDown: "UPDATE accounts SET status = 'active'",
		Batch: &migration.BatchSpec{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}, BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if d.SQL != `UPDATE "public"."accounts" SET "status" = 'archived';` {
		t.Fatalf("rendered SQL = %q", d.SQL)
	}
	job := f.enqueue(t, f.approved(t, d, migration.OpUp))
	partial := f.settled(t, job.ID)
	if partial.Status != jobs.Blocked || partial.Effect != jobs.EffectPartial || partial.CommittedBatches != 2 || partial.CommittedRows != 200 ||
		partial.TotalRows == nil || *partial.TotalRows != 500 {
		t.Fatalf("failed batch job = %+v", partial)
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 200 {
		t.Fatalf("%d rows archived after failure, want the 200 committed", n)
	}
	return d, partial
}

func (f *flow) recovery(t *testing.T, d migration.Draft, op migration.Operation, execution string) migration.Request {
	t.Helper()
	detail, err := f.service.Detail(f.editor.Ctx, "db", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := f.service.CreateRequest(f.editor.Ctx, "db", d.ID, migration.RequestInput{Operation: op, ExpectedRevision: detail.Draft.Revision,
		ExpectedDefinition: detail.Draft.DefinitionHash(), ExpectedVersion: detail.CurrentVersion, RecoversExecutionID: execution})
	if err != nil {
		t.Fatal(err)
	}
	return f.review(t, req)
}

func (f *flow) attempt(t *testing.T, id string) migration.Attempt {
	t.Helper()
	h, err := f.service.History(f.auditor.Ctx, "db")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range h.Attempts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("execution %s not found", id)
	return migration.Attempt{}
}

func TestPartialBatchBlocksTheTargetAndAnApprovedResumeCommitsHistory(t *testing.T) {
	f := newFlow(t)
	stop := f.worker(t, f.runner())
	d, first := f.partialBatch(t)
	if a := f.attempt(t, first.ExecutionID); a.Status != migration.StatusPartiallyApplied || !a.Batch {
		t.Fatalf("execution = %+v", a)
	}
	detail, err := f.service.Detail(f.editor.Ctx, "db", d.ID)
	if err != nil || detail.CurrentVersion != 0 || !strings.Contains(detail.Blocked, "partially applied in batches") {
		t.Fatalf("detail after partial = version %d, blocked %q, %v", detail.CurrentVersion, detail.Blocked, err)
	}
	// A later migration on the same target waits for recovery.
	later := f.enqueue(t, f.approved(t, f.define(t, "Later", "CREATE TABLE later (id int)", "DROP TABLE later"), migration.OpUp))
	time.Sleep(600 * time.Millisecond)
	if j, _ := f.store.Get(t.Context(), "db", later.ID); j.Status != jobs.Queued || j.BlockedBy != first.ID || f.has(t, "later") {
		t.Fatalf("later job = %+v, want queued behind %s", j, first.ID)
	}
	stop()
	if _, err := f.target.Exec(t.Context(), `DELETE FROM fail_switch`); err != nil {
		t.Fatal(err)
	}
	resumeReq := f.recovery(t, d, migration.OpResume, first.ExecutionID)
	resume := f.enqueue(t, resumeReq)
	f.worker(t, f.runner())
	done := f.settled(t, resume.ID)
	if done.Status != jobs.Succeeded || done.CommittedBatches != 5 || done.CommittedRows != 500 || done.RecoversExecutionID != first.ExecutionID {
		t.Fatalf("resume job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 500 {
		t.Fatalf("%d rows archived after resume", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sqlwise_internal.batch_checkpoints WHERE execution_id='`+first.ExecutionID+`'`); n != 5 {
		t.Fatalf("%d checkpoints; a batch was redone", n)
	}
	// History records the original execution only once the whole action completed.
	h, err := f.service.History(f.auditor.Ctx, "db")
	if err != nil || len(h.Committed) == 0 || h.Committed[0].ID != first.ExecutionID || h.Committed[0].AppliedVersion != 1 {
		t.Fatalf("committed history = %+v, %v", h.Committed, err)
	}
	if j, _ := f.store.Get(t.Context(), "db", first.ID); j.ResolvedBy != resume.ID {
		t.Fatalf("blocked job after resume = %+v", j)
	}
	// The waiting request was approved at version 0; the resumed migration became version 1.
	if j := f.settled(t, later.ID); j.Status != jobs.Failed || !strings.Contains(j.Error, "moved from version 0 to 1") || f.has(t, "later") {
		t.Fatalf("stale later job after recovery = %+v", j)
	}
	// Redelivering the finished resume reports its recorded outcome and changes nothing.
	gate, err := f.gates.Gate(t.Context(), "db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	if _, err := f.service.RunWith(t.Context(), migrationrun.Target(gate), migrationrun.Batches(gate, nil), resumeReq.ID); err != nil {
		t.Fatalf("redelivered resume = %v", err)
	}
}

func TestCompensationRevertsCommittedBatchesAndReleasesTheTarget(t *testing.T) {
	f := newFlow(t)
	f.worker(t, f.runner())
	d, first := f.partialBatch(t)
	job := f.enqueue(t, f.recovery(t, d, migration.OpCompensate, first.ExecutionID))
	done := f.settled(t, job.ID)
	if done.Status != jobs.Succeeded || done.CommittedBatches != 2 || done.CommittedRows != 200 {
		t.Fatalf("compensation job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status <> 'active'`); n != 0 {
		t.Fatalf("%d rows still changed after compensation", n)
	}
	a := f.attempt(t, first.ExecutionID)
	if a.Status != migration.StatusReverted || a.AppliedVersion != 0 {
		t.Fatalf("execution after compensation = %+v", a)
	}
	h, err := f.service.History(f.auditor.Ctx, "db")
	if err != nil || len(h.Committed) != 0 || h.CurrentVersion != 0 {
		t.Fatalf("a compensated execution entered history: %+v, %v", h.Committed, err)
	}
	later := f.enqueue(t, f.approved(t, f.define(t, "Later", "CREATE TABLE later (id int)", "DROP TABLE later"), migration.OpUp))
	if j := f.settled(t, later.ID); j.Status != jobs.Succeeded {
		t.Fatalf("job after compensation = %+v", j)
	}
}

func TestCompensationStopsOnExternalChangesAndKeepsTheTargetBlocked(t *testing.T) {
	f := newFlow(t)
	f.worker(t, f.runner())
	d, first := f.partialBatch(t)
	// An application changes a row of the newest committed batch.
	if _, err := f.target.Exec(t.Context(), `UPDATE accounts SET status='closed' WHERE id=150`); err != nil {
		t.Fatal(err)
	}
	job := f.enqueue(t, f.recovery(t, d, migration.OpCompensate, first.ExecutionID))
	stopped := f.settled(t, job.ID)
	if stopped.Status != jobs.Blocked || stopped.Effect != jobs.EffectPartial || !strings.Contains(stopped.Error, "changed after it committed") {
		t.Fatalf("compensation job = %+v", stopped)
	}
	if got := stopped.RecoveryTarget(); got != first.ExecutionID || len(stopped.Recovery()) != 2 {
		t.Fatalf("recovery offered for %q %v, want the original execution", got, stopped.Recovery())
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE id=150 AND status='closed'`); n != 1 {
		t.Fatal("compensation overwrote the external change")
	}
	if a := f.attempt(t, first.ExecutionID); a.Status != migration.StatusPartiallyApplied || !strings.Contains(a.Error, "Compensation stopped") {
		t.Fatalf("execution after stopped compensation = %+v", a)
	}
	later := f.enqueue(t, f.approved(t, f.define(t, "Later", "CREATE TABLE later (id int)", "DROP TABLE later"), migration.OpUp))
	time.Sleep(600 * time.Millisecond)
	if j, _ := f.store.Get(t.Context(), "db", later.ID); j.Status != jobs.Queued || j.BlockedBy != job.ID {
		t.Fatalf("later job = %+v, want it blocked by the stopped compensation", j)
	}
}

func TestInterruptedCompensationIsPartiallyRevertedAndCannotBeResumed(t *testing.T) {
	f := newFlow(t)
	f.worker(t, f.runner())
	d, first := f.partialBatch(t)
	// An application changes a row of the older batch: batch 2 is reverted, batch 1 stops.
	if _, err := f.target.Exec(t.Context(), `UPDATE accounts SET status='closed' WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	stopped := f.settled(t, f.enqueue(t, f.recovery(t, d, migration.OpCompensate, first.ExecutionID)).ID)
	if stopped.Status != jobs.Blocked || stopped.Effect != jobs.EffectPartial || stopped.CommittedBatches != 1 {
		t.Fatalf("compensation job = %+v", stopped)
	}
	if got := stopped.Recovery(); len(got) != 1 || got[0] != jobs.OpCompensate {
		t.Fatalf("recovery offered %v, want compensation only", got)
	}
	if a := f.attempt(t, first.ExecutionID); a.Status != migration.StatusPartiallyReverted || a.AppliedVersion != 0 {
		t.Fatalf("execution after interrupted compensation = %+v", a)
	}
	// A resume would skip the reverted batch yet record the migration as complete.
	detail, err := f.service.Detail(f.editor.Ctx, "db", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateRequest(f.editor.Ctx, "db", d.ID, migration.RequestInput{Operation: migration.OpResume,
		ExpectedRevision: detail.Draft.Revision, ExpectedDefinition: detail.Draft.DefinitionHash(), ExpectedVersion: detail.CurrentVersion,
		RecoversExecutionID: first.ExecutionID}); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("resume after partial compensation = %v, want a conflict", err)
	}
	if detail.Blocked == "" {
		t.Fatal("a partially reverted execution does not block the database")
	}
	// Once the outside change is undone, compensation finishes without reverting batch 2 again.
	if _, err := f.target.Exec(t.Context(), `UPDATE accounts SET status='archived' WHERE id=50`); err != nil {
		t.Fatal(err)
	}
	done := f.settled(t, f.enqueue(t, f.recovery(t, d, migration.OpCompensate, first.ExecutionID)).ID)
	if done.Status != jobs.Succeeded || done.CommittedBatches != 2 || done.CommittedRows != 200 {
		t.Fatalf("second compensation job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status <> 'active'`); n != 0 {
		t.Fatalf("%d rows still changed after compensation", n)
	}
	if a := f.attempt(t, first.ExecutionID); a.Status != migration.StatusReverted {
		t.Fatalf("execution after finished compensation = %+v", a)
	}
	h, err := f.service.History(f.auditor.Ctx, "db")
	if err != nil || len(h.Committed) != 0 {
		t.Fatalf("a compensated execution entered history: %+v, %v", h.Committed, err)
	}
	later := f.enqueue(t, f.approved(t, f.define(t, "Later", "CREATE TABLE later (id int)", "DROP TABLE later"), migration.OpUp))
	if j := jobstest.Wait(t, f.store, "db", later.ID, 2*time.Second, jobstest.Settled); j.Status != jobs.Succeeded || !f.has(t, "later") {
		t.Fatalf("job after second compensation = %+v", j)
	}
}

type faultRow struct{ err error }

func (r faultRow) Scan(...any) error { return r.err }

type startupFault struct {
	migration.Target
	stage string
}

func (s startupFault) QueryRow(ctx context.Context, sql string, args ...any) migration.Row {
	if s.stage == "provision" && strings.Contains(sql, "to_regclass") {
		return faultRow{&pgconn.PgError{Code: "08006"}}
	}
	return s.Target.QueryRow(ctx, sql, args...)
}

func (s startupFault) Begin(ctx context.Context) (migration.TargetTx, error) {
	if s.stage == "begin" {
		return nil, &pgconn.PgError{Code: "08006"}
	}
	return s.Target.Begin(ctx)
}

type runnerFunc func(context.Context, *database.Gate, jobs.Job, jobs.Progress) jobs.Outcome

func (r runnerFunc) Run(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
	return r(ctx, gate, job, progress)
}

func TestConfirmedStartupFailureCanRetryWithTheSameExecution(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		revoke      bool
	}{
		{"provision", "provision", false}, {"begin", "begin", false}, {"revoked", "begin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t)
			d := f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets")
			req := f.approved(t, d, migration.OpUp)
			job := f.enqueue(t, req)
			f.worker(t, runnerFunc(func(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
				if job.Attempts != 0 {
					return f.runner().Run(ctx, gate, job, progress)
				}
				target := startupFault{migrationrun.Target(gate), tc.stage}
				if _, err := f.service.Run(ctx, target, req.ID); !errors.Is(err, migration.ErrTargetUnavailable) {
					t.Errorf("first attempt = %v", err)
					return jobs.Failure("unexpected startup outcome")
				}
				var applied bool
				if err := gate.QueryRow(ctx, `SELECT to_regclass('widgets') IS NOT NULL`).Scan(&applied); err != nil || applied {
					t.Errorf("migration ran before retry: %v, %v", applied, err)
				}
				if tc.revoke {
					if _, err := f.p.Pool.Exec(ctx, `DELETE FROM group_memberships WHERE user_id=$1`, f.editor.ID); err != nil {
						t.Errorf("revoke: %v", err)
					}
				}
				return jobs.Transient("startup interrupted")
			}))
			done := f.settled(t, job.ID)
			if done.Attempts != 2 || done.ExecutionID != job.ExecutionID {
				t.Fatalf("retry did not reuse execution: %+v", done)
			}
			if tc.revoke {
				if done.Status != jobs.Failed || !strings.Contains(done.Error, "changed after review") || f.has(t, "widgets") {
					t.Fatalf("revoked retry = %+v", done)
				}
				return
			}
			if done.Status != jobs.Succeeded || !f.has(t, "widgets") || f.attempt(t, job.ExecutionID).Status != migration.StatusSucceeded {
				t.Fatalf("retried job = %+v", done)
			}
			var receipt string
			if err := f.target.QueryRow(t.Context(), `SELECT outcome FROM sqlwise_internal.migration_receipts WHERE attempt_id=$1`, job.ExecutionID).Scan(&receipt); err != nil || receipt != "applied" {
				t.Fatalf("receipt = %q, %v", receipt, err)
			}
		})
	}
}

type lostBatchAck struct {
	migration.BatchRunner
	gate   *database.Gate
	stopAt int
}

func (b lostBatchAck) hooks() database.BatchHooks {
	return database.BatchHooks{BeforeCommit: func(n int) error {
		if n == b.stopAt {
			return errors.New("batch stopped")
		}
		return nil
	}}
}

func (b lostBatchAck) Forward(ctx context.Context, id string, spec migration.BatchSpec, size int) (migration.BatchResult, error) {
	plan, err := b.gate.PrepareBatch(ctx, id, database.BatchUpdate(spec), size)
	if err != nil {
		return migration.BatchResult{}, err
	}
	_, _ = b.gate.RunBatches(ctx, plan, b.hooks())
	b.gate.Close()
	return migration.BatchResult{}, migration.ErrOutcomeUnknown
}

func (b lostBatchAck) Compensate(ctx context.Context, id string) (migration.BatchResult, error) {
	_, _ = b.gate.RevertBatches(ctx, id, b.hooks())
	b.gate.Close()
	return migration.BatchResult{}, migration.ErrOutcomeUnknown
}

func TestUncertainBatchForwardRequiresLedgerBeforeRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stopAt int
		status migration.Status
		effect jobs.Effect
		rows   int64
	}{
		{"zero", 1, migration.StatusFailed, jobs.EffectNone, 0},
		{"partial", 2, migration.StatusPartiallyApplied, jobs.EffectPartial, 100},
		{"complete", -1, migration.StatusSucceeded, jobs.EffectCommitted, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t)
			f.seedAccounts(t)
			if _, err := f.target.Exec(t.Context(), `DELETE FROM fail_switch`); err != nil {
				t.Fatal(err)
			}
			d, err := f.service.Create(f.editor.Ctx, "db", migration.DraftInput{Title: "Archive", SQLDown: "UPDATE accounts SET status='active'", BatchSize: 100,
				Batch: &migration.BatchSpec{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}})
			if err != nil {
				t.Fatal(err)
			}
			req := f.approved(t, d, migration.OpUp)
			job := f.enqueue(t, req)
			f.loseAck(t, req, tc.stopAt)
			f.worker(t, f.runner())
			if done := f.settled(t, job.ID); done.Effect != tc.effect || done.CommittedRows != tc.rows {
				t.Fatalf("reconciled job = %+v", done)
			}
			if a := f.attempt(t, job.ExecutionID); a.Status != tc.status {
				t.Fatalf("reconciled attempt = %+v", a)
			}
			if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != tc.rows {
				t.Fatalf("%d rows archived, want %d", n, tc.rows)
			}
		})
	}
}

func (f *flow) loseAck(t *testing.T, req migration.Request, stopAt int) {
	t.Helper()
	gate, err := f.gates.Gate(t.Context(), "db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	a, err := f.service.RunWith(t.Context(), migrationrun.Target(gate), lostBatchAck{migrationrun.Batches(gate, nil), gate, stopAt}, req.ID)
	if !errors.Is(err, migration.ErrOutcomeUnknown) || a.Status != migration.StatusUnknown {
		t.Fatalf("lost acknowledgement = %+v, %v", a, err)
	}
}

func TestUncertainCompensationReconcilesWithoutMoreWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stopAt int
		status migration.Status
		effect jobs.Effect
		rows   int64
	}{
		{"zero", 2, migration.StatusPartiallyApplied, jobs.EffectPartial, 0},
		{"partial", 1, migration.StatusPartiallyReverted, jobs.EffectPartial, 100},
		{"complete", -1, migration.StatusReverted, jobs.EffectCommitted, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t)
			stop := f.worker(t, f.runner())
			d, first := f.partialBatch(t)
			stop()
			req := f.recovery(t, d, migration.OpCompensate, first.ExecutionID)
			job := f.enqueue(t, req)
			f.loseAck(t, req, tc.stopAt)
			if j, _ := f.store.Get(t.Context(), "db", first.ID); len(j.Recovery()) != 0 {
				t.Fatalf("recovery offered while ledger unverified: %v", j.Recovery())
			}
			f.p.Exec(t, `DELETE FROM group_memberships WHERE user_id=$1`, f.editor.ID)
			f.worker(t, f.runner())
			done := f.settled(t, job.ID)
			if done.Effect != tc.effect || done.CommittedRows != tc.rows {
				t.Fatalf("reconciled job = %+v", done)
			}
			if tc.status == migration.StatusPartiallyReverted && (len(done.Recovery()) != 1 || done.Recovery()[0] != jobs.OpCompensate) {
				t.Fatalf("unsafe recovery = %v", done.Recovery())
			}
			if a := f.attempt(t, first.ExecutionID); a.Status != tc.status {
				t.Fatalf("reconciled attempt = %+v", a)
			}
			if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 200-tc.rows {
				t.Fatalf("%d rows archived after recovery reconciliation", n)
			}
		})
	}
}

func TestRecoveryRequestsNeedAPartialBatchExecution(t *testing.T) {
	f := newFlow(t)
	f.worker(t, f.runner())
	d := f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets")
	job := f.enqueue(t, f.approved(t, d, migration.OpUp))
	f.settled(t, job.ID)
	detail, _ := f.service.Detail(f.editor.Ctx, "db", d.ID)
	for _, in := range []migration.RequestInput{
		{Operation: migration.OpResume, RecoversExecutionID: job.ExecutionID},
		{Operation: migration.OpCompensate, RecoversExecutionID: "0123456789abcdef0123456789abcdef"},
		{Operation: migration.OpResume},
	} {
		in.ExpectedRevision, in.ExpectedDefinition, in.ExpectedVersion = detail.Draft.Revision, detail.Draft.DefinitionHash(), detail.CurrentVersion
		if _, err := f.service.CreateRequest(f.editor.Ctx, "db", d.ID, in); err == nil {
			t.Fatalf("recovery request %+v accepted for a non-batch execution", in)
		}
	}
	if _, err := f.service.Create(f.editor.Ctx, "db", migration.DraftInput{Title: "Bad", SQLDown: "SELECT 1",
		Batch: &migration.BatchSpec{Schema: "public", Table: "a; DROP TABLE a", Key: "id", Column: "c"}, BatchSize: 10}); !errors.Is(err, migration.ErrValidation) {
		t.Fatalf("unsafe batch identifier = %v", err)
	}
}

// dieAfterClaim records the state a worker leaves when its process dies: the
// claim with a short lease and the running execution, but no recorded outcome.
func (f *flow) dieAfterClaim(t *testing.T, jobID, requestID, mode string) {
	t.Helper()
	f.p.Exec(t, `UPDATE jobs SET status='running', claimed_by='dead', lease_until=now() + interval '1 second', attempts=1 WHERE id=$1`, jobID)
	f.p.Exec(t, `INSERT INTO migration_attempts (id, database_id, migration_id, operation, request_id, draft_revision, candidate_version,
		sql_text, sql_sha256, dialect, statement_count, executor_id, status, started_at, mode)
		SELECT r.execution_id, r.database_id, r.migration_id, r.operation, r.id, d.revision, r.expected_version + 1, d.sql_text,
			encode(sha256(convert_to(d.sql_text, 'UTF8')), 'hex'), d.dialect, 1, r.requested_by, 'running', now(), $2
		FROM migration_requests r JOIN migration_drafts d ON d.id = r.migration_id WHERE r.id = $1`, requestID, mode)
}

func TestWorkerDeathBeforeTargetCommitIsFencedNotReplayed(t *testing.T) {
	f := newFlow(t)
	if _, err := f.target.Exec(t.Context(), `CREATE TABLE ledger (id serial PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	d := f.define(t, "Ledger entry", "INSERT INTO ledger DEFAULT VALUES", "DELETE FROM ledger")
	req := f.approved(t, d, migration.OpUp)
	job := f.enqueue(t, req)
	// The worker recorded the running execution, then died before its target transaction committed.
	f.dieAfterClaim(t, job.ID, req.ID, "transaction")
	f.worker(t, f.runner())
	done := f.settled(t, job.ID)
	if done.Status != jobs.Failed || done.Effect != jobs.EffectNone || done.Attempts != 2 || !strings.Contains(done.Error, "nothing was applied") {
		t.Fatalf("recovered job = %+v", done)
	}
	if n := f.count(t, `SELECT count(*) FROM ledger`); n != 0 {
		t.Fatalf("ledger has %d rows; the uncommitted execution was replayed", n)
	}
	var outcome string
	if err := f.target.QueryRow(t.Context(), `SELECT outcome FROM sqlwise_internal.migration_receipts WHERE attempt_id=$1`, job.ExecutionID).Scan(&outcome); err != nil || outcome != "fenced" {
		t.Fatalf("receipt = %q, %v; a fence must stop the lost transaction from ever committing", outcome, err)
	}
	if a := f.attempt(t, job.ExecutionID); a.Status != migration.StatusFailed || a.ReconciledAt == nil {
		t.Fatalf("execution = %+v", a)
	}
}

// A job claimed ahead of a dead batch run must not block the database on the
// batch's behalf: only the batch's own delivery can settle its ledger.
func TestJobQueuedBehindADeadBatchRunWaitsInsteadOfBlocking(t *testing.T) {
	f := newFlow(t)
	f.seedAccounts(t)
	if _, err := f.target.Exec(t.Context(), `DELETE FROM fail_switch`); err != nil {
		t.Fatal(err)
	}
	spec := migration.BatchSpec{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}
	d, err := f.service.Create(f.editor.Ctx, "db", migration.DraftInput{Title: "Archive accounts", SQLDown: "UPDATE accounts SET status = 'active'",
		Batch: &spec, BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	next := f.approved(t, f.define(t, "Widgets", "CREATE TABLE widgets (id int)", "DROP TABLE widgets"), migration.OpUp)
	batchReq := f.approved(t, d, migration.OpUp)
	batch := f.enqueue(t, batchReq)
	f.dieAfterClaim(t, batch.ID, batchReq.ID, "batch")
	gate, err := f.gates.Gate(t.Context(), "db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := gate.PrepareBatch(t.Context(), batch.ExecutionID, database.BatchUpdate(spec), 100)
	if err != nil {
		t.Fatal(err)
	}
	died := errors.New("process ended")
	if _, err := gate.RunBatches(t.Context(), plan, database.BatchHooks{BeforeCommit: func(n int) error {
		if n == 3 {
			return died
		}
		return nil
	}}); !errors.Is(err, died) {
		t.Fatal(err)
	}
	gate.Close()
	queued := f.enqueue(t, next)
	// Order the reclaimed batch after the queued job, as a busy deferral does.
	f.p.Exec(t, `UPDATE jobs SET next_run_at = now() + interval '1 hour' WHERE id=$1`, batch.ID)
	f.worker(t, f.runner())

	if done := f.settled(t, batch.ID); done.Status != jobs.Blocked || done.Effect != jobs.EffectPartial || len(done.Recovery()) != 2 {
		t.Fatalf("dead batch job = %+v", done)
	}
	waiting := jobstest.Wait(t, f.store, "db", queued.ID, 10*time.Second, func(j jobs.Job) bool { return j.Status != jobs.Running })
	if waiting.Status != jobs.Queued || waiting.Effect != jobs.EffectNone || waiting.Attempts != 0 {
		t.Fatalf("job behind the batch = %+v; it must wait without blocking", waiting)
	}
	if _, found := f.attemptMaybe(t, queued.ExecutionID); found {
		t.Fatal("the waiting job recorded an execution")
	}
}

func (f *flow) attemptMaybe(t *testing.T, id string) (migration.Attempt, bool) {
	t.Helper()
	var n int
	if err := f.p.Pool.QueryRow(t.Context(), `SELECT count(*) FROM migration_attempts WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return migration.Attempt{}, n > 0
}

func TestWorkerDeathDuringBatchRunLeavesItPartialForApprovedRecovery(t *testing.T) {
	f := newFlow(t)
	f.seedAccounts(t)
	if _, err := f.target.Exec(t.Context(), `DELETE FROM fail_switch`); err != nil {
		t.Fatal(err)
	}
	spec := migration.BatchSpec{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "archived"}
	d, err := f.service.Create(f.editor.Ctx, "db", migration.DraftInput{Title: "Archive accounts", SQLDown: "UPDATE accounts SET status = 'active'",
		Batch: &spec, BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	req := f.approved(t, d, migration.OpUp)
	job := f.enqueue(t, req)
	f.dieAfterClaim(t, job.ID, req.ID, "batch")
	// The dead worker committed two batches before its process ended.
	gate, err := f.gates.Gate(t.Context(), "db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := gate.PrepareBatch(t.Context(), job.ExecutionID, database.BatchUpdate(spec), 100)
	if err != nil {
		t.Fatal(err)
	}
	died := errors.New("process ended")
	if _, err := gate.RunBatches(t.Context(), plan, database.BatchHooks{BeforeCommit: func(batch int) error {
		if batch == 3 {
			return died
		}
		return nil
	}}); !errors.Is(err, died) {
		t.Fatal(err)
	}
	gate.Close()
	f.worker(t, f.runner())
	done := f.settled(t, job.ID)
	if done.Status != jobs.Blocked || done.Effect != jobs.EffectPartial || len(done.Recovery()) != 2 {
		t.Fatalf("job after worker death = %+v", done)
	}
	if a := f.attempt(t, job.ExecutionID); a.Status != migration.StatusPartiallyApplied || !a.Batch {
		t.Fatalf("execution = %+v", a)
	}
	// Nothing continues automatically: only the two committed batches are applied.
	if n := f.count(t, `SELECT count(*) FROM accounts WHERE status='archived'`); n != 200 {
		t.Fatalf("%d rows archived; the interrupted run continued without approval", n)
	}
}
