package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/handlers"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/pgtarget"
	"sqlwise/internal/schemadiff"
	"sqlwise/internal/web/views"
)

// databaseTargets are the registered-target adapters. Without them, execution
// and live snapshots report "not configured".
type databaseTargets struct {
	targets  migration.Targets
	snapshot schemadiff.Source
}

// loadDatabaseTargets uses the pgtarget stand-in configured from
// SQLWISE_DEV_TARGETS. The returned close releases its pools.
// TODO(jason): build from database.Resolver and SchemaSnapshot instead.
func loadDatabaseTargets() (databaseTargets, func(), error) {
	spec := os.Getenv(pgtarget.EnvVar)
	if spec == "" {
		return databaseTargets{}, func() {}, nil
	}
	registry, err := pgtarget.Connect(spec)
	if err != nil {
		return databaseTargets{}, nil, err
	}
	log.Printf("migration targets: %d from %s (temporary stand-in for the database adapter)", registry.Len(), pgtarget.EnvVar)
	return databaseTargets{targets: registry, snapshot: registry}, registry.Close, nil
}

func registerDatabaseRoutes(router *gin.Engine, service *auth.Service, pool *pgxpool.Pool, assets views.Assets, targets databaseTargets) {
	guard := handlers.NewAuth(service, assets, os.Getenv("APP_ENV") == "production")
	authz := access.NewService(access.NewStore(pool), service)
	migrations := migration.NewService(migration.NewStore(pool), authz, targets.targets, time.Now)
	diff := schemadiff.NewService(authz, targets.snapshot, migrations)
	handlers.NewMigrations(guard, migrations, diff, assets).Register(router)
}

func checkMigrationSchema(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `SELECT d.revision, a.seq, a.status, l.database_id FROM migration_drafts d, migration_attempts a, migration_locks l LIMIT 0`); err != nil {
		return errors.New("migration schema unavailable; run go run ./cmd/admin -schema-only")
	}
	return nil
}
