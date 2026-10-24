package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/handlers"
	"sqlwise/internal/migration"
	"sqlwise/internal/schemadiff"
	"sqlwise/internal/web/views"
)

// databaseTargets are the registered-target adapters. Without them, execution
// and live snapshots report "not configured".
type databaseTargets struct {
	targets  migration.Targets
	snapshot schemadiff.Source
	store    *database.Store
	pools    *database.Pools
}

// loadDatabaseTargets resolves targets from the connection registrations in
// the platform database. The returned close releases the target pools.
func loadDatabaseTargets(pool *pgxpool.Pool) (databaseTargets, func(), error) {
	cipher, err := database.NewCipher(os.Getenv(database.KeyEnv))
	if err != nil {
		return databaseTargets{}, nil, err
	}
	store := database.NewStore(pool, cipher)
	pools := database.NewPools(store)
	adapter := migrationTargets{resolver: pools}
	return databaseTargets{targets: adapter, snapshot: adapter, store: store, pools: pools}, pools.Close, nil
}

func registerDatabaseRoutes(router *gin.Engine, service *auth.Service, policy *access.Service, pool *pgxpool.Pool, assets views.Assets, targets databaseTargets) {
	guard := handlers.NewAuth(service, assets, os.Getenv("APP_ENV") == "production", policy)
	migrations := migration.NewService(migration.NewStore(pool), policy, targets.targets, time.Now)
	diff := schemadiff.NewService(policy, targets.snapshot, migrations)
	handlers.NewMigrations(guard, policy, migrations, diff, assets).Register(router)
	if targets.store != nil {
		handlers.NewConnectionAdmin(guard, policy, targets.store, targets.pools, assets).Register(router)
	}
}

func checkMigrationSchema(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `SELECT d.revision, a.seq, a.status, l.database_id FROM migration_drafts d, migration_attempts a, migration_locks l LIMIT 0`); err != nil {
		return errors.New("migration schema unavailable; run go run ./cmd/admin -schema-only")
	}
	return nil
}

func checkConnectionSchema(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `SELECT database_id, dbms, host, port, database_name, username, tls_mode, password_sealed FROM database_connections LIMIT 0`); err != nil {
		return errors.New("connection schema unavailable; run go run ./cmd/admin -schema-only")
	}
	return nil
}
