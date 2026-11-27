// Package migrationrun is the composition seam between the jobs runtime and
// the migration domain. Neither package imports the other; this one adapts
// migration.Service to jobs.Consumer and jobs.Runner, and the queue to
// migration.Jobs.
package migrationrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	return jobs.Consumed{ExecutionID: e.ID, MigrationID: e.MigrationID, Operation: string(e.Operation), ActorID: e.ActorID,
		RecoversExecutionID: e.RecoversExecutionID}, nil
}

// Runner executes jobs on the gate the worker already holds.
type Runner struct {
	Service *migration.Service
}

func (r Runner) Run(ctx context.Context, gate *database.Gate, job jobs.Job, progress jobs.Progress) jobs.Outcome {
	a, err := r.Service.RunWith(ctx, Target(gate), Batches(gate, progress), job.RequestID)
	return classify(job, a, err)
}

// RecordFailure keeps a refused history entry for a job that failed before
// its execution recorded an attempt; recorded executions are left alone.
func (r Runner) RecordFailure(ctx context.Context, job jobs.Job, message string) {
	if _, err := r.Service.RecordRefusal(ctx, job.RequestID, message); err != nil {
		slog.WarnContext(ctx, "refused execution not recorded", "event", "migration.execution.refusal_unrecorded",
			"database", job.DatabaseID, "job_id", job.ID, "execution_id", job.ExecutionID, "error", err.Error())
	}
}

var _ jobs.FailureRecorder = Runner{}

// Batches runs batch templates and their compensation on the gate session.
func Batches(gate *database.Gate, progress jobs.Progress) migration.BatchRunner {
	if progress == nil {
		progress = func(string, database.BatchProgress) {}
	}
	return gateBatches{gate: gate, progress: progress}
}

type gateBatches struct {
	gate     *database.Gate
	progress jobs.Progress
}

func (b gateBatches) Forward(ctx context.Context, executionID string, spec migration.BatchSpec, size int) (migration.BatchResult, error) {
	plan, err := b.gate.PrepareBatch(ctx, executionID, database.BatchUpdate(spec), size)
	if err != nil {
		return migration.BatchResult{}, batchError(err)
	}
	return b.track(database.Forward, func(hooks database.BatchHooks) (database.BatchProgress, error) {
		return b.gate.RunBatches(ctx, plan, hooks)
	})
}

func (b gateBatches) Compensate(ctx context.Context, executionID string) (migration.BatchResult, error) {
	return b.track(database.Compensate, func(hooks database.BatchHooks) (database.BatchProgress, error) {
		return b.gate.RevertBatches(ctx, executionID, hooks)
	})
}

func (b gateBatches) Inspect(ctx context.Context, executionID string, operation migration.Operation) (migration.BatchResult, error) {
	p, err := b.gate.ReadBatchProgress(ctx, executionID)
	if err == nil {
		direction := database.Forward
		if operation == migration.OpCompensate {
			direction = database.Compensate
		}
		b.progress(direction, p)
	}
	return batchResult(p), batchError(err)
}

// track publishes durable progress after every batch; a lost gate keeps the last ledger read.
func (b gateBatches) track(direction string, run func(database.BatchHooks) (database.BatchProgress, error)) (migration.BatchResult, error) {
	var last database.BatchProgress
	p, err := run(database.BatchHooks{Committed: func(p database.BatchProgress) {
		last = p
		b.progress(direction, p)
	}})
	if err != nil && p == (database.BatchProgress{}) {
		p = last
	} else if p != (database.BatchProgress{}) {
		b.progress(direction, p)
	}
	return batchResult(p), batchError(err)
}

func batchResult(p database.BatchProgress) migration.BatchResult {
	return migration.BatchResult{Batches: p.Batches, Rows: p.Rows, TotalRows: p.TotalRows, Compensated: p.Compensated, Complete: p.Complete}
}

// batchError keeps the safe target message under the migration error kinds.
func batchError(err error) error {
	if err == nil {
		return nil
	}
	message := database.Message(err)
	if message == "" {
		message = "The batch run stopped unexpectedly."
	}
	kind := migration.ErrExecutionFailed
	switch {
	case errors.Is(err, database.ErrOutcomeUnknown):
		kind = migration.ErrOutcomeUnknown
	case errors.Is(err, database.ErrConflict):
		kind = migration.ErrConflict
	case errors.Is(err, database.ErrValidation), errors.Is(err, database.ErrNotFound):
		kind = migration.ErrValidation
	}
	return &migration.Error{Kind: kind, Message: message}
}

// classify maps a migration result to a job outcome.
func classify(job jobs.Job, a migration.Attempt, err error) jobs.Outcome {
	message := migration.Message(err)
	partial := a.Status == migration.StatusPartiallyApplied || a.Status == migration.StatusPartiallyReverted
	ownPartial := partial && (a.ID == job.ExecutionID || a.ID == job.RecoversExecutionID)
	switch {
	case err == nil:
		return jobs.Success()
	case errors.Is(err, migration.ErrRecoveryRequired) && ownPartial:
		return jobs.Partial(or(a.Error, message))
	case errors.Is(err, migration.ErrBatchPending) && a.ID == "":
		// Nothing of this job ran; blocking it would stop the batch's own job
		// from being claimed to settle the ledger.
		return jobs.Deferred(or(message, "Waiting for another execution's batch run to settle."))
	case errors.Is(err, migration.ErrOutcomeUnknown):
		return jobs.Unknown(message)
	case errors.Is(err, migration.ErrTargetUnavailable) && a.ID != "":
		return jobs.Failure(or(message, "The target database is unavailable."))
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
