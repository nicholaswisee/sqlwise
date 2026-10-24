package database_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"sqlwise/internal/access"
	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"

	"github.com/jackc/pgx/v5"
)

func newKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func newCipher(t *testing.T) *database.Cipher {
	t.Helper()
	c, err := database.NewCipher(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCipherSealsWithFreshNonceAndRejectsWrongKey(t *testing.T) {
	c := newCipher(t)
	first, err := c.Seal("target secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Seal("target secret")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, []byte("target secret")) {
		t.Fatal("sealed password is deterministic or readable")
	}
	if got, err := c.Open(first); err != nil || got != "target secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := newCipher(t).Open(first); err == nil {
		t.Fatal("another key opened the credential")
	}
	if _, err := c.Open(first[:4]); err == nil {
		t.Fatal("short ciphertext opened")
	}
	for _, key := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := database.NewCipher(key); err == nil {
			t.Fatalf("NewCipher(%q) accepted", key)
		}
	}
}

func validConnection() database.Connection {
	return database.Connection{DBMS: database.PostgreSQL, Host: "db.internal", Port: 5432, Database: "demo", Username: "sqlwise_target", Password: "first password", TLS: database.TLSRequire}
}

func TestConnectionValidation(t *testing.T) {
	if err := validConnection().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*database.Connection){
		"dbms":     func(c *database.Connection) { c.DBMS = "oracle" },
		"host":     func(c *database.Connection) { c.Host = "  " },
		"port-low": func(c *database.Connection) { c.Port = 0 },
		"port-hi":  func(c *database.Connection) { c.Port = 65536 },
		"database": func(c *database.Connection) { c.Database = "" },
		"username": func(c *database.Connection) { c.Username = "" },
		"tls":      func(c *database.Connection) { c.TLS = "prefer" },
		"sandbox":  func(c *database.Connection) { c.DBMS, c.Sandbox = database.MySQL, true },
	} {
		c := validConnection()
		mutate(&c)
		err := c.Validate()
		if err == nil || database.Message(err) == "" {
			t.Fatalf("%s accepted: %v", name, err)
		}
		if name == "dbms" && !errors.Is(err, database.ErrUnsupportedDialect) || name != "dbms" && !errors.Is(err, database.ErrValidation) {
			t.Fatalf("%s error kind = %v", name, err)
		}
	}
}

