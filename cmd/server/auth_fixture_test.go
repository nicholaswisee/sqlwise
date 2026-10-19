package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/html"
	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
	"sqlwise/internal/web/views"
)

type authFixture struct {
	router http.Handler
	pool   *pgxpool.Pool
	now    *time.Time
}

func newAuthFixture(t *testing.T) authFixture {
	t.Helper()
	pool := testdb.Open(t)
	store := auth.NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.ParseCredentials("admin", "integration test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	service, err := auth.NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return authFixture{router: newRouter(views.Assets{Dev: true}, service), pool: pool, now: &now}
}

type authRequest struct {
	method  string
	path    string
	form    url.Values
	cookies []*http.Cookie
	headers map[string]string
}

func (f authFixture) do(input authRequest) *httptest.ResponseRecorder {
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
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("missing %s cookie", name)
	return nil
}

func formCSRF(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(response.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	var visit func(*html.Node) string
	visit = func(node *html.Node) string {
		if node.Type == html.ElementNode && node.Data == "input" {
			name, value := "", ""
			for _, attr := range node.Attr {
				if attr.Key == "name" {
					name = attr.Val
				}
				if attr.Key == "value" {
					value = attr.Val
				}
			}
			if name == "csrf_token" {
				return value
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if token := visit(child); token != "" {
				return token
			}
		}
		return ""
	}
	value := visit(doc)
	if value == "" {
		t.Fatal("missing CSRF field")
	}
	return value
}

func (f authFixture) loginRequest(t *testing.T) authRequest {
	t.Helper()
	page := f.do(authRequest{method: "GET", path: "/login"})
	if page.Code != http.StatusOK {
		t.Fatalf("login page status = %d", page.Code)
	}
	return authRequest{method: "POST", path: "/login", cookies: []*http.Cookie{responseCookie(t, page, "sqlwise_login")},
		form: url.Values{"username": {"admin"}, "password": {"integration test passphrase"}, "csrf_token": {formCSRF(t, page)}}}
}
