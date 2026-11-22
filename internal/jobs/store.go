package jobs

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ApplySchema creates the queue; it runs after the migration schema.
func (s *Store) ApplySchema(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(311006)"); err != nil {
			return fmt.Errorf("lock jobs schema: %w", err)
		}
		if _, err := tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("apply jobs schema: %w", err)
		}
		return nil
	})
}

// CheckSchema fails when the queue schema was not initialized.
func (s *Store) CheckSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `SELECT j.id, j.status, j.effect, j.lease_until, j.resolved_by_job_id, d.outcome
		FROM jobs j LEFT JOIN job_deliveries d ON d.job_id=j.id LIMIT 0`)
	if err != nil {
		return errors.New("jobs schema unavailable; run go run ./cmd/admin -schema-only")
	}
	return nil
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

const jobColumns = `j.id, j.database_id, j.migration_id, j.request_id, j.execution_id, COALESCE(j.recovers_execution_id, ''), j.operation,
	j.actor_id, j.status, j.effect, j.attempts, j.max_attempts, j.next_run_at, j.committed_batches, j.committed_rows, j.total_rows,
	COALESCE(j.error_message, ''), COALESCE(j.traceparent, ''), COALESCE(j.tracestate, ''), COALESCE(j.resolved_by_job_id, ''),
	j.created_at, j.started_at, j.finished_at, j.updated_at`

func scanJob(row pgx.Row, extra ...any) (Job, error) {
	var j Job
	var status, effect string
	dest := []any{&j.ID, &j.DatabaseID, &j.MigrationID, &j.RequestID, &j.ExecutionID, &j.RecoversExecutionID, &j.Operation,
		&j.ActorID, &status, &effect, &j.Attempts, &j.MaxAttempts, &j.NextRunAt, &j.CommittedBatches, &j.CommittedRows, &j.TotalRows,
		&j.Error, &j.Trace.Traceparent, &j.Trace.Tracestate, &j.ResolvedBy, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.UpdatedAt}
	err := row.Scan(append(dest, extra...)...)
	j.Status, j.Effect = Status(status), Effect(effect)
	return j, err
}

// insert publishes j unless its request or key already has a job.
func (s *Store) insert(ctx context.Context, tx pgx.Tx, j Job, key string) (Job, bool, error) {
	byKey, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs j WHERE j.database_id=$1 AND j.idempotency_key=$2`, j.DatabaseID, key))
	switch {
	case err == nil && byKey.RequestID != j.RequestID:
		return Job{}, false, fail(ErrConflict, "This idempotency key was already used for another request.")
	case err == nil:
		return byKey, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Job{}, false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO jobs (id, database_id, migration_id, request_id, execution_id, recovers_execution_id, operation,
		actor_id, idempotency_key, traceparent, tracestate, status, max_attempts, next_run_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'queued', $12, now(), now(), now())
		ON CONFLICT DO NOTHING`, j.ID, j.DatabaseID, j.MigrationID, j.RequestID, j.ExecutionID, nullable(j.RecoversExecutionID), j.Operation,
		j.ActorID, key, nullable(j.Trace.Traceparent), nullable(j.Trace.Tracestate), j.MaxAttempts)
	if err != nil {
		return Job{}, false, err
	}
	out, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs j WHERE j.request_id=$1`, j.RequestID))
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent publisher took the key for another request.
		return Job{}, false, fail(ErrConflict, "This idempotency key was already used for another request.")
	}
	return out, tag.RowsAffected() == 1, err
}

// Get loads a job of databaseID; jobs of other databases are not found.
func (s *Store) Get(ctx context.Context, databaseID, jobID string) (Job, error) {
	var blocker string
	j, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+`, COALESCE((SELECT b.id FROM jobs b WHERE b.database_id=j.database_id
		AND b.status='blocked' AND b.resolved_by_job_id IS NULL AND b.id<>j.id AND j.status='queued'
		AND j.operation NOT IN ('resume', 'compensate') ORDER BY b.created_at LIMIT 1), '')
		FROM jobs j WHERE j.database_id=$1 AND j.id=$2`, databaseID, jobID), &blocker)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, fail(ErrNotFound, "Job not found for this database.")
	}
	j.BlockedBy = blocker
	return j, err
}

// List returns the newest jobs of a database.
func (s *Store) List(ctx context.Context, databaseID string, limit int) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+jobColumns+` FROM jobs j WHERE j.database_id=$1 ORDER BY j.created_at DESC, j.id DESC LIMIT $2`, databaseID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Job, error) { return scanJob(row) })
}

// claimed is a job together with the status it had before this claim.
type claimed struct {
	Job
	previous Status
	// first is true when this delivery started the job.
	first bool
	// delivery identifies this claim; a retry or reclaim gets a new one.
	delivery string
}

// claim takes the next deliverable job: queued and due, running with an
// expired lease (its worker died), or blocked with an unknown effect that
// is due for reconciliation. Databases with an unresolved block only
// deliver that block and recovery jobs. Lease expiry never fences the
// target; the gate does.
func (s *Store) claim(ctx context.Context, worker string, lease time.Duration) (claimed, bool, error) {
	var c claimed
	var previous string
	delivery, err := newID()
	if err != nil {
		return claimed{}, false, err
	}
	c.Job, err = scanJob(s.pool.QueryRow(ctx, `WITH next AS (
			SELECT j.id, j.status AS previous, j.started_at IS NULL AS first FROM jobs j
			WHERE ((j.status='queued' AND j.next_run_at <= now())
				OR (j.status='running' AND j.lease_until < now())
				OR (j.status='blocked' AND j.effect='unknown' AND j.resolved_by_job_id IS NULL AND j.next_run_at <= now()))
			AND (j.status='blocked' OR j.operation IN ('resume', 'compensate') OR NOT EXISTS (SELECT 1 FROM jobs b
				WHERE b.database_id=j.database_id AND b.status='blocked' AND b.resolved_by_job_id IS NULL))
			AND NOT EXISTS (SELECT 1 FROM jobs r WHERE r.database_id=j.database_id AND r.id<>j.id
				AND r.status='running' AND r.lease_until >= now())
			ORDER BY j.next_run_at, j.created_at
			LIMIT 1 FOR UPDATE OF j SKIP LOCKED
		), abandoned AS (
			-- A delivery still open here lost its lease: its worker died or stalled.
			-- It cannot see the row inserted below, which shares this statement's snapshot.
			UPDATE job_deliveries SET finished_at=now(), outcome='abandoned' WHERE job_id IN (SELECT id FROM next) AND finished_at IS NULL
		), delivered AS (
			INSERT INTO job_deliveries (id, job_id, worker_id, claimed_at)
			SELECT $3, id, $1, now() FROM next
		)
		UPDATE jobs j SET status='running', claimed_by=$1, lease_until=now() + make_interval(secs => $2),
			started_at=COALESCE(j.started_at, now()), finished_at=NULL, updated_at=now()
		FROM next WHERE j.id=next.id
		RETURNING `+jobColumns+`, next.previous, next.first`, worker, lease.Seconds(), delivery), &previous, &c.first)
	if errors.Is(err, pgx.ErrNoRows) {
		return claimed{}, false, nil
	}
	c.previous, c.delivery = Status(previous), delivery
	return c, err == nil, err
}

// expireOwn ends the leases of a previous process that used this worker ID.
func (s *Store) expireOwn(ctx context.Context, worker string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE jobs SET lease_until=now() - interval '1 second', updated_at=now()
		WHERE claimed_by=$1 AND status='running'`, worker)
	return tag.RowsAffected(), err
}

