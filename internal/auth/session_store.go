package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	LoginLifetime    = 10 * time.Minute
	SessionLifetime  = 12 * time.Hour
	IdleTimeout      = 30 * time.Minute
	RotationInterval = 15 * time.Minute
	AliasGrace       = 30 * time.Second
)

type Session struct {
	Principal      Principal
	CSRFToken      string
	ExpiresAt      time.Time
	Replacement    *IssuedSession
	presentedToken string
	authzEpoch     int64
	proof          *sessionProof
}

func (s Session) PresentedToken() string { return s.presentedToken }
func (s Session) AuthzEpoch() int64      { return s.authzEpoch }

type IssuedSession struct {
	Session
	Token string
}

var randomMu sync.Mutex

func newSession(now time.Time, lifetime time.Duration) (IssuedSession, error) {
	return newSessionWithReader(now, lifetime, rand.Reader)
}
func newSessionWithReader(now time.Time, lifetime time.Duration, random io.Reader) (IssuedSession, error) {
	var token, csrf [32]byte
	randomMu.Lock()
	defer randomMu.Unlock()
	if _, err := io.ReadFull(random, token[:]); err != nil {
		return IssuedSession{}, fmt.Errorf("generate session: %w", err)
	}
	if _, err := io.ReadFull(random, csrf[:]); err != nil {
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

func (s *Store) createChallenge(ctx context.Context, now time.Time, random io.Reader) (IssuedSession, error) {
	issued, err := newSessionWithReader(now, LoginLifetime, random)
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
	clock          func() time.Time
	random         io.Reader
}

func (s *Store) promote(ctx context.Context, request promotion) (IssuedSession, error) {
	issued, err := newSessionWithReader(time.Time{}, SessionLifetime, request.random)
	if err != nil {
		return IssuedSession{}, err
	}
	issued.Principal = request.user.principal
	var familyBytes [16]byte
	randomMu.Lock()
	_, err = io.ReadFull(request.random, familyBytes[:])
	randomMu.Unlock()
	if err != nil {
		return IssuedSession{}, fmt.Errorf("generate session family: %w", err)
	}
	familyID := base64.RawURLEncoding.EncodeToString(familyBytes[:])
	digest, err := digestToken(issued.Token)
	if err != nil {
		return IssuedSession{}, err
	}
	challengeDigest, err := digestToken(request.challengeToken)
	if err != nil {
		return IssuedSession{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := LockAuthz(ctx, tx, false); err != nil {
			return err
		}
		var previousFamily string
		if previous, err := digestToken(request.previousToken); err == nil {
			lockErr := tx.QueryRow(ctx, `SELECT f.id FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id WHERE s.token_hash=$1 FOR UPDATE OF f`, previous).Scan(&previousFamily)
			if lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
				return lockErr
			}
		}
		var challengeExpiry time.Time
		if err := tx.QueryRow(ctx, `SELECT expires_at FROM auth_sessions WHERE token_hash=$1 AND user_id IS NULL FOR UPDATE`, challengeDigest).Scan(&challengeExpiry); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrSession
			}
			return err
		}
		now := request.clock()
		if !challengeExpiry.After(now) {
			return ErrSession
		}
		issued.ExpiresAt = now.Add(SessionLifetime)
		result, err := tx.Exec(ctx, `DELETE FROM auth_sessions WHERE token_hash = $1 AND user_id IS NULL AND expires_at > $2`, challengeDigest, now)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrSession
		}
		result, err = tx.Exec(ctx, `INSERT INTO auth_session_families
			(id, user_id, created_at, last_seen_at, absolute_expires_at, csrf_token)
			SELECT $1, id, $3, $3, $4, $2 FROM users WHERE id = $5 AND active AND password_hash = $6`,
			familyID, issued.CSRFToken, now, issued.ExpiresAt, issued.Principal.UserID, request.user.passwordHash)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrCredentials
		}
		if err := tx.QueryRow(ctx, `SELECT authz_epoch FROM users WHERE id=$1`, issued.Principal.UserID).Scan(&issued.authzEpoch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO auth_sessions
			(token_hash, user_id, family_id, csrf_token, created_at, last_seen_at, expires_at, authz_epoch)
			SELECT $1, $2, $3, $4, $5, $5, $6, authz_epoch FROM users WHERE id=$2`, digest, issued.Principal.UserID, familyID, issued.CSRFToken, now, issued.ExpiresAt); err != nil {
			return err
		}
		if previousFamily != "" {
			if _, err := tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at = $2
				WHERE id = $1 AND revoked_at IS NULL`, previousFamily, now); err != nil {
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

func (s *Store) session(ctx context.Context, token string, clock func() time.Time, random io.Reader, readOnly bool) (Session, error) {
	digest, err := digestToken(token)
	if err != nil {
		return Session{}, err
	}
	var session Session
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := LockAuthz(ctx, tx, false); err != nil {
			return err
		}
		var lockedFamily string
		lockErr := tx.QueryRow(ctx, `SELECT f.id FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id
			WHERE s.token_hash=$1 FOR UPDATE OF f`, digest).Scan(&lockedFamily)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ErrSession
		}
		if lockErr != nil {
			return lockErr
		}
		now := clock()
		var family, uid string
		var created, lastSeen, absolute, alias *time.Time
		var tokenEpoch, userEpoch int64
		err := tx.QueryRow(ctx, `SELECT f.id, u.id, u.username, u.is_platform_admin, f.csrf_token, f.absolute_expires_at,
			s.created_at, f.last_seen_at, s.alias_until, s.authz_epoch, u.authz_epoch
			FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id JOIN users u ON u.id=f.user_id
			WHERE s.token_hash=$1 AND s.user_id=u.id AND u.active AND f.revoked_at IS NULL`, digest).Scan(&family, &uid, &session.Principal.Username, &session.Principal.IsPlatformAdmin, &session.CSRFToken, &absolute, &created, &lastSeen, &alias, &tokenEpoch, &userEpoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSession
		}
		if err != nil {
			return err
		}
		if !absolute.After(now) || !lastSeen.After(now.Add(-IdleTimeout)) {
			return ErrSession
		}
		session.Principal.UserID, session.ExpiresAt, session.authzEpoch, session.presentedToken = uid, *absolute, tokenEpoch, token
		var tokenDigest [32]byte
		copy(tokenDigest[:], digest)
		session.proof = &sessionProof{digest: tokenDigest, epoch: tokenEpoch, userID: uid, familyID: family}
		if alias != nil && alias.After(now) {
			return nil
		}
		if alias != nil {
			return ErrSession
		}
		if readOnly {
			return nil
		}
		var successorHash []byte
		if now.Sub(*created) >= RotationInterval {
			next, err := newSessionWithReader(now, SessionLifetime, random)
			if err != nil {
				return err
			}
			next.Principal = session.Principal
			next.CSRFToken = session.CSRFToken
			next.authzEpoch = userEpoch
			if next.ExpiresAt.After(*absolute) {
				next.ExpiresAt = *absolute
			}
			successorHash, err = digestToken(next.Token)
			if err != nil {
				return err
			}
			updated, err := tx.Exec(ctx, `UPDATE auth_sessions SET alias_until=$2,rotated_at=$3,successor_hash=$4
				WHERE token_hash=$1 AND rotated_at IS NULL`, digest, now.Add(AliasGrace), now, successorHash)
			if err != nil {
				return err
			}
			if updated.RowsAffected() != 1 {
				return ErrSession
			}
			if _, err = tx.Exec(ctx, `INSERT INTO auth_sessions(token_hash,user_id,family_id,csrf_token,created_at,last_seen_at,expires_at,authz_epoch) VALUES($1,$2,$3,$4,$5,$5,$6,$7)`, successorHash, uid, family, session.CSRFToken, now, next.ExpiresAt, userEpoch); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE auth_session_families SET last_seen_at=$2 WHERE id=$1`, family, now); err != nil {
				return err
			}
			session.Replacement = &next
		} else {
			if _, err := tx.Exec(ctx, `UPDATE auth_session_families SET last_seen_at=$2 WHERE id=$1`, family, now); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	return session, nil
}

func (s *Store) logout(ctx context.Context, token, csrf string, clock func() time.Time) error {
	digest, err := digestToken(token)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := LockAuthz(ctx, tx, false); err != nil {
			return err
		}
		var lockedFamily string
		lockErr := tx.QueryRow(ctx, `SELECT f.id FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id
			WHERE s.token_hash=$1 FOR UPDATE OF f`, digest).Scan(&lockedFamily)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ErrSession
		}
		if lockErr != nil {
			return lockErr
		}
		now := clock()
		var expectedCSRF string
		err := tx.QueryRow(ctx, `SELECT f.csrf_token
			FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id JOIN users u ON u.id=f.user_id
			WHERE s.token_hash=$1 AND s.user_id=u.id AND f.user_id=u.id AND u.active AND f.revoked_at IS NULL
			AND f.absolute_expires_at>$2 AND f.last_seen_at>$3 AND (s.alias_until IS NULL OR s.alias_until>$2)`, digest, now, now.Add(-IdleTimeout)).Scan(&expectedCSRF)
		if err != nil {
			return err
		}
		if !ValidCSRF(expectedCSRF, csrf) {
			return ErrCSRF
		}
		_, err = tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, lockedFamily, now)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSession
	}
	if err != nil {
		if errors.Is(err, ErrSession) || errors.Is(err, ErrCSRF) {
			return err
		}
		return fmt.Errorf("logout session: %w", err)
	}
	return nil
}

const AuthzLockKey int64 = 311004

func LockAuthz(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	fn := "pg_advisory_xact_lock_shared"
	if exclusive {
		fn = "pg_advisory_xact_lock"
	}
	_, err := tx.Exec(ctx, fmt.Sprintf("SELECT %s($1)", fn), AuthzLockKey)
	return err
}
