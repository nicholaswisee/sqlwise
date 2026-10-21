package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"time"
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
	return s.store.session(ctx, token, s.now, s.random)
}

func (s *Service) Logout(ctx context.Context, token, csrf string) error {
	return s.store.logout(ctx, token, csrf, s.now)
}

func ValidCSRF(expected, actual string) bool {
	return len(expected) == 43 && len(actual) == 43 && subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
