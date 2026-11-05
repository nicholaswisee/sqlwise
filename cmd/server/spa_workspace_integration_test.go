package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
)

type spaWorkspaceFixture struct {
	*testfixture.Fixture
	router *gin.Engine
}

func newSPAWorkspaceFixture(t *testing.T) *spaWorkspaceFixture {
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
	router := newAPIRouter(f.Auth, f.Access)
	adapter := migrationTargets{resolver: pools}
	registerDatabaseAPIRoutes(router, f.Auth, f.Access, f.Pool, databaseTargets{targets: adapter, snapshot: adapter, store: store, pools: pools})
	return &spaWorkspaceFixture{Fixture: f, router: router}
}

func (f *spaWorkspaceFixture) request(t *testing.T, method, path string, actor *testfixture.Actor, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if actor != nil {
		req.AddCookie(&http.Cookie{Name: "sqlwise_session", Value: actor.SessionToken})
		req.Header.Set("X-CSRF-Token", actor.CSRFToken)
	}
	r := httptest.NewRecorder()
	f.router.ServeHTTP(r, req)
	if r.Code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, r.Code, want, r.Body)
	}
	if !strings.HasPrefix(r.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("non-JSON response: %s", r.Body)
	}
	return r
}

func spaData[T any](t *testing.T, r *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func (f *spaWorkspaceFixture) register(t *testing.T, conn database.Connection) string {
	t.Helper()
	r := f.request(t, "POST", "/api/v1/admin/databases", &f.Admin, map[string]any{
		"name": "Isolated API target", "dbms": conn.DBMS, "host": conn.Host, "port": strconv.Itoa(conn.Port),
		"database": conn.Database, "username": conn.Username, "password": conn.Password, "tls": conn.TLS,
	}, http.StatusCreated)
	d := spaData[struct {
		Form struct {
			ID string `json:"id"`
		} `json:"form"`
	}](t, r)
	if d.Form.ID == "" || r.Header().Get("Location") != "/api/v1/admin/databases/"+d.Form.ID {
		t.Fatalf("registration missing ID/Location: %s", r.Body)
	}
	if strings.Contains(r.Body.String(), `"password"`) {
		t.Fatal("registration leaked password field")
	}
	// Grant fixture groups directly so their existing session proofs remain fresh.
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO group_database_grants(group_id,database_id,permission) VALUES ($1,$3,'view'),($2,$3,'edit')`, f.ViewGroup, f.EditGroup, d.Form.ID); err != nil {
		t.Fatal(err)
	}
	return d.Form.ID
}

type spaDraft struct {
	ID       string `json:"id"`
	SQL      string `json:"sql"`
	SQLHash  string `json:"sql_hash"`
	Revision int64  `json:"revision"`
	Dialect  string `json:"dialect"`
}

type spaAttempt struct {
	ID              string `json:"id"`
	MigrationID     string `json:"migration_id"`
	Status          string `json:"status"`
	AppliedVersion  int64  `json:"applied_version"`
	FailedStatement int    `json:"failed_statement"`
	SQL             string `json:"sql"`
}

func spaConfirmation(d spaDraft, version int64) map[string]any {
	return map[string]any{"expected_revision": d.Revision, "expected_version": version, "expected_sql_hash": d.SQLHash}
}

func TestSPAWorkspacePostgresHTTP(t *testing.T) {
	f := newSPAWorkspaceFixture(t)
	target := migrationtest.TargetDatabase(t)
	cc := target.Config().ConnConfig
	id := f.register(t, database.Connection{DBMS: database.PostgreSQL, Host: cc.Host, Port: int(cc.Port), Database: cc.Database, Username: cc.User, Password: cc.Password, TLS: database.TLSDisable})
	base := "/api/v1/databases/" + id
	if _, err := target.Exec(t.Context(), `CREATE TABLE items (id integer PRIMARY KEY, label text, data bytea);
		INSERT INTO items VALUES (1, NULL, NULL), (2, 'two', '\xdeadbeef'), (3, repeat('x',5000), NULL), (4, 'four', NULL)`); err != nil {
		t.Fatal(err)
	}
	t.Run("explorer and permissions", func(t *testing.T) {
		list := spaData[struct {
			Databases []struct {
				ID string `json:"id"`
			} `json:"databases"`
		}](t, f.request(t, "GET", "/api/v1/databases", &f.Viewer, nil, 200))
		found := false
		for _, db := range list.Databases {
			found = found || db.ID == id
		}
		if !found {
			t.Fatal("registered database not listed")
		}
		for _, actor := range []*testfixture.Actor{&f.NoAccess, &f.Admin} {
			for _, path := range []string{base, base + "/tables/" + database.TableRef{Schema: "public", Name: "items"}.ID(), base + "/migrations"} {
				f.request(t, "GET", path, actor, nil, 403)
			}
		}
		explorer := spaData[struct {
			Schema string `json:"schema"`
			Tables []struct {
				Name string `json:"name"`
			} `json:"tables"`
			Workspace struct {
				CanEdit bool `json:"can_edit"`
			} `json:"workspace"`
		}](t, f.request(t, "GET", base, &f.Viewer, nil, 200))
		if explorer.Schema != "public" || len(explorer.Tables) != 1 || explorer.Tables[0].Name != "items" || explorer.Workspace.CanEdit {
			t.Fatalf("explorer=%+v", explorer)
		}
		path := base + "/tables/" + database.TableRef{Schema: "public", Name: "items"}.ID()
		type page struct {
			Offset    int                `json:"offset"`
			Limit     int                `json:"limit"`
			HasNext   bool               `json:"has_next"`
			Ordered   bool               `json:"ordered"`
			Rows      [][]map[string]any `json:"rows"`
			Workspace struct {
				ID string `json:"id"`
			} `json:"workspace"`
		}
		first := spaData[page](t, f.request(t, "GET", path+"?limit=2", &f.Viewer, nil, 200))
		if len(first.Rows) != 2 || !first.HasNext || !first.Ordered || first.Limit != 2 || first.Workspace.ID != id {
			t.Fatalf("first page=%+v", first)
		}
		if first.Rows[0][1]["null"] != true || first.Rows[1][2]["binary"] != true || first.Rows[1][2]["text"] != `\xdeadbeef` {
			t.Fatalf("cell flags=%+v", first.Rows)
		}
		for _, key := range []string{"text", "null", "binary", "truncated"} {
			if _, ok := first.Rows[0][0][key]; !ok {
				t.Fatalf("missing lowercase cell key %s", key)
			}
		}
		last := spaData[page](t, f.request(t, "GET", path+"?limit=2&offset=2", &f.Viewer, nil, 200))
		if len(last.Rows) != 2 || last.HasNext || last.Offset != 2 || last.Rows[0][1]["truncated"] != true || len(last.Rows[0][1]["text"].(string)) > 4096 {
			t.Fatalf("last page=%+v", last)
		}
		f.request(t, "GET", path+"?limit=201", &f.Viewer, nil, 422)
		f.request(t, "GET", base+"?schema=missing", &f.Viewer, nil, 404)
	})
	t.Run("draft execution and immutable attempt ownership", func(t *testing.T) {
		input := map[string]any{"title": "API migration", "sql": "CREATE TABLE executed (id int);"}
		f.request(t, "POST", base+"/migrations", &f.Viewer, input, 403)
		created := f.request(t, "POST", base+"/migrations", &f.Editor, input, 201)
		d := spaData[spaDraft](t, created)
		path := base + "/migrations/" + d.ID
		if d.ID == "" || d.Revision != 1 || d.Dialect != "postgresql" || created.Header().Get("Location") != path {
			t.Fatalf("draft=%+v", d)
		}
		update := map[string]any{"title": "Updated", "sql": "CREATE TABLE executed (id int); INSERT INTO executed VALUES (7);", "expected_revision": 1}
		d = spaData[spaDraft](t, f.request(t, "PATCH", path, &f.Editor, update, 200))
		if d.Revision != 2 || d.SQLHash == "" {
			t.Fatalf("updated=%+v", d)
		}
		f.request(t, "PATCH", path, &f.Editor, update, 409)
		f.request(t, "POST", path+"/execute", &f.Viewer, spaConfirmation(d, 0), 403)
		for _, bad := range []map[string]any{
			{"expected_revision": 1, "expected_version": 0, "expected_sql_hash": d.SQLHash},
			{"expected_revision": 2, "expected_version": 1, "expected_sql_hash": d.SQLHash},
			{"expected_revision": 2, "expected_version": 0, "expected_sql_hash": strings.Repeat("0", 64)},
		} {
			f.request(t, "POST", path+"/execute", &f.Editor, bad, 409)
		}
		a := spaData[spaAttempt](t, f.request(t, "POST", path+"/execute", &f.Editor, spaConfirmation(d, 0), 200))
		if a.ID == "" || a.Status != "succeeded" || a.AppliedVersion != 1 {
			t.Fatalf("attempt=%+v", a)
		}
		var n int
		if err := target.QueryRow(t.Context(), "SELECT id FROM executed").Scan(&n); err != nil || n != 7 {
			t.Fatalf("target value=%d err=%v", n, err)
		}
		f.request(t, "POST", path+"/execute", &f.Editor, spaConfirmation(d, 0), 409)
		failedDraft := spaData[spaDraft](t, f.request(t, "POST", base+"/migrations", &f.Editor, map[string]any{"title": "Rollback", "sql": "CREATE TABLE rolled_back (id int); INSERT INTO missing_table VALUES (1);"}, 201))
		failedPath := base + "/migrations/" + failedDraft.ID
		failed := f.request(t, "POST", failedPath+"/execute", &f.Editor, spaConfirmation(failedDraft, 1), 422)
		var failure struct {
			Error struct {
				Code    string     `json:"code"`
				Attempt spaAttempt `json:"attempt"`
			} `json:"error"`
		}
		if err := json.Unmarshal(failed.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		fa := failure.Error.Attempt
		if failure.Error.Code != "validation" || fa.ID == "" || fa.Status != "failed" || fa.FailedStatement != 2 || fa.SQL != failedDraft.SQL {
			t.Fatalf("lost failed attempt: %s", failed.Body)
		}
		var exists bool
		if err := target.QueryRow(t.Context(), "SELECT to_regclass('public.rolled_back') IS NOT NULL").Scan(&exists); err != nil || exists {
			t.Fatalf("rollback exists=%v err=%v", exists, err)
		}
		got := spaData[spaAttempt](t, f.request(t, "GET", failedPath+"/attempts/"+fa.ID, &f.Viewer, nil, 200))
		if got.ID != fa.ID || got.SQL != failedDraft.SQL {
			t.Fatalf("attempt detail=%+v", got)
		}
		f.request(t, "GET", path+"/attempts/"+fa.ID, &f.Viewer, nil, 404)
		f.request(t, "GET", "/api/v1/databases/"+testfixture.PrimaryTargetID+"/migrations/"+d.ID, &f.Viewer, nil, 404)
		f.request(t, "POST", "/api/v1/databases/"+testfixture.PrimaryTargetID+"/migrations/"+d.ID+"/execute", &f.Editor, spaConfirmation(d, 0), 404)
		history := spaData[struct {
			CurrentVersion int64        `json:"current_version"`
			Attempts       []spaAttempt `json:"attempts"`
		}](t, f.request(t, "GET", base+"/migrations", &f.Viewer, nil, 200))
		if history.CurrentVersion != 1 || len(history.Attempts) != 2 {
			t.Fatalf("denied/stale/duplicate requests changed history: %+v", history)
		}
	})
	t.Run("diff and estimated plans never execute", func(t *testing.T) {
		var before int
		if err := f.Pool.QueryRow(t.Context(), "SELECT count(*) FROM migration_attempts WHERE database_id=$1", id).Scan(&before); err != nil {
			t.Fatal(err)
		}
		desired := `{"dialect":"postgresql","schema":"public","tables":[{"name":"preview_only","columns":[{"name":"id","type":"bigint","nullable":false}],"primaryKey":["id"]}]}`
		p := spaData[struct {
			SQL         string `json:"sql"`
			Fingerprint string `json:"source_fingerprint"`
		}](t, f.request(t, "POST", base+"/schema-diff/preview", &f.Viewer, map[string]any{"desired": desired}, 200))
		if !strings.Contains(p.SQL, `CREATE TABLE "public"."preview_only"`) || p.Fingerprint == "" {
			t.Fatalf("proposal=%+v", p)
		}
		draftInput := map[string]any{"desired": desired, "source_fingerprint": p.Fingerprint, "title": "Diff draft"}
		f.request(t, "POST", base+"/schema-diff/draft", &f.Viewer, draftInput, 403)
		d := spaData[spaDraft](t, f.request(t, "POST", base+"/schema-diff/draft", &f.Editor, draftInput, 201))
		if d.SQL != p.SQL {
			t.Fatal("diff draft lost preview SQL")
		}
		plan := spaData[struct {
			Mode string `json:"mode"`
			Root struct {
				Type string `json:"type"`
			} `json:"root"`
		}](t, f.request(t, "POST", base+"/explain", &f.Editor, map[string]any{"sql": "INSERT INTO items VALUES (99, 'not executed', NULL)", "mode": "explain"}, 200))
		if plan.Mode != "explain" || plan.Root.Type == "" {
			t.Fatalf("plan=%+v", plan)
		}
		f.request(t, "POST", base+"/explain", &f.Viewer, map[string]any{"sql": "SELECT * FROM items", "mode": "analyze", "confirmed": true}, 403)
		f.request(t, "POST", base+"/explain", &f.Editor, map[string]any{"sql": "SELECT * FROM items", "mode": "analyze"}, 422)
		f.request(t, "POST", base+"/explain", &f.Editor, map[string]any{"sql": "SELECT * FROM items", "mode": "analyze", "confirmed": true}, 422)
		var exists bool
		var rows, after int
		if err := target.QueryRow(t.Context(), "SELECT to_regclass('public.preview_only') IS NOT NULL, (SELECT count(*) FROM items)").Scan(&exists, &rows); err != nil || exists || rows != 4 {
			t.Fatalf("preview/plan changed target: exists=%v rows=%d err=%v", exists, rows, err)
		}
		if err := f.Pool.QueryRow(t.Context(), "SELECT count(*) FROM migration_attempts WHERE database_id=$1", id).Scan(&after); err != nil || after != before {
			t.Fatalf("preview/plan changed attempts: %d -> %d err=%v", before, after, err)
		}
	})
}

func TestSPAWorkspaceMySQLHTTP(t *testing.T) {
	target, conn := mysqlTargetDB(t)
	f := newSPAWorkspaceFixture(t)
	id := f.register(t, conn)
	base := "/api/v1/databases/" + id
	if _, err := target.Exec("CREATE TABLE items (id INT PRIMARY KEY, label TEXT NULL, data BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec("INSERT INTO items VALUES (1, NULL, X'deadbeef'), (2, 'two', NULL)"); err != nil {
		t.Fatal(err)
	}
	explorer := spaData[struct {
		Schema string `json:"schema"`
	}](t, f.request(t, "GET", base, &f.Viewer, nil, 200))
	if explorer.Schema != conn.Database {
		t.Fatalf("MySQL schema=%q", explorer.Schema)
	}
	page := spaData[struct {
		Rows    [][]map[string]any `json:"rows"`
		HasNext bool               `json:"has_next"`
	}](t, f.request(t, "GET", base+"/tables/"+database.TableRef{Schema: conn.Database, Name: "items"}.ID()+"?limit=1", &f.Viewer, nil, 200))
	if len(page.Rows) != 1 || !page.HasNext || page.Rows[0][1]["null"] != true || page.Rows[0][2]["binary"] != true {
		t.Fatalf("MySQL page=%+v", page)
	}
	d := spaData[spaDraft](t, f.request(t, "POST", base+"/migrations", &f.Editor, map[string]any{"title": "MySQL API", "sql": "CREATE TABLE api_created (id INT PRIMARY KEY); INSERT INTO api_created VALUES (9);"}, 201))
	if d.Dialect != "mysql" {
		t.Fatalf("MySQL draft=%+v", d)
	}
	a := spaData[spaAttempt](t, f.request(t, "POST", base+"/migrations/"+d.ID+"/execute", &f.Editor, spaConfirmation(d, 0), 200))
	if a.Status != "succeeded" || a.AppliedVersion != 1 {
		t.Fatalf("MySQL attempt=%+v", a)
	}
	var n int
	if err := target.QueryRow("SELECT id FROM api_created").Scan(&n); err != nil || n != 9 {
		t.Fatalf("MySQL target=%d err=%v", n, err)
	}
	for _, actor := range []*testfixture.Actor{&f.NoAccess, &f.Admin} {
		f.request(t, "GET", base, actor, nil, 403)
	}
}
