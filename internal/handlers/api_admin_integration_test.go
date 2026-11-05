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

	bad := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/users/"+userID, `{"username":""}`, &adminCookie, csrf)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation status=%d body=%s", bad.Code, bad.Body)
	}
	userCookie, userCSRF := apiIntegrationLogin(t, r, "renamed-user", "a sufficiently long user password")
	denied := apiIntegrationRequest(t, r, http.MethodGet, "/api/v1/admin/users", "", &userCookie, userCSRF)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d body=%s", denied.Code, denied.Body)
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
