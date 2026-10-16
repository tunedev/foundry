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
	issueOutcomes, _ = meter.Int64Counter("foundry_issue_outcomes_total")
	agentCost, _     = meter.Float64Counter("foundry_agent_cost_usd_total")
	reviewRounds, _  = meter.Int64Histogram("foundry_review_rounds", metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 4, 5, 8))
	frictionFiled, _ = meter.Int64Counter("foundry_friction_filed_total")
)
