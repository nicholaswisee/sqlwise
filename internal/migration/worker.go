package migration

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access"
)

// Run executes a queued request. target must be the pinned session of the
// execution gate the worker already holds for the request's database; Run
// takes no lock of its own. The requester is the durable actor: access,
// approvals, definition, head and linear eligibility are rechecked here,
// before any target write. Redelivery of a recorded execution reconciles or
// returns its outcome instead of running the SQL again.
// TODO(jason): called by the internal/jobs worker with its gate session.
func (s *Service) Run(ctx context.Context, target Target, requestID string) (Attempt, error) {
	if !validID(requestID) {
		return Attempt{}, fail(ErrNotFound, "Request not found.")
	}
	r, err := s.store.request(ctx, s.store.pool, "", requestID, false)
	if err != nil {
		return Attempt{}, err
	}
	if r.Status != RequestQueued {
		return Attempt{}, fail(ErrConflict, "Only a queued request can be executed.")
	}
	if a, found, err := s.store.attemptByID(ctx, r.ExecutionID); err != nil || found {
		if err != nil {
			return Attempt{}, err
		}
		if a.Status == StatusRunning || a.Status == StatusUnknown {
			if err := s.reconcile(ctx, target, r.DatabaseID); err != nil {
				return a, err
			}
			if a, _, err = s.store.attemptByID(ctx, r.ExecutionID); err != nil {
				return Attempt{}, err
			}
		}
		return a, outcome(a)
	}
	if err := s.reconcile(ctx, target, r.DatabaseID); err != nil {
		return Attempt{}, err
	}
	var attempt Attempt
	var statements []Statement
	err = pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		groups, err := s.approved(ctx, tx, r)
		if err != nil {
			return err
		}
		if actorPermission(groups, r.RequestedBy) < access.Edit {
			return fail(access.ErrPermissionDenied, "The requester no longer has edit access to this database. Nothing was executed.")
		}
		d, err := s.fresh(ctx, tx, r)
		if err != nil {
			return err
		}
		sql := d.SQL
		if r.Operation == OpRollback {
			sql = d.SQLDown
		}
		if statements, err = ParsePostgres(sql); err != nil {
			return fail(ErrValidation, err.Error())
		}
		attempt = Attempt{ID: r.ExecutionID, DatabaseID: r.DatabaseID, MigrationID: r.MigrationID, MigrationTitle: d.Title,
			Operation: r.Operation, RequestID: r.ID, CancelsExecutionID: r.CancelsExecutionID, DraftRevision: d.Revision,
			CandidateVersion: r.ExpectedVersion + 1, SQL: sql, SQLHash: HashSQL(sql), Dialect: d.Dialect,
			StatementCount: len(statements), ExecutorID: r.RequestedBy, Status: StatusRunning, StartedAt: s.now().UTC()}
		// The running execution is recorded in the same transaction as the checks.
		return s.store.insertAttempt(ctx, tx, attempt)
	})
	if err != nil {
		slog.WarnContext(ctx, "migration execution refused", "event", "migration.execution.refused", "database", r.DatabaseID,
			"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", r.ExecutionID, "actor", r.RequestedBy, "status", "refused")
		return Attempt{}, err
	}
	slog.InfoContext(ctx, "migration execution started", "event", "migration.execution.start", "database", r.DatabaseID,
		"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", attempt.ID, "operation", string(r.Operation),
		"actor", r.RequestedBy, "statements", len(statements), "sql_sha256", attempt.SQLHash)
	attempt, kind := s.run(ctx, target, attempt, statements)
	attempt, err = s.record(ctx, attempt, kind)
	slog.InfoContext(ctx, "migration execution finished", "event", "migration.execution.result", "database", r.DatabaseID,
		"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", attempt.ID, "operation", string(r.Operation),
		"actor", r.RequestedBy, "status", string(attempt.Status), "failed_statement", attempt.FailedStatement)
	return attempt, err
}

