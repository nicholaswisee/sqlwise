package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	LoginLifetime   = 10 * time.Minute
	SessionLifetime = 12 * time.Hour
	IdleTimeout     = 30 * time.Minute
)

type Session struct {
	Principal Principal
	CSRFToken string
	ExpiresAt time.Time
}

type IssuedSession struct {
	Session
	Token string
}

func newSession(now time.Time, lifetime time.Duration) (IssuedSession, error) {
	var token, csrf [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return IssuedSession{}, fmt.Errorf("generate session: %w", err)
	}
	if _, err := rand.Read(csrf[:]); err != nil {
		return IssuedSession{}, fmt.Errorf("generate CSRF token: %w", err)
	}
	return IssuedSession{
		Token:   base64.RawURLEncoding.EncodeToString(token[:]),
		Session: Session{CSRFToken: base64.RawURLEncoding.EncodeToString(csrf[:]), ExpiresAt: now.Add(lifetime)},
	}, nil
}

func digestToken(token string) ([]byte, error) {
	if len(token) != 43 {
		return nil, ErrSession
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return nil, ErrSession
	}
	digest := sha256.Sum256(decoded)
	return digest[:], nil
}

func (s *Store) challenge(ctx context.Context, token string, now time.Time) (Session, error) {
	digest, err := digestToken(token)
	if err != nil {
		return Session{}, err
	}
	var session Session
	err = s.pool.QueryRow(ctx, `SELECT csrf_token, expires_at FROM auth_sessions
		WHERE token_hash = $1 AND user_id IS NULL AND expires_at > $2`, digest, now).
		Scan(&session.CSRFToken, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("read login state: %w", err)
	}
	return session, nil
}

func (s *Store) createChallenge(ctx context.Context, now time.Time) (IssuedSession, error) {
	issued, err := newSession(now, LoginLifetime)
	if err != nil {
		return IssuedSession{}, err
	}
	digest, err := digestToken(issued.Token)
	if err != nil {
		return IssuedSession{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM auth_sessions WHERE token_hash IN
			(SELECT token_hash FROM auth_sessions WHERE expires_at <= $1 LIMIT 100)`, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_sessions (token_hash, csrf_token, created_at, last_seen_at, expires_at)
			VALUES ($1, $2, $3, $3, $4)`, digest, issued.CSRFToken, now, issued.ExpiresAt)
		return err
	})
	if err != nil {
		return IssuedSession{}, fmt.Errorf("create login state: %w", err)
	}
	return issued, nil
}

type promotion struct {
	user           userRecord
	challengeToken string
	previousToken  string
	now            time.Time
}

func (s *Store) promote(ctx context.Context, request promotion) (IssuedSession, error) {
	issued, err := newSession(request.now, SessionLifetime)
	if err != nil {
		return IssuedSession{}, err
	}
	issued.Principal = request.user.principal
	digest, err := digestToken(issued.Token)
	if err != nil {
		return IssuedSession{}, err
	}
	challengeDigest, err := digestToken(request.challengeToken)
	if err != nil {
		return IssuedSession{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `DELETE FROM auth_sessions WHERE token_hash = $1 AND user_id IS NULL AND expires_at > $2`, challengeDigest, request.now)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrSession
		}
		result, err = tx.Exec(ctx, `INSERT INTO auth_sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at)
			SELECT $1, id, $2, $3, $3, $4 FROM users WHERE id = $5 AND active AND password_hash = $6`,
			digest, issued.CSRFToken, request.now, issued.ExpiresAt, issued.Principal.UserID, request.user.passwordHash)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrCredentials
		}
		if previous, err := digestToken(request.previousToken); err == nil {
			if _, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE token_hash = $1", previous); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return IssuedSession{}, fmt.Errorf("issue session: %w", err)
	}
	return issued, nil
}

func (s *Store) session(ctx context.Context, token string, now time.Time) (Session, error) {
	digest, err := digestToken(token)
	if err != nil {
		return Session{}, err
	}
	var session Session
	err = s.pool.QueryRow(ctx, `UPDATE auth_sessions AS s SET last_seen_at = $2
		FROM users AS u WHERE s.token_hash = $1 AND s.user_id = u.id AND u.active
		AND s.expires_at > $2 AND s.last_seen_at > $3
		RETURNING u.id, u.username, u.is_platform_admin, s.csrf_token, s.expires_at`, digest, now, now.Add(-IdleTimeout)).
		Scan(&session.Principal.UserID, &session.Principal.Username, &session.Principal.IsPlatformAdmin,
			&session.CSRFToken, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	return session, nil
}

func (s *Store) revoke(ctx context.Context, token string) error {
	digest, err := digestToken(token)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM auth_sessions WHERE token_hash = $1", digest); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}
