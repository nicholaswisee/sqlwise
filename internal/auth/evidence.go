package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrAuthorizationEpoch = errors.New("session authorization has changed")

type sessionProof struct {
	digest   [32]byte
	epoch    int64
	userID   string
	familyID string
}

type sessionProofContextKey struct{}

// UserIDFromContext returns identity only. It does not grant authorization.
func UserIDFromContext(ctx context.Context) (string, bool) {
	p, ok := ctx.Value(sessionProofContextKey{}).(sessionProof)
	return p.userID, ok && p.userID != ""
}

func (s *Service) BindSession(ctx context.Context, session Session) (context.Context, error) {
	if session.proof == nil || session.proof.userID == "" || session.proof.familyID == "" || session.proof.epoch != session.authzEpoch {
		return nil, ErrSession
	}
	return context.WithValue(ctx, sessionProofContextKey{}, *session.proof), nil
}

func (s *Service) ValidateContext(ctx context.Context, tx pgx.Tx) (Principal, error) {
	p, ok := ctx.Value(sessionProofContextKey{}).(sessionProof)
	if !ok || p.userID == "" || p.familyID == "" || p.digest == ([32]byte{}) {
		return Principal{}, ErrSession
	}
	if err := LockAuthz(ctx, tx, false); err != nil {
		return Principal{}, err
	}
	now := s.now()
	var principal Principal
	var family string
	var tokenEpoch, userEpoch int64
	err := tx.QueryRow(ctx, `SELECT u.id,u.username,u.is_platform_admin,f.id,s.authz_epoch,u.authz_epoch
		FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id JOIN users u ON u.id=f.user_id
		WHERE s.token_hash=$1 AND s.user_id=u.id AND u.active AND f.revoked_at IS NULL
		AND f.absolute_expires_at>$2 AND f.last_seen_at>$3 AND (s.alias_until IS NULL OR s.alias_until>$2)`, p.digest[:], now, now.Add(-IdleTimeout)).
		Scan(&principal.UserID, &principal.Username, &principal.IsPlatformAdmin, &family, &tokenEpoch, &userEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrSession
	}
	if err != nil {
		return Principal{}, err
	}
	if principal.UserID != p.userID || family != p.familyID {
		return Principal{}, ErrSession
	}
	if tokenEpoch != p.epoch || userEpoch != p.epoch {
		return Principal{}, ErrAuthorizationEpoch
	}
	return principal, nil
}

func (s *Service) ValidateContextFresh(ctx context.Context) (Principal, error) {
	var principal Principal
	err := pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		var err error
		principal, err = s.ValidateContext(ctx, tx)
		return err
	})
	return principal, err
}
