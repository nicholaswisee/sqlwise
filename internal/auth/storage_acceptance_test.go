package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/testdb"
)

func TestLoginAndRotationPersistOnlyIndependentTokenDigests(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseCredentials("admin", "integration test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
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
	assertCurrentTokenHash(t, pool, issued.Principal.UserID, issued.Token)
	if _, err := pool.Exec(t.Context(), `UPDATE auth_sessions SET created_at=$1 WHERE user_id=$2 AND rotated_at IS NULL`, now.Add(-RotationInterval), issued.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(RotationInterval)
	session, err := service.Session(t.Context(), issued.Token)
	if err != nil || session.Replacement == nil {
		t.Fatalf("rotate session: replacement=%v err=%v", session.Replacement, err)
	}
	assertCurrentTokenHash(t, pool, issued.Principal.UserID, session.Replacement.Token)
}

func assertCurrentTokenHash(t *testing.T, pool *pgxpool.Pool, userID, token string) {
	t.Helper()
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("invalid token encoding: %v", err)
	}
	expected := sha256.Sum256(raw)
	var stored []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_hash FROM auth_sessions WHERE user_id=$1 AND rotated_at IS NULL`, userID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != sha256.Size || string(stored) != string(expected[:]) || string(stored) == string(raw) {
		t.Fatalf("stored current token material does not match independent SHA-256 digest")
	}
}

func TestLoginChallengeCleanupDoesNotRestoreExpiredSessionValidity(t *testing.T) {
	for _, expiredBy := range []string{"idle", "absolute"} {
		t.Run(expiredBy, func(t *testing.T) {
			pool := testdb.Open(t)
			store := NewStore(pool)
			if err := store.ApplySchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			credentials, err := ParseCredentials("admin", "integration test passphrase")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
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
			digest := acceptanceTokenHash(t, issued.Token)
			var update string
			if expiredBy == "idle" {
				update = `UPDATE auth_session_families SET last_seen_at=$1 WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$2)`
				_, err = pool.Exec(t.Context(), update, now.Add(-IdleTimeout), digest)
			} else {
				update = `UPDATE auth_session_families SET absolute_expires_at=$1 WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$2)`
				now = now.Add(2 * time.Second)
				_, err = pool.Exec(t.Context(), update, now.Add(-time.Second), digest)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Session(t.Context(), issued.Token); !errors.Is(err, ErrSession) {
				t.Fatalf("session at %s boundary = %v, want ErrSession", expiredBy, err)
			}
			if _, err := service.LoginChallenge(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE token_hash=$1`, digest).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("cleanup removed retained session record: rows=%d", rows)
			}
			if _, err := service.Session(t.Context(), issued.Token); !errors.Is(err, ErrSession) {
				t.Fatalf("login challenge cleanup restored %s-expired token: %v", expiredBy, err)
			}
		})
	}
}

func acceptanceTokenHash(t *testing.T, token string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("invalid token encoding: %v", err)
	}
	hash := sha256.Sum256(raw)
	return hash[:]
}
