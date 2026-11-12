package telemetry

import (
	"bytes"
	"context"
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
