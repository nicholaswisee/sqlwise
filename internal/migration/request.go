package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access"
)

const requestColumns = `r.id, r.database_id, r.migration_id, d.title, r.operation, r.definition_revision, r.definition_sha256,
	r.expected_version, COALESCE(r.cancels_execution_id, ''), COALESCE(r.recovers_execution_id, ''), r.requested_by, u.username, r.status, r.review_round,
	COALESCE(r.policy_sha256, ''), COALESCE(r.execution_id, ''), r.created_at, r.updated_at
	FROM migration_requests r JOIN migration_drafts d ON d.id=r.migration_id JOIN users u ON u.id=r.requested_by`

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	var operation, status string
	err := row.Scan(&r.ID, &r.DatabaseID, &r.MigrationID, &r.MigrationTitle, &operation, &r.DefinitionRevision, &r.DefinitionHash,
		&r.ExpectedVersion, &r.CancelsExecutionID, &r.RecoversExecutionID, &r.RequestedBy, &r.RequestedByName, &status, &r.ReviewRound,
		&r.PolicyHash, &r.ExecutionID, &r.CreatedAt, &r.UpdatedAt)
	r.Operation, r.Status = Operation(operation), RequestStatus(status)
	return r, err
}

// request loads a request of databaseID ("" for any, used by the worker),
// optionally locking it for a state change.
func (s *Store) request(ctx context.Context, q querier, databaseID, requestID string, lock bool) (Request, error) {
	sql := `SELECT ` + requestColumns + ` WHERE r.id=$1 AND ($2 = '' OR r.database_id=$2)`
	if lock {
		sql += ` FOR UPDATE OF r`
	}
	r, err := scanRequest(q.QueryRow(ctx, sql, requestID, databaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, fail(ErrNotFound, "Request not found for this database.")
	}
	return r, err
}

func (s *Store) requests(ctx context.Context, q querier, databaseID, migrationID string) ([]Request, error) {
	rows, err := q.Query(ctx, `SELECT `+requestColumns+` WHERE r.database_id=$1 AND r.migration_id=$2 ORDER BY r.created_at DESC, r.id DESC`, databaseID, migrationID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Request, error) { return scanRequest(row) })
}

// policyHash fingerprints the groups, grants, leaders and memberships that
// decide who must approve; any change invalidates the review round.
func policyHash(groups []access.ApprovalGroup) string {
	body, _ := json.Marshal(groups)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// actorPermission is the requester's access derived from the same policy
// snapshot, so the worker needs no browser session.
// TODO(wise): replace with the durable-actor authorization contract.
func actorPermission(groups []access.ApprovalGroup, userID string) access.Permission {
	best := access.None
	for _, g := range groups {
		for _, m := range g.Members {
			if m.UserID == userID && m.Active && g.Permission > best {
				best = g.Permission
			}
		}
	}
	return best
}

// validateDefinition parses SQL up and down with the same rules as execution.
func validateDefinition(d Draft) error {
	if _, err := ParsePostgres(d.SQL); err != nil {
		return fail(ErrValidation, "SQL up: "+err.Error())
	}
	if d.SQLDown != "" {
		if _, err := ParsePostgres(d.SQLDown); err != nil {
			return fail(ErrValidation, "SQL down: "+err.Error())
		}
	}
	return nil
}

// fresh reports whether a request still matches the definition, the history
// position and the linear rules; the error says why it went stale.
func (s *Service) fresh(ctx context.Context, q querier, r Request) (Draft, error) {
	d, err := s.store.draft(ctx, q, r.DatabaseID, r.MigrationID)
	if err != nil {
		return Draft{}, err
	}
	if d.Revision != r.DefinitionRevision || d.DefinitionHash() != r.DefinitionHash {
		return Draft{}, fail(ErrConflict, "The migration changed after this request was made. Create a new request for the current revision.")
	}
	version, err := s.store.currentVersion(ctx, q, r.DatabaseID)
	if err != nil {
		return Draft{}, err
	}
	if version != r.ExpectedVersion {
		return Draft{}, fail(ErrConflict, fmt.Sprintf("The database moved from version %d to %d after this request was made. Create a new request.", r.ExpectedVersion, version))
	}
	if r.Operation.Recovery() {
		return d, s.recoverable(ctx, d, r)
	}
	head, err := s.store.head(ctx, q, r.DatabaseID)
	if err != nil {
		return Draft{}, err
	}
	cancels, err := eligible(head, d, r.Operation)
	if err != nil {
		return Draft{}, err
	}
	if cancels != r.CancelsExecutionID {
		return Draft{}, fail(ErrConflict, "The execution this rollback cancels is no longer the active one.")
	}
	return d, nil
}

// approved checks that every currently authorized group approved the current
// round under an unchanged policy. It returns the policy for further checks.
func (s *Service) approved(ctx context.Context, tx pgx.Tx, r Request) ([]access.ApprovalGroup, error) {
	groups, err := s.authz.ApprovalPolicy(ctx, tx, r.DatabaseID)
	if err != nil {
		return nil, err
	}
	if policyHash(groups) != r.PolicyHash {
		return nil, fail(ErrConflict, "The groups, leaders or members with access changed after review. Submit the request again.")
	}
	decisions, err := s.store.decisions(ctx, tx, r.ID, r.ReviewRound)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if decisions[g.GroupID] != "approved" {
			return nil, fail(ErrConflict, "Not every authorized group has approved this request.")
		}
	}
	return groups, nil
}

