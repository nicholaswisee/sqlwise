package auth

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AdminUser is a credential-free account record for platform administration.
type AdminUser struct {
	ID              string
	Username        string
	IsPlatformAdmin bool
	Active          bool
}

func (s *Service) ListAdminUsers(ctx context.Context) ([]AdminUser, error) {
	var users []AdminUser
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		actor, err := s.ValidateContext(ctx, tx)
		if err != nil {
			return err
		}
		if !actor.IsPlatformAdmin {
			return ErrSession
		}
		rows, err := tx.Query(ctx, `SELECT id,username,is_platform_admin,active FROM users ORDER BY username,id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u AdminUser
			if err := rows.Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.Active); err != nil {
				return err
			}
			users = append(users, u)
		}
		return rows.Err()
	})
	return users, err
}

func (s *Service) RenameUser(ctx context.Context, userID, username string) error {
	credentials, err := ParseCredentials(username, "placeholder")
	if err != nil {
		return ErrInvalidInput
	}
	return s.adminTransaction(ctx, func(tx pgx.Tx, now time.Time) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrUserNotFound
		}
		_, err := tx.Exec(ctx, `UPDATE users SET username=$2,authz_epoch=authz_epoch+1,updated_at=$3 WHERE id=$1`, userID, credentials.username, now)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrUsernameTaken
		}
		return err
	})
}

func (s *Service) UpdateUserPassword(ctx context.Context, userID, password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 128 {
		return ErrInvalidInput
	}
	select {
	case s.passwordSlots <- struct{}{}:
	default:
		return ErrBusy
	}
	hash, err := HashPassword(password)
	<-s.passwordSlots
	if err != nil {
		return err
	}
	return s.adminTransaction(ctx, func(tx pgx.Tx, now time.Time) error {
		var existingID string
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&existingID); errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2,authz_epoch=authz_epoch+1,updated_at=$3 WHERE id=$1`, userID, hash, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now)
		return err
	})
}
