package auth

import (
	"errors"
	"testing"
	"time"

	"sqlwise/internal/testdb"
)

func TestBootstrapAdmin(t *testing.T) {
	pool := testdb.Open(t)
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal("schema is not idempotent:", err)
	}
	credentials, err := ParseCredentials("First.Admin", "bootstrap test password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	user, err := store.findUser(t.Context(), "first.admin")
	if err != nil {
		t.Fatal(err)
	}
	if !user.active || !user.principal.IsPlatformAdmin || user.principal.UserID == "" {
		t.Fatal("invalid initial admin")
	}
	valid, err := VerifyPassword(user.passwordHash, credentials.password)
	if err != nil || !valid {
		t.Fatal("password did not round-trip")
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); !errors.Is(err, ErrAdminExists) {
		t.Fatalf("duplicate bootstrap = %v", err)
	}
	if _, err := store.findUser(t.Context(), "unknown"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("missing user = %v", err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, username, password_hash) VALUES ('duplicate', 'first.admin', 'hash')`); err == nil {
		t.Fatal("username uniqueness not enforced")
	}
}

func TestApplySchemaPreservesW1SessionsAndChallenges(t *testing.T) {
	pool := testdb.Open(t)
	_, err := pool.Exec(t.Context(), `
		CREATE TABLE users (
			id text PRIMARY KEY, username text NOT NULL UNIQUE, password_hash text NOT NULL,
			is_platform_admin boolean NOT NULL DEFAULT false, active boolean NOT NULL DEFAULT true,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE auth_sessions (
			token_hash bytea PRIMARY KEY, user_id text REFERENCES users(id) ON DELETE CASCADE,
			csrf_token text NOT NULL, created_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL,
			expires_at timestamptz NOT NULL
		);
		INSERT INTO users (id, username, password_hash) VALUES ('legacy-user', 'legacy', 'hash');`)
	if err != nil {
		t.Fatal("create W1 database:", err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	authenticated, err := newSession(now, SessionLifetime)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := newSession(now, LoginLifetime)
	if err != nil {
		t.Fatal(err)
	}
	authDigest, _ := digestToken(authenticated.Token)
	challengeDigest, _ := digestToken(challenge.Token)
	_, err = pool.Exec(t.Context(), `INSERT INTO auth_sessions
		(token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at) VALUES
		($1, 'legacy-user', $2, $3, $3, $4), ($5, NULL, $6, $3, $3, $7)`,
		authDigest, authenticated.CSRFToken, now, authenticated.ExpiresAt,
		challengeDigest, challenge.CSRFToken, challenge.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal("repeat schema apply:", err)
	}
	var epoch int64
	if err := pool.QueryRow(t.Context(), "SELECT authz_epoch FROM users WHERE id = 'legacy-user'").Scan(&epoch); err != nil || epoch != 0 {
		t.Fatalf("legacy user epoch = %d, %v", epoch, err)
	}
	var familyID string
	if err := pool.QueryRow(t.Context(), "SELECT family_id FROM auth_sessions WHERE token_hash = $1", authDigest).Scan(&familyID); err != nil || familyID == "" {
		t.Fatalf("legacy session family = %q, %v", familyID, err)
	}
	service, err := NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(t.Context(), authenticated.Token); err != nil {
		t.Fatalf("legacy authenticated session lost: %v", err)
	}
	if err := service.ValidateLoginCSRF(t.Context(), challenge.Token, challenge.CSRFToken); err != nil {
		t.Fatalf("legacy login challenge lost: %v", err)
	}
}