// CreateRequest records a draft request for one operation. Edit required.
func (s *Service) CreateRequest(ctx context.Context, databaseID, migrationID string, in RequestInput) (Request, error) {
	userID, err := s.authorize(ctx, databaseID, access.Edit)
	if err != nil {
		return Request{}, err
	}
	if !validID(migrationID) {
		return Request{}, fail(ErrNotFound, "Migration not found for this database.")
	}
	d, err := s.store.draft(ctx, s.store.pool, databaseID, migrationID)
	if err != nil {
		return Request{}, err
	}
	if d.Dialect != PostgreSQL {
		return Request{}, fail(ErrUnsupportedDialect, "Reviewed execution supports PostgreSQL targets only.")
	}
	if !d.Complete() {
		return Request{}, fail(ErrValidation, "Add SQL down or mark the migration irreversible with a reason before requesting review.")
	}
	if err := validateDefinition(d); err != nil {
		return Request{}, err
	}
	id, err := newID()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	r := Request{ID: id, DatabaseID: databaseID, MigrationID: migrationID, Operation: in.Operation, DefinitionRevision: in.ExpectedRevision,
		DefinitionHash: in.ExpectedDefinition, ExpectedVersion: in.ExpectedVersion, RequestedBy: userID}
	if in.Operation.Recovery() {
		r.RecoversExecutionID = in.RecoversExecutionID
	} else {
		head, err := s.store.head(ctx, s.store.pool, databaseID)
		if err != nil {
			return Request{}, err
		}
		if r.CancelsExecutionID, err = eligible(head, d, in.Operation); err != nil {
			return Request{}, err
		}
	}
	if _, err := s.fresh(ctx, s.store.pool, r); err != nil {
		return Request{}, err
	}
	if _, err := s.store.pool.Exec(ctx, `INSERT INTO migration_requests (id, database_id, migration_id, operation, definition_revision,
		definition_sha256, expected_version, cancels_execution_id, requested_by, status, created_at, updated_at, recovers_execution_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft', $10, $10, $11)`, r.ID, r.DatabaseID, r.MigrationID, string(r.Operation),
		r.DefinitionRevision, r.DefinitionHash, r.ExpectedVersion, nullable(r.CancelsExecutionID), r.RequestedBy, now, nullable(r.RecoversExecutionID)); err != nil {
		return Request{}, err
	}
	return s.Request(ctx, databaseID, id)
}

