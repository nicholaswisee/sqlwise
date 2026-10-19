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
				now = now.Add(IdleTimeout)
			case "absolute":
				now = now.Add(SessionLifetime)
				if _, err := pool.Exec(t.Context(), "UPDATE auth_sessions SET last_seen_at = $1", now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
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
