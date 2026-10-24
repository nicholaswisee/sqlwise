package main

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
)

func TestSchemaOnlyRejectsCredentialFlagsBeforeDatabaseAccess(t *testing.T) {
	binary := buildAdminCLI(t)
	for _, args := range [][]string{{"-schema-only", "-username", "admin"}, {"-schema-only", "-username="}, {"-schema-only", "-password-stdin"}, {"-schema-only", "-password-stdin=false"}, {"-schema-only", "unexpected"}} {
		out, err := runAdminCLI(t, binary, "", "", args...)
		if err == nil || !strings.Contains(out, "cannot be combined") || strings.Contains(out, "DATABASE_URL is required") {
			t.Fatalf("args %v: err=%v output=%s", args, err, out)
		}
	}
}

func TestSchemaOnlyInitializesEmptyDatabaseWithoutReadingPassword(t *testing.T) {
	pool := testdb.Open(t)
	output, err := runAdminCLI(t, buildAdminCLI(t), isolatedDatabaseURL(t, pool), "", "-schema-only")
	if err != nil || !strings.Contains(output, "schemas initialized") {
		t.Fatalf("schema-only: err=%v output=%s", err, output)
	}
	for _, table := range []string{"users", "auth_sessions", "database_registrations", "groups", "group_memberships", "group_database_grants"} {
		var count int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s count = %d, %v; want 0", table, count, err)
		}
	}
}

func TestSchemaOnlyPreservesAuthenticationAndAccessData(t *testing.T) {
	pool := testdb.Open(t)
	if err := auth.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal("apply auth schema:", err)
	}
	if err := access.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal("apply access schema:", err)
	}
	credentials, err := auth.ParseCredentials("preserved.admin", "original schema-only password")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.NewStore(pool).BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal("bootstrap fixture admin:", err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO auth_sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at)
		SELECT decode(repeat('01', 32), 'hex'), id, repeat('a', 43), now(), now(), now() + interval '1 hour' FROM users WHERE username = 'preserved.admin';
		INSERT INTO database_registrations (id, name, active) VALUES ('db1', 'Analytics', true);
		INSERT INTO groups (id, name) VALUES ('g1', 'Operators');
		INSERT INTO group_memberships (user_id, group_id) SELECT id, 'g1' FROM users WHERE username = 'preserved.admin';
		INSERT INTO group_database_grants (group_id, database_id, permission) VALUES ('g1', 'db1', 'edit');`); err != nil {
		t.Fatal("seed preserved rows:", err)
	}
	var beforeID, beforeHash string
	var beforeAdmin bool
	if err := pool.QueryRow(t.Context(), `SELECT id, password_hash, is_platform_admin FROM users WHERE username = 'preserved.admin'`).Scan(&beforeID, &beforeHash, &beforeAdmin); err != nil {
		t.Fatal("read fixture admin:", err)
	}
	binary := buildAdminCLI(t)
	for range 2 {
		if output, err := runAdminCLI(t, binary, isolatedDatabaseURL(t, pool), "", "-schema-only"); err != nil || !strings.Contains(output, "schemas initialized") {
			t.Fatalf("schema-only: err=%v output=%s", err, output)
		}
	}
	var afterID, afterHash, databaseName, groupName, permission string
	var afterAdmin bool
	var sessionCount int
	if err := pool.QueryRow(t.Context(), `SELECT id, password_hash, is_platform_admin FROM users WHERE username = 'preserved.admin'`).Scan(&afterID, &afterHash, &afterAdmin); err != nil {
		t.Fatal("read preserved admin:", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE token_hash = decode(repeat('01', 32), 'hex')`).Scan(&sessionCount); err != nil {
		t.Fatal("count preserved session:", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT d.name, g.name, x.permission
		FROM group_memberships m JOIN groups g ON g.id = m.group_id
		JOIN group_database_grants x ON x.group_id = g.id
		JOIN database_registrations d ON d.id = x.database_id
		WHERE m.user_id = $1`, beforeID).Scan(&databaseName, &groupName, &permission); err != nil {
		t.Fatal("read preserved access data:", err)
	}
	if afterID != beforeID || afterHash != beforeHash || afterAdmin != beforeAdmin || !afterAdmin || sessionCount != 1 || databaseName != "Analytics" || groupName != "Operators" || permission != "edit" {
		t.Fatalf("schema-only changed existing rows: admin=(%q,%t) session_count=%d database=%q group=%q grant=%q", afterID, afterHash == beforeHash, sessionCount, databaseName, groupName, permission)
	}
}

func TestBootstrapCLIInitializesSchemasAndDoesNotResetExistingAdmin(t *testing.T) {
	pool := testdb.Open(t)
	binary := buildAdminCLI(t)
	databaseURL := isolatedDatabaseURL(t, pool)
	const firstPassword = "cli bootstrap password"
	if output, err := runAdminCLI(t, binary, databaseURL, firstPassword+"\n", "-username", "cli.admin", "-password-stdin"); err != nil || !strings.Contains(output, "administrator created") {
		t.Fatalf("bootstrap: err=%v output=%s", err, output)
	}
	var beforeID, beforeHash string
	if err := pool.QueryRow(t.Context(), `SELECT id, password_hash FROM users WHERE username = 'cli.admin' AND is_platform_admin`).Scan(&beforeID, &beforeHash); err != nil {
		t.Fatal("query bootstrapped admin:", err)
	}
	valid, err := auth.VerifyPassword(beforeHash, firstPassword)
	if err != nil || !valid {
		t.Fatalf("stored password hash does not verify: valid=%v err=%v", valid, err)
	}
	for _, table := range []string{"users", "auth_sessions", "database_registrations", "groups", "group_memberships", "group_database_grants"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), "SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL", table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s exists=%v err=%v", table, exists, err)
		}
	}
	if output, err := runAdminCLI(t, binary, databaseURL, "replacement bootstrap password\n", "-username", "cli.admin", "-password-stdin"); err == nil || !strings.Contains(output, "administrator already exists") {
		t.Fatalf("second bootstrap: err=%v output=%s; want already-exists", err, output)
	}
	var afterID, afterHash string
	if err := pool.QueryRow(t.Context(), `SELECT id, password_hash FROM users WHERE username = 'cli.admin' AND is_platform_admin`).Scan(&afterID, &afterHash); err != nil {
		t.Fatal("query admin after repeated bootstrap:", err)
	}
	if afterID != beforeID || afterHash != beforeHash {
		t.Fatal("second bootstrap changed existing administrator credentials")
	}
}

func buildAdminCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build admin CLI: %v\n%s", err, output)
	}
	return path
}

func isolatedDatabaseURL(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	parsed, err := url.Parse(pool.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal("parse test database URL")
	}
	query := parsed.Query()
	query.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func runAdminCLI(t *testing.T, binary, databaseURL, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "DATABASE_URL=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "DATABASE_URL="+databaseURL)
	cmd.Stdin = strings.NewReader(stdin)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	return string(output), err
}
