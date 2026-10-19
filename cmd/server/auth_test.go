package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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
				*f.now = f.now.Add(10 * time.Minute)
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
