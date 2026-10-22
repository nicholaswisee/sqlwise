package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/auth"
	"sqlwise/internal/handlers"
	"sqlwise/internal/web/views"
)

func cookieDigest(t *testing.T, cookie *http.Cookie) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return digest[:]
}

func TestPublicGuardNativeFormsAndRejectionEffects(t *testing.T) {
	f := newAuthFixture(t)
	effects := 0
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		f.router.Handle(method, "/native", f.guard.RequireSession(), func(c *gin.Context) { effects++; c.Status(204) })
	}
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	csrf := formCSRF(t, f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}}))
	other := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	otherCSRF := formCSRF(t, f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{other}}))
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, tc := range []struct {
			name, token, origin, query string
			native                     bool
			want                       int
		}{
			{name: "header", token: csrf, want: 204},
			{name: "native", token: csrf, native: true, want: 204},
			{name: "missing", want: 403},
			{name: "wrong", token: strings.Repeat("x", 43), want: 403},
			{name: "cross-session", token: otherCSRF, want: 403},
			{name: "query", query: "?csrf_token=" + csrf, want: 403},
			{name: "origin", token: csrf, origin: "https://attacker.example", want: 403},
		} {
			if method == "DELETE" && tc.native {
				continue
			}
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				before := effects
				input := authRequest{method: method, path: "/native" + tc.query, cookies: []*http.Cookie{cookie}, headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}
				if tc.native {
					input.form = url.Values{"csrf_token": {tc.token}, "sql": {strings.Repeat("s", 8192)}}
				} else {
					input.headers["X-CSRF-Token"] = tc.token
					input.headers["HX-Request"] = "true"
				}
				input.headers["Origin"] = tc.origin
				response := f.do(input)
				wantEffects := before
				if tc.want == 204 {
					wantEffects++
				}
				if effects != wantEffects {
					t.Errorf("business effects=%d, want %d (status %d)", effects, wantEffects, response.Code)
				}
				if response.Code != tc.want {
					t.Errorf("status=%d want %d", response.Code, tc.want)
				}
				if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("Referrer-Policy") != "no-referrer" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Error("standalone guard omitted security headers")
				}
				if tc.want != 204 && len(response.Result().Cookies()) != 0 {
					t.Error("rejection changed cookies")
				}
			})
		}
	}
}

func TestFailedCSRFDoesNotTouchOrRotate(t *testing.T) {
	f := newAuthFixture(t)
	effects := 0
	f.router.POST("/effect", f.guard.RequireSession(), func(c *gin.Context) { effects++ })
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	f.now.Advance(auth.RotationInterval)
	for _, headers := range []map[string]string{nil, {"Origin": "https://attacker.example"}} {
		response := f.do(authRequest{method: "POST", path: "/effect", cookies: []*http.Cookie{cookie}, headers: headers})
		var seen time.Time
		var rotated *time.Time
		if err := f.pool.QueryRow(t.Context(), `SELECT f.last_seen_at,s.rotated_at FROM auth_sessions s JOIN auth_session_families f ON f.id=s.family_id WHERE s.token_hash=$1`, cookieDigest(t, cookie)).Scan(&seen, &rotated); err != nil {
			t.Fatal(err)
		}
		if effects != 0 || rotated != nil || !seen.Equal(f.now.Now().Add(-auth.RotationInterval)) || response.Code != 403 {
			t.Fatalf("effects=%d rotated=%v lastSeen=%v status=%d", effects, rotated, seen, response.Code)
		}
	}
}

type failingRandom struct{}

func (failingRandom) Read([]byte) (int, error) { return 0, errors.New("random unavailable") }

func TestHTTPLogoutDoesNotNeedRotationRandomness(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	csrf := formCSRF(t, f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}}))
	svc, err := auth.NewServiceWithRandom(auth.NewStore(f.pool), f.now.Now, failingRandom{})
	if err != nil {
		t.Fatal(err)
	}
	f.router = newRouter(views.Assets{Dev: true}, svc)
	f.now.Advance(auth.RotationInterval)
	response := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{cookie}, form: url.Values{"csrf_token": {csrf}}})
	var revoked *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT revoked_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, cookieDigest(t, cookie)).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if response.Code != 303 || revoked == nil {
		t.Fatalf("logout status=%d revoked=%v", response.Code, revoked)
	}
}

