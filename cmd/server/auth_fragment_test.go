package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestAccountHTMXLogoutUsesSessionCSRFAndNeverRendersCookie(t *testing.T) {
	f := newAuthFixture(t)
	login := f.do(f.loginRequest(t))
	cookie := responseCookie(t, login, "sqlwise_session")
	account := f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}})
	csrf := formCSRF(t, account)
	if strings.Contains(account.Body.String(), cookie.Value) {
		t.Fatal("account exposed session cookie")
	}
	var headers string
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "hx-headers" {
					headers = a.Val
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			find(child)
		}
	}
	doc, err := html.Parse(strings.NewReader(account.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	find(doc)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(headers), &parsed); err != nil || parsed["X-CSRF-Token"] != csrf {
		t.Fatalf("HX headers=%q, want session CSRF %q (err %v)", headers, csrf, err)
	}
	logout := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{cookie}, headers: map[string]string{"HX-Request": "true", "X-CSRF-Token": csrf}})
	if logout.Code != http.StatusNoContent || logout.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("HX logout status=%d redirect=%q", logout.Code, logout.Header().Get("HX-Redirect"))
	}
	cleared := false
	for _, c := range logout.Result().Cookies() {
		if c.Name == "sqlwise_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("HX logout did not clear session cookie")
	}
	var revoked bool
	if err := f.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, cookieDigest(t, cookie)).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("HX logout did not revoke session family")
	}
}

func TestAccountSessionFragmentAndNativeNavigation(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	fragment := f.do(authRequest{method: "GET", path: "/account/session", cookies: []*http.Cookie{cookie}, headers: map[string]string{"HX-Request": "true"}})
	if fragment.Code != http.StatusOK || strings.Contains(fragment.Body.String(), "<!DOCTYPE html>") || !strings.Contains(fragment.Body.String(), "Session active") {
		t.Fatalf("fragment status=%d body=%q", fragment.Code, fragment.Body.String())
	}
	direct := f.do(authRequest{method: "GET", path: "/account/session", cookies: []*http.Cookie{cookie}})
	if direct.Code != http.StatusSeeOther || direct.Header().Get("Location") != "/account" {
		t.Fatalf("direct fragment navigation status=%d location=%q", direct.Code, direct.Header().Get("Location"))
	}
	account := f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}})
	if !strings.Contains(account.Body.String(), "hx-get=\"/account/session\"") || !strings.Contains(account.Body.String(), "Session active") {
		t.Fatal("account missing accessible session refresh")
	}
	bad := f.do(authRequest{method: "GET", path: "/account/session", headers: map[string]string{"HX-Request": "true"}})
	if bad.Code != http.StatusUnauthorized || bad.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("anonymous fragment status=%d redirect=%q", bad.Code, bad.Header().Get("HX-Redirect"))
	}
}

func TestLogoutRejectsMissingCrossSessionAndCrossOriginCSRFWithoutRevoking(t *testing.T) {
	f := newAuthFixture(t)
	first := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	second := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	firstCSRF := formCSRF(t, f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{first}}))
	secondCSRF := formCSRF(t, f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{second}}))
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{name: "missing token", headers: map[string]string{"HX-Request": "true"}},
		{name: "cross-session token, no Origin", headers: map[string]string{"HX-Request": "true", "X-CSRF-Token": secondCSRF}},
		{name: "cross-origin with valid session token", headers: map[string]string{"HX-Request": "true", "X-CSRF-Token": firstCSRF, "Origin": "https://attacker.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{first}, headers: tc.headers})
			if response.Code != http.StatusForbidden || len(response.Result().Cookies()) != 0 {
				t.Fatalf("invalid HX logout status=%d cookies=%v", response.Code, response.Result().Cookies())
			}
			var revoked bool
			if err := f.pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, cookieDigest(t, first)).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if revoked {
				t.Fatal("rejected logout revoked session family")
			}
		})
	}
	native := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{first}, form: url.Values{"csrf_token": {firstCSRF}}})
	if native.Code != http.StatusSeeOther || native.Header().Get("Location") != "/login" {
		t.Fatalf("native logout status=%d location=%q", native.Code, native.Header().Get("Location"))
	}
}
