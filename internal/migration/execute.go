package migration

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"sqlwise/internal/access"
)

// Execute runs the stored SQL of a draft exactly as confirmed by the caller.
// It returns the recorded attempt whenever one was created, together with a
// domain error for every outcome other than success.
func (s *Service) Execute(ctx context.Context, databaseID, migrationID string, in ExecuteInput) (Attempt, error) {
	userID, err := s.authorize(ctx, databaseID, access.Edit)
	if err != nil {
		return Attempt{}, err
	}
	if !validID(migrationID) {
		return Attempt{}, fail(ErrNotFound, "Migration not found for this database.")
	}
	draft, err := s.store.draft(ctx, s.store.pool, databaseID, migrationID)
	if err != nil {
		return Attempt{}, err
	}
	statements, err := s.precheck(draft, in)
	if err != nil {
		return Attempt{}, err
	}
	if s.targets == nil {
		return Attempt{}, fail(ErrTargetUnavailable, "Target execution is not configured on this server.")
	}

	lock, release, err := s.store.lock(ctx, databaseID, s.lockWait)
	if err != nil {
		return Attempt{}, err
	}
	defer release()
	// Revocation while waiting for the lock must still stop the execution.
	if _, err := s.authorize(ctx, databaseID, access.Edit); err != nil {
		return Attempt{}, err
	}
	if draft, err = s.store.shareDraft(ctx, lock, databaseID, migrationID); err != nil {
		return Attempt{}, err
	}
	if statements, err = s.precheck(draft, in); err != nil {
		return Attempt{}, err
	}
	target, err := s.targets.Open(ctx, databaseID)
	if err != nil {
		return Attempt{}, fail(ErrTargetUnavailable, "The target database is unavailable. Nothing was executed.")
	}
	defer target.Close()
	if err := s.reconcile(ctx, target, databaseID); err != nil {
		return Attempt{}, err
	}
	current, err := s.store.currentVersion(ctx, s.store.pool, databaseID)
	if err != nil {
		return Attempt{}, err
	}
	if current != in.ExpectedVersion {
		return Attempt{}, fail(ErrConflict, fmt.Sprintf("The database is at version %d, not %d. Reload to review the current history before executing.", current, in.ExpectedVersion))
	}

	id, err := newID()
	if err != nil {
		return Attempt{}, err
	}
	attempt := Attempt{ID: id, DatabaseID: databaseID, MigrationID: migrationID, MigrationTitle: draft.Title,
		DraftRevision: draft.Revision, CandidateVersion: current + 1, SQL: draft.SQL, SQLHash: draft.SQLHash(),
		Dialect: draft.Dialect, StatementCount: len(statements), ExecutorID: userID, Status: StatusRunning, StartedAt: s.now().UTC()}
	// The durable running attempt precedes any target change.
	if err := s.store.insertAttempt(ctx, attempt); err != nil {
		return Attempt{}, err
	}
	attempt, kind := s.run(ctx, target, attempt, statements)
	return s.record(ctx, attempt, kind)
}

// precheck validates a draft snapshot against the confirmation the caller saw.
func (s *Service) precheck(d Draft, in ExecuteInput) ([]Statement, error) {
	if d.Applied() {
		return nil, fail(ErrConflict, fmt.Sprintf("This migration was already applied as version %d and will not run again.", d.AppliedVersion))
	}
	if d.Revision != in.ExpectedRevision || d.SQLHash() != in.ExpectedSQLHash {
		return nil, fail(ErrConflict, "This migration changed after you reviewed it. Reload and confirm the current SQL.")
	}
	if d.Dialect != PostgreSQL {
		return nil, fail(ErrUnsupportedDialect, "Executing "+string(d.Dialect)+" migrations is not supported yet.")
	}
	statements, err := ParsePostgres(d.SQL)
	if err != nil {
		return nil, fail(ErrValidation, err.Error())
	}
	return statements, nil
}

