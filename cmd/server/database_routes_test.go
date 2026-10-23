package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
	"sqlwise/internal/migration/migrationtest"
	"sqlwise/internal/web/views"
)

type databaseFixture struct {
	platform *migrationtest.Platform
	targets  *migrationtest.Targets
	router   *gin.Engine
	editor   migrationtest.User
	viewer   migrationtest.User
	outsider migrationtest.User
}

func newDatabaseFixture(t *testing.T) *databaseFixture {
	t.Helper()
	p := migrationtest.NewPlatform(t)
	p.Database(t, "db-a", "Alpha")
	p.Database(t, "db-b", "Beta")
	targets := migrationtest.NewTargets()
	targets.Add("db-a", migrationtest.TargetDatabase(t))
	assets := views.Assets{Dev: true}
	router := newRouter(assets, p.Sessions)
	registerDatabaseRoutes(router, p.Sessions, p.Access, p.Pool, assets, databaseTargets{targets: targets, snapshot: migrationtest.Snapshots{Targets: targets}})
	return &databaseFixture{platform: p, targets: targets, router: router,
		editor:   p.User(t, "editor", map[string]string{"db-a": "edit", "db-b": "edit"}),
		viewer:   p.User(t, "viewer", map[string]string{"db-a": "view"}),
		outsider: p.User(t, "outsider", nil)}
}

type dbRequest struct {
	method, path string
	user         *migrationtest.User
	form         url.Values
	hx           bool
	noCSRF       bool
	headers      map[string]string
}

func (f *databaseFixture) do(r dbRequest) *httptest.ResponseRecorder {
	form := url.Values{}
	for k, v := range r.form {
		form[k] = v
	}
	if r.user != nil && !r.noCSRF && r.method != http.MethodGet {
		form.Set("csrf_token", r.user.CSRF)
	}
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(form.Encode()))
	if r.method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.hx {
		req.Header.Set("HX-Request", "true")
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	if r.user != nil {
		req.AddCookie(&http.Cookie{Name: "sqlwise_session", Value: r.user.Token})
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func (f *databaseFixture) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := f.platform.Pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func expectStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body: %.400s", response.Code, want, response.Body.String())
	}
}

