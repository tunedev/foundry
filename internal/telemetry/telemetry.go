// Package telemetry installs foundry's OpenTelemetry providers and carries trace context across processes.
package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Scope names foundry's tracer and meter.
const Scope = "github.com/tunedev/foundry"

// Setup installs OTLP/HTTP trace and metric providers configured by the standard OTEL_EXPORTER_OTLP_*
// variables, and the W3C trace-context propagator. OTEL_TRACES_EXPORTER=none or OTEL_METRICS_EXPORTER=none
// leaves that signal on the no-op provider. Metrics use delta temporality. Export errors are logged, never
// returned. The returned shutdown flushes both providers within 5s.
func Setup(ctx context.Context, getenv func(string) string) (func(context.Context), error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.WarnContext(context.Background(), "telemetry export failed", "error", err.Error())
	}))
	res := resource.NewSchemaless(attribute.String("service.name", "foundry"))
	var stops []func(context.Context) error
	if getenv("OTEL_TRACES_EXPORTER") != "none" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
		otel.SetTracerProvider(tp)
		stops = append(stops, tp.Shutdown)
	}
	if getenv("OTEL_METRICS_EXPORTER") != "none" {
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithTemporalitySelector(delta))
		if err != nil {
			return nil, err
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)), sdkmetric.WithResource(res))
		otel.SetMeterProvider(mp)
		stops = append(stops, mp.Shutdown)
	}
	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for _, stop := range stops {
			if err := stop(ctx); err != nil {
				slog.WarnContext(ctx, "telemetry shutdown", "error", err.Error())
			}
		}
	}, nil
}

func delta(sdkmetric.InstrumentKind) metricdata.Temporality { return metricdata.DeltaTemporality }

// Start begins a command's root span, as a child of the TRACEPARENT the environment carries, if any.
func Start(ctx context.Context, name string, getenv func(string) string) (context.Context, trace.Span) {
	carrier := propagation.MapCarrier{"traceparent": getenv("TRACEPARENT"), "tracestate": getenv("TRACESTATE")}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	return otel.Tracer(Scope).Start(ctx, name)
}

// Env returns TRACEPARENT and TRACESTATE entries for ctx's span, for a child process's environment.
func Env(ctx context.Context) []string {
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	var env []string
	if v := c["traceparent"]; v != "" {
		env = append(env, "TRACEPARENT="+v)
	}
	if v := c["tracestate"]; v != "" {
		env = append(env, "TRACESTATE="+v)
	}
	return env
}
