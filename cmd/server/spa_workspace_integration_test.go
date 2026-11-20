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
	"time"

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
		"database": conn.Database, "username": conn.Username, "password": conn.Password, "tls": conn.TLS, "sandbox": conn.Sandbox,
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
	ID             string `json:"id"`
	SQL            string `json:"sql"`
	SQLDown        string `json:"sql_down"`
	DefinitionHash string `json:"definition_hash"`
	Revision       int64  `json:"revision"`
	Dialect        string `json:"dialect"`
}

type spaAttempt struct {
	ID               string `json:"id"`
	MigrationID      string `json:"migration_id"`
	Operation        string `json:"operation"`
	CancelsExecution string `json:"cancels_execution_id"`
	Status           string `json:"status"`
	AppliedVersion   int64  `json:"applied_version"`
	FailedStatement  int    `json:"failed_statement"`
	SQL              string `json:"sql"`
}

type spaRequest struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Approvals []struct {
		GroupID string `json:"group_id"`
	} `json:"approvals"`
	Execution *spaAttempt `json:"execution"`
}

type spaJob struct {
	ID          string `json:"job_id"`
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	Error       string `json:"error"`
}

func spaRequestInput(operation string, d spaDraft, version int64) map[string]any {
	return map[string]any{"operation": operation, "expected_revision": d.Revision, "expected_definition_hash": d.DefinitionHash, "expected_version": version}
}

// approveAll submits a request and approves it as both fixture group leaders.
func (f *spaWorkspaceFixture) approveAll(t *testing.T, base, requestID string) {
	t.Helper()
	f.request(t, "POST", base+"/requests/"+requestID+"/submit", &f.Editor, nil, 200)
	f.request(t, "POST", base+"/requests/"+requestID+"/approve", &f.Viewer, map[string]any{"group_id": f.ViewGroup}, 200)
	f.request(t, "POST", base+"/requests/"+requestID+"/approve", &f.Editor, map[string]any{"group_id": f.EditGroup}, 200)
}

