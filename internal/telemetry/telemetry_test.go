package telemetry

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestRedactSQL(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"SELECT id FROM users WHERE id=$1", "SELECT id FROM users WHERE id=$1"},
		{"SELECT 'secret-canary', 42, E'private', $$hidden$$", "SELECT [REDACTED], [REDACTED], E[REDACTED], [REDACTED]"},
		{"SELECT \"private-column\" FROM x -- secret-canary\nWHERE x=1", "SELECT [REDACTED] FROM x  \nWHERE x=[REDACTED]"},
		{"SELECT /* secret-canary */ x", "SELECT   x"},
		{"SELECT 'unterminated secret-canary", "[REDACTED]"},
		{"SELECT postgres://user:secret-canary@host/database", "[REDACTED]"},
	} {
		if got := RedactSQL(tc.query); got != tc.want {
			t.Errorf("RedactSQL(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestPGXTracerOmitsArgumentsAndDriverDetails(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	tracer := PGXTracer{}
	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{
		SQL:  "SELECT 'secret-canary' WHERE id=$1",
		Args: []any{"argument-canary"},
	})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		CommandTag: pgconn.NewCommandTag("SELECT 1"),
		Err:        &pgconn.PgError{Code: "22000", Detail: "driver-canary"},
	})
	logs := output.String()
	for _, secret := range []string{"secret-canary", "argument-canary", "driver-canary"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %q", secret)
		}
	}
	for _, expected := range []string{"sql.start", "sql.result", "22000", "[REDACTED]", "SELECT"} {
		if !strings.Contains(logs, expected) {
			t.Errorf("log misses %q", expected)
		}
	}
}

func TestHTTPMiddlewareUsesRouteAndOmitsQueryString(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	router := gin.New()
	router.Use(HTTPMiddleware())
	router.GET("/items/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items/private-id?token=secret-canary", nil))
	if response.Code != http.StatusNoContent || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("response = %d, request ID = %q", response.Code, response.Header().Get("X-Request-ID"))
	}
	if strings.Contains(output.String(), "private-id") || strings.Contains(output.String(), "secret-canary") || !strings.Contains(output.String(), "/items/:id") {
		t.Fatalf("unexpected request log: %s", output.String())
	}
}

func TestHTTPRouteForTelemetryNormalizesLineSeparators(t *testing.T) {
	for _, tc := range []struct {
		route string
		want  string
	}{
		{"/items/:id", "/items/:id"},
		{"/items/\n:id\r", "/items/ :id "},
		{"", ""},
	} {
		if got := safeHTTPRoute(tc.route); got != tc.want {
			t.Errorf("safeHTTPRoute(%q) = %q, want %q", tc.route, got, tc.want)
		}
	}
}

func TestHTTPMiddlewareBoundsCustomMethodsInTelemetry(t *testing.T) {
	mode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(mode) })
	var output bytes.Buffer
	previousLogger := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	router := gin.New()
	router.Use(HTTPMiddleware())
	const route = "/telemetry-method-test/:id"
	router.Handle("BREW", route, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	t.Cleanup(func() {
		requests.DeleteLabelValues(route, "OTHER", "204")
		requestDuration.DeleteLabelValues(route, "OTHER")
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("BREW", "/telemetry-method-test/item", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("custom method route status = %d", response.Code)
	}

	logs := output.String()
	if strings.Contains(logs, "BREW") || !strings.Contains(logs, `"method":"OTHER"`) {
		t.Fatalf("custom method log is not normalized: %s", logs)
	}

	metrics := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `sqlwise_http_requests_total{method="OTHER",route="/telemetry-method-test/:id",status="204"} 1`+"\n") {
		t.Fatalf("custom method metric is not normalized")
	}
}

func TestTraceContextRoundTrip(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	traceID, err := trace.TraceIDFromHex("11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("2222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))
	value := InjectTrace(ctx)
	if value.Traceparent == "" || value.Tracestate != "" {
		t.Fatalf("trace context = %+v", value)
	}
	if got := trace.SpanContextFromContext(ExtractTrace(context.Background(), value)).TraceID(); got != traceID {
		t.Fatal("trace ID changed across queue propagation")
	}
}

func TestSQLLogCarriesContextFields(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx := WithLogFields(WithLogFields(context.Background(), "database", "db-1"), "job_id", "job-1")
	tracer := PGXTracer{}
	tracer.TraceQueryEnd(tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"}), nil, pgx.TraceQueryEndData{})
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		for _, expected := range []string{`"database":"db-1"`, `"job_id":"job-1"`} {
			if !strings.Contains(line, expected) {
				t.Errorf("%s misses %s", line, expected)
			}
		}
	}
}
