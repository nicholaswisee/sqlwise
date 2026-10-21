package access

import (
	"context"
	"errors"
	"fmt"

	"sqlwise/internal/auth"

	"github.com/jackc/pgx/v5"
)

var ErrPermissionDenied = errors.New("permission denied")

type DatabasePermission struct {
	ID         string
	Name       string
	Permission Permission
}

type Service struct {
	store    *Store
	sessions *auth.Service
}

func NewService(store *Store, sessions *auth.Service) *Service {
	return &Service{store: store, sessions: sessions}
}

func (s *Service) EffectivePermission(ctx context.Context, userID, databaseID string) (Permission, error) {
	var permission Permission
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		principal, err := s.sessions.ValidateContext(ctx, tx)
		if err != nil {
			return err
		}
		if principal.UserID != userID {
			return auth.ErrSession
		}
		permission, err = permissionFor(ctx, tx, userID, databaseID)
		return err
	})
	return permission, err
}

func (s *Service) Require(ctx context.Context, userID, databaseID string, required Permission) error {
	if required != View && required != Edit {
		return ErrInvalidPermission
	}
	permission, err := s.EffectivePermission(ctx, userID, databaseID)
	if err != nil {
		return err
	}
	if permission < required {
		return ErrPermissionDenied
	}
	return nil
}

func (s *Service) ListDatabases(ctx context.Context, userID string) ([]DatabasePermission, error) {
	var databases []DatabasePermission
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		principal, err := s.sessions.ValidateContext(ctx, tx)
		if err != nil {
			return err
		}
		if principal.UserID != userID {
			return auth.ErrSession
		}
		rows, err := tx.Query(ctx, `SELECT d.id,d.name,
			CASE max(CASE x.permission WHEN 'edit' THEN 2 WHEN 'view' THEN 1 ELSE 0 END)
				WHEN 2 THEN 'edit' WHEN 1 THEN 'view' ELSE 'none' END
			FROM group_memberships m
			JOIN groups g ON g.id=m.group_id AND g.active
			JOIN group_database_grants x ON x.group_id=g.id
			JOIN database_registrations d ON d.id=x.database_id AND d.active
			WHERE m.user_id=$1 GROUP BY d.id,d.name ORDER BY d.name,d.id`, userID)
		if err != nil {
			return fmt.Errorf("list authorized databases: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var database DatabasePermission
			var raw string
			if err := rows.Scan(&database.ID, &database.Name, &raw); err != nil {
				return err
			}
			database.Permission, err = ParsePermission(raw)
			if err != nil || database.Permission == None {
				return ErrInvalidPermission
			}
			databases = append(databases, database)
		}
		return rows.Err()
	})
	return databases, err
}

func permissionFor(ctx context.Context, tx pgx.Tx, userID, databaseID string) (Permission, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT CASE max(CASE WHEN g.id IS NOT NULL AND x.permission='edit' THEN 2 WHEN g.id IS NOT NULL AND x.permission='view' THEN 1 ELSE 0 END)
		WHEN 2 THEN 'edit' WHEN 1 THEN 'view' ELSE 'none' END
		FROM database_registrations d
		LEFT JOIN group_database_grants x ON x.database_id=d.id
		LEFT JOIN group_memberships m ON m.group_id=x.group_id AND m.user_id=$1
		LEFT JOIN groups g ON g.id=m.group_id AND g.active
		WHERE d.id=$2 AND d.active AND g.id IS NOT NULL`, userID, databaseID).Scan(&raw)
	if err != nil {
		return None, fmt.Errorf("evaluate database permission: %w", err)
	}
	permission, err := ParsePermission(raw)
	if err != nil {
		return None, err
	}
	return permission, nil
}
