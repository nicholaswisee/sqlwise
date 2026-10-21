package main

import (
	"net/http"
	"testing"
	"time"
)

func TestAccountReturnsWinningRotationCookie(t *testing.T) {
	f := newAuthFixture(t)
	login := f.do(f.loginRequest(t))
	cookie := responseCookie(t, login, "sqlwise_session")
	*f.now = f.now.Add(15 * time.Minute)
	response := f.do(authRequest{method: http.MethodGet, path: "/account", cookies: []*http.Cookie{cookie}})
	if response.Code != http.StatusOK {
		t.Fatalf("account = %d", response.Code)
	}
	if replacement := responseCookie(t, response, "sqlwise_session"); replacement.Value == cookie.Value {
		t.Fatal("rotation did not replace cookie")
	}
}
