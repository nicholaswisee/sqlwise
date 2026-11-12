package access

import (
	"context"
	"errors"

	"sqlwise/internal/auth"

	"github.com/jackc/pgx/v5"
)

type ApprovalMember struct {
	UserID string
	Active bool
}

type ApprovalGroup struct {
	GroupID      string
	Permission   Permission
	LeaderUserID string
	LeaderActive bool
	Members      []ApprovalMember
}

// ApprovalPolicy reads the current approval inputs in the caller's transaction.
// The caller must keep that transaction and its shared authz lock through its approval write.
func (s *Service) ApprovalPolicy(ctx context.Context, tx pgx.Tx, databaseID string) ([]ApprovalGroup, error) {
	if err := auth.LockAuthz(ctx, tx, false); err != nil {
		return nil, err
	}
	if databaseID == "" {
		return nil, ErrInvalidInput
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT active FROM database_registrations WHERE id=$1`, databaseID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	groups := make([]ApprovalGroup, 0)
	rows, err := tx.Query(ctx, `SELECT g.id,x.permission,COALESCE(l.user_id,''),COALESCE(u.active,false)
		FROM group_database_grants x JOIN groups g ON g.id=x.group_id AND g.active
		LEFT JOIN group_leaders l ON l.group_id=g.id
		LEFT JOIN users u ON u.id=l.user_id
		WHERE x.database_id=$1 ORDER BY g.id`, databaseID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var group ApprovalGroup
		var permission string
		if err := rows.Scan(&group.GroupID, &permission, &group.LeaderUserID, &group.LeaderActive); err != nil {
			rows.Close()
			return nil, err
		}
		group.Permission, err = ParsePermission(permission)
		if err != nil || group.Permission == None {
			rows.Close()
			return nil, ErrInvalidPermission
		}
		group.Members = make([]ApprovalMember, 0)
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	byID := make(map[string]int, len(groups))
	for i := range groups {
		byID[groups[i].GroupID] = i
	}
	rows, err = tx.Query(ctx, `SELECT m.group_id,m.user_id,u.active
		FROM group_memberships m JOIN users u ON u.id=m.user_id
		JOIN group_database_grants x ON x.group_id=m.group_id AND x.database_id=$1
		JOIN groups g ON g.id=m.group_id AND g.active
		ORDER BY m.group_id,m.user_id`, databaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID string
		var member ApprovalMember
		if err := rows.Scan(&groupID, &member.UserID, &member.Active); err != nil {
			return nil, err
		}
		groups[byID[groupID]].Members = append(groups[byID[groupID]].Members, member)
	}
	return groups, rows.Err()
}
