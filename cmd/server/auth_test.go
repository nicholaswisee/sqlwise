package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestLoginAndLogout(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	f := newAuthFixture(t)
	input := f.loginRequest(t)
	response := f.do(input)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/account" {
		t.Fatalf("login status = %d", response.Code)
	}
	cookie := responseCookie(t, response, "sqlwise_session")
	if cookie.Value == input.cookies[0].Value || len(cookie.Value) != 43 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("invalid session cookie")
	}
	if responseCookie(t, response, "sqlwise_login").MaxAge != -1 {
		t.Fatal("pre-login cookie not cleared")
	}
	page := f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}})
	if page.Code != 200 || !strings.Contains(page.Body.String(), "admin") {
		t.Fatal("authenticated account unavailable")
	}
	if page.Header().Get("Cache-Control") != "no-store" || strings.Contains(page.Body.String(), cookie.Value) {
		t.Fatal("session exposed or cached")
	}
	logout := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{cookie}, form: url.Values{"csrf_token": {formCSRF(t, page)}}})
	if logout.Code != 303 || responseCookie(t, logout, "sqlwise_session").MaxAge != -1 {
		t.Fatal("logout failed")
	}
	replay := f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}})
	if replay.Code != 303 || replay.Header().Get("Location") != "/login" {
		t.Fatal("logged-out token still authenticates")
	}
	if f.do(input).Code != 403 {
		t.Fatal("pre-login state replay succeeded")
	}
}

func TestLoginFailuresNeverIssueAuthenticatedCookies(t *testing.T) {
	f := newAuthFixture(t)
	for _, test := range []struct {
		name, username, password string
		inactive                 bool
		status                   int
	}{
		{"wrong password", "admin", "wrong password", false, 401},
		{"unknown user", "unknown", "integration test passphrase", false, 401},
		{"inactive user", "admin", "integration test passphrase", true, 401},
		{"invalid username", "<script>alert(1)</script>", "wrong password", false, 422},
		{"empty password", "admin", "", false, 422},
		{"long password", "admin", strings.Repeat("x", 129), false, 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := f.pool.Exec(t.Context(), "UPDATE users SET active = $1", !test.inactive); err != nil {
				t.Fatal(err)
			}
			input := f.loginRequest(t)
			input.form.Set("username", test.username)
			input.form.Set("password", test.password)
			response := f.do(input)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == "sqlwise_session" {
					t.Fatal("failed login issued auth cookie")
				}
			}
			if strings.Contains(response.Body.String(), "<script>alert(1)</script>") || (test.password != "" && strings.Contains(response.Body.String(), test.password)) {
				t.Fatal("response leaked input")
			}
			if test.status == 401 && !strings.Contains(response.Body.String(), "Unable to sign in with those credentials.") {
				t.Fatal("credential failure differs")
			}
		})
	}
}

func TestLoginRejectsCSRFAndCrossOriginRequests(t *testing.T) {
	f := newAuthFixture(t)
	for _, scenario := range []string{"missing", "forged", "other-session", "origin", "fetch-site", "expired", "query", "large", "wrong-content-type"} {
		t.Run(scenario, func(t *testing.T) {
			input := f.loginRequest(t)
			want := 403
			switch scenario {
			case "missing":
				input.form.Del("csrf_token")
			case "forged":
				input.form.Set("csrf_token", strings.Repeat("x", 43))
			case "other-session":
				input.form.Set("csrf_token", f.loginRequest(t).form.Get("csrf_token"))
			case "origin":
				input.headers = map[string]string{"Origin": "https://attacker.example"}
			case "fetch-site":
				input.headers = map[string]string{"Sec-Fetch-Site": "cross-site"}
			case "expired":
				f.now.Advance(10 * time.Minute)
			case "query":
				input.path += "?password=must-not-be-accepted"
				want = 400
			case "large":
				input.form.Set("password", strings.Repeat("x", 5000))
				want = 422
			case "wrong-content-type":
				input.headers = map[string]string{"Content-Type": "application/json"}
				want = 415
			}
			response := f.do(input)
			if response.Code != want {
				t.Fatalf("status = %d, want %d", response.Code, want)
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("rejected request issued a cookie")
			}
		})
	}
}