// SubmitRequest starts a review round. Every authorized group must have an
// active leader. A stale policy may be resubmitted; other staleness needs a
// new request. Edit required.
func (s *Service) SubmitRequest(ctx context.Context, databaseID, requestID string) (Request, error) {
	if _, err := s.authorize(ctx, databaseID, access.Edit); err != nil {
		return Request{}, err
	}
	if !validID(requestID) {
		return Request{}, fail(ErrNotFound, "Request not found for this database.")
	}
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		r, err := s.store.request(ctx, tx, databaseID, requestID, true)
		if err != nil {
			return err
		}
		groups, err := s.authz.ApprovalPolicy(ctx, tx, databaseID)
		if err != nil {
			return err
		}
		hash := policyHash(groups)
		resubmit := (r.Status == RequestPendingReview || r.Status == RequestApproved) && r.PolicyHash != hash
		if r.Status != RequestDraft && r.Status != RequestRejected && !resubmit {
			return fail(ErrConflict, "Only a draft, rejected or policy-stale request can be submitted for review.")
		}
		if len(groups) == 0 {
			return fail(ErrConflict, "No group has access to this database, so no one can approve the request.")
		}
		names, err := s.store.names(ctx, tx, "groups", "name", groupIDs(groups))
		if err != nil {
			return err
		}
		for _, g := range groups {
			if g.LeaderUserID == "" || !g.LeaderActive {
				return fail(ErrConflict, fmt.Sprintf("Group %q has no active leader to approve the request. Ask an administrator to assign one.", names[g.GroupID]))
			}
		}
		if _, err := s.fresh(ctx, tx, r); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE migration_requests SET status='pending_review', review_round=review_round+1, policy_sha256=$2, updated_at=$3 WHERE id=$1`,
			r.ID, hash, s.now().UTC())
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return s.Request(ctx, databaseID, requestID)
}

// Decide records the caller's approval or rejection for a group they lead.
// A leader with view access may decide; a rejection ends the round.
func (s *Service) Decide(ctx context.Context, databaseID, requestID, groupID string, approve bool) (Request, error) {
	userID, err := s.authorize(ctx, databaseID, access.View)
	if err != nil {
		return Request{}, err
	}
	if !validID(requestID) || !validID(groupID) {
		return Request{}, fail(ErrNotFound, "Request not found for this database.")
	}
	err = pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		r, err := s.store.request(ctx, tx, databaseID, requestID, true)
		if err != nil {
			return err
		}
		if r.Status != RequestPendingReview {
			return fail(ErrConflict, "Only a request pending review can be approved or rejected.")
		}
		groups, err := s.authz.ApprovalPolicy(ctx, tx, databaseID)
		if err != nil {
			return err
		}
		if policyHash(groups) != r.PolicyHash {
			return fail(ErrConflict, "The groups, leaders or members with access changed after submission. Submit the request again.")
		}
		var group *access.ApprovalGroup
		for i := range groups {
			if groups[i].GroupID == groupID {
				group = &groups[i]
			}
		}
		if group == nil || group.LeaderUserID != userID || !group.LeaderActive {
			return access.ErrPermissionDenied
		}
		if _, err := s.fresh(ctx, tx, r); err != nil {
			return err
		}
		decisions, err := s.store.decisions(ctx, tx, r.ID, r.ReviewRound)
		if err != nil {
			return err
		}
		if decisions[groupID] != "" {
			return fail(ErrConflict, "This group already decided in the current review round.")
		}
		decision, status := "approved", RequestPendingReview
		if !approve {
			decision, status = "rejected", RequestRejected
		}
		decisions[groupID] = decision
		if approve {
			status = RequestApproved
			for _, g := range groups {
				if decisions[g.GroupID] != "approved" {
					status = RequestPendingReview
				}
			}
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `INSERT INTO migration_approvals (request_id, review_round, group_id, approver_id, decision, decided_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, r.ID, r.ReviewRound, groupID, userID, decision, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE migration_requests SET status=$2, updated_at=$3 WHERE id=$1`, r.ID, string(status), now)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return s.Request(ctx, databaseID, requestID)
}

// Consume turns an approved, still-valid request into an execution inside
// the caller's transaction, which also publishes the job. Consuming a queued
// request again returns its execution, so publication can be retried.
// Edit required.
func (s *Service) Consume(ctx context.Context, tx pgx.Tx, databaseID, requestID string) (Execution, error) {
	if _, err := s.authorize(ctx, databaseID, access.Edit); err != nil {
		return Execution{}, err
	}
	if !validID(requestID) {
		return Execution{}, fail(ErrNotFound, "Request not found for this database.")
	}
	r, err := s.store.request(ctx, tx, databaseID, requestID, true)
	if err != nil {
		return Execution{}, err
	}
	execution := Execution{ID: r.ExecutionID, RequestID: r.ID, DatabaseID: r.DatabaseID, MigrationID: r.MigrationID, Operation: r.Operation,
		ActorID: r.RequestedBy, RecoversExecutionID: r.RecoversExecutionID}
	if r.Status == RequestQueued {
		return execution, nil
	}
	if r.Status != RequestApproved {
		return Execution{}, fail(ErrConflict, "Only an approved request can be executed.")
	}
	if _, err := s.approved(ctx, tx, r); err != nil {
		return Execution{}, err
	}
	if _, err := s.fresh(ctx, tx, r); err != nil {
		return Execution{}, err
	}
	if execution.ID, err = newID(); err != nil {
		return Execution{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE migration_requests SET status='queued', execution_id=$2, updated_at=$3 WHERE id=$1`, r.ID, execution.ID, s.now().UTC())
	return execution, err
}

