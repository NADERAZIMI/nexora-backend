// Package observability configures OpenTelemetry tracing and Prometheus
// metrics for Go services.
package observability

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Providers owns process-wide telemetry resources.
type Providers struct {
	Registry       *prometheus.Registry
	TracerProvider *sdktrace.TracerProvider
}

// New configures OTLP trace export and a Prometheus registry.
//
// An empty endpoint disables trace export and leaves the global tracer provider
// untouched.
func New(ctx context.Context, service, environment, endpoint string) (*Providers, error) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	providers := &Providers{Registry: registry}
	if strings.TrimSpace(endpoint) == "" {
		return providers, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(grpcEndpoint(endpoint)),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	res := resource.NewSchemaless(
		attribute.String("service.name", service),
		attribute.String("deployment.environment.name", environment),
	)

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	providers.TracerProvider = provider
	return providers, nil
}

// Shutdown flushes and stops trace export.
func (p *Providers) Shutdown(ctx context.Context) error {
	if p == nil || p.TracerProvider == nil {
		return nil
	}
	return p.TracerProvider.Shutdown(ctx)
}

func grpcEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://")
	}
	return parsed.Host
}
