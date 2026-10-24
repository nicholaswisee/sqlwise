package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func newExplainFixture(t *testing.T) (*connectionFixture, string) {
	t.Helper()
	f := newConnectionFixture(t)
	if _, err := f.target.Exec(t.Context(), `CREATE TABLE customers (id integer PRIMARY KEY, name text NOT NULL);
		INSERT INTO customers SELECT g, 'Customer ' || g FROM generate_series(1, 500) g;
		CREATE TABLE orders (id integer PRIMARY KEY, customer_id integer REFERENCES customers(id));
		INSERT INTO orders SELECT g, g % 500 + 1 FROM generate_series(1, 2000) g;
		ANALYZE customers; ANALYZE orders`); err != nil {
		t.Fatal(err)
	}
	id := f.create(t, f.form)
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO group_database_grants(group_id,database_id,permission) VALUES ($1,$3,'view'),($2,$3,'edit')`, f.ViewGroup, f.EditGroup, id); err != nil {
		t.Fatal(err)
	}
	return f, id
}

func explainForm(sql, mode string, confirm bool) url.Values {
	form := url.Values{"sql": {sql}, "mode": {mode}}
	if confirm {
		form.Set("confirm", "on")
	}
	return form
}

func TestExplainShowsEstimatedPlansWithoutRunning(t *testing.T) {
	f, id := newExplainFixture(t)
	path := "/databases/" + id + "/explain"
	response := f.do(http.MethodPost, path, &f.Viewer, explainForm("SELECT c.name, count(*) FROM customers c JOIN orders o ON o.customer_id = c.id WHERE c.id < 50 GROUP BY c.name", "explain", false), true, true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Join") || !strings.Contains(body, "on customers") || !strings.Contains(body, "Est. cost") || !strings.Contains(body, "N/A") || !strings.Contains(body, "not executed") {
		t.Fatalf("viewer explain = %d %s", response.Code, body)
	}
	if strings.Contains(strings.ToLower(body), "<!doctype") {
		t.Fatal("HTMX explain returned a full page")
	}
	if full := f.do(http.MethodPost, path, &f.Viewer, explainForm("SELECT * FROM customers WHERE id = 7", "explain", false), false, true); full.Code != http.StatusOK || !strings.Contains(full.Body.String(), "Query plan") {
		t.Fatalf("form fallback = %d", full.Code)
	}

	deleteSQL := "DELETE FROM customers WHERE id = 1"
	if denied := f.do(http.MethodPost, path, &f.Viewer, explainForm(deleteSQL, "explain", false), true, true); denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "edit access") {
		t.Fatalf("viewer DML explain = %d %s", denied.Code, denied.Body)
	}
	if planned := f.do(http.MethodPost, path, &f.Editor, explainForm(deleteSQL, "explain", false), true, true); planned.Code != http.StatusOK || !strings.Contains(planned.Body.String(), "Delete") {
		t.Fatalf("editor DML explain = %d %s", planned.Code, planned.Body)
	}
	var customers int
	if err := f.target.QueryRow(t.Context(), `SELECT count(*) FROM customers`).Scan(&customers); err != nil || customers != 500 {
		t.Fatalf("explain changed the target: %d %v", customers, err)
	}

	for name, tc := range map[string]struct {
		form url.Values
		code int
		text string
	}{
		"stacked":          {explainForm("SELECT 1; DROP TABLE customers", "explain", false), http.StatusUnprocessableEntity, "one statement"},
		"ddl":              {explainForm("DROP TABLE customers", "explain", false), http.StatusUnprocessableEntity, "DDL"},
		"syntax":           {explainForm("SELEC 1", "explain", false), http.StatusUnprocessableEntity, "could not be parsed"},
		"missing table":    {explainForm("SELECT * FROM missing_table", "explain", false), http.StatusUnprocessableEntity, "missing_table"},
		"analyze unmarked": {explainForm("SELECT * FROM customers", "analyze", true), http.StatusUnprocessableEntity, "analyze sandbox"},
		"analyze confirm":  {explainForm("SELECT * FROM customers", "analyze", false), http.StatusUnprocessableEntity, "Confirm"},
	} {
		response := f.do(http.MethodPost, path, &f.Editor, tc.form, true, true)
		if response.Code != tc.code || !strings.Contains(response.Body.String(), tc.text) {
			t.Fatalf("%s = %d %s", name, response.Code, response.Body)
		}
	}
	var tables int
	if err := f.target.QueryRow(t.Context(), `SELECT count(*) FROM pg_tables WHERE tablename = 'customers'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatal("rejected statement reached the target")
	}
	var attempts int
	if err := f.Pool.QueryRow(t.Context(), `SELECT count(*) FROM migration_attempts WHERE database_id = $1`, id).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("explain created migration attempts: %d %v", attempts, err)
	}
}