type storeFixture struct {
	*testfixture.Fixture
	store *database.Store
	admin context.Context
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	f := testfixture.New(t)
	if err := migration.NewStore(f.Pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := database.NewStore(f.Pool, newCipher(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &storeFixture{Fixture: f, store: store, admin: f.Admin.Context}
}

func (f *storeFixture) register(t *testing.T, name string, c database.Connection) access.DatabaseRegistration {
	t.Helper()
	registration, err := f.Access.RegisterDatabase(f.admin, name, func(tx pgx.Tx, databaseID string) error {
		return f.store.Insert(t.Context(), tx, databaseID, c)
	})
	if err != nil {
		t.Fatal(err)
	}
	return registration
}

func (f *storeFixture) update(t *testing.T, databaseID string, c database.Connection) error {
	t.Helper()
	return f.Access.UpdateDatabase(f.admin, databaseID, "Renamed", func(tx pgx.Tx) error {
		return f.store.Update(t.Context(), tx, databaseID, c)
	})
}

func TestStoreRegistersEncryptedConnection(t *testing.T) {
	f := newStoreFixture(t)
	registration := f.register(t, "Demo", validConnection())
	got, err := f.store.Get(t.Context(), registration.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := validConnection()
	want.DatabaseID, want.Name, want.Active = registration.ID, "Demo", true
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
	if got != want || got.CreatedAt.IsZero() {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
	var sealed []byte
	if err := f.Pool.QueryRow(t.Context(), `SELECT password_sealed FROM database_connections WHERE database_id=$1`, registration.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("first password")) {
		t.Fatal("password stored in plaintext")
	}
	list, err := f.store.List(t.Context())
	if err != nil || len(list) != 1 || list[0].DatabaseID != registration.ID || list[0].Password != "" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if _, err := f.store.Get(t.Context(), "missing"); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("missing Get: %v", err)
	}
	if err := f.Access.SetDatabaseActive(f.admin, registration.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Get(t.Context(), registration.ID); err != nil || got.Active {
		t.Fatalf("disabled Get = %+v, %v", got, err)
	}
}

func TestStoreInvalidConnectionRollsBackRegistration(t *testing.T) {
	f := newStoreFixture(t)
	invalid := validConnection()
	invalid.Port = 0
	_, err := f.Access.RegisterDatabase(f.admin, "Rejected", func(tx pgx.Tx, databaseID string) error {
		return f.store.Insert(t.Context(), tx, databaseID, invalid)
	})
	if !errors.Is(err, database.ErrValidation) {
		t.Fatalf("error = %v", err)
	}
	var registrations, connections int
	if err := f.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM database_registrations WHERE name='Rejected'),(SELECT count(*) FROM database_connections)`).Scan(&registrations, &connections); err != nil {
		t.Fatal(err)
	}
	if registrations != 0 || connections != 0 {
		t.Fatalf("rows left: registrations=%d connections=%d", registrations, connections)
	}
}

func TestStoreUpdateKeepsBlankPassword(t *testing.T) {
	f := newStoreFixture(t)
	registration := f.register(t, "Demo", validConnection())
	changed := validConnection()
	changed.Password, changed.Username, changed.TLS, changed.Host = "", "rotated_user", database.TLSVerifyFull, "replica.internal"
	if err := f.update(t, registration.ID, changed); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Get(t.Context(), registration.ID)
	if err != nil || got.Password != "first password" || got.Username != "rotated_user" || got.Host != "replica.internal" || got.TLS != database.TLSVerifyFull || got.Name != "Renamed" {
		t.Fatalf("after blank password = %+v, %v", got, err)
	}
	changed.Password = "second password"
	if err := f.update(t, registration.ID, changed); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Get(t.Context(), registration.ID); err != nil || got.Password != "second password" {
		t.Fatalf("after new password = %+v, %v", got, err)
	}
	if err := f.update(t, testfixture.PrimaryTargetID, validConnection()); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("registration without connection: %v", err)
	}
}

func TestStoreRejectsPhysicalChangeAfterHistory(t *testing.T) {
	f := newStoreFixture(t)
	registration := f.register(t, "Demo", validConnection())
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO migration_drafts(id,database_id,title,sql_text,dialect,revision,created_by,updated_by,created_at,updated_at)
		VALUES('draft',$1,'History','SELECT 1','postgresql',1,$2,$2,now(),now())`, registration.ID, f.Admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO migration_attempts(id,database_id,migration_id,draft_revision,candidate_version,sql_text,sql_sha256,dialect,statement_count,executor_id,status,started_at,finished_at)
		VALUES('attempt',$1,'draft',1,1,'SELECT 1',$3,'postgresql',1,$2,'failed',now(),now())`, registration.ID, f.Admin.UserID, migration.HashSQL("SELECT 1")); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Get(t.Context(), registration.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*database.Connection){
		"dbms":     func(c *database.Connection) { c.DBMS = database.MySQL },
		"host":     func(c *database.Connection) { c.Host = "other.internal" },
		"port":     func(c *database.Connection) { c.Port = 5433 },
		"database": func(c *database.Connection) { c.Database = "other" },
	} {
		c := validConnection()
		mutate(&c)
		if err := f.update(t, registration.ID, c); !errors.Is(err, database.ErrConflict) {
			t.Fatalf("%s change: %v", name, err)
		}
	}
	if after, err := f.store.Get(t.Context(), registration.ID); err != nil || after != before {
		t.Fatalf("rejected change altered row: %+v, %v", after, err)
	}
	credentials := validConnection()
	credentials.Username, credentials.Password = "rotated_user", "rotated password"
	if err := f.update(t, registration.ID, credentials); err != nil {
		t.Fatalf("credential rotation with history: %v", err)
	}
}
