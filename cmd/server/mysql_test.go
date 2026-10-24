package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
)

func mysqlTargetDB(t *testing.T) (*sql.DB, database.Connection) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_URL")
	if dsn == "" {
		t.Skip("set TEST_MYSQL_URL to run MySQL integration tests")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid TEST_MYSQL_URL")
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "sqlwise_my_" + hex.EncodeToString(suffix[:])
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec("CREATE DATABASE `" + name + "`"); err != nil {
		t.Fatalf("create MySQL test database: %v", err)
	}
	t.Cleanup(func() {
		if cleanup, err := sql.Open("mysql", dsn); err == nil {
			defer cleanup.Close()
			if _, err := cleanup.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
				t.Errorf("drop MySQL test database: %v", err)
			}
		}
	})
	target := cfg.Clone()
	target.DBName = name
	db, err := sql.Open("mysql", target.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	host, port, _ := net.SplitHostPort(cfg.Addr)
	p, _ := strconv.Atoi(port)
	return db, database.Connection{DBMS: database.MySQL, Host: host, Port: p, Database: name, Username: cfg.User, Password: cfg.Passwd, TLS: database.TLSDisable}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

type lostReplyTargets struct {
	migrationTargets
	lose *atomic.Bool
}

func (l lostReplyTargets) Open(ctx context.Context, id string) (migration.Target, error) {
	t, err := l.migrationTargets.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	return lostReplyTarget{t.(migrationTarget), l.lose}, nil
}

type lostReplyTarget struct {
	migrationTarget
	lose *atomic.Bool
}

func (l lostReplyTarget) Pin(ctx context.Context) (migration.TargetConn, error) {
	conn, err := l.migrationTarget.Pin(ctx)
	if err != nil {
		return nil, err
	}
	return lostReplyConn{conn, l.lose}, nil
}

type lostReplyConn struct {
	migration.TargetConn
	lose *atomic.Bool
}

func (l lostReplyConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	n, err := l.TargetConn.Exec(ctx, sql, args...)
	if err == nil && l.lose.Load() && strings.Contains(sql, "lost_reply") {
		return n, fmt.Errorf("%w: connection reset after the statement ran", migration.ErrCommitUnknown)
	}
	return n, err
}

func TestMySQLMigrationsRunStatementByStatement(t *testing.T) {
	target, conn := mysqlTargetDB(t)
	p := migrationtest.NewPlatform(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := database.NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	store := database.NewStore(p.Pool, cipher)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.Database(t, "my", "MySQL")
	if err := pgx.BeginFunc(t.Context(), p.Pool, func(tx pgx.Tx) error { return store.Insert(t.Context(), tx, "my", conn) }); err != nil {
		t.Fatal(err)
	}
	pools := database.NewPools(store)
	t.Cleanup(pools.Close)
	lose := &atomic.Bool{}
	migrations := migration.NewService(migration.NewStore(p.Pool), p.Access, lostReplyTargets{migrationTargets{resolver: pools}, lose}, time.Now)
	editor := p.User(t, "editor", map[string]string{"my": "edit"})
	viewer := p.User(t, "viewer", map[string]string{"my": "view"})
	execute := func(title, sqlText string, version int64) (migration.Attempt, error) {
		t.Helper()
		d, err := migrations.Create(editor.Ctx, "my", migration.DraftInput{Title: title, SQL: sqlText})
		if err != nil || d.Dialect != migration.MySQL {
			t.Fatalf("Create %s = %+v, %v", title, d, err)
		}
		return migrations.Execute(editor.Ctx, "my", d.ID, migration.ExecuteInput{ExpectedRevision: d.Revision, ExpectedVersion: version, ExpectedSQLHash: d.SQLHash()})
	}

	a, err := execute("Create items", "CREATE TABLE items (id INT PRIMARY KEY);\nINSERT INTO items VALUES (1), (2);\n", 0)
	if err != nil || a.Status != migration.StatusSucceeded || a.AppliedVersion != 1 || a.StatementCount != 2 {
		t.Fatalf("success = %+v, %v", a, err)
	}
	a, err = execute("Bad first", "CREATE TABLE items (id INT);\nCREATE TABLE never (id INT);\n", 1)
	if !errors.Is(err, migration.ErrExecutionFailed) || a.Status != migration.StatusFailed || a.FailedStatement != 1 || !strings.Contains(a.Error, "Nothing was applied") || tableExists(t, target, "never") {
		t.Fatalf("first statement failure = %+v, %v", a, err)
	}
	partial, err := execute("Half done", "CREATE TABLE step_one (id INT);\nCREATE TABLE items (id INT);\nCREATE TABLE step_three (id INT);\n", 1)
	if !errors.Is(err, migration.ErrExecutionFailed) || partial.Status != migration.StatusPartiallyApplied || partial.FailedStatement != 2 ||
		!strings.Contains(partial.Error, "Statements 1–1 were already committed") || !strings.Contains(partial.Error, "MySQL error 1050") {
		t.Fatalf("partial = %+v, %v", partial, err)
	}
	if !tableExists(t, target, "step_one") || tableExists(t, target, "step_three") {
		t.Fatal("partial execution did not keep exactly the committed statement")
	}
	if _, err := execute("Blocked", "CREATE TABLE blocked (id INT);\n", 1); !errors.Is(err, migration.ErrRecoveryRequired) || tableExists(t, target, "blocked") {
		t.Fatalf("execution after partial = %v", err)
	}
	detail, err := migrations.Detail(viewer.Ctx, "my", partial.MigrationID)
	if err != nil || !strings.Contains(detail.Blocked, "partially applied") || detail.BlockedBy == nil || detail.BlockedBy.ID != partial.ID {
		t.Fatalf("blocked detail = %+v, %v", detail, err)
	}
	if _, err := migrations.MarkReconciled(viewer.Ctx, "my", partial.MigrationID, partial.ID); err == nil {
		t.Fatal("viewer reconciled an attempt")
	}
	if _, err := migrations.MarkReconciled(editor.Ctx, "my", partial.MigrationID, partial.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.MarkReconciled(editor.Ctx, "my", partial.MigrationID, partial.ID); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("second reconcile = %v", err)
	}
	a, err = execute("Finish", "CREATE TABLE step_three (id INT);\n", 1)
	if err != nil || a.Status != migration.StatusSucceeded || a.AppliedVersion != 2 {
		t.Fatalf("after reconcile = %+v, %v", a, err)
	}

	lose.Store(true)
	unknown, err := execute("Lost reply", "CREATE TABLE before_loss (id INT);\nCREATE TABLE lost_reply (id INT);\n", 2)
	lose.Store(false)
	if !errors.Is(err, migration.ErrOutcomeUnknown) || unknown.Status != migration.StatusUnknown || !strings.Contains(unknown.Error, "outcome is unknown") {
		t.Fatalf("lost reply = %+v, %v", unknown, err)
	}
	if _, err := execute("Blocked again", "CREATE TABLE blocked_again (id INT);\n", 2); !errors.Is(err, migration.ErrRecoveryRequired) {
		t.Fatalf("execution after unknown = %v", err)
	}
	if _, err := migrations.MarkReconciled(editor.Ctx, "my", unknown.MigrationID, unknown.ID); err != nil {
		t.Fatal(err)
	}

	history, err := migrations.History(viewer.Ctx, "my")
	if err != nil {
		t.Fatal(err)
	}
	var statuses []string
	for _, attempt := range history.Attempts {
		statuses = append(statuses, fmt.Sprintf("v%d:%s", attempt.CandidateVersion, attempt.Status))
	}
	if history.CurrentVersion != 2 || strings.Join(statuses, ",") != "v3:unknown,v2:succeeded,v2:partially_applied,v2:failed,v1:succeeded" {
		t.Fatalf("history version=%d attempts=%v", history.CurrentVersion, statuses)
	}
}

func TestMySQLAccessAndReconcileOverHTTP(t *testing.T) {
	target, conn := mysqlTargetDB(t)
	f := newConnectionFixture(t)
	if _, err := target.Exec("CREATE TABLE items (id INT PRIMARY KEY, label VARCHAR(20) NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec("INSERT INTO items VALUES (1, NULL), (2, 'two')"); err != nil {
		t.Fatal(err)
	}
	id := f.create(t, f.with(map[string]string{"name": "MySQL demo", "dbms": "mysql", "host": conn.Host, "port": strconv.Itoa(conn.Port),
		"database": conn.Database, "username": conn.Username, "password": conn.Password, "tls": "disable"}))
	if body := f.test(t, id); !strings.Contains(body, "Connection succeeded.") {
		t.Fatalf("MySQL test connection = %s", body)
	}
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO group_database_grants(group_id,database_id,permission) VALUES ($1,$3,'view'),($2,$3,'edit')`, f.ViewGroup, f.EditGroup, id); err != nil {
		t.Fatal(err)
	}
	base := "/databases/" + id
	items := base + "/tables/" + database.TableRef{Schema: conn.Database, Name: "items"}.ID()
	explorer := f.do(http.MethodGet, base, &f.Viewer, nil, false, true)
	if explorer.Code != http.StatusOK || !strings.Contains(explorer.Body.String(), "Tables in "+conn.Database) {
		t.Fatalf("viewer explorer = %d %s", explorer.Code, explorer.Body)
	}
	if page := f.do(http.MethodGet, items, &f.Viewer, nil, false, true).Body.String(); !strings.Contains(page, ">NULL<") || !strings.Contains(page, "two") {
		t.Fatalf("viewer table page = %s", page)
	}
	if plan := f.do(http.MethodPost, base+"/explain", &f.Viewer, url.Values{"sql": {"SELECT * FROM items"}, "mode": {"explain"}}, true, true); plan.Code != http.StatusUnprocessableEntity || !strings.Contains(plan.Body.String(), "not available for MySQL") {
		t.Fatalf("MySQL explain = %d %s", plan.Code, plan.Body)
	}
	createForm := url.Values{"title": {"Add notes"}, "sql": {"CREATE TABLE notes (id INT);\nCREATE TABLE items (id INT);\n"}}
	for name, actor := range map[string]*testfixture.Actor{"viewer": &f.Viewer, "no access": &f.NoAccess, "admin": &f.Admin} {
		if response := f.do(http.MethodPost, base+"/migrations", actor, createForm, false, true); response.Code != http.StatusForbidden {
			t.Fatalf("%s create migration = %d", name, response.Code)
		}
		if name != "viewer" {
			if response := f.do(http.MethodGet, base, actor, nil, false, true); response.Code != http.StatusForbidden {
				t.Fatalf("%s explorer = %d", name, response.Code)
			}
		}
	}
	created := f.do(http.MethodPost, base+"/migrations", &f.Editor, createForm, false, true)
	location := created.Header().Get("Location")
	if created.Code != http.StatusSeeOther || !strings.HasPrefix(location, base+"/migrations/") {
		t.Fatalf("editor create = %d %s", created.Code, location)
	}
	migrationID := strings.TrimPrefix(location, base+"/migrations/")
	sum := sha256.Sum256([]byte(createForm.Get("sql")))
	confirm := url.Values{"expected_revision": {"1"}, "expected_version": {"0"}, "expected_sql_hash": {hex.EncodeToString(sum[:])}}
	if response := f.do(http.MethodPost, location+"/execute", &f.Viewer, confirm, false, true); response.Code != http.StatusForbidden {
		t.Fatalf("viewer execute = %d", response.Code)
	}
	executed := f.do(http.MethodPost, location+"/execute", &f.Editor, confirm, false, true)
	if executed.Code != http.StatusUnprocessableEntity || !strings.Contains(executed.Body.String(), "were already committed") {
		t.Fatalf("partial execute = %d %s", executed.Code, executed.Body)
	}
	if !tableExists(t, target, "notes") {
		t.Fatal("committed statement missing on target")
	}
	detail := f.do(http.MethodGet, location, &f.Editor, nil, false, true).Body.String()
	var attemptID string
	if err := f.Pool.QueryRow(t.Context(), `SELECT id FROM migration_attempts WHERE migration_id=$1`, migrationID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	reconcile := location + "/attempts/" + attemptID + "/reconcile"
	if !strings.Contains(detail, "partially applied") || !strings.Contains(detail, `action="`+reconcile+`"`) {
		t.Fatalf("detail does not offer reconciliation: %s", detail)
	}
	if viewerDetail := f.do(http.MethodGet, location, &f.Viewer, nil, false, true).Body.String(); strings.Contains(viewerDetail, "/reconcile") {
		t.Fatal("viewer was offered reconciliation")
	}
	if response := f.do(http.MethodPost, reconcile, &f.Viewer, nil, false, true); response.Code != http.StatusForbidden {
		t.Fatalf("viewer reconcile = %d", response.Code)
	}
	if response := f.do(http.MethodPost, reconcile, &f.Editor, nil, false, false); response.Code != http.StatusForbidden {
		t.Fatalf("reconcile without CSRF = %d", response.Code)
	}
	if response := f.do(http.MethodPost, reconcile, &f.Editor, nil, false, true); response.Code != http.StatusSeeOther {
		t.Fatalf("editor reconcile = %d %s", response.Code, response.Body)
	}
	if after := f.do(http.MethodGet, location, &f.Editor, nil, false, true).Body.String(); strings.Contains(after, "/reconcile") || strings.Contains(after, "Executions are blocked") {
		t.Fatal("block was not lifted")
	}
}
