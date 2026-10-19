package auth

import (
	"errors"
	"testing"
	"time"

	"sqlwise/internal/testdb"
)

func TestLoginRejectsCredentialsAndInvalidChallenges(t *testing.T) {
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
	for _, test := range []struct {
		name, username, password string
		disabled                 bool
	}{
		{"wrong password", "admin", "wrong password", false},
		{"unknown user", "nobody", "test login passphrase", false},
		{"disabled user", "admin", "test login passphrase", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), "UPDATE users SET active = $1", !test.disabled); err != nil {
				t.Fatal(err)
			}
			challenge, err := service.LoginChallenge(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			input, err := ParseCredentials(test.username, test.password)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Login(t.Context(), LoginRequest{Credentials: input, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
			if !errors.Is(err, ErrCredentials) {
				t.Fatalf("login = %v", err)
			}
			var count int
			if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_sessions WHERE user_id IS NOT NULL").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed login created a session")
			}
		})
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateLoginCSRF(t.Context(), challenge.Token, "forged"); !errors.Is(err, ErrCSRF) {
		t.Fatal("forged CSRF accepted")
	}
	now = now.Add(LoginLifetime)
	if err := service.ValidateLoginCSRF(t.Context(), challenge.Token, challenge.CSRFToken); !errors.Is(err, ErrCSRF) {
		t.Fatal("expired challenge accepted")
	}
}
