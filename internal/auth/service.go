package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Service struct {
	store         *Store
	now           func() time.Time
	dummyHash     string
	passwordSlots chan struct{}
	random        io.Reader
}

func NewService(store *Store, now func() time.Time) (*Service, error) {
	return NewServiceWithRandom(store, now, rand.Reader)
}

func NewServiceWithRandom(store *Store, now func() time.Time, random io.Reader) (*Service, error) {
	if random == nil {
		random = rand.Reader
	}
	dummy, err := HashPassword("invalid-user timing placeholder")
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now, dummyHash: dummy, passwordSlots: make(chan struct{}, 2), random: random}, nil
}

func (s *Service) LoginChallenge(ctx context.Context, token string) (IssuedSession, error) {
	session, err := s.store.challenge(ctx, token, s.now())
	if err == nil {
		return IssuedSession{Token: token, Session: session}, nil
	}
	if !errors.Is(err, ErrSession) {
		return IssuedSession{}, err
	}
	return s.store.createChallenge(ctx, s.now(), s.random)
}

func (s *Service) ValidateLoginCSRF(ctx context.Context, token, csrf string) error {
	session, err := s.store.challenge(ctx, token, s.now())
	if errors.Is(err, ErrSession) {
		return ErrCSRF
	}
	if err != nil {
		return err
	}
	if !ValidCSRF(session.CSRFToken, csrf) {
		return ErrCSRF
	}
	return nil
}

type LoginRequest struct {
	Credentials          Credentials
	ChallengeToken       string
	CSRFToken            string
	PreviousSessionToken string
}

func (s *Service) Login(ctx context.Context, request LoginRequest) (IssuedSession, error) {
	if err := s.ValidateLoginCSRF(ctx, request.ChallengeToken, request.CSRFToken); err != nil {
		return IssuedSession{}, err
	}
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	default:
		return IssuedSession{}, ErrBusy
	}
	user, err := s.store.findUser(ctx, request.Credentials.username)
	missing := errors.Is(err, ErrCredentials)
	if err != nil && !missing {
		return IssuedSession{}, err
	}
	hash := user.passwordHash
	if missing {
		hash = s.dummyHash
	}
	valid, err := VerifyPassword(hash, request.Credentials.password)
	if err != nil {
		return IssuedSession{}, err
	}
	if missing || !user.active || !valid {
		return IssuedSession{}, ErrCredentials
	}
	return s.store.promote(ctx, promotion{user: user, challengeToken: request.ChallengeToken,
		previousToken: request.PreviousSessionToken, clock: s.now, random: s.random})
}

func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	return s.store.session(ctx, token, s.now, s.random, false)
}

func (s *Service) ReadOnlySession(ctx context.Context, token string) (Session, error) {
	return s.store.session(ctx, token, s.now, s.random, true)
}

func (s *Service) Logout(ctx context.Context, token, csrf string) error {
	return s.store.logout(ctx, token, csrf, s.now)
}

// Now returns the service's injected clock value for coordinated mutations.
func (s *Service) Now() time.Time { return s.now() }

func (s *Service) adminTransaction(ctx context.Context, mutate func(tx pgx.Tx, now time.Time) error) error {
	return pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		if err := LockAuthz(ctx, tx, true); err != nil {
			return err
		}
		actor, err := s.ValidateContext(ctx, tx)
		if err != nil {
			return err
		}
		if !actor.IsPlatformAdmin {
			return ErrSession
		}
		return mutate(tx, s.now())
	})
}

func (s *Service) CreateUser(ctx context.Context, credentials Credentials) (Principal, error) {
	validated, err := ParseCredentials(credentials.username, credentials.password)
	if err != nil || utf8.RuneCountInString(credentials.password) < 12 {
		return Principal{}, ErrInvalidInput
	}
	actor, err := s.ValidateContextFresh(ctx)
	if err != nil {
		return Principal{}, err
	}
	if !actor.IsPlatformAdmin {
		return Principal{}, ErrSession
	}
	select {
	case s.passwordSlots <- struct{}{}:
	default:
		return Principal{}, ErrBusy
	}
	hash, err := HashPassword(validated.password)
	<-s.passwordSlots
	if err != nil {
		return Principal{}, err
	}
	idBytes := make([]byte, 16)
	randomMu.Lock()
	_, err = io.ReadFull(s.random, idBytes)
	randomMu.Unlock()
	if err != nil {
		return Principal{}, err
	}
	id := hex.EncodeToString(idBytes)
	var principal Principal
	err = s.adminTransaction(ctx, func(tx pgx.Tx, now time.Time) error {
		_, err = tx.Exec(ctx, `INSERT INTO users(id,username,password_hash,active,created_at,updated_at) VALUES($1,$2,$3,true,$4,$4)`, id, validated.username, hash, now)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_username_key" {
				return ErrUsernameTaken
			}
			return err
		}
		principal = Principal{UserID: id, Username: validated.username}
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func (s *Service) SetUserActive(ctx context.Context, userID string, active bool) error {
	return s.adminTransaction(ctx, func(tx pgx.Tx, now time.Time) error {
		var previous bool
		if err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&previous); errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		} else if err != nil {
			return err
		}
		if previous == active {
			return nil
		}
		if active {
			_, err := tx.Exec(ctx, `UPDATE users SET active=true,authz_epoch=authz_epoch+1,updated_at=$2 WHERE id=$1`, userID, now)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET active=false,updated_at=$2 WHERE id=$1`, userID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now)
		return err
	})
}

func (s *Service) SetPlatformAdmin(ctx context.Context, userID string, admin bool) error {
	return s.adminTransaction(ctx, func(tx pgx.Tx, now time.Time) error {
		var previous bool
		if err := tx.QueryRow(ctx, `SELECT is_platform_admin FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&previous); errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		} else if err != nil {
			return err
		}
		if previous == admin {
			return nil
		}
		if admin {
			_, err := tx.Exec(ctx, `UPDATE users SET is_platform_admin=true,authz_epoch=authz_epoch+1,updated_at=$2 WHERE id=$1`, userID, now)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET is_platform_admin=false,updated_at=$2 WHERE id=$1`, userID, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now)
		return err
	})
}

func (s *Service) RevokeUserSessions(ctx context.Context, userID string) error {
	return s.adminTransaction(ctx, func(tx pgx.Tx, now time.Time) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrUserNotFound
		}
		_, err := tx.Exec(ctx, `UPDATE auth_session_families SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now)
		return err
	})
}

func ValidCSRF(expected, actual string) bool {
	return len(expected) == 43 && len(actual) == 43 && subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
