package jobs

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/telemetry"
)

// MaxAttempts is the frozen retry budget: three deliveries in total.
const MaxAttempts = 3

// Consumed is the execution identity of an approved request.
type Consumed struct {
	ExecutionID         string
	MigrationID         string
	Operation           string
	ActorID             string
	RecoversExecutionID string
}

// Consumer authorizes and consumes an approved request in the publishing
// transaction; the migration domain implements it at the composition root.
type Consumer interface {
	Consume(ctx context.Context, tx pgx.Tx, databaseID, requestID string) (Consumed, error)
}

// Queue publishes jobs. It never runs target SQL.
type Queue struct {
	store    *Store
	consumer Consumer
}

func NewQueue(store *Store, consumer Consumer) *Queue {
	return &Queue{store: store, consumer: consumer}
}

func (q *Queue) Store() *Store { return q.store }

// Enqueue consumes the request and publishes its job atomically, so the job
// exists durably before the caller answers 202. Repeating the call with the
// same request returns the same job.
func (q *Queue) Enqueue(ctx context.Context, databaseID, requestID, key string) (Job, bool, error) {
	if key == "" || len(key) > 128 {
		return Job{}, false, fail(ErrValidation, "An idempotency key of at most 128 characters is required.")
	}
	var out Job
	var created bool
	err := pgx.BeginFunc(ctx, q.store.pool, func(tx pgx.Tx) error {
		c, err := q.consumer.Consume(ctx, tx, databaseID, requestID)
		if err != nil {
			return err
		}
		id, err := newID()
		if err != nil {
			return err
		}
		j := Job{ID: id, DatabaseID: databaseID, MigrationID: c.MigrationID, RequestID: requestID, ExecutionID: c.ExecutionID,
			RecoversExecutionID: c.RecoversExecutionID, Operation: c.Operation, ActorID: c.ActorID, MaxAttempts: MaxAttempts,
			Trace: telemetry.InjectTrace(ctx)}
		out, created, err = q.store.insert(ctx, tx, j, key)
		return err
	})
	if err != nil {
		return Job{}, false, err
	}
	if created {
		jobsPublished.WithLabelValues(operationLabel(out.Operation)).Inc()
		slog.InfoContext(ctx, "job enqueued", "event", "job.enqueued", "job_id", out.ID, "request_id", out.RequestID,
			"execution_id", out.ExecutionID, "database", out.DatabaseID, "operation", out.Operation, "actor", out.ActorID,
			"correlation_id", telemetry.Correlation(ctx), "status", string(out.Status))
	}
	return out, created, nil
}

func (q *Queue) Job(ctx context.Context, databaseID, jobID string) (Job, error) {
	return q.store.Get(ctx, databaseID, jobID)
}

func (q *Queue) List(ctx context.Context, databaseID string, limit int) ([]Job, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	return q.store.List(ctx, databaseID, limit)
}
