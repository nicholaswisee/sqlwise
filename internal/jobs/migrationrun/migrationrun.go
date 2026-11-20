// Package migrationrun is the composition seam between the jobs runtime and
// the migration domain. Neither package imports the other; this one adapts
// migration.Service to jobs.Consumer and jobs.Runner, and the queue to
// migration.Jobs.
package migrationrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access"
	"sqlwise/internal/database"
	"sqlwise/internal/jobs"
	"sqlwise/internal/migration"
)

// Consumer consumes approved requests through the migration domain.
type Consumer struct {
	Service *migration.Service
}

func (c Consumer) Consume(ctx context.Context, tx pgx.Tx, databaseID, requestID string) (jobs.Consumed, error) {
	e, err := c.Service.Consume(ctx, tx, databaseID, requestID)
	if err != nil {
		return jobs.Consumed{}, err
	}
	// TODO(james): carry the interrupted execution ID for resume/compensate requests.
	return jobs.Consumed{ExecutionID: e.ID, MigrationID: e.MigrationID, Operation: string(e.Operation), ActorID: e.ActorID}, nil
}

// BatchSource resolves the approved batch template of a job, if any.
// TODO(james): batch-mode definitions implement this from the immutable revision.
type BatchSource interface {
	Batch(ctx context.Context, job jobs.Job) (spec database.BatchUpdate, size int, ok bool, err error)
}

// Runner executes jobs on the gate the worker already holds.
type Runner struct {
	Service *migration.Service
	Batches BatchSource
}

func (r Runner) Run(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
	if r.Batches != nil {
		spec, size, ok, err := r.Batches.Batch(ctx, job)
		if err != nil {
			return classify(err)
		}
		if ok {
			return RunBatch(ctx, gate, job, spec, size, progress)
		}
	}
	switch job.Operation {
	case jobs.OpResume, jobs.OpCompensate:
		// TODO(james): dispatch compensation to the migration domain using database.Gate.CommitBatch.
		return jobs.Failure("This recovery operation is not available yet. Nothing was executed.")
	}
	_, err := r.Service.Run(ctx, Target(gate), job.RequestID)
	return classify(err)
}

// RunBatch runs or resumes the forward batches of one execution. A resume
// continues the ledger of the interrupted execution, never a new scope.
func RunBatch(ctx context.Context, gate *database.Gate, job jobs.Job, spec database.BatchUpdate, size int, progress jobs.Progress) jobs.Outcome {
	execution := job.ExecutionID
	if job.Operation == jobs.OpResume {
		execution = job.RecoversExecutionID
	}
	plan, err := gate.PrepareBatch(ctx, execution, spec, size)
	if err != nil {
		return batchFailure(err, database.BatchProgress{})
	}
	var last database.BatchProgress
	p, err := gate.RunBatches(ctx, plan, database.BatchHooks{Committed: func(p database.BatchProgress) {
		last = p
		progress(database.Forward, p)
	}})
	if err != nil {
		// A lost gate cannot read the ledger; keep the last durable progress seen.
		if p == (database.BatchProgress{}) {
			p = last
		}
		return batchFailure(err, p)
	}
	progress(database.Forward, p)
	// TODO(james): finalize committed history only after this confirmed completion.
	return jobs.Success()
}

func batchFailure(err error, p database.BatchProgress) jobs.Outcome {
	message := database.Message(err)
	if message == "" {
		message = "The batch stopped unexpectedly."
	}
	if p.Batches > 0 || errors.Is(err, database.ErrOutcomeUnknown) {
		return jobs.Partial(fmt.Sprintf("%s %d batches (%d rows) stay committed; resume or compensate through an approved request.", message, p.Batches, p.Rows))
	}
	if errors.Is(err, database.ErrConnectionUnavailable) || errors.Is(err, database.ErrTimeout) {
		return jobs.Transient(message + " No batch was committed.")
	}
	return jobs.Failure(message + " No batch was committed.")
}

