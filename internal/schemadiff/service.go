package schemadiff

import (
	"context"
	"errors"
	"time"

	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/migration"
)

// Source reads the live schema of a registered target. It must not include
// the reserved receipt schema in user snapshots.
type Source interface {
	Snapshot(ctx context.Context, databaseID, schema string) (Snapshot, error)
}

const SnapshotTimeout = 5 * time.Second

type Service struct {
	authz  migration.Authorizer
	source Source
	drafts *migration.Service
}

func NewService(authz migration.Authorizer, source Source, drafts *migration.Service) *Service {
	return &Service{authz: authz, source: source, drafts: drafts}
}

func (s *Service) authorize(ctx context.Context, databaseID string, required access.Permission) error {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return auth.ErrSession
	}
	return s.authz.Require(ctx, userID, databaseID, required)
}

// Preview compares the live schema with the desired snapshot. It requires
// view access and writes neither the target nor the migration history.
func (s *Service) Preview(ctx context.Context, databaseID, desiredJSON string) (Proposal, error) {
	if err := s.authorize(ctx, databaseID, access.View); err != nil {
		return Proposal{}, err
	}
	return s.propose(ctx, databaseID, desiredJSON)
}

// UseAsDraft regenerates the proposal against the current live schema and
// stores its SQL as an ordinary editable draft. A changed live schema since
// the reviewed preview is rejected so the user previews again.
func (s *Service) UseAsDraft(ctx context.Context, databaseID, desiredJSON, sourceFingerprint, title string) (migration.Draft, error) {
	if err := s.authorize(ctx, databaseID, access.Edit); err != nil {
		return migration.Draft{}, err
	}
	p, err := s.propose(ctx, databaseID, desiredJSON)
	if err != nil {
		return migration.Draft{}, err
	}
	if p.SourceFingerprint != sourceFingerprint {
		return migration.Draft{}, &migration.Error{Kind: migration.ErrConflict, Message: "The live schema changed since this preview. Preview again before creating a draft."}
	}
	if p.SQL == "" {
		return migration.Draft{}, &migration.Error{Kind: migration.ErrValidation, Message: "This proposal has no generated SQL to use as a draft."}
	}
	if title == "" {
		title = "Schema diff for " + p.Schema
	}
	return s.drafts.Create(ctx, databaseID, migration.DraftInput{Title: title, SQL: p.SQL})
}

func (s *Service) propose(ctx context.Context, databaseID, desiredJSON string) (Proposal, error) {
	desired, err := ParseDesired(desiredJSON)
	if err != nil {
		return Proposal{}, domainError(err)
	}
	if s.source == nil {
		return Proposal{}, &migration.Error{Kind: migration.ErrTargetUnavailable, Message: "Live schema snapshots are not configured on this server."}
	}
	snapshotCtx, cancel := context.WithTimeout(ctx, SnapshotTimeout)
	defer cancel()
	current, err := s.source.Snapshot(snapshotCtx, databaseID, desired.Schema)
	if err != nil {
		if errors.Is(snapshotCtx.Err(), context.DeadlineExceeded) {
			return Proposal{}, &migration.Error{Kind: migration.ErrTimeout, Message: "Reading the live schema timed out."}
		}
		var domain *migration.Error
		if errors.As(err, &domain) {
			return Proposal{}, err
		}
		return Proposal{}, &migration.Error{Kind: migration.ErrTargetUnavailable, Message: "The live schema could not be read from the target."}
	}
	p, err := Propose(current, desired)
	return p, domainError(err)
}

// domainError maps snapshot validation errors to the shared migration kinds.
func domainError(err error) error {
	var validation *ValidationError
	var dialect *UnsupportedDialectError
	switch {
	case errors.As(err, &dialect):
		return &migration.Error{Kind: migration.ErrUnsupportedDialect, Message: dialect.Error()}
	case errors.As(err, &validation):
		return &migration.Error{Kind: migration.ErrValidation, Message: validation.Message}
	}
	return err
}
