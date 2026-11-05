package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

func TestAPIDecodeRequiresSingleJSONValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"valid", `{"username":"u"}`, "application/json", http.StatusNoContent},
		{"malformed", `{"username":`, "application/json", http.StatusBadRequest},
		{"extra value", `{} {}`, "application/json", http.StatusBadRequest},
		{"unknown field", `{"unknown":true}`, "application/json", http.StatusBadRequest},
		{"wrong content type", `{}`, "text/plain", http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.POST("/", func(c *gin.Context) {
				var input struct {
					Username string `json:"username"`
				}
				if apiDecode(c, &input) {
					c.Status(http.StatusNoContent)
				}
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body, tc.status)
			}
			if tc.status != http.StatusNoContent && !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("non-JSON error: %s", w.Header().Get("Content-Type"))
			}
			if tc.status != http.StatusNoContent && !strings.Contains(w.Body.String(), `"fields":{}`) {
				t.Fatalf("error fields missing or non-empty: %s", w.Body)
			}
		})
	}
}

func TestAPISessionIncludesNeedsRefreshWithoutPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Auth{access: nil}
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		c.Set("auth_session", auth.Session{})
		h.apiSession(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var response struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if value, ok := response.Data["needs_refresh"]; !ok || value != false {
		t.Fatalf("needs_refresh=%v present=%t body=%s", value, ok, w.Body)
	}
}

