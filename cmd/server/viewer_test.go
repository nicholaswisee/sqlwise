package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"sqlwise/internal/access/testfixture"
	"sqlwise/internal/database"
)

func newViewerFixture(t *testing.T) (*connectionFixture, string) {
	t.Helper()
	f := newConnectionFixture(t)
	if _, err := f.target.Exec(t.Context(), `CREATE SCHEMA sales;
		CREATE TABLE public.items (id integer PRIMARY KEY, label text, data bytea);
		INSERT INTO public.items SELECT g, 'item ' || g, NULL FROM generate_series(1, 120) g;
		UPDATE public.items SET label = NULL WHERE id = 1;
		UPDATE public.items SET label = 'héllo 世界', data = '\xdeadbeef' WHERE id = 2;
		CREATE INDEX items_label_idx ON public.items (label);
		CREATE TABLE public.keyless (v text);
		INSERT INTO public.keyless VALUES ('a');
		CREATE TABLE sales.items (id integer PRIMARY KEY, total numeric(10,2) CHECK (total >= 0));
		INSERT INTO sales.items VALUES (7, 12.50);
		CREATE SCHEMA sqlwise_internal;
		CREATE TABLE sqlwise_internal.migration_receipts (attempt_id text)`); err != nil {
		t.Fatal(err)
	}
	id := f.create(t, f.form)
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO group_database_grants(group_id,database_id,permission) VALUES ($1,$3,'view'),($2,$3,'edit')`, f.ViewGroup, f.EditGroup, id); err != nil {
		t.Fatal(err)
	}
	return f, id
}

func tableURL(id string, ref database.TableRef, query string) string {
	return "/databases/" + id + "/tables/" + ref.ID() + query
}

func TestViewerListsOnlyViewableDatabases(t *testing.T) {
	f, id := newViewerFixture(t)
	link := `href="/databases/` + id + `"`
	for name, tc := range map[string]struct {
		actor *testfixture.Actor
		want  bool
	}{
		"viewer":    {&f.Viewer, true},
		"editor":    {&f.Editor, true},
		"no access": {&f.NoAccess, false},
		"admin":     {&f.Admin, false},
	} {
		response := f.do(http.MethodGet, "/databases", tc.actor, nil, false, true)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), link) != tc.want {
			t.Fatalf("%s status=%d listed=%v", name, response.Code, !tc.want)
		}
	}
	if account := f.do(http.MethodGet, "/account", &f.Viewer, nil, false, true).Body.String(); !strings.Contains(account, link) {
		t.Fatal("account page does not link the viewer")
	}
	if response := f.do(http.MethodGet, "/databases", nil, nil, false, true); response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous list status=%d", response.Code)
	}
}

func TestViewerExploresSchemas(t *testing.T) {
	f, id := newViewerFixture(t)
	response := f.do(http.MethodGet, "/databases/"+id, &f.Viewer, nil, false, true)
	page := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(page, "Tables in public") || !strings.Contains(page, "View only") || !strings.Contains(page, `href="/databases/`+id+`/migrations"`) {
		t.Fatalf("explorer status=%d body=%s", response.Code, page)
	}
	for _, want := range []string{"items_label_idx", "keyless", `<option value="sales">sales</option>`, "PK", tableURL(id, database.TableRef{Schema: "public", Name: "items"}, "?limit=50&amp;offset=0")} {
		if !strings.Contains(page, want) {
			t.Fatalf("explorer missing %q", want)
		}
	}
	if strings.Contains(page, "sqlwise_internal") || strings.Contains(page, "migration_receipts") {
		t.Fatal("explorer shows the receipt schema")
	}
	if editor := f.do(http.MethodGet, "/databases/"+id, &f.Editor, nil, false, true).Body.String(); !strings.Contains(editor, "View + Edit") {
		t.Fatal("editor badge missing")
	}
	fragment := f.do(http.MethodGet, "/databases/"+id+"?schema=sales", &f.Viewer, nil, true, true)
	if body := fragment.Body.String(); fragment.Code != http.StatusOK || strings.Contains(strings.ToLower(body), "<!doctype") || !strings.Contains(body, "Tables in sales") || !strings.Contains(body, "CHECK") {
		t.Fatalf("schema fragment status=%d body=%s", fragment.Code, body)
	}
	for _, schema := range []string{"missing", "sqlwise_internal", "pg_catalog"} {
		if response := f.do(http.MethodGet, "/databases/"+id+"?"+url.Values{"schema": {schema}}.Encode(), &f.Viewer, nil, false, true); response.Code != http.StatusNotFound {
			t.Fatalf("schema %s status=%d", schema, response.Code)
		}
	}
}

func TestViewerPagesTableData(t *testing.T) {
	f, id := newViewerFixture(t)
	items := database.TableRef{Schema: "public", Name: "items"}
	response := f.do(http.MethodGet, tableURL(id, items, ""), &f.Viewer, nil, false, true)
	page := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(page, "Rows 1–50") || !strings.Contains(page, ">NULL<") || !strings.Contains(page, "héllo 世界") || !strings.Contains(page, `\xdeadbeef`) {
		t.Fatalf("first page status=%d body=%s", response.Code, page)
	}
	next := tableURL(id, items, "?limit=50&amp;offset=50")
	if !strings.Contains(page, next) || strings.Contains(page, "Previous") || strings.Contains(page, "no primary key") {
		t.Fatal("first page navigation is wrong")
	}
	last := f.do(http.MethodGet, tableURL(id, items, "?offset=100&limit=50"), &f.Viewer, nil, true, true).Body.String()
	if !strings.Contains(last, "Rows 101–120") || strings.Contains(last, ">Next<") || !strings.Contains(last, "Previous") || strings.Contains(strings.ToLower(last), "<!doctype") {
		t.Fatalf("last page fragment = %s", last)
	}
	if keyless := f.do(http.MethodGet, tableURL(id, database.TableRef{Schema: "public", Name: "keyless"}, ""), &f.Viewer, nil, false, true).Body.String(); !strings.Contains(keyless, "no primary key") {
		t.Fatal("keyless table does not explain unstable ordering")
	}
	if sales := f.do(http.MethodGet, tableURL(id, database.TableRef{Schema: "sales", Name: "items"}, ""), &f.Viewer, nil, false, true).Body.String(); !strings.Contains(sales, "12.50") || strings.Contains(sales, "item 1") {
		t.Fatal("sales.items resolved to the wrong table")
	}
	for name, tc := range map[string]struct {
		path string
		code int
	}{
		"limit too large":  {tableURL(id, items, "?limit=201"), http.StatusUnprocessableEntity},
		"negative offset":  {tableURL(id, items, "?offset=-1"), http.StatusUnprocessableEntity},
		"malformed id":     {"/databases/" + id + "/tables/!!", http.StatusNotFound},
		"missing table":    {tableURL(id, database.TableRef{Schema: "public", Name: "missing"}, ""), http.StatusNotFound},
		"receipt table":    {tableURL(id, database.TableRef{Schema: "sqlwise_internal", Name: "migration_receipts"}, ""), http.StatusNotFound},
		"other target":     {tableURL(testfixture.PrimaryTargetID, items, ""), http.StatusNotFound},
		"unknown database": {tableURL("does-not-exist", items, ""), http.StatusForbidden},
	} {
		if response := f.do(http.MethodGet, tc.path, &f.Viewer, nil, false, true); response.Code != tc.code {
			t.Fatalf("%s status=%d want %d", name, response.Code, tc.code)
		}
	}
}

func TestViewerDeniesAndReportsFailures(t *testing.T) {
	f, id := newViewerFixture(t)
	items := tableURL(id, database.TableRef{Schema: "public", Name: "items"}, "")
	for name, actor := range map[string]*testfixture.Actor{"no access": &f.NoAccess, "admin without grant": &f.Admin} {
		for _, path := range []string{"/databases/" + id, items} {
			if response := f.do(http.MethodGet, path, actor, nil, false, true); response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "item 2") {
				t.Fatalf("%s %s status=%d", name, path, response.Code)
			}
		}
	}
	if response := f.do(http.MethodGet, items, nil, nil, false, true); response.Code != http.StatusSeeOther {
		t.Fatalf("anonymous table status=%d", response.Code)
	}
	if response := f.do(http.MethodPost, "/admin/databases/"+id, &f.Admin, f.with(map[string]string{"password": "wrong target password"}), true, true); response.Code != http.StatusOK {
		t.Fatalf("update status=%d", response.Code)
	}
	response := f.do(http.MethodGet, "/databases/"+id, &f.Viewer, nil, false, true)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "username or password") || strings.Contains(response.Body.String(), "wrong target password") {
		t.Fatalf("connection failure status=%d body=%s", response.Code, response.Body)
	}
}