func TestStaleEpochWinnerCookieHandoff(t *testing.T) {
	f := newAuthFixture(t)
	effects := 0
	f.router.GET("/admin-proof", f.guard.RequireSession(), f.guard.RequirePlatformAdmin(), func(c *gin.Context) { effects++; c.Status(204) })
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	absolute := f.now.Now().Add(16 * time.Minute)
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE auth_session_families SET absolute_expires_at=$1`, absolute); err != nil {
		t.Fatal(err)
	}
	f.now.Advance(auth.RotationInterval)
	response := f.do(authRequest{method: "GET", path: "https://example.com/admin-proof", cookies: []*http.Cookie{cookie}})
	if response.Code != 403 || effects != 0 {
		t.Fatalf("stale proof status=%d effects=%d", response.Code, effects)
	}
	next := responseCookie(t, response, "sqlwise_session")
	if next.Value == cookie.Value || !next.Expires.Equal(absolute) || !next.HttpOnly || !next.Secure || next.Domain != "" || next.Path != "/" || next.SameSite != http.SameSiteLaxMode || next.MaxAge != 0 {
		t.Fatalf("invalid winning cookie: %+v", next)
	}
	alias := f.do(authRequest{method: "GET", path: "/admin-proof", cookies: []*http.Cookie{cookie}})
	if alias.Code != 403 || effects != 0 || len(alias.Result().Cookies()) != 0 {
		t.Fatal("alias upgraded or emitted cookie")
	}
	response = f.do(authRequest{method: "GET", path: "/admin-proof", cookies: []*http.Cookie{next}})
	if response.Code != 204 || effects != 1 {
		t.Fatalf("successor status=%d effects=%d", response.Code, effects)
	}
}

func TestAdminGuardUsesBoundProofNotCookie(t *testing.T) {
	f := newAuthFixture(t)
	effects := 0
	f.router.GET("/bound", f.guard.RequireSession(), func(c *gin.Context) { c.Request.Header.Del("Cookie") }, f.guard.RequirePlatformAdmin(), func(c *gin.Context) { effects++; c.Status(204) })
	f.router.GET("/unbound", f.guard.RequirePlatformAdmin(), func(c *gin.Context) { effects++; c.Status(204) })
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	response := f.do(authRequest{method: "GET", path: "/bound", cookies: []*http.Cookie{cookie}})
	if response.Code != 204 || effects != 1 {
		t.Errorf("bound proof lost: status=%d effects=%d", response.Code, effects)
	}
	before := effects
	response = f.do(authRequest{method: "GET", path: "/unbound", cookies: []*http.Cookie{cookie}})
	if response.Code != 303 || effects != before {
		t.Errorf("unbound proof accepted: status=%d effects=%d", response.Code, effects)
	}
}

func TestGuardFreshBackendFailureIs503AndAborts(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	calls := 0
	svc, err := auth.NewService(auth.NewStore(f.pool), func() time.Time {
		calls++
		if calls == 2 {
			if _, err := f.pool.Exec(t.Context(), `ALTER TABLE auth_sessions RENAME TO unavailable_sessions`); err != nil {
				t.Fatal(err)
			}
		}
		return f.now.Now()
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := handlers.NewAuth(svc, views.Assets{Dev: true}, false)
	effects := 0
	f.router.GET("/backend", guard.RequireSession(), func(c *gin.Context) { effects++ })
	req := httptest.NewRequest("GET", "/backend", nil)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, req)
	if response.Code != 503 || effects != 0 || len(response.Result().Cookies()) != 0 {
		t.Fatalf("backend failure status=%d effects=%d cookies=%v", response.Code, effects, response.Result().Cookies())
	}
}

func TestRotationWinnerSurvivesFreshBackendFailure(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	f.now.Advance(auth.RotationInterval)
	calls := 0
	svc, err := auth.NewService(auth.NewStore(f.pool), func() time.Time {
		calls++
		// The second clock sample is fresh validation, after Session commits rotation.
		if calls == 2 {
			if _, err := f.pool.Exec(t.Context(), `ALTER TABLE auth_sessions RENAME TO unavailable_sessions`); err != nil {
				t.Fatal(err)
			}
		}
		return f.now.Now()
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := handlers.NewAuth(svc, views.Assets{Dev: true}, false)
	effects := 0
	f.router.GET("/rotation-backend", guard.RequireSession(), func(c *gin.Context) { effects++; c.Status(http.StatusNoContent) })
	response := f.do(authRequest{method: "GET", path: "/rotation-backend", cookies: []*http.Cookie{cookie}})
	if response.Code != http.StatusServiceUnavailable || effects != 0 {
		t.Fatalf("fresh failure status=%d effects=%d", response.Code, effects)
	}
	next := responseCookie(t, response, "sqlwise_session")
	if next.Value == cookie.Value || next.MaxAge < 0 {
		t.Fatal("fresh failure did not preserve the winning replacement")
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE unavailable_sessions RENAME TO auth_sessions`); err != nil {
		t.Fatal(err)
	}
	alias := f.do(authRequest{method: "GET", path: "/rotation-backend", cookies: []*http.Cookie{cookie}})
	if alias.Code != http.StatusNoContent || effects != 1 || len(alias.Result().Cookies()) != 0 {
		t.Fatalf("alias status=%d effects=%d cookies=%v", alias.Code, effects, alias.Result().Cookies())
	}
	f.now.Advance(auth.AliasGrace)
	expired := f.do(authRequest{method: "GET", path: "/rotation-backend", cookies: []*http.Cookie{cookie}})
	if expired.Code != http.StatusSeeOther || effects != 1 || len(expired.Result().Cookies()) != 0 {
		t.Fatalf("expired alias status=%d effects=%d cookies=%v", expired.Code, effects, expired.Result().Cookies())
	}
	retry := f.do(authRequest{method: "GET", path: "/rotation-backend", cookies: []*http.Cookie{next}})
	if retry.Code != http.StatusNoContent || effects != 2 || len(retry.Result().Cookies()) != 0 {
		t.Fatalf("recovered successor status=%d effects=%d cookies=%v", retry.Code, effects, retry.Result().Cookies())
	}
}

