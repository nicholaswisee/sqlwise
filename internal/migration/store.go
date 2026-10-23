package migration

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) ApplySchema(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(311005)"); err != nil {
			return fmt.Errorf("lock migration schema: %w", err)
		}
		if _, err := tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("apply migration schema: %w", err)
		}
		return nil
	})
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (s *Store) databaseName(ctx context.Context, databaseID string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM database_registrations WHERE id=$1 AND active`, databaseID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(ErrNotFound, "Database not found.")
	}
	return name, err
}

const draftColumns = `d.id, d.database_id, d.title, d.sql_text, d.dialect, d.revision,
	d.created_by, cu.username, d.updated_by, uu.username, d.created_at, d.updated_at,
	COALESCE((SELECT a.applied_version FROM migration_attempts a WHERE a.migration_id=d.id AND a.status='succeeded'), 0),
	COALESCE((SELECT a.status FROM migration_attempts a WHERE a.migration_id=d.id ORDER BY a.seq DESC LIMIT 1), '')`

const draftFrom = ` FROM migration_drafts d JOIN users cu ON cu.id=d.created_by JOIN users uu ON uu.id=d.updated_by`

func scanDraft(row pgx.Row) (Draft, error) {
	var d Draft
	var dialect, status string
	err := row.Scan(&d.ID, &d.DatabaseID, &d.Title, &d.SQL, &dialect, &d.Revision, &d.CreatedBy, &d.CreatedByName,
		&d.UpdatedBy, &d.UpdatedByName, &d.CreatedAt, &d.UpdatedAt, &d.AppliedVersion, &status)
	d.Dialect, d.LastStatus = Dialect(dialect), Status(status)
	return d, err
}

// draft loads a migration only when it belongs to databaseID.
func (s *Store) draft(ctx context.Context, q querier, databaseID, migrationID string) (Draft, error) {
	d, err := scanDraft(q.QueryRow(ctx, `SELECT `+draftColumns+draftFrom+` WHERE d.database_id=$1 AND d.id=$2`, databaseID, migrationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, fail(ErrNotFound, "Migration not found for this database.")
	}
	return d, err
}

func (s *Store) drafts(ctx context.Context, databaseID string) ([]Draft, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+draftColumns+draftFrom+` WHERE d.database_id=$1 ORDER BY d.created_at DESC, d.id DESC`, databaseID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Draft, error) { return scanDraft(row) })
}

func (s *Store) createDraft(ctx context.Context, d Draft) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO migration_drafts
		(id, database_id, title, sql_text, dialect, revision, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $6, $7, $7)`, d.ID, d.DatabaseID, d.Title, d.SQL, string(d.Dialect), d.CreatedBy, d.CreatedAt)
	return err
}

// updateDraft applies an edit only at the expected revision of an unapplied draft.
func (s *Store) updateDraft(ctx context.Context, databaseID, migrationID string, in UpdateInput, userID string, now time.Time) (Draft, error) {
	var updated Draft
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A running execution holds this row FOR SHARE until it records its outcome.
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', '2s', true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM migration_drafts WHERE database_id=$1 AND id=$2 FOR UPDATE`, databaseID, migrationID); err != nil {
			return lockError(err, "This migration is executing. Wait for the result before editing.")
		}
		current, err := s.draft(ctx, tx, databaseID, migrationID)
		if err != nil {
			return err
		}
		if current.Applied() {
			return fail(ErrConflict, "This migration was applied and is read-only. Create a new migration for corrections.")
		}
		if current.Revision != in.ExpectedRevision {
			return fail(ErrConflict, fmt.Sprintf("This migration changed since you opened it (revision %d, now %d). Reload before saving.", in.ExpectedRevision, current.Revision))
		}
		if current.LastStatus == StatusRunning || current.LastStatus == StatusUnknown || current.LastStatus == StatusPartiallyApplied {
			return fail(ErrConflict, "This migration has an unresolved execution and cannot be edited until its outcome is known.")
		}
		if _, err := tx.Exec(ctx, `UPDATE migration_drafts SET title=$2, sql_text=$3, revision=revision+1, updated_by=$4, updated_at=$5 WHERE id=$1`,
			migrationID, in.Title, in.SQL, userID, now); err != nil {
			return lockError(err, "")
		}
		updated, err = s.draft(ctx, tx, databaseID, migrationID)
		return err
	})
	return updated, err
}

const attemptColumns = `a.id, a.seq, a.database_id, a.migration_id, d.title, a.draft_revision, a.candidate_version,
	COALESCE(a.applied_version, 0), a.sql_text, a.sql_sha256, a.dialect, a.statement_count, a.executor_id, u.username,
	a.status, COALESCE(a.error_message, ''), COALESCE(a.failed_statement, 0), a.started_at, a.finished_at, a.reconciled_at`

const attemptFrom = ` FROM migration_attempts a JOIN migration_drafts d ON d.id=a.migration_id JOIN users u ON u.id=a.executor_id`

