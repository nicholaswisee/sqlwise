package migration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"sqlwise/internal/access"
	"sqlwise/internal/telemetry"
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
			if a.Batch {
				return s.reconcileBatch(ctx, batches, a)
			}
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
	var targetTx TargetTx
	execCtx, cancel := context.WithTimeout(ctx, s.executeTimeout)
	defer cancel()
	defer func() {
		if targetTx != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
			defer cancel()
			_ = targetTx.Rollback(cleanup)
		}
	}()
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
		if !attempt.Batch {
			// Startup can be retried until a running attempt has been recorded;
			// receipt insertion and migration SQL are both after that record commits.
			targetTx, err = startTarget(execCtx, target)
			if err != nil {
				return err
			}
		}
		// The running execution is recorded in the same transaction as the checks.
		return s.store.insertAttempt(ctx, tx, attempt)
	})
	if err != nil {
		// Not final yet: transient refusals are retried; final ones are
		// recorded in history by RecordRefusal.
		slog.WarnContext(ctx, "migration execution not started", "event", "migration.execution.precheck_failed", "database", r.DatabaseID,
			"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", r.ExecutionID, "operation", string(r.Operation),
			"actor", r.RequestedBy, "status", "not_started", "error", telemetry.RedactMessage(or(Message(err), "The platform database could not be read.")))
		return Attempt{}, err
	}
	logAttempt(ctx, slog.LevelInfo, "migration.execution.start", attempt)
	var kind error
	if attempt.Batch {
		attempt, kind = s.runBatch(ctx, batches, attempt, d)
	} else {
		attempt, kind = s.runTransaction(execCtx, target, targetTx, attempt, statements)
	}
	attempt, err = s.record(ctx, attempt, kind)
	level := slog.LevelInfo
	if attempt.Status != StatusSucceeded {
		level = slog.LevelWarn
	}
	logAttempt(ctx, level, "migration.execution.result", attempt)
	return attempt, err
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func startTarget(ctx context.Context, target Target) (TargetTx, error) {
	err := ProvisionReceipts(ctx, target)
	message := "Could not prepare the execution receipt on the target: "
	if err == nil {
		var tx TargetTx
		tx, err = target.Begin(ctx)
		if err == nil {
			return tx, nil
		}
		message = "Could not start a target transaction: "
	}
	kind := ErrExecutionFailed
	var pgErr *pgconn.PgError
	var netErr net.Error
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "08000", "08003", "08006", "57P01", "57P02", "57P03", "55P03", "40001", "40P01":
			kind = ErrTargetUnavailable
		}
	} else if pgconn.SafeToRetry(err) || errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		kind = ErrTargetUnavailable
	}
	return nil, fail(kind, message+targetMessage(ctx, err)+" Nothing was executed.")
}

// RecordRefusal writes the history row of a queued execution that ended
// without ever recording an attempt: it was refused by revalidation or its
// target stayed unreachable. The row keeps the SQL that would have run.
// Executions that already recorded an attempt are left untouched, so it is
// safe to call after any final failure. Recovery operations settle the
// execution they recover instead and are not recorded here.
func (s *Service) RecordRefusal(ctx context.Context, requestID, reason string) (bool, error) {
	if !validID(requestID) {
		return false, fail(ErrNotFound, "Request not found.")
	}
	r, err := s.store.request(ctx, s.store.pool, "", requestID, false)
	if err != nil {
		return false, err
	}
	if r.ExecutionID == "" || r.Operation.Recovery() {
		return false, nil
	}
	d, err := s.store.draft(ctx, s.store.pool, r.DatabaseID, r.MigrationID)
	if err != nil {
		return false, err
	}
	sql := d.SQL
	if r.Operation == OpRollback {
		sql = d.SQLDown
	}
	count := 1
	if statements, err := ParsePostgres(sql); err == nil && len(statements) > 0 {
		count = len(statements)
	}
	if reason == "" {
		reason = "The execution ended before any SQL ran."
	}
	reason = clip(reason, 1999)
	now := s.now().UTC()
	a := Attempt{ID: r.ExecutionID, DatabaseID: r.DatabaseID, MigrationID: r.MigrationID, MigrationTitle: d.Title, Operation: r.Operation,
		RequestID: r.ID, CancelsExecutionID: r.CancelsExecutionID, DraftRevision: d.Revision, CandidateVersion: r.ExpectedVersion + 1,
		SQL: sql, SQLHash: HashSQL(sql), Dialect: d.Dialect, StatementCount: count, ExecutorID: r.RequestedBy, Status: StatusRefused,
		Error: reason, StartedAt: now, FinishedAt: &now, Batch: d.Batch != nil && r.Operation != OpRollback}
	recorded, err := s.store.insertRefusal(ctx, a)
	if err != nil || !recorded {
		return false, err
	}
	logAttempt(ctx, slog.LevelWarn, "migration.execution.refused", a)
	return true, nil
}

// logAttempt writes one structured line per execution phase with every
// identifying field of the attempt and a literal-masked copy of its SQL; the
// exact text stays in the platform history and is identified by sql_sha256.
func logAttempt(ctx context.Context, level slog.Level, event string, a Attempt) {
	args := []any{"event", event, "database", a.DatabaseID, "migration_id", a.MigrationID, "migration_title", a.MigrationTitle,
		"execution_id", a.ID, "request_id", a.RequestID, "operation", string(a.Operation), "actor", a.ExecutorID,
		"draft_revision", a.DraftRevision, "candidate_version", a.CandidateVersion, "mode", attemptMode(a.Batch),
		"status", string(a.Status), "statements", a.StatementCount, "sql", telemetry.RedactSQL(a.SQL), "sql_sha256", a.SQLHash}
	if a.CancelsExecutionID != "" {
		args = append(args, "cancels_execution_id", a.CancelsExecutionID)
	}
	if a.AppliedVersion != 0 {
		args = append(args, "applied_version", a.AppliedVersion)
	}
	if a.FinishedAt != nil {
		args = append(args, "duration_ms", a.FinishedAt.Sub(a.StartedAt).Milliseconds())
	}
	if a.FailedStatement != 0 {
		args = append(args, "failed_statement", a.FailedStatement)
	}
	if a.Error != "" {
		args = append(args, "error", telemetry.RedactMessage(a.Error))
	}
	slog.Log(ctx, level, "migration execution", args...)
}

// outcome maps a recorded execution to the error Run reports for it.
func outcome(a Attempt) error {
	switch a.Status {
	case StatusSucceeded:
		return nil
	case StatusFailed, StatusRefused:
		return fail(ErrExecutionFailed, a.Error)
	case StatusPartiallyApplied, StatusPartiallyReverted:
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
