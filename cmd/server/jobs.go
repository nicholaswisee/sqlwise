package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/jobs"
	"sqlwise/internal/jobs/migrationrun"
	"sqlwise/internal/migration"
)

// newJobQueue puts the durable queue in front of the migration service. The
// API only publishes; cmd/worker executes.
func newJobQueue(pool *pgxpool.Pool, migrations *migration.Service) *jobs.Queue {
	store := jobs.NewStore(pool)
	jobs.BindMetrics(store)
	return jobs.NewQueue(store, migrationrun.Consumer{Service: migrations})
}

func checkJobSchema(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return jobs.NewStore(pool).CheckSchema(ctx)
}