func TestAPIAuthChallengeLoginSessionLogout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := testdb.Open(t)
	store := auth.NewStore(pool)
	if err := store.ApplySchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.ParseCredentials("api-user", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	service, err := auth.NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	h := NewAuth(service, views.Assets{}, false)
	r := gin.New()
	h.RegisterAPI(r)
	r.POST("/api/v1/mutation", h.RequireAPISession(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/api/v1/db/:database", h.RequireAPISession(), h.RequireAPIDatabasePermission(&permissionSpy{err: access.ErrPermissionDenied}, "database", access.View), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	challengeResponse := httptest.NewRecorder()
	r.ServeHTTP(challengeResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login-challenge", nil))
	if challengeResponse.Code != http.StatusOK {
		t.Fatalf("challenge status=%d body=%s", challengeResponse.Code, challengeResponse.Body)
	}
	var challenge struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	cookie := challengeResponse.Result().Cookies()[0]
	if cookie.Name != loginCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Secure {
		t.Fatalf("challenge cookie flags: %+v", cookie)
	}
	if strings.Contains(challengeResponse.Body.String(), cookie.Value) {
		t.Fatal("login challenge cookie leaked into JSON")
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"api-user","password":"a sufficiently long password"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-CSRF-Token", challenge.Data.CSRF)
	login.AddCookie(cookie)
	badOrigin := login.Clone(context.Background())
	badOrigin.Header.Set("Origin", "https://attacker.invalid")
	badOriginResponse := httptest.NewRecorder()
	r.ServeHTTP(badOriginResponse, badOrigin)
	if badOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d body=%s", badOriginResponse.Code, badOriginResponse.Body)
	}
	now = now.Add(auth.LoginLifetime + time.Second)
	expiredResponse := httptest.NewRecorder()
	r.ServeHTTP(expiredResponse, login)
	if expiredResponse.Code != http.StatusForbidden {
		t.Fatalf("expired challenge status=%d body=%s", expiredResponse.Code, expiredResponse.Body)
	}
	challengeResponse = httptest.NewRecorder()
	r.ServeHTTP(challengeResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login-challenge", nil))
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &challenge); err != nil {
		t.Fatal(err)
	}
	cookie = challengeResponse.Result().Cookies()[0]
	login = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"api-user","password":"a sufficiently long password"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-CSRF-Token", challenge.Data.CSRF)
	login.AddCookie(cookie)
	loginResponse := httptest.NewRecorder()
	r.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body)
	}
	var loggedIn struct {
		Data struct {
			User struct {
				ID, Username string
				Admin        bool `json:"is_platform_admin"`
			} `json:"user"`
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loggedIn); err != nil {
		t.Fatal(err)
	}
	if loggedIn.Data.User.Username != "api-user" || !loggedIn.Data.User.Admin || loggedIn.Data.User.ID == "" {
		t.Fatalf("unsafe or missing principal: %+v", loggedIn.Data.User)
	}
	issuedCookie := loginResponse.Result().Cookies()[0]
	if issuedCookie.Name != sessionCookie || !issuedCookie.HttpOnly {
		t.Fatalf("missing opaque session cookie: %+v", issuedCookie)
	}
	if strings.Contains(loginResponse.Body.String(), issuedCookie.Value) {
		t.Fatal("session cookie leaked into JSON")
	}
	denied := httptest.NewRequest(http.MethodGet, "/api/v1/db/target", nil)
	denied.AddCookie(issuedCookie)
	deniedResponse := httptest.NewRecorder()
	r.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || !strings.Contains(deniedResponse.Header().Get("Content-Type"), "application/json") || !strings.Contains(deniedResponse.Body.String(), `"code":"permission_denied"`) {
		t.Fatalf("database denial status=%d content-type=%q body=%s", deniedResponse.Code, deniedResponse.Header().Get("Content-Type"), deniedResponse.Body)
	}
	now = now.Add(auth.RotationInterval + time.Second)
	badMutation := httptest.NewRequest(http.MethodPost, "/api/v1/mutation", nil)
	badMutation.AddCookie(issuedCookie)
	badMutation.Header.Set("X-CSRF-Token", "invalid")
	rawToken, err := base64.RawURLEncoding.DecodeString(issuedCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256(rawToken)
	var activityBefore time.Time
	if err := pool.QueryRow(context.Background(), `SELECT last_seen_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, tokenHash[:]).Scan(&activityBefore); err != nil {
		t.Fatal(err)
	}
	badMutationResponse := httptest.NewRecorder()
	r.ServeHTTP(badMutationResponse, badMutation)
	if badMutationResponse.Code != http.StatusForbidden || len(badMutationResponse.Result().Cookies()) != 0 {
		t.Fatalf("failed CSRF status=%d cookies=%+v body=%s", badMutationResponse.Code, badMutationResponse.Result().Cookies(), badMutationResponse.Body)
	}
	var activityAfter time.Time
	if err := pool.QueryRow(context.Background(), `SELECT last_seen_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, tokenHash[:]).Scan(&activityAfter); err != nil {
		t.Fatal(err)
	}
	if !activityAfter.Equal(activityBefore) {
		t.Fatalf("failed CSRF touched session: before=%s after=%s", activityBefore, activityAfter)
	}
	mutation := httptest.NewRequest(http.MethodPost, "/api/v1/mutation", nil)
	mutation.AddCookie(issuedCookie)
	mutation.Header.Set("X-CSRF-Token", loggedIn.Data.CSRF)
	mutationResponse := httptest.NewRecorder()
	r.ServeHTTP(mutationResponse, mutation)
	if mutationResponse.Code != http.StatusNoContent {
		t.Fatalf("mutation status=%d body=%s", mutationResponse.Code, mutationResponse.Body)
	}
	if err := pool.QueryRow(context.Background(), `SELECT last_seen_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, tokenHash[:]).Scan(&activityAfter); err != nil {
		t.Fatal(err)
	}
	if delta := activityAfter.Sub(now); delta < -time.Microsecond || delta > time.Microsecond {
		t.Fatalf("successful mutation did not touch session: got=%s want=%s", activityAfter, now)
	}
	replacement := mutationResponse.Result().Cookies()
	if len(replacement) == 0 || replacement[0].Name != sessionCookie || replacement[0].Value == issuedCookie.Value {
		t.Fatalf("unsafe API rotation did not return replacement cookie: %+v", replacement)
	}
	aliasReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	aliasReq.AddCookie(issuedCookie)
	aliasResponse := httptest.NewRecorder()
	r.ServeHTTP(aliasResponse, aliasReq)
	if aliasResponse.Code != http.StatusOK || len(aliasResponse.Result().Cookies()) != 0 {
		t.Fatalf("grace alias response status=%d cookies=%+v body=%s", aliasResponse.Code, aliasResponse.Result().Cookies(), aliasResponse.Body)
	}
	now = now.Add(auth.RotationInterval + time.Second)
	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.AddCookie(replacement[0])
	logout.Header.Set("X-CSRF-Token", loggedIn.Data.CSRF)
	logoutResponse := httptest.NewRecorder()
	r.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logoutResponse.Code, logoutResponse.Body)
	}
	if cookies := logoutResponse.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout should only expire the family cookie, got %+v", cookies)
	}
	afterLogout := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	afterLogout.AddCookie(replacement[0])
	afterLogoutResponse := httptest.NewRecorder()
	r.ServeHTTP(afterLogoutResponse, afterLogout)
	if afterLogoutResponse.Code != http.StatusUnauthorized || !strings.Contains(afterLogoutResponse.Body.String(), `"code":"session_expired"`) {
		t.Fatalf("logout did not revoke session family: status=%d body=%s", afterLogoutResponse.Code, afterLogoutResponse.Body)
	}
}

func TestAPIAuthRequiresSessionUsesJSONInsteadOfRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// No cookie exercises the unauthenticated API boundary without a database.
	h := &Auth{}
	r := gin.New()
	r.GET("/api", h.RequireAPISession(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api", nil))
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Header().Get("Content-Type"), "text/html") || strings.Contains(w.Header().Get("Location"), "/login") {
		t.Fatalf("status=%d content-type=%q location=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Location"), w.Body)
	}
}
