package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
)

func TestAPIRouterDoesNotMountLegacyPages(t *testing.T) {
	router := newAPIRouter(nil, nil)
	for _, p := range []string{"/", "/login", "/account", "/admin"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", p, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "<!doctype html>") {
			t.Fatalf("API-only router unexpectedly mounted legacy %s: %d", p, response.Code)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/health", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("health = %d %s", response.Code, response.Body.String())
	}
}

func TestAPIRegistrationGuardsAllFeatures(t *testing.T) {
	f := newAuthFixture(t)
	service, err := auth.NewService(auth.NewStore(f.pool), f.now.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.NewService(access.NewStore(f.pool), service)
	if err := migration.NewStore(f.pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	cipher, err := database.NewCipher("I7fWIhEZr5Q+tMLSjrP1LXprBoDi1Iz65M5fQ4oPbr4=")
	if err != nil {
		t.Fatal(err)
	}
	store := database.NewStore(f.pool, cipher)
	pools := database.NewPools(store)
	t.Cleanup(pools.Close)
	router := newAPIRouter(service, policy)
	registerDatabaseAPIRoutes(router, service, policy, f.pool, databaseTargets{store: store, pools: pools})
	for _, test := range []struct{ method, path string }{
		{"GET", "/api/v1/auth/session"},
		{"POST", "/api/v1/auth/logout"},
		{"GET", "/api/v1/admin/users"},
		{"POST", "/api/v1/admin/groups"},
		{"GET", "/api/v1/admin/databases"},
		{"POST", "/api/v1/admin/databases/a/test"},
		{"GET", "/api/v1/databases"},
		{"GET", "/api/v1/databases/a"},
		{"GET", "/api/v1/databases/a/tables/b"},
		{"POST", "/api/v1/databases/a/explain"},
		{"GET", "/api/v1/databases/a/migrations"},
		{"PATCH", "/api/v1/databases/a/migrations/b"},
		{"POST", "/api/v1/databases/a/migrations/b/execute"},
		{"POST", "/api/v1/databases/a/schema-diff/preview"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`)))
			if response.Code != http.StatusUnauthorized || response.Header().Get("Location") != "" || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("unguarded API: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