// classify maps a migration result to a job outcome. Run never repeats a
// recorded execution, so a retry after a transient error only reconciles.
func classify(err error) jobs.Outcome {
	message := migration.Message(err)
	switch {
	case err == nil:
		return jobs.Success()
	case errors.Is(err, migration.ErrOutcomeUnknown):
		return jobs.Unknown(message)
	case errors.Is(err, migration.ErrTargetUnavailable):
		return jobs.Transient(or(message, "The target database is unavailable."))
	case errors.Is(err, access.ErrPermissionDenied):
		return jobs.Failure("The requester no longer has edit access to this database. Nothing was executed.")
	case errors.Is(err, migration.ErrExecutionFailed), errors.Is(err, migration.ErrTimeout), errors.Is(err, migration.ErrConflict),
		errors.Is(err, migration.ErrValidation), errors.Is(err, migration.ErrNotFound), errors.Is(err, migration.ErrUnsupportedDialect),
		errors.Is(err, migration.ErrRecoveryRequired), errors.Is(err, migration.ErrBusy):
		return jobs.Failure(or(message, "The migration failed."))
	}
	// Platform hiccups before the attempt is recorded are safe to redeliver.
	return jobs.Transient("The worker could not reach the platform database.")
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Queue adapts the durable queue to the migration execute endpoint.
type Queue struct {
	Jobs *jobs.Queue
}

func (q Queue) Enqueue(ctx context.Context, databaseID, requestID, key string) (migration.Job, error) {
	j, _, err := q.Jobs.Enqueue(ctx, databaseID, requestID, key)
	if err != nil {
		return migration.Job{}, domainError(err)
	}
	return jobDTO(j), nil
}

func (q Queue) Job(ctx context.Context, databaseID, jobID string) (migration.Job, error) {
	j, err := q.Jobs.Job(ctx, databaseID, jobID)
	if err != nil {
		return migration.Job{}, domainError(err)
	}
	return jobDTO(j), nil
}

func jobDTO(j jobs.Job) migration.Job {
	return migration.Job{ID: j.ID, DatabaseID: j.DatabaseID, RequestID: j.RequestID, ExecutionID: j.ExecutionID, Status: string(j.Status), Error: j.Error}
}

func domainError(err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return &migration.Error{Kind: migration.ErrNotFound, Message: jobs.Message(err)}
	case errors.Is(err, jobs.ErrConflict):
		return &migration.Error{Kind: migration.ErrConflict, Message: jobs.Message(err)}
	case errors.Is(err, jobs.ErrValidation):
		return &migration.Error{Kind: migration.ErrValidation, Message: jobs.Message(err)}
	}
	return err
}

var _ migration.Jobs = Queue{}

// Target exposes the gate session as the migration target; every statement,
// receipt and transaction runs on the session holding the lock.
func Target(g *database.Gate) migration.Target { return gateTarget{g} }

type gateTarget struct{ g *database.Gate }

func (t gateTarget) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	return t.g.Exec(ctx, sql, args...)
}

func (t gateTarget) QueryRow(ctx context.Context, sql string, args ...any) migration.Row {
	return t.g.QueryRow(ctx, sql, args...)
}

func (t gateTarget) Begin(ctx context.Context) (migration.TargetTx, error) {
	tx, err := t.g.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", migration.ErrTargetUnavailable, err)
	}
	return gateTx{tx}, nil
}

// Close is a no-op: the worker owns the gate and releases it after recording.
func (t gateTarget) Close() error { return nil }

type gateTx struct{ database.Tx }

func (t gateTx) Commit(ctx context.Context) error {
	err := t.Tx.Commit(ctx)
	if errors.Is(err, database.ErrOutcomeUnknown) {
		return fmt.Errorf("%w: %w", migration.ErrCommitUnknown, err)
	}
	return err
}
