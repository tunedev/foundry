package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"

	"github.com/tunedev/foundry/internal/core/ports"
)

// step runs fn as the named use-case step inside a span of that name. It logs the step's start, outcome
// and duration, and records the duration on foundry_step_duration_seconds.
func step(ctx context.Context, name string, fn func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	slog.InfoContext(ctx, "step start", "step", name)
	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)
	stepDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String("step", name)))
	attrs := []any{"step", name, "duration_ms", elapsed.Milliseconds()}
	if err != nil {
		span.SetStatus(codes.Error, class(err))
		slog.ErrorContext(ctx, "step failed", append(attrs, "error", err.Error(), "class", class(err))...)
		return err
	}
	slog.InfoContext(ctx, "step done", attrs...)
	return nil
}

func class(err error) string {
	var na *NeedsAction
	switch {
	case errors.As(err, &na):
		return "needs_action"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	}
	return "fail_fast"
}