func scanAttempt(row pgx.Row) (Attempt, error) {
	var a Attempt
	var dialect, status string
	err := row.Scan(&a.ID, &a.Seq, &a.DatabaseID, &a.MigrationID, &a.MigrationTitle, &a.DraftRevision, &a.CandidateVersion,
		&a.AppliedVersion, &a.SQL, &a.SQLHash, &dialect, &a.StatementCount, &a.ExecutorID, &a.ExecutorName,
		&status, &a.Error, &a.FailedStatement, &a.StartedAt, &a.FinishedAt, &a.ReconciledAt)
	a.Dialect, a.Status = Dialect(dialect), Status(status)
	return a, err
}

func (s *Store) attempts(ctx context.Context, where string, args ...any) ([]Attempt, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+attemptColumns+attemptFrom+` WHERE `+where+` ORDER BY a.seq DESC`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attempt, error) { return scanAttempt(row) })
}

func (s *Store) attempt(ctx context.Context, databaseID, migrationID, attemptID string) (Attempt, error) {
	a, err := scanAttempt(s.pool.QueryRow(ctx, `SELECT `+attemptColumns+attemptFrom+` WHERE a.database_id=$1 AND a.migration_id=$2 AND a.id=$3`,
		databaseID, migrationID, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, fail(ErrNotFound, "Attempt not found for this migration.")
	}
	return a, err
}

func (s *Store) currentVersion(ctx context.Context, q querier, databaseID string) (int64, error) {
	var version int64
	err := q.QueryRow(ctx, `SELECT COALESCE(max(applied_version), 0) FROM migration_attempts WHERE database_id=$1`, databaseID).Scan(&version)
	return version, err
}

// blocking returns the oldest unresolved attempt that prevents new executions.
func (s *Store) blocking(ctx context.Context, databaseID string) ([]Attempt, error) {
	return s.attempts(ctx, `a.database_id=$1 AND a.status IN ('running', 'unknown', 'partially_applied')`, databaseID)
}

func (s *Store) insertAttempt(ctx context.Context, a Attempt) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO migration_attempts
		(id, database_id, migration_id, draft_revision, candidate_version, sql_text, sql_sha256, dialect, statement_count, executor_id, status, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'running', $11)`,
		a.ID, a.DatabaseID, a.MigrationID, a.DraftRevision, a.CandidateVersion, a.SQL, a.SQLHash, string(a.Dialect), a.StatementCount, a.ExecutorID, a.StartedAt)
	return err
}

// finish records an outcome for a running or unknown attempt; it is a no-op
// once another writer already finished it.
func (s *Store) finish(ctx context.Context, a Attempt) (bool, error) {
	var applied *int64
	if a.Status == StatusSucceeded {
		applied = &a.CandidateVersion
	}
	var message *string
	if a.Error != "" {
		message = &a.Error
	}
	var failed *int
	if a.FailedStatement > 0 {
		failed = &a.FailedStatement
	}
	tag, err := s.pool.Exec(ctx, `UPDATE migration_attempts SET status=$2, applied_version=$3, error_message=$4,
		failed_statement=$5, finished_at=COALESCE(finished_at, $6), reconciled_at=$7 WHERE id=$1 AND status IN ('running', 'unknown')`,
		a.ID, string(a.Status), applied, message, failed, a.FinishedAt, a.ReconciledAt)
	return tag.RowsAffected() == 1, err
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// lockError maps lock timeouts to ErrBusy and immutability triggers to ErrConflict.
func lockError(err error, busy string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "55P03" && busy != "":
			return fail(ErrBusy, busy)
		case pgErr.Code == "55000":
			return fail(ErrConflict, "This migration was applied or is executing and cannot be edited.")
		}
	}
	return err
}

// lock holds the per-target execution lock on a dedicated platform connection
// until release. Waiting longer than wait fails with ErrBusy. The returned
// transaction stays open so callers can also hold the executed draft row.
func (s *Store) lock(ctx context.Context, databaseID string, wait time.Duration) (pgx.Tx, func(), error) {
	if _, err := s.pool.Exec(ctx, `INSERT INTO migration_locks (database_id) VALUES ($1) ON CONFLICT DO NOTHING`, databaseID); err != nil {
		return nil, nil, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if tx.Rollback(cleanup) != nil {
			// Closing the session is the only other way to drop its row lock.
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}
	_, err = tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", wait.Milliseconds()))
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT 1 FROM migration_locks WHERE database_id=$1 FOR UPDATE`, databaseID)
	}
	if err != nil {
		release()
		return nil, nil, lockError(err, "Another migration is running on this database. Try again shortly.")
	}
	return tx, release, nil
}

// shareDraft keeps the executed draft from being edited until the lock ends.
func (s *Store) shareDraft(ctx context.Context, lock pgx.Tx, databaseID, migrationID string) (Draft, error) {
	if _, err := lock.Exec(ctx, `SELECT 1 FROM migration_drafts WHERE database_id=$1 AND id=$2 FOR SHARE`, databaseID, migrationID); err != nil {
		return Draft{}, lockError(err, "This migration is being edited. Try again shortly.")
	}
	return s.draft(ctx, lock, databaseID, migrationID)
}
