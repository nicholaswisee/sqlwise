package main

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestRegisteredProtectedRoutesRequireSession(t *testing.T) {
	f := newAuthFixture(t)
	public := map[string]bool{"GET /": true, "GET /health": true, "GET /login": true, "POST /login": true}
	seenRequired := map[string]bool{}
	invalid := &http.Cookie{Name: "sqlwise_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}

	for _, route := range f.router.Routes() {
		key := route.Method + " " + route.Path
		if public[key] || strings.HasPrefix(route.Path, "/assets/") {
			continue
		}
		if key == "GET /account" || key == "GET /account/session" || key == "POST /logout" {
			seenRequired[key] = true
		}
		for _, cookies := range [][]*http.Cookie{nil, {invalid}} {
			for _, hx := range []bool{false, true} {
				headers := map[string]string{}
				if hx {
					headers["HX-Request"] = "true"
				}
				response := f.do(authRequest{method: route.Method, path: route.Path, cookies: cookies, headers: headers})
				wantStatus, wantLocation := http.StatusSeeOther, "/login"
				if route.Method != http.MethodGet || hx {
					wantStatus = http.StatusUnauthorized
				}
				if response.Code != wantStatus {
					t.Errorf("%s HX=%t status = %d, want %d", key, hx, response.Code, wantStatus)
				}
				if location := response.Header().Get("Location"); wantStatus == http.StatusSeeOther && location != wantLocation {
					t.Errorf("%s redirect = %q, want %q", key, location, wantLocation)
				}
				if hx && response.Header().Get("HX-Redirect") != wantLocation {
					t.Errorf("%s HX redirect = %q, want %q", key, response.Header().Get("HX-Redirect"), wantLocation)
				}
				for _, cookie := range response.Result().Cookies() {
					if cookie.Name == "sqlwise_session" && cookie.Value != "" && cookie.MaxAge >= 0 {
						t.Errorf("%s issued an authenticated cookie", key)
					}
				}
			}
		}
	}
	for _, key := range []string{"GET /account", "GET /account/session", "POST /logout"} {
		if !seenRequired[key] {
			t.Errorf("required protected route %s was not registered", key)
		}
	}
}
