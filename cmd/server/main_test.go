package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wiseql/internal/web/views"
)

func TestHealth(t *testing.T) {
	router := newRouter(views.Assets{Dev: true})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body struct {
		Service string `json:"service"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Service != "wiseql" || body.Status != "ok" {
		t.Fatalf("health body = %+v, want service=wiseql status=ok", body)
	}
}

func TestHome(t *testing.T) {
	router := newRouter(views.Assets{Dev: true})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", contentType)
	}
	for _, expected := range []string{"<!doctype html>", "WiseQL", "PostgreSQL Management Platform", "IF3110"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("home response does not contain %q", expected)
		}
	}
	for _, unsupported := range []string{"hx-get=", "x-data=", "Check server status"} {
		if strings.Contains(response.Body.String(), unsupported) {
			t.Errorf("home response contains demo interaction %q", unsupported)
		}
	}
}
