package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
	"sqlwise/internal/web/views"
)

type connectionFixture struct {
	*testfixture.Fixture
	router *gin.Engine
	target *pgxpool.Pool
	form   url.Values
}

func newConnectionFixture(t *testing.T) *connectionFixture {
	t.Helper()
	f := testfixture.New(t)
	if err := migration.NewStore(f.Pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := database.NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	store := database.NewStore(f.Pool, cipher)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	pools := database.NewPools(store)
	t.Cleanup(pools.Close)
	assets := views.Assets{Dev: true}
	router := newRouter(assets, f.Auth, f.Access)
	adapter := migrationTargets{resolver: pools}
	registerDatabaseRoutes(router, f.Auth, f.Access, f.Pool, assets, databaseTargets{targets: adapter, snapshot: adapter, store: store, pools: pools})
	target := migrationtest.TargetDatabase(t)
	cc := target.Config().ConnConfig
	form := url.Values{"name": {"Demo target"}, "dbms": {"postgresql"}, "host": {cc.Host}, "port": {strconv.Itoa(int(cc.Port))}, "database": {cc.Database}, "username": {cc.User}, "password": {cc.Password}, "tls": {"disable"}}
	return &connectionFixture{Fixture: f, router: router, target: target, form: form}
}

func (f *connectionFixture) do(method, path string, actor *testfixture.Actor, form url.Values, hx, csrf bool) *httptest.ResponseRecorder {
	body := url.Values{}
	for k, v := range form {
		body[k] = v
	}
	if actor != nil && csrf && method != http.MethodGet {
		body.Set("csrf_token", actor.CSRFToken)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body.Encode()))
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	if actor != nil {
		req.AddCookie(&http.Cookie{Name: "sqlwise_session", Value: actor.SessionToken})
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func (f *connectionFixture) with(changes map[string]string) url.Values {
	form := url.Values{}
	for k, v := range f.form {
		form[k] = append([]string(nil), v...)
	}
	for k, v := range changes {
		form.Set(k, v)
	}
	return form
}

func (f *connectionFixture) registrations(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.Pool.QueryRow(t.Context(), `SELECT count(*) FROM database_registrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *connectionFixture) create(t *testing.T, form url.Values) string {
	t.Helper()
	response := f.do(http.MethodPost, "/admin/databases", &f.Admin, form, false, true)
	location := response.Header().Get("Location")
	if response.Code != http.StatusSeeOther || !strings.HasPrefix(location, "/admin/databases/") || !strings.HasSuffix(location, "?notice=saved") {
		t.Fatalf("create status=%d location=%q body=%s", response.Code, location, response.Body)
	}
	return strings.TrimSuffix(strings.TrimPrefix(location, "/admin/databases/"), "?notice=saved")
}

func (f *connectionFixture) test(t *testing.T, id string) string {
	t.Helper()
	response := f.do(http.MethodPost, "/admin/databases/"+id+"/test", &f.Admin, nil, true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", response.Code, response.Body)
	}
	return response.Body.String()
}

func TestConnectionAdminLifecycle(t *testing.T) {
	f := newConnectionFixture(t)
	password := f.form.Get("password")
	if response := f.do(http.MethodGet, "/admin", &f.Admin, nil, false, true); !strings.Contains(response.Body.String(), `href="/admin/databases"`) {
		t.Fatal("admin dashboard does not link connections")
	}
	if response := f.do(http.MethodGet, "/admin/databases", &f.Admin, nil, false, true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Register connection") {
		t.Fatalf("list status=%d", response.Code)
	}
	id := f.create(t, f.form)
	detail := f.do(http.MethodGet, "/admin/databases/"+id+"?notice=saved", &f.Admin, nil, false, true)
	page := detail.Body.String()
	if detail.Code != http.StatusOK || !strings.Contains(page, "Changes saved successfully.") || !strings.Contains(page, "Demo target") || !strings.Contains(page, "GRANT SELECT, INSERT ON sqlwise_internal.migration_receipts") {
		t.Fatalf("detail status=%d body=%s", detail.Code, page)
	}
	list := f.do(http.MethodGet, "/admin/databases", &f.Admin, nil, false, true).Body.String()
	if strings.Contains(page, password) || strings.Contains(list, password) || !strings.Contains(list, "Demo target") {
		t.Fatal("password rendered or connection missing from list")
	}
	if body := f.test(t, id); !strings.Contains(body, "Connection succeeded.") {
		t.Fatalf("test = %s", body)
	}

	if response := f.do(http.MethodPost, "/admin/databases/"+id, &f.Admin, f.with(map[string]string{"password": "wrong target password"}), true, true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Changes saved successfully.") {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body)
	}
	if body := f.test(t, id); !strings.Contains(body, "username or password") || strings.Contains(body, "wrong target password") {
		t.Fatalf("wrong password test = %s", body)
	}
	f.do(http.MethodPost, "/admin/databases/"+id, &f.Admin, f.form, true, true)
	if response := f.do(http.MethodPost, "/admin/databases/"+id, &f.Admin, f.with(map[string]string{"password": "", "name": "Renamed target"}), true, true); response.Code != http.StatusOK {
		t.Fatalf("blank password update status=%d", response.Code)
	}
	if body := f.test(t, id); !strings.Contains(body, "Connection succeeded.") {
		t.Fatalf("blank password kept = %s", body)
	}

	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO migration_drafts(id,database_id,title,sql_text,dialect,revision,created_by,updated_by,created_at,updated_at) VALUES('draft',$1,'History','SELECT 1','postgresql',1,$2,$2,now(),now())`, id, f.Admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO migration_attempts(id,database_id,migration_id,draft_revision,candidate_version,sql_text,sql_sha256,dialect,statement_count,executor_id,status,started_at,finished_at)
		VALUES('attempt',$1,'draft',1,1,'SELECT 1',$3,'postgresql',1,$2,'failed',now(),now())`, id, f.Admin.UserID, migration.HashSQL("SELECT 1")); err != nil {
		t.Fatal(err)
	}
	if response := f.do(http.MethodPost, "/admin/databases/"+id, &f.Admin, f.with(map[string]string{"database": "another_database"}), true, true); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "migration history") {
		t.Fatalf("physical change status=%d body=%s", response.Code, response.Body)
	}

	if response := f.do(http.MethodPost, "/admin/databases/"+id+"/active", &f.Admin, url.Values{"active": {"false"}}, false, true); response.Code != http.StatusSeeOther {
		t.Fatalf("disable status=%d", response.Code)
	}
	var active bool
	if err := f.Pool.QueryRow(t.Context(), `SELECT active FROM database_registrations WHERE id=$1`, id).Scan(&active); err != nil || active {
		t.Fatalf("registration active=%v err=%v", active, err)
	}
	if body := f.test(t, id); strings.Contains(body, "Connection succeeded.") || !strings.Contains(body, "not found") {
		t.Fatalf("disabled test = %s", body)
	}
	if response := f.do(http.MethodPost, "/admin/databases/"+id+"/active", &f.Admin, url.Values{"active": {"maybe"}}, true, true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid active status=%d", response.Code)
	}
	f.do(http.MethodPost, "/admin/databases/"+id+"/active", &f.Admin, url.Values{"active": {"true"}}, true, true)
	if body := f.test(t, id); !strings.Contains(body, "Connection succeeded.") {
		t.Fatalf("re-enabled test = %s", body)
	}
}

func TestConnectionAdminRejectsInvalidInput(t *testing.T) {
	f := newConnectionFixture(t)
	before := f.registrations(t)
	for name, tc := range map[string]struct {
		changes map[string]string
		message string
	}{
		"port text":  {map[string]string{"port": "abc"}, "Port must be from 1 to 65535."},
		"port range": {map[string]string{"port": "70000"}, "Port must be from 1 to 65535."},
		"dbms":       {map[string]string{"dbms": "oracle"}, "Database type must be PostgreSQL or MySQL."},
		"tls":        {map[string]string{"tls": "prefer"}, "TLS mode must be"},
		"host":       {map[string]string{"host": " "}, "Host is required."},
		"name":       {map[string]string{"name": " "}, "Display name must be 1 to 128 characters."},
	} {
		response := f.do(http.MethodPost, "/admin/databases", &f.Admin, f.with(tc.changes), true, true)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, tc.message) || strings.Contains(body, f.form.Get("password")) {
			t.Fatalf("%s status=%d body=%s", name, response.Code, body)
		}
	}
	if f.registrations(t) != before {
		t.Fatal("invalid input created a registration")
	}
	unreachable := f.create(t, f.with(map[string]string{"name": "Unreachable", "host": "127.0.0.1", "port": "1"}))
	if body := f.test(t, unreachable); !strings.Contains(body, "unavailable") || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("unreachable test = %s", body)
	}
	missing := f.with(map[string]string{"name": "Missing database", "database": "sqlwise_missing_database"})
	if body := f.test(t, f.create(t, missing)); !strings.Contains(body, "does not exist") {
		t.Fatalf("missing database test = %s", body)
	}
	if response := f.do(http.MethodGet, "/admin/databases/does-not-exist", &f.Admin, nil, false, true); response.Code != http.StatusNotFound {
		t.Fatalf("missing detail status=%d", response.Code)
	}
}

func TestConnectionAdminRequiresAdminAndCSRF(t *testing.T) {
	f := newConnectionFixture(t)
	id := f.create(t, f.form)
	before := f.registrations(t)
	for name, response := range map[string]*httptest.ResponseRecorder{
		"viewer list":    f.do(http.MethodGet, "/admin/databases", &f.Viewer, nil, false, true),
		"viewer detail":  f.do(http.MethodGet, "/admin/databases/"+id, &f.Viewer, nil, false, true),
		"viewer create":  f.do(http.MethodPost, "/admin/databases", &f.Viewer, f.form, false, true),
		"viewer test":    f.do(http.MethodPost, "/admin/databases/"+id+"/test", &f.Viewer, nil, true, true),
		"viewer disable": f.do(http.MethodPost, "/admin/databases/"+id+"/active", &f.Viewer, url.Values{"active": {"false"}}, false, true),
		"admin no csrf":  f.do(http.MethodPost, "/admin/databases", &f.Admin, f.form, false, false),
		"test no csrf":   f.do(http.MethodPost, "/admin/databases/"+id+"/test", &f.Admin, nil, true, false),
	} {
		if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "Connection succeeded.") {
			t.Fatalf("%s status=%d", name, response.Code)
		}
	}
	if response := f.do(http.MethodGet, "/admin/databases", nil, nil, false, true); response.Code == http.StatusOK {
		t.Fatal("anonymous request listed connections")
	}
	var active bool
	if err := f.Pool.QueryRow(t.Context(), `SELECT active FROM database_registrations WHERE id=$1`, id).Scan(&active); err != nil || !active || f.registrations(t) != before {
		t.Fatalf("denied requests changed state: active=%v err=%v", active, err)
	}
}