func TestExplainAnalyzeRunsOnlyOnSandbox(t *testing.T) {
	f, id := newExplainFixture(t)
	path := "/databases/" + id + "/explain"
	if response := f.do(http.MethodPost, "/admin/databases/"+id, &f.Admin, f.with(map[string]string{"analyze_sandbox": "on"}), true, true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Changes saved") {
		t.Fatalf("mark sandbox = %d %s", response.Code, response.Body)
	}
	if detail := f.do(http.MethodGet, "/admin/databases/"+id, &f.Admin, nil, false, true).Body.String(); !strings.Contains(detail, `name="analyze_sandbox" checked`) {
		t.Fatal("sandbox flag not shown on the connection page")
	}
	response := f.do(http.MethodPost, path, &f.Editor, explainForm("SELECT count(*) FROM orders WHERE customer_id < 100", "analyze", true), true, true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "read-only transaction") || !strings.Contains(body, "Actual rows") || strings.Count(body, ">N/A<") > 0 {
		t.Fatalf("sandbox analyze = %d %s", response.Code, body)
	}
	for name, tc := range map[string]url.Values{
		"viewer analyze": explainForm("SELECT * FROM customers", "analyze", true),
		"function":       explainForm("SELECT pg_sleep(10)", "analyze", true),
		"write cte":      explainForm("WITH gone AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM gone", "analyze", true),
		"dml analyze":    explainForm("UPDATE customers SET name = 'x'", "analyze", true),
		"row lock":       explainForm("SELECT * FROM customers FOR UPDATE", "analyze", true),
	} {
		actor := &f.Editor
		want := http.StatusUnprocessableEntity
		if name == "viewer analyze" {
			actor, want = &f.Viewer, http.StatusForbidden
		}
		if response := f.do(http.MethodPost, path, actor, tc, true, true); response.Code != want {
			t.Fatalf("%s = %d %s", name, response.Code, response.Body)
		}
	}
	var orders int
	var names string
	if err := f.target.QueryRow(t.Context(), `SELECT count(*), min(name) FROM orders, (SELECT min(name) AS name FROM customers) c GROUP BY c.name`).Scan(&orders, &names); err != nil || orders != 2000 || names == "x" {
		t.Fatalf("analyze changed the target: %d %q %v", orders, names, err)
	}
}

func TestExplainRequiresAccessAndCSRF(t *testing.T) {
	f, id := newExplainFixture(t)
	path := "/databases/" + id + "/explain"
	form := explainForm("SELECT * FROM customers", "explain", false)
	for name, response := range map[string]int{
		"no access":    f.do(http.MethodPost, path, &f.NoAccess, form, true, true).Code,
		"admin":        f.do(http.MethodPost, path, &f.Admin, form, true, true).Code,
		"missing csrf": f.do(http.MethodPost, path, &f.Viewer, form, true, false).Code,
		"other target": f.do(http.MethodPost, "/databases/does-not-exist/explain", &f.Viewer, form, true, true).Code,
	} {
		if response != http.StatusForbidden {
			t.Fatalf("%s = %d", name, response)
		}
	}
	if anonymous := f.do(http.MethodPost, path, nil, form, true, true); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", anonymous.Code)
	}

	created := f.do(http.MethodPost, "/databases/"+id+"/migrations", &f.Editor, url.Values{"title": {"Plan me"}, "sql": {"SELECT 1;\n"}}, false, true)
	location := created.Header().Get("Location")
	editor := f.do(http.MethodGet, location, &f.Editor, nil, false, true).Body.String()
	viewer := f.do(http.MethodGet, location, &f.Viewer, nil, false, true).Body.String()
	if !strings.Contains(editor, "Query plan") || !strings.Contains(editor, `hx-include="#edit-sql"`) || !strings.Contains(viewer, "Query plan") || strings.Contains(viewer, `hx-include="#edit-sql"`) {
		t.Fatalf("plan panel missing on the migration page (status %s)", strconv.Itoa(created.Code))
	}
}
