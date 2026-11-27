package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func Start(ctx context.Context, service string, output io.Writer) (func(context.Context) error, error) {
	if output == nil {
		output = os.Stdout
	}
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(output, nil))).With("service", service)
	var exporter sdktrace.SpanExporter
	var err error
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exporter, err = otlptracehttp.New(ctx)
	} else {
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(output))
	}
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	slog.SetDefault(logger)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider.Shutdown, nil
}

type contextKey uint8

const (
	correlationKey contextKey = iota
	actorKey
	fieldsKey
	auditKey
)

func WithCorrelation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

func Correlation(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
}

// WithLogFields adds key/value pairs that every SQL log event in ctx carries.
func WithLogFields(ctx context.Context, args ...any) context.Context {
	return context.WithValue(ctx, fieldsKey, append(slices.Clip(logFields(ctx)), args...))
}

func logFields(ctx context.Context) []any {
	fields, _ := ctx.Value(fieldsKey).([]any)
	return fields
}

// WithAuditFields adds key/value pairs to the audit event of the current
// API call, such as the ID of the object it created or the job it queued.
func WithAuditFields(ctx context.Context, args ...any) context.Context {
	return context.WithValue(ctx, auditKey, append(slices.Clip(auditFields(ctx)), args...))
}

func auditFields(ctx context.Context) []any {
	fields, _ := ctx.Value(auditKey).([]any)
	return fields
}

// logField returns the latest string value logged under key in ctx.
func logField(ctx context.Context, key string) string {
	fields := logFields(ctx)
	for i := len(fields) - 2; i >= 0; i -= 2 {
		if k, _ := fields[i].(string); k == key {
			value, _ := fields[i+1].(string)
			return value
		}
	}
	return ""
}

func WithActor(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, actorKey, id)
}

func Actor(ctx context.Context) string {
	id, _ := ctx.Value(actorKey).(string)
	if id == "" {
		return "system"
	}
	return id
}

type TraceContext struct {
	Traceparent string `json:"traceparent"`
	Tracestate  string `json:"tracestate,omitempty"`
}

func InjectTrace(ctx context.Context) TraceContext {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return TraceContext{Traceparent: carrier.Get("traceparent"), Tracestate: carrier.Get("tracestate")}
}

func ExtractTrace(ctx context.Context, value TraceContext) context.Context {
	carrier := propagation.MapCarrier{"traceparent": value.Traceparent, "tracestate": value.Tracestate}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// contextHandler adds the trace and correlation IDs of the record's context
// to every log line that does not already carry them, so domain events logged
// with *Context calls join the request or job trace.
type contextHandler struct {
	inner          slog.Handler
	hasTrace       bool
	hasCorrelation bool
}

// NewContextHandler wraps inner with trace_id, span_id and correlation_id
// taken from the context of each record.
func NewContextHandler(inner slog.Handler) slog.Handler { return contextHandler{inner: inner} }

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	hasTrace, hasCorrelation := h.hasTrace, h.hasCorrelation
	record.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "trace_id":
			hasTrace = true
		case "correlation_id":
			hasCorrelation = true
		}
		return true
	})
	if !hasTrace {
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			record.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
		}
	}
	if id := Correlation(ctx); id != "" && !hasCorrelation {
		record.AddAttrs(slog.String("correlation_id", id))
	}
	return h.inner.Handle(ctx, record)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := h
	next.inner = h.inner.WithAttrs(attrs)
	for _, a := range attrs {
		switch a.Key {
		case "trace_id":
			next.hasTrace = true
		case "correlation_id":
			next.hasCorrelation = true
		}
	}
	return next
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	next := h
	next.inner = h.inner.WithGroup(name)
	return next
}