func TestGuardAnonymousAndInvalidSessionAbort(t *testing.T) {
	f := newAuthFixture(t)
	effects := 0
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		f.router.Handle(method, "/anonymous", f.guard.RequireSession(), func(c *gin.Context) { effects++; c.Status(204) })
		for _, htmx := range []bool{false, true} {
			for _, token := range []string{"", "invalid", strings.Repeat("x", 43)} {
				headers := map[string]string{}
				if htmx {
					headers["HX-Request"] = "true"
				}
				response := f.do(authRequest{method: method, path: "/anonymous", headers: headers, cookies: []*http.Cookie{{Name: "sqlwise_session", Value: token}}})
				want := 401
				if method == "GET" && !htmx {
					want = 303
				}
				if effects != 0 || response.Code != want || len(response.Result().Cookies()) != 0 {
					t.Fatalf("method=%s htmx=%v status=%d effects=%d", method, htmx, response.Code, effects)
				}
				if htmx && response.Header().Get("HX-Redirect") != "/login" {
					t.Fatal("missing fragment navigation")
				}
			}
		}
	}
}

func TestGuardMalformedAndOversizedFormsAbort(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	effects := 0
	f.router.POST("/body-limit", f.guard.RequireSession(), func(c *gin.Context) { effects++ })
	for _, body := range []string{"csrf_token=%zz", "csrf_token=" + strings.Repeat("s", 1<<20)} {
		req := httptest.NewRequest("POST", "/body-limit", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, req)
		if effects != 0 || response.Code != 403 || len(response.Result().Cookies()) != 0 {
			t.Fatalf("bad form status=%d effects=%d", response.Code, effects)
		}
	}
}

