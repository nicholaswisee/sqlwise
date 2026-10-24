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

type Membership struct {
	UserID  string
	GroupID string
}
type GroupGrant struct {
	GroupID    string
	DatabaseID string
	Permission Permission
}
type DatabaseRegistration struct {
	ID     string
	Name   string
	Active bool
}

type AdminSnapshot struct {
	Users       []auth.AdminUser
	Groups      []Group
	Memberships []Membership
	Grants      []GroupGrant
	Databases   []DatabaseRegistration
}

// AdminSnapshot reads a fresh, credential-free snapshot under one transaction.
func (s *Service) AdminSnapshot(ctx context.Context) (AdminSnapshot, error) {
	var snapshot AdminSnapshot
	err := pgx.BeginTxFunc(ctx, s.store.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		actor, err := s.sessions.ValidateContext(ctx, tx)
		if err != nil {
			return err
		}
		if !actor.IsPlatformAdmin {
			return auth.ErrSession
		}
		rows, err := tx.Query(ctx, `SELECT id,username,is_platform_admin,active FROM users ORDER BY username,id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u auth.AdminUser
			if err := rows.Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.Active); err != nil {
				rows.Close()
				return err
			}
			snapshot.Users = append(snapshot.Users, u)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,name,active FROM groups ORDER BY name,id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.Name, &g.Active); err != nil {
				rows.Close()
				return err
			}
			snapshot.Groups = append(snapshot.Groups, g)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT user_id,group_id FROM group_memberships ORDER BY user_id,group_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m Membership
			if err := rows.Scan(&m.UserID, &m.GroupID); err != nil {
				rows.Close()
				return err
			}
			snapshot.Memberships = append(snapshot.Memberships, m)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT group_id,database_id,permission FROM group_database_grants ORDER BY group_id,database_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g GroupGrant
			var raw string
			if err := rows.Scan(&g.GroupID, &g.DatabaseID, &raw); err != nil {
				rows.Close()
				return err
			}
			g.Permission, err = ParsePermission(raw)
			if err != nil {
				rows.Close()
				return err
			}
			snapshot.Grants = append(snapshot.Grants, g)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,name,active FROM database_registrations ORDER BY name,id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d DatabaseRegistration
			if err := rows.Scan(&d.ID, &d.Name, &d.Active); err != nil {
				rows.Close()
				return err
			}
			snapshot.Databases = append(snapshot.Databases, d)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		return nil
	})
	return snapshot, err
}

func (s *Service) RenameGroup(ctx context.Context, groupID, name string) error {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 128 {
		return ErrInvalidInput
	}
	return s.mutatePolicy(ctx, func(tx pgx.Tx, now time.Time) error {
		if err := policyResource(ctx, tx, "groups", groupID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE groups SET name=$2,updated_at=$3 WHERE id=$1`, groupID, name, now)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrConflict
		}
		return err
	})
}

func (s *Service) RegisterDatabase(ctx context.Context, name string, attach func(tx pgx.Tx, databaseID string) error) (DatabaseRegistration, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 128 {
		return DatabaseRegistration{}, ErrInvalidInput
	}
	var registration DatabaseRegistration
	err := s.mutatePolicy(ctx, func(tx pgx.Tx, _ time.Time) error {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		registration = DatabaseRegistration{ID: hex.EncodeToString(id[:]), Name: name, Active: true}
		if _, err := tx.Exec(ctx, `INSERT INTO database_registrations(id,name,active) VALUES($1,$2,true)`, registration.ID, name); err != nil {
			return err
		}
		return attach(tx, registration.ID)
	})
	if err != nil {
		return DatabaseRegistration{}, err
	}
	return registration, nil
}

func (s *Service) UpdateDatabase(ctx context.Context, databaseID, name string, attach func(tx pgx.Tx) error) error {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 128 {
		return ErrInvalidInput
	}
	return s.mutatePolicy(ctx, func(tx pgx.Tx, _ time.Time) error {
		if err := policyResource(ctx, tx, "database_registrations", databaseID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE database_registrations SET name=$2 WHERE id=$1`, databaseID, name); err != nil {
			return err
		}
		return attach(tx)
	})
}
