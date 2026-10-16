package app

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	tracer = otel.Tracer("github.com/tunedev/foundry")
	meter  = otel.Meter("github.com/tunedev/foundry")

	stepDuration, _ = meter.Float64Histogram("foundry_step_duration_seconds", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 15, 30, 60, 120, 300, 600, 1200, 2700))
)
