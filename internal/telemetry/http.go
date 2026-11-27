package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func HTTPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		correlationID := hex.EncodeToString(random[:])
		start := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx = WithCorrelation(ctx, correlationID)
		ctx, span := otel.Tracer("sqlwise/http").Start(ctx, "HTTP request", trace.WithSpanKind(trace.SpanKindServer))
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Request-ID", correlationID)
		c.Next()
		status := c.Writer.Status()
		method := normalizedHTTPMethod(c.Request.Method)
		route := safeHTTPRoute(c.FullPath())
		span.SetAttributes(attribute.String("http.request.method", method), attribute.Int("http.response.status_code", status))
		if route != "" {
			span.SetName(method + " " + route)
			span.SetAttributes(attribute.String("http.route", route))
		}
		if status >= 500 {
			span.SetStatus(codes.Error, "server error")
		}
		recordHTTP(method, route, status, time.Since(start))
		traceID, spanID := "", ""
		if sc := span.SpanContext(); sc.IsValid() {
			traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
		}
		slog.InfoContext(ctx, "http request", "event", "http.request", "method", method,
			"route", route, "status", strconv.Itoa(status), "duration_ms", time.Since(start).Milliseconds(),
			"actor", Actor(c.Request.Context()), "correlation_id", correlationID, "trace_id", traceID, "span_id", spanID)
		if action := auditAction(method, route); action != "" {
			outcome, level := auditOutcome(status)
			args := []any{"event", "audit", "action", action, "outcome", outcome, "status", strconv.Itoa(status),
				"actor", Actor(c.Request.Context()), "correlation_id", correlationID, "trace_id", traceID, "span_id", spanID}
			for _, p := range c.Params {
				args = append(args, auditTarget(p.Key), p.Value)
			}
			args = append(args, auditFields(c.Request.Context())...)
			slog.Log(ctx, level, "audit", args...)
		}
		span.End()
	}
}

// auditAction names a state-changing API call from its route template, so
// the set of actions is bounded by the routes. Reads, non-API and unmatched
// routes are not audited.
func auditAction(method, route string) string {
	if !strings.HasPrefix(route, "/api/v1/") {
		return ""
	}
	verb := map[string]string{http.MethodPost: "create", http.MethodPut: "update", http.MethodPatch: "update", http.MethodDelete: "delete"}[method]
	if verb == "" {
		return ""
	}
	raw := strings.Split(strings.TrimPrefix(route, "/api/v1/"), "/")
	var names []string
	hasParam := false
	for _, segment := range raw {
		switch {
		case strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*"):
			hasParam = true
		case segment != "":
			names = append(names, segment)
		}
	}
	if len(names) == 0 {
		return ""
	}
	action := strings.Join(names, ".")
	switch last := raw[len(raw)-1]; {
	case names[0] == "auth":
	case strings.HasPrefix(last, ":"):
		// POST or PATCH to an item updates it.
		if verb != "delete" {
			verb = "update"
		}
		action += "." + verb
	case !hasParam:
		action += "." + verb
	}
	// Literals after an ID already name the action (…/requests/:id/approve);
	// these few read better spelled out.
	if renamed, ok := auditActionNames[action]; ok {
		return renamed
	}
	return action
}

var auditActionNames = map[string]string{
	"databases.migrations":          "databases.migrations.create",
	"databases.migrations.requests": "databases.migrations.requests.create",
	"admin.groups.members":          "admin.groups.members.add",
	"admin.groups.databases":        "admin.groups.databases.grant",
	"admin.groups.databases.remove": "admin.groups.databases.revoke",
	"admin.users.admin":             "admin.users.platform_admin",
}

func auditOutcome(status int) (string, slog.Level) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "denied", slog.LevelWarn
	case status >= 500:
		return "error", slog.LevelError
	case status >= 400:
		return "rejected", slog.LevelWarn
	}
	return "success", slog.LevelInfo
}

// auditTarget turns a route parameter such as databaseID into database_id.
func auditTarget(param string) string {
	var out strings.Builder
	for i := 0; i < len(param); i++ {
		c := param[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 && param[i-1] >= 'a' && param[i-1] <= 'z' {
				out.WriteByte('_')
			}
			c += 'a' - 'A'
		}
		out.WriteByte(c)
	}
	name := out.String()
	if name == "id" {
		return "target_id"
	}
	return name
}

func safeHTTPRoute(route string) string {
	return strings.ReplaceAll(strings.ReplaceAll(route, "\r", " "), "\n", " ")
}
