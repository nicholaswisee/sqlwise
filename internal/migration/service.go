package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

// Authorizer is Wise's access service; it validates the session bound to ctx
// and evaluates live group grants for every call.
type Authorizer interface {
	EffectivePermission(ctx context.Context, userID, databaseID string) (access.Permission, error)
	Require(ctx context.Context, userID, databaseID string, required access.Permission) error
}

const (
	LockWait       = 2 * time.Second
	ExecuteTimeout = 10 * time.Second
	CleanupTimeout = 3 * time.Second
)

type Service struct {
	store   *Store
	authz   Authorizer
	targets Targets
	now     func() time.Time
	// Overridable in tests; production uses the frozen contract values.
	lockWait, executeTimeout, cleanupTimeout time.Duration
}

func NewService(store *Store, authz Authorizer, targets Targets, now func() time.Time) *Service {
	return &Service{store: store, authz: authz, targets: targets, now: now,
		lockWait: LockWait, executeTimeout: ExecuteTimeout, cleanupTimeout: CleanupTimeout}
}

// WithTimeouts overrides the lock wait, execution and cleanup bounds (tests).
func (s *Service) WithTimeouts(lockWait, execute, cleanup time.Duration) *Service {
	s.lockWait, s.executeTimeout, s.cleanupTimeout = lockWait, execute, cleanup
	return s
}

// authorize returns the caller's user ID once required access is confirmed.
// Executors and authors always come from the session, never from input.
func (s *Service) authorize(ctx context.Context, databaseID string, required access.Permission) (string, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return "", auth.ErrSession
	}
	if !validID(databaseID) {
		return "", access.ErrPermissionDenied
	}
	if err := s.authz.Require(ctx, userID, databaseID, required); err != nil {
		return "", err
	}
	return userID, nil
}

// Database returns target identity and the caller's edit capability; view required.
func (s *Service) Database(ctx context.Context, databaseID string) (Database, error) {
	userID, err := s.authorize(ctx, databaseID, access.View)
	if err != nil {
		return Database{}, err
	}
	permission, err := s.authz.EffectivePermission(ctx, userID, databaseID)
	if err != nil {
		return Database{}, err
	}
	name, err := s.store.databaseName(ctx, databaseID)
	if err != nil {
		return Database{}, err
	}
	return Database{ID: databaseID, Name: name, CanEdit: permission >= access.Edit}, nil
}

func (s *Service) History(ctx context.Context, databaseID string) (History, error) {
	db, err := s.Database(ctx, databaseID)
	if err != nil {
		return History{}, err
	}
	h := History{Database: db}
	if h.CurrentVersion, err = s.store.currentVersion(ctx, s.store.pool, databaseID); err != nil {
		return History{}, err
	}
	if h.Drafts, err = s.store.drafts(ctx, databaseID); err != nil {
		return History{}, err
	}
	if h.Attempts, err = s.store.attempts(ctx, `a.database_id=$1`, databaseID); err != nil {
		return History{}, err
	}
	return h, nil
}

func (s *Service) Detail(ctx context.Context, databaseID, migrationID string) (Detail, error) {
	db, err := s.Database(ctx, databaseID)
	if err != nil {
		return Detail{}, err
	}
	if !validID(migrationID) {
		return Detail{}, fail(ErrNotFound, "Migration not found for this database.")
	}
	d := Detail{Database: db}
	if d.Draft, err = s.store.draft(ctx, s.store.pool, databaseID, migrationID); err != nil {
		return Detail{}, err
	}
	if d.CurrentVersion, err = s.store.currentVersion(ctx, s.store.pool, databaseID); err != nil {
		return Detail{}, err
	}
	if d.Attempts, err = s.store.attempts(ctx, `a.database_id=$1 AND a.migration_id=$2`, databaseID, migrationID); err != nil {
		return Detail{}, err
	}
	blocking, err := s.store.blocking(ctx, databaseID)
	if err != nil {
		return Detail{}, err
	}
	if len(blocking) > 0 {
		d.Blocked = blockedMessage(blocking[len(blocking)-1])
	}
	return d, nil
}

func (s *Service) Attempt(ctx context.Context, databaseID, migrationID, attemptID string) (Database, Attempt, error) {
	db, err := s.Database(ctx, databaseID)
	if err != nil {
		return Database{}, Attempt{}, err
	}
	if !validID(migrationID) || !validID(attemptID) {
		return Database{}, Attempt{}, fail(ErrNotFound, "Attempt not found for this migration.")
	}
	a, err := s.store.attempt(ctx, databaseID, migrationID, attemptID)
	return db, a, err
}

func (s *Service) Create(ctx context.Context, databaseID string, in DraftInput) (Draft, error) {
	userID, err := s.authorize(ctx, databaseID, access.Edit)
	if err != nil {
		return Draft{}, err
	}
	if in, err = in.normalize(); err != nil {
		return Draft{}, err
	}
	if _, err := s.store.databaseName(ctx, databaseID); err != nil {
		return Draft{}, err
	}
	// Without configured targets the dialect cannot be read; assume PostgreSQL.
	dialect := PostgreSQL
	if s.targets != nil {
		if dialect, err = s.targets.Dialect(ctx, databaseID); err != nil {
			return Draft{}, err
		}
	}
	id, err := newID()
	if err != nil {
		return Draft{}, err
	}
	now := s.now().UTC()
	d := Draft{ID: id, DatabaseID: databaseID, Title: in.Title, SQL: in.SQL, Dialect: dialect, CreatedBy: userID, CreatedAt: now}
	if err := s.store.createDraft(ctx, d); err != nil {
		return Draft{}, err
	}
	return s.store.draft(ctx, s.store.pool, databaseID, id)
}

func (s *Service) Update(ctx context.Context, databaseID, migrationID string, in UpdateInput) (Draft, error) {
	userID, err := s.authorize(ctx, databaseID, access.Edit)
	if err != nil {
		return Draft{}, err
	}
	if !validID(migrationID) {
		return Draft{}, fail(ErrNotFound, "Migration not found for this database.")
	}
	if in.DraftInput, err = in.normalize(); err != nil {
		return Draft{}, err
	}
	return s.store.updateDraft(ctx, databaseID, migrationID, in, userID, s.now().UTC())
}

func blockedMessage(a Attempt) string {
	switch a.Status {
	case StatusPartiallyApplied:
		return fmt.Sprintf("Version %d was partially applied. Executions are blocked until an operator reconciles the target.", a.CandidateVersion)
	case StatusRunning:
		return fmt.Sprintf("Version %d is executing or was interrupted. Its outcome is checked before the next execution.", a.CandidateVersion)
	}
	return fmt.Sprintf("The outcome of version %d is unknown. Executions are blocked until its receipt is checked on the target.", a.CandidateVersion)
}

// IsPermission reports access denials from the evaluator.
func IsPermission(err error) bool { return errors.Is(err, access.ErrPermissionDenied) }
