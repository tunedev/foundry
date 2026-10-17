package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// RetryOnce runs fn, and runs it once more after a pause of pause/2 plus up to pause of jitter
// when the first error is transient. Every other error is returned at once.
func RetryOnce(ctx context.Context, pause time.Duration, fn func() error) error {
	err := fn()
	if !errors.Is(err, ports.ErrTransient) {
		return err
	}
	slog.WarnContext(ctx, "retrying transient failure", "error", err.Error())
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(pause/2 + rand.N(pause)):
	}
	return fn()
}

func isTransient(err error) bool { return errors.Is(err, ports.ErrTransient) }