// Request returns a request with its approvals, staleness and execution.
func (s *Service) Request(ctx context.Context, databaseID, requestID string) (Request, error) {
	userID, err := s.authorize(ctx, databaseID, access.View)
	if err != nil {
		return Request{}, err
	}
	if !validID(requestID) {
		return Request{}, fail(ErrNotFound, "Request not found for this database.")
	}
	var r Request
	err = pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		if r, err = s.store.request(ctx, tx, databaseID, requestID, false); err != nil {
			return err
		}
		requests, err := s.describe(ctx, tx, userID, []Request{r})
		if err == nil {
			r = requests[0]
		}
		return err
	})
	return r, err
}

func (s *Service) requestsOf(ctx context.Context, userID, databaseID, migrationID string) ([]Request, error) {
	var out []Request
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		requests, err := s.store.requests(ctx, tx, databaseID, migrationID)
		if err != nil {
			return err
		}
		out, err = s.describe(ctx, tx, userID, requests)
		return err
	})
	return out, err
}

// describe fills approvals from the live policy, staleness and executions.
func (s *Service) describe(ctx context.Context, tx pgx.Tx, userID string, requests []Request) ([]Request, error) {
	if len(requests) == 0 {
		return requests, nil
	}
	groups, err := s.authz.ApprovalPolicy(ctx, tx, requests[0].DatabaseID)
	if err != nil {
		return nil, err
	}
	hash := policyHash(groups)
	groupNames, err := s.store.names(ctx, tx, "groups", "name", groupIDs(groups))
	if err != nil {
		return nil, err
	}
	var leaders []string
	for _, g := range groups {
		leaders = append(leaders, g.LeaderUserID)
	}
	leaderNames, err := s.store.names(ctx, tx, "users", "username", leaders)
	if err != nil {
		return nil, err
	}
	for i := range requests {
		r := &requests[i]
		decided, err := s.store.decided(ctx, tx, r.ID, r.ReviewRound)
		if err != nil {
			return nil, err
		}
		if r.Status != RequestQueued {
			if _, err := s.fresh(ctx, tx, *r); err != nil {
				if r.Stale = Message(err); r.Stale == "" {
					return nil, err
				}
			} else if r.Status != RequestDraft && r.Status != RequestRejected && r.PolicyHash != hash {
				r.Stale = "The groups, leaders or members with access changed after submission. Submit the request again."
			}
		}
		r.Approvals = make([]GroupApproval, 0, len(groups))
		for _, g := range groups {
			a := GroupApproval{GroupID: g.GroupID, GroupName: groupNames[g.GroupID], Permission: g.Permission.String(),
				LeaderID: g.LeaderUserID, LeaderName: leaderNames[g.LeaderUserID], LeaderActive: g.LeaderActive}
			if d, ok := decided[g.GroupID]; ok {
				a.Decision, a.DecidedBy, a.DecidedAt = d.decision, d.by, &d.at
			}
			a.CanDecide = r.Status == RequestPendingReview && r.Stale == "" && a.Decision == "" && g.LeaderActive && g.LeaderUserID == userID
			r.Approvals = append(r.Approvals, a)
		}
		if r.ExecutionID != "" {
			a, found, err := s.store.attemptByID(ctx, r.ExecutionID)
			if err != nil {
				return nil, err
			}
			if found {
				r.Execution = &a
			}
		}
	}
	return requests, nil
}

type decision struct {
	decision, by string
	at           time.Time
}

func (s *Store) decided(ctx context.Context, q querier, requestID string, round int) (map[string]decision, error) {
	rows, err := q.Query(ctx, `SELECT a.group_id, a.decision, u.username, a.decided_at FROM migration_approvals a
		JOIN users u ON u.id=a.approver_id WHERE a.request_id=$1 AND a.review_round=$2`, requestID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]decision{}
	for rows.Next() {
		var group string
		var d decision
		if err := rows.Scan(&group, &d.decision, &d.by, &d.at); err != nil {
			return nil, err
		}
		out[group] = d
	}
	return out, rows.Err()
}

func (s *Store) decisions(ctx context.Context, q querier, requestID string, round int) (map[string]string, error) {
	decided, err := s.decided(ctx, q, requestID, round)
	out := map[string]string{}
	for group, d := range decided {
		out[group] = d.decision
	}
	return out, err
}

// names maps IDs to display names; table and column are constants.
func (s *Store) names(ctx context.Context, q querier, table, column string, ids []string) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT id, `+column+` FROM `+table+` WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func groupIDs(groups []access.ApprovalGroup) []string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.GroupID)
	}
	return ids
}
