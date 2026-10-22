package auth

import (
	"errors"
	"sync"
	"testing"
	"time"

	"sqlwise/internal/testdb"
)

func TestLoginSessionLifecycle(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseCredentials("admin", "test login passphrase")
	if err != nil {
		t.Fatal(err)
	}
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
	request := LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken}
	issued, err := service.Login(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token == challenge.Token || issued.CSRFToken == challenge.CSRFToken {
		t.Fatal("pre-login state was reused")
	}
	if _, err := service.Login(t.Context(), request); !errors.Is(err, ErrCSRF) {
		t.Fatal("consumed challenge replay succeeded")
	}
	if _, err := service.Session(t.Context(), challenge.Token); !errors.Is(err, ErrSession) {
		t.Fatal("pre-login token authenticated")
	}
	if _, err := service.Session(t.Context(), issued.Token); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), issued.Token, challenge.CSRFToken); !errors.Is(err, ErrCSRF) {
		t.Fatal("cross-session CSRF accepted")
	}
	if err := service.Logout(t.Context(), issued.Token, issued.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(t.Context(), issued.Token); !errors.Is(err, ErrSession) {
		t.Fatal("logout did not revoke session")
	}
}

func TestSessionExpiryAndDisabledUser(t *testing.T) {
	for _, scenario := range []string{"idle", "absolute", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			pool := testdb.Open(t)
			store := NewStore(pool)
			if err := store.ApplySchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			credentials, err := ParseCredentials("admin", "test login passphrase")
			if err != nil {
				t.Fatal(err)
			}
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
			switch scenario {
			case "idle":
				now = now.Add(IdleTimeout - time.Nanosecond)
				if _, err := service.Session(t.Context(), issued.Token); err != nil {
					t.Fatalf("session rejected just before idle boundary: %v", err)
				}
				digest, err := digestToken(issued.Token)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(t.Context(), `UPDATE auth_session_families SET last_seen_at = $1
					WHERE id = (SELECT family_id FROM auth_sessions WHERE token_hash = $2)`, now.Add(-(IdleTimeout - time.Nanosecond)), digest); err != nil {
					t.Fatal(err)
				}
				now = now.Add(time.Nanosecond)
			case "absolute":
				now = now.Add(SessionLifetime - time.Nanosecond)
				if _, err := pool.Exec(t.Context(), "UPDATE auth_session_families SET last_seen_at = $1", now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Session(t.Context(), issued.Token); err != nil {
					t.Fatalf("session rejected just before absolute boundary: %v", err)
				}
				now = now.Add(time.Nanosecond)
			case "disabled":
				if _, err := pool.Exec(t.Context(), "UPDATE users SET active = false"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Session(t.Context(), issued.Token); !errors.Is(err, ErrSession) {
				t.Fatalf("session accepted after %s: %v", scenario, err)
			}
		})
	}
}

func TestConcurrentLoginConsumesChallengeOnce(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseCredentials("admin", "test login passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	request := LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { <-start; _, err := service.Login(t.Context(), request); results <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrSession) && !errors.Is(err, ErrCSRF) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("successful logins = %d, want 1", success)
	}
}

func TestLogoutRevokesEveryFamilyToken(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseCredentials("admin", "test login passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, time.Now)
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
	issuedDigest, _ := digestToken(issued.Token)
	var familyID string
	if err := pool.QueryRow(t.Context(), "SELECT family_id FROM auth_sessions WHERE token_hash = $1", issuedDigest).Scan(&familyID); err != nil {
		t.Fatal(err)
	}
	sibling, err := newSession(time.Now(), SessionLifetime)
	if err != nil {
		t.Fatal(err)
	}
	siblingDigest, _ := digestToken(sibling.Token)
	if _, err := pool.Exec(t.Context(), `INSERT INTO auth_sessions
		(token_hash, user_id, family_id, csrf_token, created_at, last_seen_at, expires_at, alias_until, rotated_at)
		SELECT $1, user_id, id, csrf_token, now(), now(), absolute_expires_at, now() + interval '1 hour', now()
		FROM auth_session_families WHERE id = $2`, siblingDigest, familyID); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), issued.Token, issued.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(t.Context(), sibling.Token); !errors.Is(err, ErrSession) {
		t.Fatalf("sibling token survived logout: %v", err)
	}
}

func TestLoginWithPreviousTokenRevokesPreviousFamily(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseCredentials("admin", "test login passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := service.Login(t.Context(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, _ := digestToken(old.Token)
	var familyID string
	if err := pool.QueryRow(t.Context(), "SELECT family_id FROM auth_sessions WHERE token_hash = $1", oldDigest).Scan(&familyID); err != nil {
		t.Fatal(err)
	}
	sibling, err := newSession(time.Now(), SessionLifetime)
	if err != nil {
		t.Fatal(err)
	}
	siblingDigest, _ := digestToken(sibling.Token)
	if _, err := pool.Exec(t.Context(), `INSERT INTO auth_sessions
		(token_hash, user_id, family_id, csrf_token, created_at, last_seen_at, expires_at, alias_until, rotated_at)
		SELECT $1, user_id, id, csrf_token, now(), now(), absolute_expires_at, now() + interval '1 hour', now()
		FROM auth_session_families WHERE id = $2`, siblingDigest, familyID); err != nil {
		t.Fatal(err)
	}
	challenge, err = service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(t.Context(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken, PreviousSessionToken: old.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(t.Context(), sibling.Token); !errors.Is(err, ErrSession) {
		t.Fatalf("previous family sibling survived login: %v", err)
	}
}