// field returns the text of a textarea or the value of an input by name.
func field(t *testing.T, body, name string) (string, bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var found *html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && (n.Data == "textarea" || n.Data == "input") {
			for _, a := range n.Attr {
				if a.Key == "name" && a.Val == name {
					found = n
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	if found == nil {
		return "", false
	}
	if found.Data == "textarea" {
		if found.FirstChild == nil {
			return "", true
		}
		return found.FirstChild.Data, true
	}
	for _, a := range found.Attr {
		if a.Key == "value" {
			return a.Val, true
		}
	}
	return "", true
}

func TestDatabaseRoutesRequireSession(t *testing.T) {
	f := newDatabaseFixture(t)
	for _, route := range f.router.Routes() {
		if !strings.HasPrefix(route.Path, "/databases/") {
			continue
		}
		path := strings.NewReplacer(":databaseID", "db-a", ":migrationID", "m", ":attemptID", "a").Replace(route.Path)
		for _, hx := range []bool{false, true} {
			response := f.do(dbRequest{method: route.Method, path: path, hx: hx})
			want := http.StatusSeeOther
			if route.Method != http.MethodGet || hx {
				want = http.StatusUnauthorized
			}
			if response.Code != want {
				t.Errorf("%s %s HX=%t status = %d, want %d", route.Method, route.Path, hx, response.Code, want)
			}
			if hx && response.Header().Get("HX-Redirect") != "/login" {
				t.Errorf("%s %s missing HX login redirect", route.Method, route.Path)
			}
		}
	}
}

func TestMigrationRoutesEnforceCSRFAndPermissions(t *testing.T) {
	f := newDatabaseFixture(t)
	create := url.Values{"title": {"Create"}, "sql": {"CREATE TABLE http_t (id int);"}}
	for name, r := range map[string]dbRequest{
		"missing-csrf": {method: "POST", path: "/databases/db-a/migrations", user: &f.editor, form: create, noCSRF: true},
		"wrong-csrf":   {method: "POST", path: "/databases/db-a/migrations", user: &f.editor, form: url.Values{"title": {"x"}, "sql": {"SELECT 1"}, "csrf_token": {f.viewer.CSRF}}, noCSRF: true},
		"cross-site":   {method: "POST", path: "/databases/db-a/migrations", user: &f.editor, form: create, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"viewer":       {method: "POST", path: "/databases/db-a/migrations", user: &f.viewer, form: create},
		"outsider":     {method: "POST", path: "/databases/db-a/migrations", user: &f.outsider, form: create},
	} {
		t.Run(name, func(t *testing.T) {
			expectStatus(t, f.do(r), http.StatusForbidden)
		})
	}
	if f.count(t, `SELECT count(*) FROM migration_drafts`) != 0 {
		t.Fatal("rejected request created a draft")
	}
	created := f.do(dbRequest{method: "POST", path: "/databases/db-a/migrations", user: &f.editor, form: create})
	expectStatus(t, created, http.StatusSeeOther)
	location := created.Header().Get("Location")
	if !strings.HasPrefix(location, "/databases/db-a/migrations/") {
		t.Fatalf("location = %q", location)
	}
	migrationID := strings.TrimPrefix(location, "/databases/db-a/migrations/")
	page := f.do(dbRequest{method: "GET", path: location, user: &f.editor})
	expectStatus(t, page, http.StatusOK)
	hash, _ := field(t, page.Body.String(), "expected_sql_hash")
	execute := url.Values{"expected_revision": {"1"}, "expected_version": {"0"}, "expected_sql_hash": {hash}}

	// Denied executions never open the target.
	expectStatus(t, f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.editor, form: execute, noCSRF: true}), http.StatusForbidden)
	expectStatus(t, f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.viewer, form: execute}), http.StatusForbidden)
	expectStatus(t, f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.viewer, form: execute, hx: true}), http.StatusForbidden)
	expectStatus(t, f.do(dbRequest{method: "PATCH", path: location, user: &f.viewer, form: url.Values{"expected_revision": {"1"}, "title": {"x"}, "sql": {"SELECT 1"}}}), http.StatusForbidden)
	if f.targets.Opens() != 0 || f.count(t, `SELECT count(*) FROM migration_attempts`) != 0 {
		t.Fatal("denied execution reached the target")
	}

	// Viewers read history and SQL without edit or execute controls.
	viewerPage := f.do(dbRequest{method: "GET", path: location, user: &f.viewer})
	expectStatus(t, viewerPage, http.StatusOK)
	body := viewerPage.Body.String()
	if _, ok := field(t, body, "expected_sql_hash"); ok || strings.Contains(body, `name="sql"`) || !strings.Contains(body, "CREATE TABLE http_t (id int);") {
		t.Fatal("viewer page exposes edit controls or hides SQL")
	}
	history := f.do(dbRequest{method: "GET", path: "/databases/db-a/migrations", user: &f.viewer})
	expectStatus(t, history, http.StatusOK)
	if strings.Contains(history.Body.String(), "New migration") || !strings.Contains(history.Body.String(), "View only") {
		t.Fatal("viewer history shows create form")
	}
	expectStatus(t, f.do(dbRequest{method: "GET", path: "/databases/db-a/migrations", user: &f.outsider}), http.StatusForbidden)
	expectStatus(t, f.do(dbRequest{method: "GET", path: "/databases/db-b/migrations", user: &f.viewer}), http.StatusForbidden)
	// A migration ID under another database is not found, even with access there.
	expectStatus(t, f.do(dbRequest{method: "GET", path: "/databases/db-b/migrations/" + migrationID, user: &f.editor}), http.StatusNotFound)
	expectStatus(t, f.do(dbRequest{method: "POST", path: "/databases/db-b/migrations/" + migrationID + "/execute", user: &f.editor, form: execute}), http.StatusNotFound)
}

