package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"sqlwise/internal/testdb"
)

func TestSessionRotationStartsAtFifteenMinutes(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, _ := ParseCredentials("admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	service, err := NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Login(t.Context(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	absolute := now.Add(16 * time.Minute)
	if _, err := pool.Exec(t.Context(), `UPDATE auth_session_families SET absolute_expires_at=$1 WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$2)`, absolute, mustDigest(t, issued.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE auth_sessions SET authz_epoch=7 WHERE token_hash=$1`, mustDigest(t, issued.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET authz_epoch=9 WHERE id=$1`, issued.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(15 * time.Minute)
	services := make([]*Service, 2)
	for i := range services {
		services[i], err = NewService(store, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan Session, len(services))
	errs := make(chan error, len(services))
	for _, svc := range services {
		go func(svc *Service) { got, err := svc.Session(t.Context(), issued.Token); results <- got; errs <- err }(svc)
	}
	var winner *Session
	for range services {
		got, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		if got.Replacement != nil {
			if winner != nil {
				t.Fatal("multiple rotation winners")
			}
			winner = &got
		}
	}
	if winner == nil || winner.Replacement.Token == issued.Token {
		t.Fatal("rotation threshold did not provide exactly one winning replacement")
	}
	if !winner.Replacement.ExpiresAt.Equal(absolute) {
		t.Fatalf("successor expiry=%s, want absolute cap %s", winner.Replacement.ExpiresAt, absolute)
	}
	var successorHash []byte
	var rotatedAt *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT successor_hash,rotated_at FROM auth_sessions WHERE token_hash=$1`, mustDigest(t, issued.Token)).Scan(&successorHash, &rotatedAt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(successorHash, mustDigest(t, winner.Replacement.Token)) || rotatedAt == nil || !rotatedAt.Equal(now) {
		t.Fatal("rotation lineage did not persist successor digest and timestamp")
	}
	var rawTokenColumns int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='auth_sessions' AND column_name IN ('token','successor_token')`).Scan(&rawTokenColumns); err != nil {
		t.Fatal(err)
	}
	if rawTokenColumns != 0 {
		t.Fatal("auth_sessions exposes recoverable token material")
	}
	var currentCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE family_id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1) AND rotated_at IS NULL`, mustDigest(t, issued.Token)).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if currentCount != 1 {
		t.Fatalf("current family token count=%d, want 1", currentCount)
	}
	session := *winner
	if session.PresentedToken() != issued.Token {
		t.Fatal("presented token proof changed")
	}
	if session.AuthzEpoch() != 7 {
		t.Fatalf("presented epoch=%d, want token epoch 7", session.AuthzEpoch())
	}
	var successorEpoch int64
	if err := pool.QueryRow(t.Context(), `SELECT authz_epoch FROM auth_sessions WHERE token_hash=$1`, mustDigest(t, winner.Replacement.Token)).Scan(&successorEpoch); err != nil {
		t.Fatal(err)
	}
	if successorEpoch != 9 {
		t.Fatalf("successor epoch=%d, want current user epoch 9", successorEpoch)
	}
	alias, err := service.Session(t.Context(), issued.Token)
	if err != nil || alias.Replacement != nil {
		t.Fatalf("grace alias should authenticate without another cookie: %#v, %v", alias.Replacement, err)
	}
	var activity time.Time
	if err := pool.QueryRow(t.Context(), `SELECT last_seen_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, mustDigest(t, issued.Token)).Scan(&activity); err != nil {
		t.Fatal(err)
	}
	if !activity.Equal(now) {
		t.Fatalf("alias activity changed: %s", activity)
	}
	now = now.Add(AliasGrace)
	if _, err := service.Session(t.Context(), issued.Token); !errors.Is(err, ErrSession) {
		t.Fatalf("alias at grace boundary = %v", err)
	}
	if err := service.Logout(t.Context(), issued.Token, issued.CSRFToken); !errors.Is(err, ErrSession) {
		t.Fatalf("logout accepted expired alias: %v", err)
	}
	if _, err := service.Session(t.Context(), winner.Replacement.Token); err != nil {
		t.Fatalf("expired-alias logout revoked current token: %v", err)
	}
	noRandomService, err := NewServiceWithRandom(store, func() time.Time { return now }, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := noRandomService.Logout(t.Context(), winner.Replacement.Token, issued.CSRFToken); err != nil {
		t.Fatalf("logout required fresh randomness: %v", err)
	}
	var familyID string
	if err := pool.QueryRow(t.Context(), `SELECT family_id FROM auth_sessions WHERE token_hash=$1`, mustDigest(t, issued.Token)).Scan(&familyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE auth_sessions SET expires_at=$1 WHERE family_id=$2`, now, familyID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoginChallenge(t.Context(), ""); err != nil {
		t.Fatalf("cleanup of expired rotation chain: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE family_id=$1`, familyID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("expired rotation chain retained %d token rows", remaining)
	}
}

func TestSessionRandomnessRequiresExactReads(t *testing.T) {
	if _, err := newSessionWithReader(time.Now(), SessionLifetime, bytes.NewReader(make([]byte, 63))); err == nil {
		t.Fatal("short random read accepted")
	}
	if _, err := newSessionWithReader(time.Now(), SessionLifetime, failingReader{}); !errors.Is(err, errRandomRead) {
		t.Fatalf("random source failure was not propagated: %v", err)
	}
}

var errRandomRead = errors.New("random source failed")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRandomRead }

func TestLoginPromotionWaitsForAuthzLockAndRechecksChallengeTime(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, _ := ParseCredentials("admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	service, err := NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := LockAuthz(t.Context(), tx, true); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.Login(context.Background(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
		result <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-result:
			t.Fatalf("promotion bypassed authz lock, result=%v", err)
		default:
		}
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock_shared%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("promotion did not wait for authz lock")
		}
	}
	now = now.Add(LoginLifetime)
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrSession) {
		t.Fatalf("promotion used pre-lock time for challenge: %v", err)
	}
}

func TestSessionChecksClockAfterFamilyLock(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, _ := ParseCredentials("admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	service, err := NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Login(t.Context(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := LockAuthz(t.Context(), tx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT id FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1) FOR UPDATE`, mustDigest(t, issued.Token)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := service.Session(context.Background(), issued.Token); result <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%WHERE s.token_hash=$1 FOR UPDATE OF f%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session did not wait for authz lock")
		}
	}
	now = now.Add(IdleTimeout)
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrSession) {
		t.Fatalf("session validated with pre-lock time: %v", err)
	}
}

func mustDigest(t *testing.T, token string) []byte {
	t.Helper()
	digest, err := digestToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
