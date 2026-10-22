package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/auth"
)

func connectAuth() (*auth.Service, *pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, nil, errors.New("DATABASE_URL is required; configure the platform database before starting the server")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, nil, errors.New("invalid platform database configuration")
	}
	config.MaxConns = 8
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, nil, errors.New("could not initialize platform database pool")
	}
	if _, err := pool.Exec(ctx, `SELECT s.token_hash, s.family_id, s.alias_until, s.rotated_at,
		s.successor_hash, s.authz_epoch, f.id, f.revoked_at, u.authz_epoch
		FROM auth_sessions s
		LEFT JOIN auth_session_families f ON f.id = s.family_id
		LEFT JOIN users u ON u.id = s.user_id
		LIMIT 0`); err != nil {
		pool.Close()
		return nil, nil, errors.New("authentication schema unavailable; run go run ./cmd/admin -schema-only")
	}
	if _, err := pool.Exec(ctx, `SELECT g.id,g.name,g.active,m.user_id,
		d.id,d.name,d.active,x.permission FROM groups g
		LEFT JOIN group_memberships m ON m.group_id=g.id
		LEFT JOIN group_database_grants x ON x.group_id=g.id
		LEFT JOIN database_registrations d ON d.id=x.database_id LIMIT 0`); err != nil {
		pool.Close()
		return nil, nil, errors.New("access schema unavailable; run go run ./cmd/admin -schema-only")
	}
	service, err := auth.NewService(auth.NewStore(pool), time.Now)
	if err != nil {
		pool.Close()
		return nil, nil, errors.New("could not initialize authentication")
	}
	return service, pool, nil
}
