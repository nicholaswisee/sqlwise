package main

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

func TestAdminFormsCreateUserAndGroupAndGrantScopedAccountAccess(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	adminAccount := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{adminCookie}})
	adminCSRF := formCSRF(t, adminAccount)

	usersPage := f.do(authRequest{method: http.MethodGet, path: "/admin/users", cookies: []*http.Cookie{adminCookie}})
	if usersPage.Code != http.StatusOK || !strings.Contains(usersPage.Body.String(), "Create user") {
		t.Fatalf("admin users page status=%d", usersPage.Code)
	}
	createUser := f.do(authRequest{method: http.MethodPost, path: "/admin/users", cookies: []*http.Cookie{adminCookie}, form: url.Values{
		"username": {"analyst"}, "password": {"analyst integration passphrase"}, "csrf_token": {adminCSRF},
	}})
	if createUser.Code != http.StatusSeeOther || createUser.Header().Get("Location") != "/admin/users?notice=saved" {
		t.Fatalf("create user status=%d location=%q body=%q", createUser.Code, createUser.Header().Get("Location"), createUser.Body.String())
	}
	var userID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE username='analyst'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	groupsPage := f.do(authRequest{method: http.MethodGet, path: "/admin/groups", cookies: []*http.Cookie{adminCookie}})
	if groupsPage.Code != http.StatusOK || !strings.Contains(groupsPage.Body.String(), "Create group") {
		t.Fatalf("admin groups page status=%d", groupsPage.Code)
	}
	createGroup := f.do(authRequest{method: http.MethodPost, path: "/admin/groups", cookies: []*http.Cookie{adminCookie}, form: url.Values{"name": {"Analysts"}, "csrf_token": {adminCSRF}}})
	if createGroup.Code != http.StatusSeeOther || createGroup.Header().Get("Location") != "/admin/groups?notice=saved" {
		t.Fatalf("create group status=%d location=%q body=%q", createGroup.Code, createGroup.Header().Get("Location"), createGroup.Body.String())
	}
	var groupID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM groups WHERE name='Analysts'`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}

	// Registrations live in this isolated platform test database; no target database is contacted.
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO database_registrations(id,name,active) VALUES ('integration-allowed','Allowed target',true),('integration-denied','Denied target',true)`); err != nil {
		t.Fatal(err)
	}
	member := f.do(authRequest{method: http.MethodPost, path: "/admin/groups/" + groupID + "/members", cookies: []*http.Cookie{adminCookie}, form: url.Values{"user_id": {userID}, "csrf_token": {adminCSRF}}})
	if member.Code != http.StatusSeeOther {
		t.Fatalf("add member status=%d body=%q", member.Code, member.Body.String())
	}
	grant := f.do(authRequest{method: http.MethodPost, path: "/admin/groups/" + groupID + "/databases", cookies: []*http.Cookie{adminCookie}, form: url.Values{
		"database_id": {"integration-allowed"}, "permission": {"view"}, "csrf_token": {adminCSRF},
	}})
	if grant.Code != http.StatusSeeOther {
		t.Fatalf("set grant status=%d body=%q", grant.Code, grant.Body.String())
	}

	loginPage := f.do(authRequest{method: http.MethodGet, path: "/login"})
	userLogin := f.do(authRequest{method: http.MethodPost, path: "/login", cookies: []*http.Cookie{responseCookie(t, loginPage, "sqlwise_login")}, form: url.Values{
		"username": {"analyst"}, "password": {"analyst integration passphrase"}, "csrf_token": {formCSRF(t, loginPage)},
	}})
	if userLogin.Code != http.StatusSeeOther || userLogin.Header().Get("Location") != "/account" {
		t.Fatalf("user login status=%d location=%q body=%q", userLogin.Code, userLogin.Header().Get("Location"), userLogin.Body.String())
	}
	userCookie := responseCookie(t, userLogin, "sqlwise_session")
	account := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{userCookie}})
	if account.Code != http.StatusOK || !strings.Contains(account.Body.String(), "Allowed target") || !strings.Contains(account.Body.String(), "view") {
		t.Fatalf("account did not show allowed target: status=%d body=%q", account.Code, account.Body.String())
	}
	if strings.Contains(account.Body.String(), "Denied target") || strings.Contains(account.Body.String(), "integration-denied") {
		t.Fatalf("account exposed an ungranted target: %q", account.Body.String())
	}
	for _, path := range []string{"/admin", "/admin/users", "/admin/groups"} {
		response := f.do(authRequest{method: http.MethodGet, path: path, cookies: []*http.Cookie{userCookie}})
		if response.Code != http.StatusForbidden {
			t.Errorf("nonadmin GET %s status=%d want 403", path, response.Code)
		}
	}
	deniedPost := f.do(authRequest{method: http.MethodPost, path: "/admin/groups", cookies: []*http.Cookie{userCookie}, form: url.Values{"name": {"Should not exist"}, "csrf_token": {formCSRF(t, account)}}})
	if deniedPost.Code != http.StatusForbidden {
		t.Fatalf("nonadmin POST status=%d want 403", deniedPost.Code)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM groups WHERE name='Should not exist'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorized POST changed groups count=%d err=%v", count, err)
	}
}

