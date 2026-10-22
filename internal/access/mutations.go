package access

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"sqlwise/internal/auth"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidInput = errors.New("invalid policy input")
	ErrNotFound     = errors.New("policy resource not found")
	ErrConflict     = errors.New("policy resource already exists")
)

type Group struct {
	ID     string
	Name   string
	Active bool
}

func (s *Service) mutatePolicy(ctx context.Context, mutate func(pgx.Tx, time.Time) error) error {
	return pgx.BeginTxFunc(ctx, s.store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := auth.LockAuthz(ctx, tx, true); err != nil {
			return err
		}
		actor, err := s.sessions.ValidateContext(ctx, tx)
		if err != nil {
			return err
		}
		if !actor.IsPlatformAdmin {
			return auth.ErrSession
		}
		return mutate(tx, s.sessions.Now())
	})
}

func (s *Service) CreateGroup(ctx context.Context, name string) (Group, error) {
	var group Group
	err := s.mutatePolicy(ctx, func(tx pgx.Tx, now time.Time) error {
		name = strings.TrimSpace(name)
		if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 128 {
			return ErrInvalidInput
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		group = Group{ID: hex.EncodeToString(id[:]), Name: name, Active: true}
		_, err := tx.Exec(ctx, `INSERT INTO groups(id,name,active,created_at,updated_at) VALUES($1,$2,true,$3,$3)`, group.ID, name, now)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrConflict
		}
		return err
	})
	if err != nil {
		return Group{}, err
	}
	return group, nil
}

// table is a service-owned constant, never caller input. Opaque IDs remain parameters.
func policyResource(ctx context.Context, tx pgx.Tx, table, id string) error {
	if id == "" || !utf8.ValidString(id) || strings.ContainsRune(id, 0) {
		return ErrInvalidInput
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func policyUsers(ctx context.Context, tx pgx.Tx, query string, id string) ([]string, error) {
	rows, err := tx.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

const groupUsers = `SELECT user_id FROM group_memberships WHERE group_id=$1`

func invalidatePolicy(ctx context.Context, tx pgx.Tx, users []string, increase bool, now time.Time) error {
	if len(users) == 0 {
		return nil
	}
	if increase {
		_, err := tx.Exec(ctx, `UPDATE users SET authz_epoch=authz_epoch+1,updated_at=$2 WHERE id=ANY($1::text[])`, users, now)
		return err
	}
	// Revoking the family rejects every current identifier and grace alias, even if
	// another group still supplies equivalent effective permission.
	_, err := tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at=$2 WHERE user_id=ANY($1::text[]) AND revoked_at IS NULL`, users, now)
	return err
}

func (s *Service) SetGroupActive(ctx context.Context, groupID string, active bool) error {
	return s.setPolicyActive(ctx, "groups", groupID, active, groupUsers)
}

func (s *Service) SetDatabaseActive(ctx context.Context, databaseID string, active bool) error {
	return s.setPolicyActive(ctx, "database_registrations", databaseID, active, `SELECT DISTINCT m.user_id FROM group_memberships m JOIN group_database_grants g ON g.group_id=m.group_id WHERE g.database_id=$1`)
}

func (s *Service) setPolicyActive(ctx context.Context, table, id string, active bool, affectedQuery string) error {
	return s.mutatePolicy(ctx, func(tx pgx.Tx, now time.Time) error {
		if err := policyResource(ctx, tx, table, id); err != nil {
			return err
		}
		var previous bool
		if err := tx.QueryRow(ctx, "SELECT active FROM "+table+" WHERE id=$1", id).Scan(&previous); err != nil {
			return err
		}
		if previous == active {
			return nil
		}
		users, err := policyUsers(ctx, tx, affectedQuery, id)
		if err != nil {
			return err
		}
		query := "UPDATE " + table + " SET active=$2 WHERE id=$1"
		args := []any{id, active}
		if table == "groups" {
			query = "UPDATE groups SET active=$2,updated_at=$3 WHERE id=$1"
			args = append(args, now)
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return err
		}
		return invalidatePolicy(ctx, tx, users, active, now)
	})
}

func (s *Service) AddMembership(ctx context.Context, userID, groupID string) error {
	return s.changeMembership(ctx, userID, groupID, true)
}

func (s *Service) RemoveMembership(ctx context.Context, userID, groupID string) error {
	return s.changeMembership(ctx, userID, groupID, false)
}

func (s *Service) changeMembership(ctx context.Context, userID, groupID string, add bool) error {
	return s.mutatePolicy(ctx, func(tx pgx.Tx, now time.Time) error {
		if err := policyResource(ctx, tx, "users", userID); err != nil {
			return err
		}
		if err := policyResource(ctx, tx, "groups", groupID); err != nil {
			return err
		}
		var changed pgconn.CommandTag
		var err error
		if add {
			changed, err = tx.Exec(ctx, `INSERT INTO group_memberships(user_id,group_id,created_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, userID, groupID, now)
		} else {
			changed, err = tx.Exec(ctx, `DELETE FROM group_memberships WHERE user_id=$1 AND group_id=$2`, userID, groupID)
		}
		if err != nil {
			return err
		}
		if changed.RowsAffected() == 0 {
			return nil
		}
		return invalidatePolicy(ctx, tx, []string{userID}, add, now)
	})
}

func (s *Service) SetGrant(ctx context.Context, groupID, databaseID string, permission Permission) error {
	return s.changeGrant(ctx, groupID, databaseID, permission, false)
}

func (s *Service) RemoveGrant(ctx context.Context, groupID, databaseID string) error {
	return s.changeGrant(ctx, groupID, databaseID, None, true)
}

func (s *Service) changeGrant(ctx context.Context, groupID, databaseID string, permission Permission, remove bool) error {
	return s.mutatePolicy(ctx, func(tx pgx.Tx, now time.Time) error {
		if !remove && permission != View && permission != Edit {
			return ErrInvalidPermission
		}
		if err := policyResource(ctx, tx, "groups", groupID); err != nil {
			return err
		}
		if err := policyResource(ctx, tx, "database_registrations", databaseID); err != nil {
			return err
		}
		previous := None
		var raw string
		err := tx.QueryRow(ctx, `SELECT permission FROM group_database_grants WHERE group_id=$1 AND database_id=$2`, groupID, databaseID).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			previous, err = ParsePermission(raw)
			if err != nil || previous == None {
				return ErrInvalidPermission
			}
		}
		if previous == permission {
			return nil
		}
		users, err := policyUsers(ctx, tx, groupUsers, groupID)
		if err != nil {
			return err
		}
		if remove {
			_, err = tx.Exec(ctx, `DELETE FROM group_database_grants WHERE group_id=$1 AND database_id=$2`, groupID, databaseID)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO group_database_grants(group_id,database_id,permission,created_at,updated_at) VALUES($1,$2,$3,$4,$4)
				ON CONFLICT(group_id,database_id) DO UPDATE SET permission=EXCLUDED.permission,updated_at=EXCLUDED.updated_at`, groupID, databaseID, permission.String(), now)
		}
		if err != nil {
			return err
		}
		return invalidatePolicy(ctx, tx, users, permission > previous, now)
	})
}
