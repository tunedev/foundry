package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// step runs fn as the named use-case step, logging its start, outcome and duration.
func step(ctx context.Context, name string, fn func() error) error {
	slog.InfoContext(ctx, "step start", "step", name)
	start := time.Now()
	err := fn()
	attrs := []any{"step", name, "duration_ms", time.Since(start).Milliseconds()}
	if err != nil {
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