// waitJob polls a job the way the SPA does until it finishes.
func (f *spaWorkspaceFixture) waitJob(t *testing.T, base, jobID string) spaJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		job := spaData[spaJob](t, f.request(t, "GET", base+"/jobs/"+jobID, &f.Viewer, nil, 200))
		if job.Status != "queued" && job.Status != "running" {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s", jobID, job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSPAWorkspacePostgresHTTP(t *testing.T) {
	f := newSPAWorkspaceFixture(t)
	target := migrationtest.TargetDatabase(t)
	cc := target.Config().ConnConfig
	id := f.register(t, database.Connection{DBMS: database.PostgreSQL, Host: cc.Host, Port: int(cc.Port), Database: cc.Database, Username: cc.User, Password: cc.Password, TLS: database.TLSDisable, Sandbox: true})
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
				CanEdit      bool `json:"can_edit"`
				Capabilities struct {
					ExplainAnalyze bool `json:"explain_analyze"`
				} `json:"capabilities"`
			} `json:"workspace"`
		}](t, f.request(t, "GET", base, &f.Viewer, nil, 200))
		if explorer.Schema != "public" || len(explorer.Tables) != 1 || explorer.Tables[0].Name != "items" || explorer.Workspace.CanEdit || !explorer.Workspace.Capabilities.ExplainAnalyze {
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
				ID           string `json:"id"`
				Capabilities struct {
					ExplainAnalyze bool `json:"explain_analyze"`
				} `json:"capabilities"`
			} `json:"workspace"`
		}
		first := spaData[page](t, f.request(t, "GET", path+"?limit=2", &f.Viewer, nil, 200))
		if len(first.Rows) != 2 || !first.HasNext || !first.Ordered || first.Limit != 2 || first.Workspace.ID != id || !first.Workspace.Capabilities.ExplainAnalyze {
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
	t.Run("reviewed execution through the queue", func(t *testing.T) {
		// Leaders are assigned directly so the fixture sessions stay fresh.
		if _, err := f.Pool.Exec(t.Context(), `INSERT INTO group_leaders(group_id,user_id) VALUES ($1,$2),($3,$4)`, f.ViewGroup, f.Viewer.UserID, f.EditGroup, f.Editor.UserID); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"title": "API migration", "sql": "CREATE TABLE executed (id int);", "sql_down": "DROP TABLE executed;"}
		f.request(t, "POST", base+"/migrations", &f.Viewer, input, 403)
		created := f.request(t, "POST", base+"/migrations", &f.Editor, input, 201)
		d := spaData[spaDraft](t, created)
		path := base + "/migrations/" + d.ID
		if d.ID == "" || d.Revision != 1 || d.Dialect != "postgresql" || d.SQLDown != "DROP TABLE executed;" || created.Header().Get("Location") != path {
			t.Fatalf("draft=%+v", d)
		}
		update := map[string]any{"title": "Updated", "sql": "CREATE TABLE executed (id int); INSERT INTO executed VALUES (7);", "sql_down": "DROP TABLE executed;", "expected_revision": 1}
		d = spaData[spaDraft](t, f.request(t, "PATCH", path, &f.Editor, update, 200))
		if d.Revision != 2 || d.DefinitionHash == "" {
			t.Fatalf("updated=%+v", d)
		}
		f.request(t, "PATCH", path, &f.Editor, update, 409)

		up := spaRequestInput("up", d, 0)
		f.request(t, "POST", path+"/requests", &f.Viewer, up, 403)
		f.request(t, "POST", path+"/requests", &f.Editor, spaRequestInput("up", spaDraft{Revision: 1, DefinitionHash: d.DefinitionHash}, 0), 409)
		f.request(t, "POST", path+"/requests", &f.Editor, spaRequestInput("up", d, 1), 409)
		r := spaData[spaRequest](t, f.request(t, "POST", path+"/requests", &f.Editor, up, 201))
		execute := map[string]any{"request_id": r.ID, "idempotency_key": "first"}
		f.request(t, "POST", path+"/execute", &f.Editor, execute, 409) // not reviewed yet
		f.request(t, "POST", base+"/requests/"+r.ID+"/submit", &f.Viewer, nil, 403)
		r = spaData[spaRequest](t, f.request(t, "POST", base+"/requests/"+r.ID+"/submit", &f.Editor, nil, 200))
		if r.Status != "pending_review" || len(r.Approvals) != 2 {
			t.Fatalf("submitted=%+v", r)
		}
		// Each group's own leader decides; a view-only leader may approve.
		f.request(t, "POST", base+"/requests/"+r.ID+"/approve", &f.Viewer, map[string]any{"group_id": f.EditGroup}, 403)
		f.request(t, "POST", base+"/requests/"+r.ID+"/approve", &f.Viewer, map[string]any{"group_id": f.ViewGroup}, 200)
		f.request(t, "POST", path+"/execute", &f.Editor, execute, 409) // one approval missing
		r = spaData[spaRequest](t, f.request(t, "POST", base+"/requests/"+r.ID+"/approve", &f.Editor, map[string]any{"group_id": f.EditGroup}, 200))
		if r.Status != "approved" {
			t.Fatalf("approved=%+v", r)
		}
		f.request(t, "POST", path+"/execute", &f.Viewer, execute, 403)
		f.request(t, "POST", path+"/execute", &f.Editor, map[string]any{"request_id": r.ID}, 422)
		f.request(t, "POST", "/api/v1/databases/"+testfixture.PrimaryTargetID+"/migrations/"+d.ID+"/execute", &f.Editor, execute, 404)
		var exists bool
		if err := target.QueryRow(t.Context(), "SELECT to_regclass('public.executed') IS NOT NULL").Scan(&exists); err != nil || exists {
			t.Fatalf("denied requests reached the target: exists=%v err=%v", exists, err)
		}

		accepted := f.request(t, "POST", path+"/execute", &f.Editor, execute, 202)
		job := spaData[spaJob](t, accepted)
		if job.ID == "" || job.ExecutionID == "" || job.ExecutionID == r.ID || accepted.Header().Get("Location") != base+"/jobs/"+job.ID {
			t.Fatalf("job=%+v location=%q", job, accepted.Header().Get("Location"))
		}
		if job = f.waitJob(t, base, job.ID); job.Status != "succeeded" {
			t.Fatalf("job=%+v", job)
		}
		var n int
		if err := target.QueryRow(t.Context(), "SELECT id FROM executed").Scan(&n); err != nil || n != 7 {
			t.Fatalf("target value=%d err=%v", n, err)
		}
		// Enqueueing the same request again never runs it twice.
		again := f.waitJob(t, base, spaData[spaJob](t, f.request(t, "POST", path+"/execute", &f.Editor, map[string]any{"request_id": r.ID, "idempotency_key": "retry"}, 202)).ID)
		var rows int
		if err := target.QueryRow(t.Context(), "SELECT count(*) FROM executed").Scan(&rows); err != nil || rows != 1 || again.ExecutionID != job.ExecutionID {
			t.Fatalf("redelivery rows=%d job=%+v err=%v", rows, again, err)
		}
		r = spaData[spaRequest](t, f.request(t, "GET", base+"/requests/"+r.ID, &f.Viewer, nil, 200))
		if r.Status != "queued" || r.Execution == nil || r.Execution.ID != job.ExecutionID || r.Execution.Status != "succeeded" || r.Execution.AppliedVersion != 1 {
			t.Fatalf("request after job=%+v", r)
		}

		// Rollback runs SQL down as a new execution; hide_rollback hides the pair.
		rollback := spaData[spaRequest](t, f.request(t, "POST", path+"/requests", &f.Editor, spaRequestInput("rollback", d, 1), 201))
		f.approveAll(t, base, rollback.ID)
		rolledBack := f.waitJob(t, base, spaData[spaJob](t, f.request(t, "POST", path+"/execute", &f.Editor, map[string]any{"request_id": rollback.ID, "idempotency_key": "rb"}, 202)).ID)
		if err := target.QueryRow(t.Context(), "SELECT to_regclass('public.executed') IS NOT NULL").Scan(&exists); err != nil || exists || rolledBack.Status != "succeeded" {
			t.Fatalf("rollback job=%+v exists=%v err=%v", rolledBack, exists, err)
		}
		type historyBody struct {
			CurrentVersion int64        `json:"current_version"`
			Attempts       []spaAttempt `json:"attempts"`
			Committed      []spaAttempt `json:"committed"`
			Head           struct {
				Active  []map[string]string `json:"active"`
				Pending []map[string]string `json:"pending"`
			} `json:"head"`
		}
		full := spaData[historyBody](t, f.request(t, "GET", base+"/migrations", &f.Viewer, nil, 200))
		hidden := spaData[historyBody](t, f.request(t, "GET", base+"/migrations?hide_rollback=true", &f.Viewer, nil, 200))
		if full.CurrentVersion != 2 || len(full.Committed) != 2 || full.Committed[1].Operation != "rollback" || full.Committed[1].CancelsExecution != job.ExecutionID ||
			len(hidden.Committed) != 0 || len(hidden.Attempts) != 2 || len(full.Head.Active) != 0 || len(full.Head.Pending) != 1 {
			t.Fatalf("history=%+v hidden=%+v", full, hidden)
		}

		// A failing migration rolls back, stays visible as an attempt, and keeps the head.
		failedDraft := spaData[spaDraft](t, f.request(t, "POST", base+"/migrations", &f.Editor, map[string]any{"title": "Rollback", "sql": "CREATE TABLE rolled_back (id int); INSERT INTO missing_table VALUES (1);", "irreversible": true, "irreversible_reason": "test data"}, 201))
		failedPath := base + "/migrations/" + failedDraft.ID
		f.request(t, "POST", failedPath+"/requests", &f.Editor, spaRequestInput("up", failedDraft, 2), 409) // B waits for forward
		forward := spaData[spaRequest](t, f.request(t, "POST", path+"/requests", &f.Editor, spaRequestInput("forward", d, 2), 201))
		f.approveAll(t, base, forward.ID)
		f.waitJob(t, base, spaData[spaJob](t, f.request(t, "POST", path+"/execute", &f.Editor, map[string]any{"request_id": forward.ID, "idempotency_key": "fw"}, 202)).ID)
		failing := spaData[spaRequest](t, f.request(t, "POST", failedPath+"/requests", &f.Editor, spaRequestInput("up", failedDraft, 3), 201))
		f.approveAll(t, base, failing.ID)
		failedJob := f.waitJob(t, base, spaData[spaJob](t, f.request(t, "POST", failedPath+"/execute", &f.Editor, map[string]any{"request_id": failing.ID, "idempotency_key": "f"}, 202)).ID)
		if failedJob.Status != "failed" || !strings.Contains(failedJob.Error, "Statement 2 failed") {
			t.Fatalf("failed job=%+v", failedJob)
		}
		if err := target.QueryRow(t.Context(), "SELECT to_regclass('public.rolled_back') IS NOT NULL").Scan(&exists); err != nil || exists {
			t.Fatalf("rollback exists=%v err=%v", exists, err)
		}
		fa := spaData[spaAttempt](t, f.request(t, "GET", failedPath+"/attempts/"+failedJob.ExecutionID, &f.Viewer, nil, 200))
		if fa.Status != "failed" || fa.FailedStatement != 2 || fa.SQL != failedDraft.SQL {
			t.Fatalf("attempt detail=%+v", fa)
		}
		f.request(t, "GET", path+"/attempts/"+fa.ID, &f.Viewer, nil, 404)
		f.request(t, "GET", "/api/v1/databases/"+testfixture.PrimaryTargetID+"/migrations/"+d.ID, &f.Viewer, nil, 404)
		f.request(t, "GET", "/api/v1/databases/"+testfixture.PrimaryTargetID+"/requests/"+r.ID, &f.Viewer, nil, 404)
		f.request(t, "GET", "/api/v1/databases/"+testfixture.PrimaryTargetID+"/jobs/"+job.ID, &f.Viewer, nil, 404)
		after := spaData[historyBody](t, f.request(t, "GET", base+"/migrations", &f.Viewer, nil, 200))
		if after.CurrentVersion != 3 || len(after.Committed) != 3 || len(after.Attempts) != 4 || len(after.Head.Active) != 1 {
			t.Fatalf("failure changed history: %+v", after)
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
		analyzed := f.request(t, "POST", base+"/explain", &f.Editor, map[string]any{"sql": "SELECT * FROM items", "mode": "analyze", "confirmed": true}, 200)
		if spaData[struct {
			Mode string `json:"mode"`
		}](t, analyzed).Mode != "analyze" {
			t.Fatalf("analyze response=%s", analyzed.Body)
		}
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
	// Reviewed execution is PostgreSQL-only; MySQL has no synchronous fallback.
	refused := f.request(t, "POST", base+"/migrations/"+d.ID+"/requests", &f.Editor, spaRequestInput("up", d, 0), 422)
	if !strings.Contains(refused.Body.String(), "PostgreSQL targets only") {
		t.Fatalf("MySQL request=%s", refused.Body)
	}
	var tables int
	if err := target.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='api_created'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("MySQL target changed: %d err=%v", tables, err)
	}
	for _, actor := range []*testfixture.Actor{&f.NoAccess, &f.Admin} {
		f.request(t, "GET", base, actor, nil, 403)
	}
}