// heartbeat extends the lease; false means another worker took the claim.
func (s *Store) heartbeat(ctx context.Context, jobID, worker string, lease time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE jobs SET lease_until=now() + make_interval(secs => $3), updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND status='running'`, jobID, worker, lease.Seconds())
	return tag.RowsAffected() == 1, err
}

// finish records a delivery's outcome while the claim is still ours.
func (s *Store) finish(ctx context.Context, jobID, worker string, o Outcome, attempts int, recheck time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `WITH delivery AS (`+closeDelivery("$3")+`)
		UPDATE jobs SET status=$3, effect=$4, error_message=$5, attempts=$6, finished_at=now(),
		next_run_at=now() + make_interval(secs => $7), claimed_by=NULL, lease_until=NULL, updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND status='running'`, jobID, worker, string(o.Status), string(o.Effect), nullable(clip(o.Message)),
		attempts, recheck.Seconds())
	return tag.RowsAffected() == 1, err
}

// closeDelivery ends the open delivery of job $1 claimed by worker $2; a
// delivery another claim already abandoned stays as recorded.
func closeDelivery(outcome string) string {
	return `UPDATE job_deliveries SET finished_at=now(), outcome=` + outcome + ` WHERE job_id=$1 AND worker_id=$2 AND finished_at IS NULL`
}

// requeue returns a claimed job for a later delivery. A blocked job being
// reconciled goes back to blocked.
func (s *Store) requeue(ctx context.Context, jobID, worker string, previous Status, attempts int, delay time.Duration, message string) (bool, error) {
	status, finished := "queued", "NULL"
	if previous == Blocked {
		status, finished = "blocked", "now()"
	}
	tag, err := s.pool.Exec(ctx, `WITH delivery AS (`+closeDelivery("'requeued'")+`)
		UPDATE jobs SET status=$3, attempts=$4, next_run_at=now() + make_interval(secs => $5),
		error_message=COALESCE($6, error_message), finished_at=`+finished+`, claimed_by=NULL, lease_until=NULL, updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND status='running'`, jobID, worker, status, attempts, delay.Seconds(), nullable(clip(message)))
	return tag.RowsAffected() == 1, err
}

// progress projects durable target progress onto the job for polling.
func (s *Store) progress(ctx context.Context, jobID, worker string, batches, rows int64, total *int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET committed_batches=$3, committed_rows=$4, total_rows=$5, updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND status='running'`, jobID, worker, batches, rows, total)
	return err
}

// blocker returns the unresolved blocked job of a database other than jobID.
func (s *Store) blocker(ctx context.Context, databaseID, jobID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM jobs WHERE database_id=$1 AND status='blocked' AND resolved_by_job_id IS NULL
		AND id<>$2 ORDER BY created_at LIMIT 1`, databaseID, jobID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// resolve marks the blocked job of an interrupted execution as handed to recovery.
func (s *Store) resolve(ctx context.Context, databaseID, executionID, recoveryJobID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET resolved_by_job_id=$3, updated_at=now()
		WHERE database_id=$1 AND execution_id=$2 AND status='blocked' AND resolved_by_job_id IS NULL`, databaseID, executionID, recoveryJobID)
	return err
}

// Counts returns durable jobs per status for the status gauges.
func (s *Store) Counts(ctx context.Context) (map[Status]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Status]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[Status(status)] = n
	}
	return out, rows.Err()
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clip(s string) string {
	if r := []rune(s); len(r) > 2000 {
		return string(r[:1999]) + "…"
	}
	return s
}