func TestAdminMutationRejectsInvalidCSRFAndExpiredSession(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	invalid := f.do(authRequest{method: http.MethodPost, path: "/admin/groups", cookies: []*http.Cookie{adminCookie}, form: url.Values{"name": {"Invalid CSRF"}, "csrf_token": {"wrong"}}})
	if invalid.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF status=%d want 403", invalid.Code)
	}
	f.now.Advance(auth.SessionLifetime)
	expired := f.do(authRequest{method: http.MethodPost, path: "/admin/groups", cookies: []*http.Cookie{adminCookie}, form: url.Values{"name": {"Expired"}, "csrf_token": {"anything"}}})
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired admin session status=%d want 401", expired.Code)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM groups WHERE name IN ('Invalid CSRF','Expired')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected requests changed groups count=%d err=%v", count, err)
	}
}

func TestAdminPagesRenderFullDocumentAndHTMXFragment(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	page := f.do(authRequest{method: http.MethodGet, path: "/admin/users", cookies: []*http.Cookie{cookie}})
	fragment := f.do(authRequest{method: http.MethodGet, path: "/admin/users", cookies: []*http.Cookie{cookie}, headers: map[string]string{"HX-Request": "true"}})
	if page.Code != http.StatusOK || !strings.Contains(strings.ToLower(page.Body.String()), "<!doctype html>") {
		t.Fatalf("full admin page status=%d body=%q", page.Code, page.Body.String())
	}
	if fragment.Code != http.StatusOK || strings.Contains(strings.ToLower(fragment.Body.String()), "<!doctype html>") || !strings.Contains(fragment.Body.String(), "Create user") {
		t.Fatalf("admin fragment status=%d body=%q", fragment.Code, fragment.Body.String())
	}
}

