package access

import (
	"errors"
	"testing"
	"time"

	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestApplySchemaEnforcesGroupAndGrantConstraints(t *testing.T) {
	pool := testdb.Open(t)
	if err := auth.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal("apply auth schema:", err)
	}
	if err := NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal("apply access schema:", err)
	}

	_, err := pool.Exec(t.Context(), `INSERT INTO users (id, username, password_hash) VALUES ('u1', 'user.one', 'hash')`)
	if err != nil {
		t.Fatal("insert user:", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO database_registrations (id, name) VALUES ('db1', 'Analytics')`)
	if err != nil {
		t.Fatal("insert registration:", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO groups (id, name) VALUES ('g1', 'Operators')`)
	if err != nil {
		t.Fatal("insert group:", err)
	}

	var groupActive, registrationActive bool
	if err := pool.QueryRow(t.Context(), `SELECT active FROM groups WHERE id = 'g1'`).Scan(&groupActive); err != nil || !groupActive {
		t.Fatalf("group active default = %v, %v", groupActive, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT active FROM database_registrations WHERE id = 'db1'`).Scan(&registrationActive); err != nil || registrationActive {
		t.Fatalf("registration active default = %v, %v", registrationActive, err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO groups (id, name) VALUES ('g2', 'Operators')`)
	requireSQLState(t, err, "23505")

	_, err = pool.Exec(t.Context(), `INSERT INTO group_memberships (user_id, group_id) VALUES ('u-missing', 'g1')`)
	requireSQLState(t, err, "23503")
	_, err = pool.Exec(t.Context(), `INSERT INTO group_memberships (user_id, group_id) VALUES ('u1', 'g-missing')`)
	requireSQLState(t, err, "23503")
	_, err = pool.Exec(t.Context(), `INSERT INTO group_memberships (user_id, group_id) VALUES ('u1', 'g1')`)
	if err != nil {
		t.Fatal("insert membership:", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO group_memberships (user_id, group_id) VALUES ('u1', 'g1')`)
	requireSQLState(t, err, "23505")

	_, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g-missing', 'db1', 'view')`)
	requireSQLState(t, err, "23503")
	_, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g1', 'db-missing', 'view')`)
	requireSQLState(t, err, "23503")
	_, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g1', 'db1', 'admin')`)
	requireSQLState(t, err, "23514")
	_, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g1', 'db1', 'view')`)
	if err != nil {
		t.Fatal("insert grant:", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g1', 'db1', 'edit')`)
	requireSQLState(t, err, "23505")

	_, err = pool.Exec(t.Context(), `DELETE FROM database_registrations WHERE id = 'db1'`)
	requireSQLState(t, err, "23503")
}

func TestApplySchemaIsIdempotentAndPreservesData(t *testing.T) {
	pool := testdb.Open(t)
	authStore := auth.NewStore(pool)
	if err := authStore.ApplySchema(t.Context()); err != nil {
		t.Fatal("apply auth schema:", err)
	}
	if err := authStore.BootstrapAdmin(t.Context(), mustCredentials(t)); err != nil {
		t.Fatal("bootstrap admin:", err)
	}
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal("apply initial access schema:", err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO database_registrations (id, name, active) VALUES ('db1', 'Analytics', true);
		INSERT INTO groups (id, name) VALUES ('g1', 'Operators');
		INSERT INTO group_memberships (user_id, group_id) SELECT id, 'g1' FROM users WHERE username = 'schema.admin';
		INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g1', 'db1', 'edit');`); err != nil {
		t.Fatal("seed access data:", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, err := pool.Exec(t.Context(), `INSERT INTO auth_sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at)
		SELECT decode(repeat('01', 32), 'hex'), id, repeat('a', 43), $1, $1, $2 FROM users WHERE username = 'schema.admin'`, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal("insert session:", err)
	}

	for range 2 {
		if err := store.ApplySchema(t.Context()); err != nil {
			t.Fatal("reapply access schema:", err)
		}
	}
	for table, want := range map[string]int{"users": 1, "auth_sessions": 1, "database_registrations": 1, "groups": 1, "group_memberships": 1, "group_database_grants": 1} {
		var count int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Errorf("%s count = %d, %v; want %d", table, count, err, want)
		}
	}
	var userID, databaseName, groupName, permission string
	if err := pool.QueryRow(t.Context(), `SELECT u.id, d.name, g.name, x.permission
		FROM users u JOIN group_memberships m ON m.user_id = u.id
		JOIN groups g ON g.id = m.group_id
		JOIN group_database_grants x ON x.group_id = g.id
		JOIN database_registrations d ON d.id = x.database_id
		WHERE u.username = 'schema.admin'`).Scan(&userID, &databaseName, &groupName, &permission); err != nil {
		t.Fatal("read preserved access data:", err)
	}
	if userID == "" || databaseName != "Analytics" || groupName != "Operators" || permission != "edit" {
		t.Fatalf("access data changed: user=%q database=%q group=%q permission=%q", userID, databaseName, groupName, permission)
	}
	var sessionCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE token_hash = decode(repeat('01', 32), 'hex')`).Scan(&sessionCount); err != nil || sessionCount != 1 {
		t.Fatalf("auth session count = %d, %v; want 1", sessionCount, err)
	}
}

func mustCredentials(t *testing.T) auth.Credentials {
	t.Helper()
	credentials, err := auth.ParseCredentials("schema.admin", "schema test password")
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func requireSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != want {
		t.Fatalf("error = %v; want PostgreSQL SQLSTATE %s", err, want)
	}
}