func TestMigrationEditorFlowOverHTTP(t *testing.T) {
	f := newDatabaseFixture(t)
	sql := "CREATE TABLE \"web<tag>\" (id int, note text DEFAULT 'a & b');\r\nINSERT INTO \"web<tag>\" VALUES (1, '</textarea><script>x</script>');\r\n"
	stored := strings.ReplaceAll(sql, "\r\n", "\n")
	created := f.do(dbRequest{method: "POST", path: "/databases/db-a/migrations", user: &f.editor, form: url.Values{"title": {"Web flow"}, "sql": {sql}}})
	expectStatus(t, created, http.StatusSeeOther)
	location := created.Header().Get("Location")
	page := f.do(dbRequest{method: "GET", path: location, user: &f.editor})
	expectStatus(t, page, http.StatusOK)
	if shown, _ := field(t, page.Body.String(), "sql"); shown != stored {
		t.Fatalf("editor SQL = %q, want %q", shown, stored)
	}
	if strings.Contains(page.Body.String(), "<script>x</script>") {
		t.Fatal("SQL rendered unescaped")
	}
	if !strings.Contains(page.Body.String(), `hx-on::before-swap="if (event.detail.xhr.status &gt;= 400)`) {
		t.Fatal("HTMX error swap handler missing")
	}

	// Validation keeps input; stale revisions conflict both as form and fragment.
	invalid := f.do(dbRequest{method: "POST", path: "/databases/db-a/migrations", user: &f.editor, form: url.Values{"title": {""}, "sql": {"SELECT 42"}}})
	expectStatus(t, invalid, http.StatusUnprocessableEntity)
	if kept, _ := field(t, invalid.Body.String(), "sql"); kept != "SELECT 42" {
		t.Fatalf("validation lost input: %q", kept)
	}
	stale := url.Values{"expected_revision": {"7"}, "title": {"x"}, "sql": {"SELECT 1"}}
	expectStatus(t, f.do(dbRequest{method: "PATCH", path: location, user: &f.editor, form: stale, hx: true}), http.StatusConflict)
	conflict := f.do(dbRequest{method: "POST", path: location, user: &f.editor, form: stale})
	expectStatus(t, conflict, http.StatusConflict)
	if kept, _ := field(t, conflict.Body.String(), "sql"); kept != "SELECT 1" {
		t.Fatal("conflict lost the user's edit")
	}

	// A failing revision executes, rolls back and stays visible.
	failing := "INSERT INTO missing_table VALUES (1);"
	saved := f.do(dbRequest{method: "PATCH", path: location, user: &f.editor, form: url.Values{"expected_revision": {"1"}, "title": {"Web flow"}, "sql": {sql + failing}}, hx: true})
	expectStatus(t, saved, http.StatusNoContent)
	if saved.Header().Get("HX-Redirect") != location+"?saved=1" {
		t.Fatalf("HX redirect = %q", saved.Header().Get("HX-Redirect"))
	}
	page = f.do(dbRequest{method: "GET", path: location + "?saved=1", user: &f.editor})
	hash, _ := field(t, page.Body.String(), "expected_sql_hash")
	failed := f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.editor, hx: true,
		form: url.Values{"expected_revision": {"2"}, "expected_version": {"0"}, "expected_sql_hash": {hash}}})
	expectStatus(t, failed, http.StatusUnprocessableEntity)
	if !strings.Contains(failed.Body.String(), "Statement 3 failed") || !strings.Contains(failed.Body.String(), "Failed, rolled back") {
		t.Fatalf("failure fragment: %s", failed.Body.String())
	}

	// Fix and execute through the plain form fallback.
	expectStatus(t, f.do(dbRequest{method: "POST", path: location, user: &f.editor, form: url.Values{"expected_revision": {"2"}, "title": {"Web flow"}, "sql": {sql}}}), http.StatusSeeOther)
	page = f.do(dbRequest{method: "GET", path: location, user: &f.editor})
	hash, _ = field(t, page.Body.String(), "expected_sql_hash")
	executed := f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.editor,
		form: url.Values{"expected_revision": {"3"}, "expected_version": {"0"}, "expected_sql_hash": {hash}}})
	expectStatus(t, executed, http.StatusSeeOther)
	result := f.do(dbRequest{method: "GET", path: executed.Header().Get("Location"), user: &f.viewer})
	expectStatus(t, result, http.StatusOK)
	if !strings.Contains(result.Body.String(), "Applied as v1") || strings.Contains(result.Body.String(), `name="sql"`) {
		t.Fatal("applied migration page is not read-only")
	}
	// Re-submitting the same confirmation never runs it again.
	resubmit := f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.editor,
		form: url.Values{"expected_revision": {"3"}, "expected_version": {"0"}, "expected_sql_hash": {hash}}})
	expectStatus(t, resubmit, http.StatusConflict)
	expectStatus(t, f.do(dbRequest{method: "POST", path: location + "/execute", user: &f.editor, form: url.Values{"expected_revision": {"x"}}}), http.StatusUnprocessableEntity)
	if f.count(t, `SELECT count(*) FROM migration_attempts`) != 2 {
		t.Fatal("unexpected attempt count")
	}

	history := f.do(dbRequest{method: "GET", path: "/databases/db-a/migrations", user: &f.viewer})
	for _, want := range []string{"Current applied version: <strong class=\"text-slate-200\">v1</strong>", "Succeeded", "Failed, rolled back", "editor"} {
		if !strings.Contains(history.Body.String(), want) {
			t.Errorf("history lacks %q", want)
		}
	}
	var attemptPath string
	doc, _ := html.Parse(strings.NewReader(history.Body.String()))
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		for _, a := range n.Attr {
			if a.Key == "href" && strings.Contains(a.Val, "/attempts/") && attemptPath == "" {
				attemptPath = a.Val
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	attempt := f.do(dbRequest{method: "GET", path: attemptPath, user: &f.viewer})
	expectStatus(t, attempt, http.StatusOK)
	if !strings.Contains(attempt.Body.String(), "Executed SQL (immutable)") {
		t.Fatal("attempt detail missing SQL")
	}
}

func TestSchemaDiffRoutes(t *testing.T) {
	f := newDatabaseFixture(t)
	desired := `{"dialect":"postgresql","schema":"public","tables":[{"name":"made_by_diff","columns":[{"name":"id","type":"bigint","nullable":false}],"primaryKey":["id"]}]}`
	page := f.do(dbRequest{method: "GET", path: "/databases/db-a/schema-diff", user: &f.viewer})
	expectStatus(t, page, http.StatusOK)
	preview := f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/preview", user: &f.viewer, form: url.Values{"desired": {desired}}, hx: true})
	expectStatus(t, preview, http.StatusOK)
	body := preview.Body.String()
	if strings.Contains(body, "<html") || !strings.Contains(body, `CREATE TABLE &#34;public&#34;.&#34;made_by_diff&#34;`) || strings.Contains(body, "Use as draft") {
		t.Fatalf("viewer preview fragment: %s", body)
	}
	fingerprint := ""
	if editorPreview := f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/preview", user: &f.editor, form: url.Values{"desired": {desired}}}); editorPreview.Code == http.StatusOK {
		fingerprint, _ = field(t, editorPreview.Body.String(), "source_fingerprint")
	}
	if fingerprint == "" {
		t.Fatal("editor preview lacks Use as draft form")
	}
	invalid := f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/preview", user: &f.viewer, form: url.Values{"desired": {"{"}}, hx: true})
	expectStatus(t, invalid, http.StatusUnprocessableEntity)
	expectStatus(t, f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/preview", user: &f.outsider, form: url.Values{"desired": {desired}}}), http.StatusForbidden)
	draftForm := url.Values{"desired": {desired}, "source_fingerprint": {fingerprint}, "title": {"From diff"}}
	expectStatus(t, f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/draft", user: &f.viewer, form: draftForm}), http.StatusForbidden)
	expectStatus(t, f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/draft", user: &f.editor, form: draftForm, noCSRF: true}), http.StatusForbidden)
	stale := url.Values{"desired": {desired}, "source_fingerprint": {strings.Repeat("0", 64)}}
	expectStatus(t, f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/draft", user: &f.editor, form: stale}), http.StatusConflict)
	if f.count(t, `SELECT count(*) FROM migration_drafts`) != 0 {
		t.Fatal("denied or stale request created a draft")
	}
	created := f.do(dbRequest{method: "POST", path: "/databases/db-a/schema-diff/draft", user: &f.editor, form: draftForm})
	expectStatus(t, created, http.StatusSeeOther)
	draft := f.do(dbRequest{method: "GET", path: created.Header().Get("Location"), user: &f.editor})
	if sql, _ := field(t, draft.Body.String(), "sql"); !strings.Contains(sql, `CREATE TABLE "public"."made_by_diff"`) {
		t.Fatalf("draft SQL = %q", sql)
	}
	if f.count(t, `SELECT count(*) FROM migration_attempts`) != 0 {
		t.Fatal("Use as draft auto-executed")
	}
}
