package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sqlwise/internal/database"
	"sqlwise/internal/jobs/k6fixture"
	"sqlwise/internal/migration/migrationtest"
)

func TestK6FixturePreparesApprovedRequestsThroughTheAPI(t *testing.T) {
	f := newSPAWorkspaceFixture(t)
	target := migrationtest.TargetDatabase(t)
	cc := target.Config().ConnConfig
	id := f.register(t, database.Connection{DBMS: database.PostgreSQL, Host: cc.Host, Port: int(cc.Port), Database: cc.Database,
		Username: cc.User, Password: cc.Password, TLS: database.TLSDisable})
	if _, err := f.Pool.Exec(t.Context(), `INSERT INTO group_leaders(group_id,user_id) VALUES ($1,$2),($3,$4)`,
		f.ViewGroup, f.Viewer.UserID, f.EditGroup, f.Editor.UserID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.router)
	defer server.Close()
	editor := k6fixture.Account{Username: f.Editor.Username, Password: f.Editor.Password}
	viewer := k6fixture.Account{Username: f.Viewer.Username, Password: f.Viewer.Password}
	fixtures, err := k6fixture.Prepare(t.Context(), k6fixture.Config{BaseURL: server.URL, Editor: editor,
		Leaders: []k6fixture.Account{editor, viewer}, Databases: []string{id}, PerDatabase: 2, Run: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 2 || fixtures[0].RequestID == fixtures[1].RequestID || fixtures[0].ExpectedVersion != 0 {
		t.Fatalf("fixtures = %+v", fixtures)
	}
	// Both were approved at one head: the first commits, the second is the expected stale-head conflict.
	base := "/api/v1/databases/" + id
	var jobs []spaJob
	for _, fx := range fixtures {
		accepted := f.request(t, "POST", fx.ExecutePath, &f.Editor, map[string]any{"request_id": fx.RequestID, "idempotency_key": fx.IdempotencyKey}, 202)
		jobs = append(jobs, spaData[spaJob](t, accepted))
	}
	first, second := f.waitJob(t, base, jobs[0].ID), f.waitJob(t, base, jobs[1].ID)
	if first.Status != "succeeded" || second.Status != "failed" || !strings.Contains(second.Error, "moved from version 0 to 1") {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
	exists := func(table string) bool {
		var ok bool
		if err := target.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !exists(fixtures[0].Table) || exists(fixtures[1].Table) {
		t.Fatal("only the first fixture may change the target")
	}
	targetURL := fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", url.UserPassword(cc.User, cc.Password), cc.Host, cc.Port, cc.Database)
	if n, err := k6fixture.Reset(t.Context(), targetURL, "t1"); err != nil || n != 1 || exists(fixtures[0].Table) {
		t.Fatalf("reset dropped %d tables, %v", n, err)
	}
	if _, err := k6fixture.Prepare(t.Context(), k6fixture.Config{BaseURL: server.URL, Editor: editor, Leaders: []k6fixture.Account{viewer},
		Databases: []string{id}, PerDatabase: 1, Run: "t2"}); err == nil || !strings.Contains(err.Error(), "after review") {
		t.Fatalf("prepare without every group's leader = %v", err)
	}
	if _, err := k6fixture.Prepare(t.Context(), k6fixture.Config{BaseURL: server.URL, Editor: viewer, Leaders: []k6fixture.Account{editor, viewer},
		Databases: []string{id}, PerDatabase: 1, Run: "t3"}); err == nil {
		t.Fatal("a view-only user prepared a request")
	}
}
