package main

import (
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionCookiePolicyAcrossEnvironmentAndTransport(t *testing.T) {
	for _, tc := range []struct {
		name, environment string
		tls, secure       bool
	}{
		{"development-http", "development", false, false},
		{"production-http", "production", false, true},
		{"development-https", "development", true, true},
		{"production-https", "production", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.environment)
			f := newAuthFixture(t)
			input := f.loginRequest(t)
			response := serveAuthRequest(f, input, tc.tls)
			if response.Code != http.StatusSeeOther {
				t.Fatalf("login status=%d", response.Code)
			}
			cookie := responseCookie(t, response, "sqlwise_session")
			decoded, err := base64.RawURLEncoding.Strict().DecodeString(cookie.Value)
			if err != nil || len(decoded) != 32 {
				t.Fatalf("session token is not strict unpadded base64url for 32 bytes: %v", err)
			}
			if cookie.Name != "sqlwise_session" || cookie.Path != "/" || cookie.Domain != "" || !cookie.HttpOnly || cookie.Secure != tc.secure || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("session cookie policy: %#v", cookie)
			}
			if cookie.Value == input.cookies[0].Value || strings.Contains(response.Body.String(), cookie.Value) {
				t.Fatal("login reused pre-login state or exposed session token in HTML")
			}
			page := serveAuthRequest(f, authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}}, tc.tls)
			if page.Code != http.StatusOK || strings.Contains(page.Body.String(), cookie.Value) {
				t.Fatal("session unavailable or exposed in account HTML")
			}
			f.now.Advance(15 * time.Minute)
			rotated := serveAuthRequest(f, authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}}, tc.tls)
			replacement := responseCookie(t, rotated, "sqlwise_session")
			if replacement.Value == cookie.Value || replacement.Path != cookie.Path || replacement.Domain != cookie.Domain || replacement.HttpOnly != cookie.HttpOnly || replacement.Secure != cookie.Secure || replacement.SameSite != cookie.SameSite || !replacement.Expires.Equal(cookie.Expires) {
				t.Fatalf("rotation changed cookie policy or absolute expiry: old=%#v new=%#v", cookie, replacement)
			}
		})
	}
}

func TestAliasAndMissingSessionDoNotIssueSessionCookie(t *testing.T) {
	f := newAuthFixture(t)
	login := f.do(f.loginRequest(t))
	cookie := responseCookie(t, login, "sqlwise_session")
	f.now.Advance(15 * time.Minute)
	if response := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}}); response.Code != http.StatusOK {
		t.Fatalf("rotation status=%d", response.Code)
	}
	alias := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
	if alias.Code != http.StatusOK {
		t.Fatalf("alias status=%d", alias.Code)
	}
	assertNoSessionCookie(t, alias)
	f.now.Advance(30 * time.Second)
	failed := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
	if failed.Code != http.StatusSeeOther {
		t.Fatalf("expired alias status=%d", failed.Code)
	}
	assertNoSessionCookie(t, failed)
	missing := f.do(authRequest{method: http.MethodGet, path: "/account"})
	assertNoSessionCookie(t, missing)
}

func serveAuthRequest(f authFixture, input authRequest, useTLS bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(input.method, input.path, strings.NewReader(input.form.Encode()))
	if input.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for key, value := range input.headers {
		req.Header.Set(key, value)
	}
	for _, cookie := range input.cookies {
		req.AddCookie(cookie)
	}
	if useTLS {
		req.TLS = &tls.ConnectionState{}
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func assertNoSessionCookie(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "sqlwise_session" {
			t.Fatalf("unexpected session cookie: %#v", cookie)
		}
	}
}