// outcome maps a recorded execution to the error Run reports for it.
func outcome(a Attempt) error {
	switch a.Status {
	case StatusSucceeded:
		return nil
	case StatusFailed:
		return fail(ErrExecutionFailed, a.Error)
	}
	return fail(ErrOutcomeUnknown, blockedMessage(a))
}

// Jobs is the durable queue behind the execute endpoint: Enqueue consumes an
// approved request and publishes its job atomically.
// TODO(jason): internal/jobs implements this; LocalQueue stands in until then.
type Jobs interface {
	Enqueue(ctx context.Context, databaseID, requestID, idempotencyKey string) (Job, error)
	Job(ctx context.Context, databaseID, jobID string) (Job, error)
}

type Job struct {
	ID          string
	DatabaseID  string
	RequestID   string
	ExecutionID string
	// Status is queued, running, succeeded or failed.
	Status string
	Error  string
}

// LocalQueue runs jobs in this process, behind the Milestone 1 platform lock.
// shortcut: jobs live in memory, so a restart loses queued work and failed
// deliveries are not retried automatically; replace with Jason's queue.
// TODO(jason): delete once internal/jobs and cmd/worker exist.
type LocalQueue struct {
	service *Service
	mu      sync.Mutex
	jobs    map[string]*Job
	running sync.WaitGroup
}

const localGateWait = time.Minute

func NewLocalQueue(service *Service) *LocalQueue {
	return &LocalQueue{service: service, jobs: map[string]*Job{}}
}

func (q *LocalQueue) Enqueue(ctx context.Context, databaseID, requestID, _ string) (Job, error) {
	var execution Execution
	err := pgx.BeginFunc(ctx, q.service.store.pool, func(tx pgx.Tx) error {
		var err error
		execution, err = q.service.Consume(ctx, tx, databaseID, requestID)
		return err
	})
	if err != nil {
		return Job{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// The idempotency key is the request: a live job is returned, a finished
	// one is redelivered, and Run never repeats a recorded execution.
	for _, j := range q.jobs {
		if j.RequestID == requestID && (j.Status == "queued" || j.Status == "running") {
			return *j, nil
		}
	}
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	job := &Job{ID: id, DatabaseID: databaseID, RequestID: requestID, ExecutionID: execution.ID, Status: "queued"}
	q.jobs[id] = job
	q.running.Add(1)
	go q.work(job)
	return *job, nil
}

func (q *LocalQueue) Job(ctx context.Context, databaseID, jobID string) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[jobID]
	if !ok || j.DatabaseID != databaseID {
		return Job{}, fail(ErrNotFound, "Job not found for this database.")
	}
	return *j, nil
}

// Wait blocks until every started job finished (tests and shutdown).
func (q *LocalQueue) Wait() { q.running.Wait() }

func (q *LocalQueue) work(job *Job) {
	defer q.running.Done()
	// Jobs outlive the HTTP request and never use its session.
	ctx, cancel := context.WithTimeout(context.Background(), localGateWait+q.service.executeTimeout+time.Minute)
	defer cancel()
	q.set(job, "running", "")
	_, err := q.gated(ctx, job)
	if err != nil {
		message := Message(err)
		if message == "" {
			message = "The job failed before the target was changed."
		}
		q.set(job, "failed", message)
		return
	}
	q.set(job, "succeeded", "")
}

// gated stands in for the execution gate: the platform lock row plus a
// target handle opened for this job only.
func (q *LocalQueue) gated(ctx context.Context, job *Job) (Attempt, error) {
	s := q.service
	if s.targets == nil {
		return Attempt{}, fail(ErrTargetUnavailable, "Target execution is not configured on this server.")
	}
	_, release, err := s.store.lock(ctx, job.DatabaseID, localGateWait)
	if err != nil {
		return Attempt{}, err
	}
	defer release()
	target, err := s.targets.Open(ctx, job.DatabaseID)
	if err != nil {
		return Attempt{}, fail(ErrTargetUnavailable, "The target database is unavailable. Nothing was executed.")
	}
	defer target.Close()
	return s.Run(ctx, target, job.RequestID)
}

func (q *LocalQueue) set(job *Job, status, message string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job.Status, job.Error = status, message
}