func TestAdminFreshValidationRejectionEffects(t *testing.T) {
	for _, scenario := range []string{"role", "epoch", "revoke", "backend"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAuthFixture(t)
			cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
			effects := 0
			f.router.GET("/admin-fresh", f.guard.RequireSession(), func(c *gin.Context) {
				var err error
				switch scenario {
				case "role":
					_, err = f.pool.Exec(t.Context(), `UPDATE users SET is_platform_admin=false`)
				case "epoch":
					_, err = f.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1`)
				case "revoke":
					_, err = f.pool.Exec(t.Context(), `UPDATE auth_session_families SET revoked_at=$1`, f.now.Now())
				case "backend":
					_, err = f.pool.Exec(t.Context(), `ALTER TABLE auth_sessions RENAME TO unavailable_sessions`)
				}
				if err != nil {
					t.Fatal(err)
				}
			}, f.guard.RequirePlatformAdmin(), func(c *gin.Context) { effects++ })
			response := f.do(authRequest{method: "GET", path: "/admin-fresh", cookies: []*http.Cookie{cookie}})
			want := 403
			if scenario == "revoke" {
				want = 303
			}
			if scenario == "backend" {
				want = 503
			}
			if response.Code != want || effects != 0 || len(response.Result().Cookies()) != 0 {
				t.Fatalf("fresh %s status=%d effects=%d cookies=%v", scenario, response.Code, effects, response.Result().Cookies())
			}
		})
	}
}

func TestStaleEpochAccountAndLogoutRemainAvailable(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1`); err != nil {
		t.Fatal(err)
	}
	account := f.do(authRequest{method: "GET", path: "/account", cookies: []*http.Cookie{cookie}})
	if account.Code != 200 {
		t.Fatalf("stale identity account status=%d", account.Code)
	}
	response := f.do(authRequest{method: "POST", path: "/logout", cookies: []*http.Cookie{cookie}, form: url.Values{"csrf_token": {formCSRF(t, account)}}})
	var revoked *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT revoked_at FROM auth_session_families WHERE id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1)`, cookieDigest(t, cookie)).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if response.Code != 303 || revoked == nil {
		t.Fatalf("stale logout status=%d revoked=%v", response.Code, revoked)
	}
}

func TestCancelledStorageFailureKeeps503AndAborts(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	svc, err := auth.NewService(auth.NewStore(f.pool), func() time.Time {
		calls++
		if calls == 2 {
			cancel()
		}
		return f.now.Now()
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := handlers.NewAuth(svc, views.Assets{Dev: true}, false)
	effects := 0
	f.router.GET("/cancelled-backend", guard.RequireSession(), func(c *gin.Context) { effects++ })
	req := httptest.NewRequest("GET", "/cancelled-backend", nil).WithContext(ctx)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, req)
	if response.Code != 503 || effects != 0 || len(response.Result().Cookies()) != 0 {
		t.Fatalf("cancelled storage status=%d effects=%d", response.Code, effects)
	}
}

func TestUnavailableSessionBackendAborts(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	effects := 0
	f.router.GET("/unavailable", f.guard.RequireSession(), func(c *gin.Context) { effects++ })
	f.pool.Close()
	response := f.do(authRequest{method: "GET", path: "/unavailable", cookies: []*http.Cookie{cookie}})
	if response.Code != 503 || effects != 0 || len(response.Result().Cookies()) != 0 {
		t.Fatalf("unavailable backend status=%d effects=%d", response.Code, effects)
	}
}

func TestSessionFreshRevocationAborts(t *testing.T) {
	f := newAuthFixture(t)
	cookie := responseCookie(t, f.do(f.loginRequest(t)), "sqlwise_session")
	calls := 0
	svc, err := auth.NewService(auth.NewStore(f.pool), func() time.Time {
		calls++
		if calls == 2 {
			if _, err := f.pool.Exec(t.Context(), `UPDATE auth_session_families SET revoked_at=$1`, f.now.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return f.now.Now()
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := handlers.NewAuth(svc, views.Assets{Dev: true}, false)
	effects := 0
	f.router.GET("/revoked-fresh", guard.RequireSession(), func(c *gin.Context) { effects++ })
	response := f.do(authRequest{method: "GET", path: "/revoked-fresh", cookies: []*http.Cookie{cookie}})
	if response.Code != 303 || effects != 0 || len(response.Result().Cookies()) != 0 {
		t.Fatalf("revoked fresh status=%d effects=%d", response.Code, effects)
	}
}