func TestAccountShowsRefreshStateUntilElevatedSessionIsPresented(t *testing.T) {
	f := newAuthFixture(t)
	oldCookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1 WHERE username='admin'`); err != nil {
		t.Fatal(err)
	}
	stale := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{oldCookie}})
	if stale.Code != http.StatusOK || !strings.Contains(stale.Body.String(), "Your permissions changed.") {
		t.Fatalf("stale account status=%d body=%q", stale.Code, stale.Body.String())
	}
	if strings.Contains(stale.Body.String(), "No database access has been assigned") || strings.Contains(stale.Body.String(), "Manage platform") {
		t.Fatal("stale account displayed an inaccurate empty state or elevated admin action")
	}
	newCookie := responseCookie(t, stale, "sqlwise_session")
	if newCookie.Value == oldCookie.Value {
		t.Fatal("authorization change did not rotate the session")
	}
	fresh := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{newCookie}})
	if fresh.Code != http.StatusOK || strings.Contains(fresh.Body.String(), "Your permissions changed.") || !strings.Contains(fresh.Body.String(), "Manage platform") {
		t.Fatalf("successor account status=%d body=%q", fresh.Code, fresh.Body.String())
	}
}

func TestAdminRoleFormKeepsTargetAccessSeparate(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	adminAccount := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{adminCookie}})
	csrf := formCSRF(t, adminAccount)
	created := f.do(authRequest{method: http.MethodPost, path: "/admin/users", cookies: []*http.Cookie{adminCookie}, form: url.Values{
		"username": {"role-user"}, "password": {"role integration passphrase"}, "csrf_token": {csrf},
	}})
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create user status=%d", created.Code)
	}
	var userID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE username='role-user'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	loginPage := f.do(authRequest{method: http.MethodGet, path: "/login"})
	userLogin := f.do(authRequest{method: http.MethodPost, path: "/login", cookies: []*http.Cookie{responseCookie(t, loginPage, "sqlwise_login")}, form: url.Values{
		"username": {"role-user"}, "password": {"role integration passphrase"}, "csrf_token": {formCSRF(t, loginPage)},
	}})
	if userLogin.Code != http.StatusSeeOther {
		t.Fatalf("user login status=%d", userLogin.Code)
	}
	oldCookie := responseCookie(t, userLogin, "sqlwise_session")
	rolePath := "/admin/users/" + userID + "/admin"
	promote := f.do(authRequest{method: http.MethodPost, path: rolePath, cookies: []*http.Cookie{adminCookie}, form: url.Values{"admin": {"true"}, "csrf_token": {csrf}}})
	if promote.Code != http.StatusSeeOther {
		t.Fatalf("promote status=%d body=%q", promote.Code, promote.Body.String())
	}
	stale := f.do(authRequest{method: http.MethodGet, path: "/admin", cookies: []*http.Cookie{oldCookie}})
	if stale.Code != http.StatusForbidden {
		t.Fatalf("old proof accessed admin: status=%d", stale.Code)
	}
	newCookie := responseCookie(t, stale, "sqlwise_session")
	if page := f.do(authRequest{method: http.MethodGet, path: "/admin", cookies: []*http.Cookie{newCookie}}); page.Code != http.StatusOK {
		t.Fatalf("promoted successor admin status=%d", page.Code)
	}
	account := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{newCookie}})
	if account.Code != http.StatusOK || !strings.Contains(account.Body.String(), "Manage platform") || !strings.Contains(account.Body.String(), "No database access has been assigned") {
		t.Fatalf("platform admin gained target access or lost admin link: status=%d body=%q", account.Code, account.Body.String())
	}
	demote := f.do(authRequest{method: http.MethodPost, path: rolePath, cookies: []*http.Cookie{adminCookie}, form: url.Values{"admin": {"false"}, "csrf_token": {csrf}}})
	if demote.Code != http.StatusSeeOther {
		t.Fatalf("demote status=%d body=%q", demote.Code, demote.Body.String())
	}
	if denied := f.do(authRequest{method: http.MethodGet, path: "/admin", cookies: []*http.Cookie{newCookie}}); denied.Code != http.StatusSeeOther || denied.Header().Get("Location") != "/login" {
		t.Fatalf("demoted session survived: status=%d location=%q", denied.Code, denied.Header().Get("Location"))
	}
}

func TestAdminSelfChangesNavigateToSessionRecovery(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		name := "native"
		if htmx {
			name = "htmx"
		}
		t.Run("rename "+name, func(t *testing.T) {
			f := newAuthFixture(t)
			cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
			account := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
			var adminID string
			if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
				t.Fatal(err)
			}
			input := authRequest{method: http.MethodPost, path: "/admin/users/" + adminID, cookies: []*http.Cookie{cookie}, form: url.Values{
				"username": {"renamed.admin"}, "csrf_token": {formCSRF(t, account)},
			}}
			if htmx {
				input.headers = map[string]string{"HX-Request": "true"}
			}
			changed := f.do(input)
			if htmx {
				if changed.Code != http.StatusNoContent || changed.Header().Get("HX-Redirect") != "/account" {
					t.Fatalf("HTMX rename status=%d redirect=%q", changed.Code, changed.Header().Get("HX-Redirect"))
				}
			} else if changed.Code != http.StatusSeeOther || changed.Header().Get("Location") != "/account" {
				t.Fatalf("native rename status=%d location=%q", changed.Code, changed.Header().Get("Location"))
			}
			stale := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
			if stale.Code != http.StatusOK || !strings.Contains(stale.Body.String(), "Your permissions changed.") {
				t.Fatalf("stale account status=%d body=%q", stale.Code, stale.Body.String())
			}
			freshCookie := responseCookie(t, stale, "sqlwise_session")
			freshAdmin := f.do(authRequest{method: http.MethodGet, path: "/admin", cookies: []*http.Cookie{freshCookie}})
			if freshAdmin.Code != http.StatusOK {
				t.Fatalf("fresh admin status=%d", freshAdmin.Code)
			}
		})
	}
	t.Run("self disable", func(t *testing.T) {
		f := newAuthFixture(t)
		cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
		account := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
		var adminID string
		if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
			t.Fatal(err)
		}
		changed := f.do(authRequest{method: http.MethodPost, path: "/admin/users/" + adminID + "/active", cookies: []*http.Cookie{cookie}, form: url.Values{
			"active": {"false"}, "csrf_token": {formCSRF(t, account)},
		}})
		if changed.Code != http.StatusSeeOther || changed.Header().Get("Location") != "/login" {
			t.Fatalf("self disable status=%d location=%q", changed.Code, changed.Header().Get("Location"))
		}
		if replay := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}}); replay.Code != http.StatusSeeOther {
			t.Fatalf("disabled session account status=%d", replay.Code)
		}
	})
}

func TestDatabasePermissionMiddlewareGuardsTestOnlyHTTPRoutes(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	adminAccount := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{adminCookie}})
	csrf := formCSRF(t, adminAccount)

	created := f.do(authRequest{method: http.MethodPost, path: "/admin/users", cookies: []*http.Cookie{adminCookie}, form: url.Values{
		"username": {"guarded-user"}, "password": {"guarded integration passphrase"}, "csrf_token": {csrf},
	}})
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create user status=%d body=%q", created.Code, created.Body.String())
	}
	var userID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE username='guarded-user'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO database_registrations(id,name,active) VALUES ('guarded-target','Guarded target',true),('other-target','Other target',true)`); err != nil {
		t.Fatal(err)
	}
	groupCreated := f.do(authRequest{method: http.MethodPost, path: "/admin/groups", cookies: []*http.Cookie{adminCookie}, form: url.Values{"name": {"Guarded group"}, "csrf_token": {csrf}}})
	if groupCreated.Code != http.StatusSeeOther {
		t.Fatalf("create group status=%d body=%q", groupCreated.Code, groupCreated.Body.String())
	}
	var groupID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM groups WHERE name='Guarded group'`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO group_memberships(user_id,group_id) VALUES($1,$2)`, userID, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO group_database_grants(group_id,database_id,permission) VALUES($1,'guarded-target','view')`, groupID); err != nil {
		t.Fatal(err)
	}

	// Construct a new checker to exercise the same shared-store boundary as independently wired layers.
	sessionService, err := auth.NewService(auth.NewStore(f.pool), f.now.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.NewService(access.NewStore(f.pool), sessionService)
	var viewWork, editWork atomic.Int32
	f.router.GET("/databases/:databaseID/view", f.guard.RequireSession(), f.guard.RequireDatabasePermission(policy, "databaseID", access.View), func(c *gin.Context) {
		viewWork.Add(1)
		c.Status(http.StatusNoContent)
	})
	f.router.GET("/databases/:databaseID/edit", f.guard.RequireSession(), f.guard.RequireDatabasePermission(policy, "databaseID", access.Edit), func(c *gin.Context) {
		editWork.Add(1)
		c.Status(http.StatusNoContent)
	})

	adminDenied := f.do(authRequest{method: http.MethodGet, path: "/databases/guarded-target/view", cookies: []*http.Cookie{adminCookie}})
	if adminDenied.Code != http.StatusForbidden || viewWork.Load() != 0 {
		t.Fatalf("admin without grant status=%d target work=%d, want 403 and no work", adminDenied.Code, viewWork.Load())
	}
	userLoginPage := f.do(authRequest{method: http.MethodGet, path: "/login"})
	userLogin := f.do(authRequest{method: http.MethodPost, path: "/login", cookies: []*http.Cookie{responseCookie(t, userLoginPage, "sqlwise_login")}, form: url.Values{
		"username": {"guarded-user"}, "password": {"guarded integration passphrase"}, "csrf_token": {formCSRF(t, userLoginPage)},
	}})
	if userLogin.Code != http.StatusSeeOther {
		t.Fatalf("user login status=%d body=%q", userLogin.Code, userLogin.Body.String())
	}
	userCookie := responseCookie(t, userLogin, "sqlwise_session")
	allowed := f.do(authRequest{method: http.MethodGet, path: "/databases/guarded-target/view", cookies: []*http.Cookie{userCookie}})
	if allowed.Code != http.StatusNoContent || viewWork.Load() != 1 {
		t.Fatalf("granted view status=%d target work=%d, want 204 and one invocation", allowed.Code, viewWork.Load())
	}
	for _, path := range []string{"/databases/guarded-target/edit", "/databases/other-target/view"} {
		denied := f.do(authRequest{method: http.MethodGet, path: path, cookies: []*http.Cookie{userCookie}})
		if denied.Code != http.StatusForbidden {
			t.Errorf("denied %s status=%d want 403", path, denied.Code)
		}
	}
	if editWork.Load() != 0 || viewWork.Load() != 1 {
		t.Fatalf("denied permissions reached target handlers: view=%d edit=%d", viewWork.Load(), editWork.Load())
	}
}
