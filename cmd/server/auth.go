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
		return nil, nil, errors.New("DATABASE_URL is required; follow the administrator setup in README.md")
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
	if _, err := pool.Exec(ctx, "SELECT token_hash FROM auth_sessions LIMIT 0"); err != nil {
		pool.Close()
		return nil, nil, errors.New("authentication schema unavailable; check DATABASE_URL and run administrator setup")
	}
	service, err := auth.NewService(auth.NewStore(pool), time.Now)
	if err != nil {
		pool.Close()
		return nil, nil, errors.New("could not initialize authentication")
	}
	return service, pool, nil
}