func TestAccountAndLogoutRequireSessionAndCSRF(t *testing.T) {
	f := newAuthFixture(t)
	if response := f.do(authRequest{method: "GET", path: "/account"}); response.Code != 303 {
		t.Fatal("anonymous account accepted")
	}
	if response := f.do(authRequest{method: "POST", path: "/logout"}); response.Code != 401 {
		t.Fatal("anonymous logout accepted")
	}
	response := f.do(f.loginRequest(t))
	cookie := responseCookie(t, response, "sqlwise_session")
	badLogout := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{cookie}})
	if badLogout.Code != 403 {
		t.Fatal("logout without CSRF accepted")
	}
	if f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}}).Code != 200 {
		t.Fatal("invalid logout revoked session")
	}
	if f.do(authRequest{method: "GET", path: "/logout", cookies: []*http.Cookie{cookie}}).Code != 404 {
		t.Fatal("GET logout route exposed")
	}
}

func TestSessionGuardDistinguishesPageAndHTMXAndAllowsQueries(t *testing.T) {
	f := newAuthFixture(t)
	page := f.do(authRequest{method: "GET", path: "/account?limit=20"})
	if page.Code != http.StatusSeeOther || page.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous page: status=%d location=%q", page.Code, page.Header().Get("Location"))
	}
	fragment := f.do(authRequest{method: "GET", path: "/account?limit=20", headers: map[string]string{"HX-Request": "true"}})
	if fragment.Code != http.StatusUnauthorized || fragment.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("anonymous fragment: status=%d HX-Redirect=%q", fragment.Code, fragment.Header().Get("HX-Redirect"))
	}
	login := f.do(f.loginRequest(t))
	cookie := responseCookie(t, login, "sqlwise_session")
	account := f.do(authRequest{method: "GET", path: "/account?limit=20", cookies: []*http.Cookie{cookie}})
	if account.Code != http.StatusOK {
		t.Fatalf("query rejected for protected session: %d", account.Code)
	}
}

func TestSessionGuardCSRFAndOriginAcrossUnsafeMethods(t *testing.T) {
	f := newAuthFixture(t)
	effects := 0
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		f.router.Handle(method, "/guard-test", f.guard.RequireSession(), func(c *gin.Context) { effects++; c.Status(http.StatusNoContent) })
	}
	login := f.do(f.loginRequest(t))
	cookie := responseCookie(t, login, "sqlwise_session")
	account := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
	csrf := formCSRF(t, account)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, test := range []struct {
			name    string
			headers map[string]string
			want    int
		}{
			{"valid", map[string]string{"X-CSRF-Token": csrf}, http.StatusNoContent},
			{"missing", nil, http.StatusForbidden},
			{"cross-origin", map[string]string{"X-CSRF-Token": csrf, "Origin": "https://attacker.example"}, http.StatusForbidden},
		} {
			t.Run(method+"/"+test.name, func(t *testing.T) {
				before := effects
				response := f.do(authRequest{method: method, path: "/guard-test", cookies: []*http.Cookie{cookie}, headers: test.headers})
				wantEffects := before
				if test.want == http.StatusNoContent {
					wantEffects++
				}
				if effects != wantEffects {
					t.Fatalf("effects=%d want %d", effects, wantEffects)
				}
				if response.Code != test.want {
					t.Fatalf("status=%d, want %d", response.Code, test.want)
				}
			})
		}
	}
}

func TestLoginStatusProbeDoesNotTouchOrRotateSession(t *testing.T) {
	f := newAuthFixture(t)
	login := f.do(f.loginRequest(t))
	cookie := responseCookie(t, login, "sqlwise_session")
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if _, err := f.pool.Exec(t.Context(), `UPDATE auth_sessions SET created_at=created_at-interval '16 minutes' WHERE token_hash=$1`, digest[:]); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT last_seen_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, digest[:]).Scan(&before); err != nil {
		t.Fatal(err)
	}
	response := f.do(authRequest{method: http.MethodGet, path: "/login", cookies: []*http.Cookie{cookie}})
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/account" {
		t.Fatalf("status probe=%d, location=%q", response.Code, response.Header().Get("Location"))
	}
	for _, setCookie := range response.Result().Cookies() {
		if setCookie.Name == "sqlwise_session" {
			t.Fatal("status probe rotated authenticated session")
		}
	}
	var after time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT last_seen_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, digest[:]).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("status probe touched idle activity: before=%s after=%s", before, after)
	}
}
