package migration

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access"
)

// Run executes a queued request. target must be the pinned session of the
// execution gate the worker already holds for the request's database; Run
// takes no lock of its own. The requester is the durable actor: access,
// approvals, definition, head and linear eligibility are rechecked here,
// before any target write. Redelivery of a recorded execution reconciles or
// returns its outcome instead of running the SQL again.
func (s *Service) Run(ctx context.Context, target Target, requestID string) (Attempt, error) {
	return s.RunWith(ctx, target, nil, requestID)
}

// RunWith is Run with the batch runtime of the worker's gate; batch-mode
// definitions and recovery requests need it.
func (s *Service) RunWith(ctx context.Context, target Target, batches BatchRunner, requestID string) (Attempt, error) {
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
	if r.Operation.Recovery() {
		return s.recover(ctx, batches, r)
	}
	if a, found, err := s.store.attemptByID(ctx, r.ExecutionID); err != nil || found {
		if err != nil {
			return Attempt{}, err
		}
		if a.Status == StatusRunning || a.Status == StatusUnknown {
			if err := s.reconcile(ctx, target, r.DatabaseID); err != nil {
				if reloaded, found, _ := s.store.attemptByID(ctx, r.ExecutionID); found {
					a = reloaded
				}
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
	var d Draft
	err = pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		groups, err := s.approved(ctx, tx, r)
		if err != nil {
			return err
		}
		if actorPermission(groups, r.RequestedBy) < access.Edit {
			return fail(access.ErrPermissionDenied, "The requester no longer has edit access to this database. Nothing was executed.")
		}
		d, err = s.fresh(ctx, tx, r)
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
			StatementCount: len(statements), ExecutorID: r.RequestedBy, Status: StatusRunning, StartedAt: s.now().UTC(),
			Batch: d.Batch != nil && r.Operation != OpRollback}
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
	var kind error
	if attempt.Batch {
		attempt, kind = s.runBatch(ctx, batches, attempt, d)
	} else {
		attempt, kind = s.run(ctx, target, attempt, statements)
	}
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
	case StatusPartiallyApplied:
		return fail(ErrRecoveryRequired, blockedMessage(a))
	case StatusReverted:
		if a.RecoveredBy != "" {
			return nil
		}
		return fail(ErrExecutionFailed, a.Error)
	}
	return fail(ErrOutcomeUnknown, blockedMessage(a))
}

// Jobs is the durable queue behind the execute endpoint: Enqueue consumes an
// approved request and publishes its job atomically.
// internal/jobs implements it; cmd/worker executes the jobs.
type Jobs interface {
	Enqueue(ctx context.Context, databaseID, requestID, idempotencyKey string) (Job, error)
	Job(ctx context.Context, databaseID, jobID string) (Job, error)
}

type Job struct {
	ID          string
	DatabaseID  string
	RequestID   string
	ExecutionID string
	// Status is queued, running, succeeded, failed or blocked.
	Status string
	Error  string
}
