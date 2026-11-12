package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func Start(ctx context.Context, service string, output io.Writer) (func(context.Context) error, error) {
	if output == nil {
		output = os.Stdout
	}
	logger := slog.New(slog.NewJSONHandler(output, nil)).With("service", service)
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
)

func WithCorrelation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

func Correlation(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
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
