package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSPAStaticServing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"_shell.html":       "<!doctype html><html><body>public shell</body></html>",
		"assets/app-123.js": "console.log('sqlwise')",
		"private.go":        "private source must not be published",
		"assets/app.js.map": "private source map",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	if err := registerSPA(router, root); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path string
		status       int
		shell        bool
	}{
		{"GET", "/", 200, true},
		{"GET", "/login", 200, true},
		{"GET", "/admin/groups/a/members", 200, true},
		{"GET", "/databases/a/migrations/b?schema=public", 200, true},
		{"GET", "/not-a-page", 200, true},
		{"HEAD", "/databases/a", 200, false},
		{"GET", "/assets/app-123.js", 200, false},
		{"HEAD", "/assets/app-123.js", 200, false},
		{"GET", "/assets/missing.js", 404, false},
		{"GET", "/assets", 404, false},
		{"GET", "/assets/", 404, false},
		{"GET", "/assets/app.js.map", 404, false},
		{"GET", "/assets/../_shell.html", 404, false},
		{"GET", "/assets/%2e%2e/_shell.html", 404, false},
		{"GET", "/private.go", 404, false},
		{"GET", "/.env", 404, false},
		{"GET", "/.vite/manifest.json", 404, false},
		{"GET", "/_shell.html", 404, false},
		{"GET", "/missing.css", 404, false},
		{"GET", "/api/v1/missing", 404, false},
		{"GET", "/api/v2/missing", 404, false},
		{"GET", "/api", 404, false},
		{"POST", "/login", 405, false},
		{"POST", "/assets/app-123.js", 405, false},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(test.method, test.path, nil))
			if res.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", res.Code, test.status, res.Body.String())
			}
			if strings.Contains(res.Body.String(), "public shell") != test.shell {
				t.Fatalf("unexpected shell response: %s", res.Body.String())
			}
			if strings.Contains(test.path, "api") && !strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("API miss content type = %q", res.Header().Get("Content-Type"))
			}
			if test.method == "HEAD" && res.Body.Len() != 0 {
				t.Fatal("HEAD must not return a response body")
			}
			if test.path == "/assets/app-123.js" && test.method == "GET" {
				if !strings.Contains(res.Header().Get("Cache-Control"), "immutable") {
					t.Fatal("hashed asset cache policy missing")
				}
			}
			if test.shell && res.Header().Get("Cache-Control") != "no-cache" {
				t.Fatal("shell must revalidate")
			}
		})
	}
}

func TestSPARegisteredAPIRoutesTakePrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "_shell.html"), []byte("public shell"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/api/v1/example", func(c *gin.Context) { c.JSON(200, gin.H{"data": "api"}) })
	if err := registerSPA(router, root); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(method, "/api/v1/example", nil))
		want := 200
		if method == "POST" {
			want = 405
		}
		if res.Code != want || !strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") || strings.Contains(res.Body.String(), "public shell") {
			t.Fatalf("%s API = %d %q", method, res.Code, res.Body.String())
		}
	}
}

func TestSPARequiresPublishedShell(t *testing.T) {
	if err := registerSPA(gin.New(), t.TempDir()); err == nil {
		t.Fatal("missing shell must fail startup")
	}
}
