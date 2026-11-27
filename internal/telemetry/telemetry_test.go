package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestAuditActionNamesEveryMutatingAPIRoute(t *testing.T) {
	for _, tc := range []struct{ method, route, want string }{
		{http.MethodPost, "/api/v1/auth/login", "auth.login"},
		{http.MethodPost, "/api/v1/auth/logout", "auth.logout"},
		{http.MethodPost, "/api/v1/admin/users", "admin.users.create"},
		{http.MethodPost, "/api/v1/admin/users/:id", "admin.users.update"},
		{http.MethodPost, "/api/v1/admin/users/:id/disable", "admin.users.disable"},
		{http.MethodPost, "/api/v1/admin/users/:id/admin", "admin.users.platform_admin"},
		{http.MethodPost, "/api/v1/admin/groups/:id/leader", "admin.groups.leader"},
		{http.MethodPost, "/api/v1/admin/groups/:id/members", "admin.groups.members.add"},
		{http.MethodPost, "/api/v1/admin/groups/:id/members/remove", "admin.groups.members.remove"},
		{http.MethodPost, "/api/v1/admin/groups/:id/databases", "admin.groups.databases.grant"},
		{http.MethodPost, "/api/v1/admin/groups/:id/databases/remove", "admin.groups.databases.revoke"},
		{http.MethodPost, "/api/v1/admin/databases", "admin.databases.create"},
		{http.MethodPost, "/api/v1/admin/databases/:id/test", "admin.databases.test"},
		{http.MethodPost, "/api/v1/databases/:databaseID/migrations", "databases.migrations.create"},
		{http.MethodPatch, "/api/v1/databases/:databaseID/migrations/:migrationID", "databases.migrations.update"},
		{http.MethodPost, "/api/v1/databases/:databaseID/migrations/:migrationID/requests", "databases.migrations.requests.create"},
		{http.MethodPost, "/api/v1/databases/:databaseID/requests/:requestID/submit", "databases.requests.submit"},
		{http.MethodPost, "/api/v1/databases/:databaseID/requests/:requestID/approve", "databases.requests.approve"},
		{http.MethodPost, "/api/v1/databases/:databaseID/requests/:requestID/reject", "databases.requests.reject"},
		{http.MethodPost, "/api/v1/databases/:databaseID/migrations/:migrationID/execute", "databases.migrations.execute"},
		{http.MethodGet, "/api/v1/databases/:databaseID/jobs", ""},
		{http.MethodPost, "/login", ""},
		{http.MethodPost, "", ""},
	} {
		if got := auditAction(tc.method, tc.route); got != tc.want {
			t.Errorf("auditAction(%s %s) = %q, want %q", tc.method, tc.route, got, tc.want)
		}
	}
}

func TestHTTPMiddlewareAuditsMutationsWithActorTargetsAndOutcome(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	router := gin.New()
	router.Use(HTTPMiddleware())
	withActor := func(c *gin.Context) { c.Request = c.Request.WithContext(WithActor(c.Request.Context(), "user-1")) }
	router.POST("/api/v1/databases/:databaseID/requests/:requestID/approve", withActor, func(c *gin.Context) {
		c.Request = c.Request.WithContext(WithAuditFields(c.Request.Context(), "group_id", "group-1"))
		c.Status(http.StatusOK)
	})
	router.POST("/api/v1/databases/:databaseID/requests/:requestID/reject", withActor, func(c *gin.Context) { c.Status(http.StatusForbidden) })
	router.GET("/api/v1/databases/:databaseID/jobs", withActor, func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, path := range []string{"/api/v1/databases/db-1/requests/req-1/approve", "/api/v1/databases/db-1/requests/req-2/reject"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"password":"body-canary"}`)))
	}
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/databases/db-1/jobs", nil))
	var audits []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		if entry["event"] == "audit" {
			audits = append(audits, entry)
		}
	}
	if len(audits) != 2 || strings.Contains(output.String(), "body-canary") {
		t.Fatalf("audit lines = %v\n%s", audits, output.String())
	}
	approve, reject := audits[0], audits[1]
	if approve["action"] != "databases.requests.approve" || approve["outcome"] != "success" || approve["level"] != "INFO" ||
		approve["actor"] != "user-1" || approve["database_id"] != "db-1" || approve["request_id"] != "req-1" || approve["group_id"] != "group-1" || approve["correlation_id"] == "" {
		t.Errorf("approve audit = %v", approve)
	}
	if reject["action"] != "databases.requests.reject" || reject["outcome"] != "denied" || reject["level"] != "WARN" || reject["request_id"] != "req-2" {
		t.Errorf("reject audit = %v", reject)
	}
}

func TestFailedSQLIsAWarningWithMaskedErrorAndParameterTypesOnly(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	tracer := PGXTracer{}
	ctx := WithLogFields(context.Background(), "database", "db-1", "batch", 3)
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "UPDATE t SET v=$1 WHERE id=$2", Args: []any{"value-canary", int64(7)}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "message-canary"`}})
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want start and result lines, got %s", output.String())
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &result); err != nil {
		t.Fatal(err)
	}
	params, _ := result["params"].([]any)
	if result["level"] != "WARN" || result["status"] != "failure" || result["sqlstate"] != "22P02" || result["batch"] != float64(3) ||
		result["database"] != "db-1" || len(params) != 2 || params[0] != "[REDACTED string]" || params[1] != "[REDACTED int64]" ||
		!strings.Contains(fmt.Sprint(result["error"]), "invalid input syntax for type integer: [REDACTED]") {
		t.Fatalf("result line = %v", result)
	}
	for _, secret := range []string{"value-canary", "message-canary"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
}

func TestContextHandlerAddsTraceAndCorrelationOnce(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(&output, nil)))
	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))
	ctx = WithCorrelation(ctx, "corr-1")
	logger.InfoContext(ctx, "domain event", "event", "migration.execution.start")
	logger.With("correlation_id", "job-1", "trace_id", "explicit").InfoContext(ctx, "already tagged")
	logger.Info("no context")
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %s", output.String())
	}
	var first, second, third map[string]any
	for i, target := range []*map[string]any{&first, &second, &third} {
		if err := json.Unmarshal([]byte(lines[i]), target); err != nil {
			t.Fatal(err)
		}
	}
	if first["trace_id"] != traceID.String() || first["span_id"] != spanID.String() || first["correlation_id"] != "corr-1" {
		t.Errorf("context line = %v", first)
	}
	if strings.Count(lines[1], `"correlation_id"`) != 1 || strings.Count(lines[1], `"trace_id"`) != 1 || second["correlation_id"] != "job-1" {
		t.Errorf("pre-tagged line duplicated keys: %s", lines[1])
	}
	if _, ok := third["trace_id"]; ok {
		t.Errorf("line without context got a trace: %v", third)
	}
}

func TestRedactMessageMasksQuotedValuesButKeepsCodes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`Statement 1 failed: invalid input syntax for type integer: "rd_secret" (SQLSTATE 22P02). All changes were rolled back.`,
			`Statement 1 failed: invalid input syntax for type integer: [REDACTED] (SQLSTATE 22P02). All changes were rolled back.`},
		{`value 'a''b' and "c"`, `value [REDACTED][REDACTED] and [REDACTED]`},
		{`unterminated 'secret tail`, `unterminated [REDACTED]`},
		{"Gave up after 3 attempts.", "Gave up after 3 attempts."},
	} {
		if got := RedactMessage(tc.in); got != tc.want {
			t.Errorf("RedactMessage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