// run executes all statements in one target transaction. The receipt claim is
// the first write, so the commit carries proof of exactly this attempt. The
// returned error kind classifies every outcome other than success.
func (s *Service) run(ctx context.Context, target Target, a Attempt, statements []Statement) (Attempt, error) {
	execCtx, cancel := context.WithTimeout(ctx, s.executeTimeout)
	defer cancel()
	failed := func(kind error, statement int, message string) (Attempt, error) {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			kind = ErrTimeout
		}
		a.Status, a.FailedStatement, a.Error = StatusFailed, statement, message
		return a, kind
	}
	if err := ProvisionReceipts(execCtx, target); err != nil {
		return failed(ErrTargetUnavailable, 0, "Could not prepare the execution receipt on the target: "+targetMessage(execCtx, err)+" Nothing was executed.")
	}
	tx, err := target.Begin(execCtx)
	if err != nil {
		return failed(ErrTargetUnavailable, 0, "Could not start a target transaction: "+targetMessage(execCtx, err)+" Nothing was executed.")
	}
	rollback := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
		defer cancel()
		// Without a commit the target cannot keep any change, even if this fails.
		_ = tx.Rollback(cleanup)
	}
	if _, err := tx.Exec(execCtx, insertReceipt, a.ID, a.DatabaseID, a.CandidateVersion, a.SQLHash); err != nil {
		rollback()
		return failed(ErrTargetUnavailable, 0, "Could not record the execution receipt: "+targetMessage(execCtx, err)+" Nothing was committed.")
	}
	for i, statement := range statements {
		if _, err := tx.Exec(execCtx, statement.SQL); err != nil {
			rollback()
			return failed(ErrExecutionFailed, i+1, fmt.Sprintf("Statement %d failed: %s All changes were rolled back.", i+1, targetMessage(execCtx, err)))
		}
	}
	err = tx.Commit(execCtx)
	switch {
	case err == nil:
		a.Status = StatusSucceeded
		return a, nil
	case errors.Is(err, ErrCommitUnknown):
		a.Status, a.Error = StatusUnknown, "The connection failed while committing, so the outcome is unknown. Further executions are blocked until the receipt is checked."
		a = s.resolve(ctx, target, a)
		switch a.Status {
		case StatusSucceeded:
			return a, nil
		case StatusFailed:
			return a, ErrExecutionFailed
		}
		return a, ErrOutcomeUnknown
	}
	return failed(ErrExecutionFailed, 0, "The commit was rejected: "+targetMessage(execCtx, err)+" All changes were rolled back.")
}

// record finishes the attempt in the platform. If that write fails after a
// target commit the attempt stays running and the next execution reconciles it.
func (s *Service) record(ctx context.Context, a Attempt, kind error) (Attempt, error) {
	finished := s.now().UTC()
	a.FinishedAt = &finished
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
	defer cancel()
	if _, err := s.store.finish(cleanup, a); err != nil {
		a.Status, a.FinishedAt = StatusUnknown, nil
		a.Error = "The target finished but sqlwise could not record the result. Further executions are blocked until the receipt is checked."
		return a, fail(ErrOutcomeUnknown, a.Error)
	}
	if kind != nil {
		return a, fail(kind, a.Error)
	}
	a.AppliedVersion = a.CandidateVersion
	return a, nil
}

// resolve checks the receipt of an attempt whose commit outcome is unknown.
func (s *Service) resolve(ctx context.Context, target Target, a Attempt) Attempt {
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
	defer cancel()
	outcome, err := resolveReceipt(probe, target, a)
	if err != nil {
		return a
	}
	now := s.now().UTC()
	switch outcome {
	case receiptApplied:
		a.Status, a.Error, a.ReconciledAt = StatusSucceeded, "", &now
	case receiptAbsent:
		a.Status, a.ReconciledAt = StatusFailed, &now
		a.Error = "The commit did not complete; the receipt check confirmed nothing was applied."
	}
	return a
}

// reconcile settles unfinished attempts while holding the database lock. A
// running attempt here belongs to an executor that lost its lock or crashed.
func (s *Service) reconcile(ctx context.Context, target Target, databaseID string) error {
	pending, err := s.store.blocking(ctx, databaseID)
	if err != nil {
		return err
	}
	for i := len(pending) - 1; i >= 0; i-- {
		a := pending[i]
		if a.Status == StatusPartiallyApplied {
			return fail(ErrRecoveryRequired, blockedMessage(a))
		}
		a = s.resolve(ctx, target, a)
		finished := s.now().UTC()
		a.FinishedAt = &finished
		if a.Status == StatusRunning {
			a.Status = StatusUnknown
			a.Error = "The executor stopped before recording an outcome and the receipt could not be checked."
		}
		if _, err := s.store.finish(ctx, a); err != nil {
			return err
		}
		if a.Status == StatusUnknown {
			return fail(ErrOutcomeUnknown, blockedMessage(a))
		}
	}
	return nil
}

// targetMessage keeps the server message and SQLSTATE but drops DETAIL/WHERE,
// which may contain row values.
func targetMessage(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "execution timed out."
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "the request was cancelled."
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		message := pgErr.Message
		if len(message) > 500 {
			message = message[:500] + "…"
		}
		return fmt.Sprintf("%s (SQLSTATE %s).", message, pgErr.Code)
	}
	if errors.Is(err, ErrTargetUnavailable) {
		return "the target connection is unavailable."
	}
	return "the target reported an error."
}
