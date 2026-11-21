package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func envOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestConfigDefaultsFollowTheFrozenRetryPolicy(t *testing.T) {
	cfg, addr, err := config(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID == "" || cfg.Concurrency < 1 || addr != "127.0.0.1:9091" {
		t.Fatalf("cfg = %+v addr %q", cfg, addr)
	}
	if len(cfg.Backoff) != 2 || cfg.Backoff[0] != time.Second || cfg.Backoff[1] != 2*time.Second {
		t.Fatalf("backoff = %v, want 1s then 2s", cfg.Backoff)
	}
	if cfg.JobTimeout < 10*time.Minute {
		t.Fatalf("job timeout %v inherits a request-sized deadline", cfg.JobTimeout)
	}
}

func TestConfigReadsOverridesAndRejectsInvalidValues(t *testing.T) {
	cfg, addr, err := config(envOf(map[string]string{"WORKER_ID": "w-1", "WORKER_CONCURRENCY": "2", "WORKER_JOB_TIMEOUT": "2h",
		"WORKER_LOCK_WAIT": "250ms", "WORKER_METRICS_ADDR": "off"}))
	if err != nil || cfg.ID != "w-1" || cfg.Concurrency != 2 || cfg.JobTimeout != 2*time.Hour || cfg.LockWait != 250*time.Millisecond || addr != "" {
		t.Fatalf("cfg = %+v addr %q err %v", cfg, addr, err)
	}
	for _, bad := range []map[string]string{
		{"WORKER_CONCURRENCY": "0"},
		{"WORKER_CONCURRENCY": "many"},
		{"WORKER_LEASE": "-1s"},
		{"WORKER_JOB_TIMEOUT": "forever"},
	} {
		if _, _, err := config(envOf(bad)); err == nil {
			t.Fatalf("config accepted %v", bad)
		}
	}
}

func TestMetricsEndpointServesJobMetrics(t *testing.T) {
	response := httptest.NewRecorder()
	metricsMux().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, name := range []string{"sqlwise_target_gate_sessions", "sqlwise_target_pool_idle_connections", "sqlwise_db_pool_active_connections"} {
		if !strings.Contains(body, name) {
			t.Errorf("metrics missing %s", name)
		}
	}
	health := httptest.NewRecorder()
	metricsMux().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), "sqlwise-worker") {
		t.Fatalf("health = %d %s", health.Code, health.Body)
	}
}

func TestConnectRequiresPlatformDatabase(t *testing.T) {
	if _, err := connect(""); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("connect without DSN = %v", err)
	}
}
