package auth

import (
	"errors"
	"testing"

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
