package auth

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
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
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(311001)"); err != nil {
			return fmt.Errorf("lock auth schema: %w", err)
		}
		if _, err := tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("apply auth schema: %w", err)
		}
		return nil
	})
}

func (s *Store) BootstrapAdmin(ctx context.Context, credentials Credentials) error {
	hash, err := HashPassword(credentials.password)
	if err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return fmt.Errorf("generate user ID: %w", err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(311002)"); err != nil {
			return fmt.Errorf("lock admin bootstrap: %w", err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE is_platform_admin)").Scan(&exists); err != nil {
			return fmt.Errorf("check administrator: %w", err)
		}
		if exists {
			return ErrAdminExists
		}
		_, err := tx.Exec(ctx, `INSERT INTO users (id, username, password_hash, is_platform_admin)
			VALUES ($1, $2, $3, true)`, hex.EncodeToString(id), credentials.username, hash)
		if err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}
		return nil
	})
}

type userRecord struct {
	principal    Principal
	passwordHash string
	active       bool
}

func (s *Store) findUser(ctx context.Context, username string) (userRecord, error) {
	var user userRecord
	err := s.pool.QueryRow(ctx, `SELECT id, username, is_platform_admin, password_hash, active
		FROM users WHERE username = $1`, username).Scan(&user.principal.UserID, &user.principal.Username,
		&user.principal.IsPlatformAdmin, &user.passwordHash, &user.active)
	if errors.Is(err, pgx.ErrNoRows) {
		return userRecord{}, ErrCredentials
	}
	if err != nil {
		return userRecord{}, fmt.Errorf("find user: %w", err)
	}
	return user, nil
}
