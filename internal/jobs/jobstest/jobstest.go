// Package jobstest provides real PostgreSQL fixtures for queue and worker tests.
package jobstest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/database"
	"sqlwise/internal/jobs"
	"sqlwise/internal/migration/migrationtest"
)

// Gates opens real gate sessions on caller-owned target pools.
type Gates struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
	opens atomic.Int64
}

func NewGates() *Gates { return &Gates{pools: map[string]*pgxpool.Pool{}} }

func (g *Gates) Add(databaseID string, pool *pgxpool.Pool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pools[databaseID] = pool
}

// Opens counts gate requests, proving refused jobs never reach a target.
func (g *Gates) Opens() int64 { return g.opens.Load() }

func (g *Gates) Gate(ctx context.Context, databaseID string, wait time.Duration) (*database.Gate, error) {
	g.opens.Add(1)
	g.mu.Lock()
	pool, ok := g.pools[databaseID]
	g.mu.Unlock()
	if !ok {
		return nil, database.Unavailable()
	}
	return database.AcquireGate(ctx, pool.Config().ConnConfig.Copy(), databaseID, wait)
}

// Store applies the jobs schema on a migration platform.
func Store(t *testing.T, p *migrationtest.Platform) *jobs.Store {
	t.Helper()
	store := jobs.NewStore(p.Pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	return store
}

func ID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// Request inserts a consumed request and its draft without review, for
// queue-only tests; reviewed flows use the migration service instead.
func Request(t *testing.T, p *migrationtest.Platform, databaseID, actorID string) (requestID, executionID, migrationID string) {
	t.Helper()
	requestID, executionID, migrationID = ID(t), ID(t), ID(t)
	p.Exec(t, `INSERT INTO migration_drafts (id, database_id, title, sql_text, dialect, revision, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, 'fixture', 'SELECT 1', 'postgresql', 1, $3, $3, now(), now())`, migrationID, databaseID, actorID)
	p.Exec(t, `INSERT INTO migration_requests (id, database_id, migration_id, operation, definition_revision, definition_sha256,
		expected_version, requested_by, status, execution_id, created_at, updated_at)
		VALUES ($1, $2, $3, 'up', 1, repeat('a', 64), 0, $4, 'queued', $5, now(), now())`, requestID, databaseID, migrationID, actorID, executionID)
	return requestID, executionID, migrationID
}

// Insert publishes a job row directly, e.g. a recovery job before the review
// workflow can request one.
func Insert(t *testing.T, p *migrationtest.Platform, j jobs.Job) jobs.Job {
	t.Helper()
	if j.ID == "" {
		j.ID = ID(t)
	}
	var recovers *string
	if j.RecoversExecutionID != "" {
		recovers = &j.RecoversExecutionID
	}
	p.Exec(t, `INSERT INTO jobs (id, database_id, migration_id, request_id, execution_id, recovers_execution_id, operation, actor_id,
		idempotency_key, status, max_attempts, next_run_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $1, 'queued', 3, now(), now(), now())`,
		j.ID, j.DatabaseID, j.MigrationID, j.RequestID, j.ExecutionID, recovers, j.Operation, j.ActorID)
	return j
}

// Wait polls until the job satisfies done or the timeout elapses.
func Wait(t *testing.T, store *jobs.Store, databaseID, jobID string, timeout time.Duration, done func(jobs.Job) bool) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		j, err := store.Get(t.Context(), databaseID, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if done(j) {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s/%s after %v: %s", jobID, j.Status, j.Effect, timeout, j.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Settled reports a job the worker will not deliver again.
func Settled(j jobs.Job) bool { return j.Terminal() }

// Config is a fast worker configuration for tests.
func Config(id string) jobs.Config {
	cfg := jobs.DefaultConfig()
	cfg.ID, cfg.Poll, cfg.Lease, cfg.LockWait = id, 50*time.Millisecond, 2*time.Second, 300*time.Millisecond
	cfg.Defer, cfg.Recheck, cfg.ShutdownGrace = 100*time.Millisecond, 200*time.Millisecond, 10*time.Second
	return cfg
}

// Start runs a worker until the test ends and returns its stop function.
func Start(t *testing.T, w *jobs.Worker) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := w.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}
