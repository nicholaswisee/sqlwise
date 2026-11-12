package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
	"sqlwise/internal/web/views"
)

func TestAdminAPIPostgresMutationAndAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := testdb.Open(t)
	store := auth.NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	accessStore := access.NewStore(pool)
	if err := accessStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds, err := auth.ParseCredentials("api-admin", "a sufficiently long admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.NewService(accessStore, sessions)
	h := NewAuth(sessions, views.Assets{}, false, policy)
	r := gin.New()
	h.RegisterAPI(r)
	NewAccessAdmin(h, policy, views.Assets{}).RegisterAPI(r)

	adminCookie, csrf := apiIntegrationLogin(t, r, "api-admin", "a sufficiently long admin password")
	empty := apiIntegrationRequest(t, r, http.MethodGet, "/api/v1/admin/groups", "", &adminCookie, csrf)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty snapshot status=%d body=%s", empty.Code, empty.Body)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(empty.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["data"], &data); err != nil {
		t.Fatal(err)
	}
	var initialUsers []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data["users"], &initialUsers); err != nil {
		t.Fatal(err)
	}
	adminID := ""
	for _, u := range initialUsers {
		if u.Username == "api-admin" {
			adminID = u.ID
		}
	}
	if adminID == "" {
		t.Fatalf("admin missing from snapshot: %s", empty.Body)
	}
	for _, key := range []string{"users", "groups", "databases", "memberships", "grants"} {
		if string(data[key]) == "null" || len(data[key]) == 0 {
			t.Errorf("snapshot %s should be a JSON array, got %s", key, data[key])
		}
	}
	create := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/users", `{"username":"managed-user","password":"a sufficiently long user password"}`, &adminCookie, csrf)
	if create.Code != http.StatusOK {
		t.Fatalf("create user status=%d body=%s", create.Code, create.Body)
	}
	var response struct {
		Data struct {
			Users []struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"users"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var userID string
	for _, u := range response.Data.Users {
		if u.Username == "managed-user" {
			userID = u.ID
		}
	}
	if userID == "" {
		t.Fatalf("created user missing from response: %s", create.Body)
	}
	if strings.Contains(strings.ToLower(create.Body.String()), "password") {
		t.Fatalf("password leaked: %s", create.Body)
	}

	rename := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/users/"+userID, `{"username":"renamed-user"}`, &adminCookie, csrf)
	if rename.Code != http.StatusOK || !strings.Contains(rename.Body.String(), "renamed-user") {
		t.Fatalf("rename status=%d body=%s", rename.Code, rename.Body)
	}
	for _, tc := range []struct {
		path, body string
		want       string
	}{
		{"/api/v1/admin/users/" + userID, `{"password":"another sufficiently long password"}`, "renamed-user"},
		{"/api/v1/admin/users/" + userID + "/active", `{"active":false}`, `"active":false`},
		{"/api/v1/admin/users/" + userID + "/active", `{"active":true}`, `"active":true`},
		{"/api/v1/admin/users/" + userID + "/admin", `{"admin":true}`, `"is_platform_admin":true`},
		{"/api/v1/admin/users/" + userID + "/admin", `{"admin":false}`, `"is_platform_admin":false`},
	} {
		got := apiIntegrationRequest(t, r, http.MethodPost, tc.path, tc.body, &adminCookie, csrf)
		if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), tc.want) {
			t.Errorf("POST %s status=%d body=%s", tc.path, got.Code, got.Body)
		}
	}

	group := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/groups", `{"name":"operators"}`, &adminCookie, csrf)
	if group.Code != http.StatusOK {
		t.Fatalf("create group status=%d body=%s", group.Code, group.Body)
	}
	var groupResponse struct {
		Data struct {
			Groups []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(group.Body.Bytes(), &groupResponse); err != nil {
		t.Fatal(err)
	}
	var groupID string
	for _, g := range groupResponse.Data.Groups {
		if g.Name == "operators" {
			groupID = g.ID
		}
	}
	if groupID == "" {
		t.Fatalf("group missing from response: %s", group.Body)
	}
	member := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/groups/"+groupID+"/members", `{"user_id":"`+userID+`"}`, &adminCookie, csrf)
	if member.Code != http.StatusOK || !strings.Contains(member.Body.String(), userID) {
		t.Fatalf("add member status=%d body=%s", member.Code, member.Body)
	}
	leaderPath := "/api/v1/admin/groups/" + groupID + "/leader"
	missingLeader := apiIntegrationRequest(t, r, http.MethodPost, leaderPath, `{}`, &adminCookie, csrf)
	if missingLeader.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing leader field status=%d body=%s", missingLeader.Code, missingLeader.Body)
	}
	setLeader := apiIntegrationRequest(t, r, http.MethodPost, leaderPath, `{"user_id":"`+userID+`"}`, &adminCookie, csrf)
	if setLeader.Code != http.StatusOK || !strings.Contains(setLeader.Body.String(), `"leader_user_id":"`+userID+`"`) {
		t.Fatalf("set leader status=%d body=%s", setLeader.Code, setLeader.Body)
	}
	clearLeader := apiIntegrationRequest(t, r, http.MethodPost, leaderPath, `{"user_id":""}`, &adminCookie, csrf)
	if clearLeader.Code != http.StatusOK || !strings.Contains(clearLeader.Body.String(), `"leader_user_id":""`) {
		t.Fatalf("clear leader status=%d body=%s", clearLeader.Code, clearLeader.Body)
	}
	setLeader = apiIntegrationRequest(t, r, http.MethodPost, leaderPath, `{"user_id":"`+userID+`"}`, &adminCookie, csrf)
	if setLeader.Code != http.StatusOK {
		t.Fatalf("restore leader status=%d body=%s", setLeader.Code, setLeader.Body)
	}
	for _, tc := range []struct{ path, body, want string }{
		{"/api/v1/admin/groups/" + groupID, `{"name":"renamed operators"}`, "renamed operators"},
		{"/api/v1/admin/groups/" + groupID + "/active", `{"active":false}`, `"active":false`},
		{"/api/v1/admin/groups/" + groupID + "/active", `{"active":true}`, `"active":true`},
	} {
		got := apiIntegrationRequest(t, r, http.MethodPost, tc.path, tc.body, &adminCookie, csrf)
		if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), tc.want) {
			t.Errorf("POST %s status=%d body=%s", tc.path, got.Code, got.Body)
		}
	}
	removedMember := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/groups/"+groupID+"/members/remove", `{"user_id":"`+userID+`"}`, &adminCookie, csrf)
	var memberSnapshot struct {
		Data struct {
			Memberships []json.RawMessage `json:"memberships"`
		} `json:"data"`
	}
	if removedMember.Code != http.StatusOK || json.Unmarshal(removedMember.Body.Bytes(), &memberSnapshot) != nil || len(memberSnapshot.Data.Memberships) != 0 {
		t.Fatalf("remove member status=%d body=%s", removedMember.Code, removedMember.Body)
	}
	if !strings.Contains(removedMember.Body.String(), `"leader_user_id":""`) {
		t.Fatalf("removed member remains leader: %s", removedMember.Body)
	}

	bad := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/users/"+userID, `{"username":""}`, &adminCookie, csrf)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation status=%d body=%s", bad.Code, bad.Body)
	}
	userCookie, userCSRF := apiIntegrationLogin(t, r, "renamed-user", "another sufficiently long password")
	denied := apiIntegrationRequest(t, r, http.MethodGet, "/api/v1/admin/users", "", &userCookie, userCSRF)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d body=%s", denied.Code, denied.Body)
	}
	denied = apiIntegrationRequest(t, r, http.MethodPost, leaderPath, `{"user_id":""}`, &userCookie, userCSRF)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin leader status=%d body=%s", denied.Code, denied.Body)
	}
	adminCookie, csrf = apiIntegrationLogin(t, r, "api-admin", "a sufficiently long admin password")
	self := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/users/"+adminID+"/admin", `{"admin":false}`, &adminCookie, csrf)
	if self.Code != http.StatusUnauthorized || !strings.Contains(self.Body.String(), "session_expired") {
		t.Fatalf("self-demotion status=%d body=%s", self.Code, self.Body)
	}
}

func apiIntegrationRequest(t *testing.T, r http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func apiIntegrationLogin(t *testing.T, r http.Handler, username, password string) (http.Cookie, string) {
	t.Helper()
	challenge := httptest.NewRecorder()
	r.ServeHTTP(challenge, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login-challenge", nil))
	var data struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if challenge.Code != http.StatusOK {
		t.Fatalf("challenge status=%d body=%s", challenge.Code, challenge.Body)
	}
	if err := json.Unmarshal(challenge.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", data.Data.CSRF)
	request.AddCookie(challenge.Result().Cookies()[0])
	result := httptest.NewRecorder()
	r.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", result.Code, result.Body)
	}
	var loggedIn struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &loggedIn); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range result.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return *cookie, loggedIn.Data.CSRF
		}
	}
	t.Fatalf("session cookie missing: %+v", result.Result().Cookies())
	return http.Cookie{}, ""
}
