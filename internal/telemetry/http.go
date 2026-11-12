package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
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
		span.SetAttributes(attribute.String("http.request.method", c.Request.Method), attribute.Int("http.response.status_code", status))
		if route := c.FullPath(); route != "" {
			span.SetName(c.Request.Method + " " + route)
			span.SetAttributes(attribute.String("http.route", route))
		}
		if status >= 500 {
			span.SetStatus(codes.Error, "server error")
		}
		recordHTTP(c.Request.Method, c.FullPath(), status, time.Since(start))
		traceID, spanID := "", ""
		if sc := span.SpanContext(); sc.IsValid() {
			traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
		}
		slog.InfoContext(ctx, "http request", "event", "http.request", "method", c.Request.Method,
			"route", c.FullPath(), "status", strconv.Itoa(status), "duration_ms", time.Since(start).Milliseconds(),
			"actor", Actor(c.Request.Context()), "correlation_id", correlationID, "trace_id", traceID, "span_id", spanID)
		span.End()
	}
}
