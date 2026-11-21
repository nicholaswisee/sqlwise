package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/database"
	"sqlwise/internal/jobs"
	"sqlwise/internal/jobs/migrationrun"
	"sqlwise/internal/migration"
)

// startWorker runs a real worker beside the API under test, as cmd/worker does.
func startWorker(t *testing.T, pool *pgxpool.Pool, policy *access.Service, pools *database.Pools) {
	t.Helper()
	store := jobs.NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := jobs.DefaultConfig()
	cfg.ID, cfg.Poll, cfg.Lease, cfg.LockWait = "test-worker", 20*time.Millisecond, 5*time.Second, time.Second
	migrations := migration.NewService(migration.NewStore(pool), policy, nil, time.Now)
	w, err := jobs.NewWorker(store, pools, migrationrun.Runner{Service: migrations}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
}
