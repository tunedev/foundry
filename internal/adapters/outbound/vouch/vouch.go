// Package vouch runs the vouch reviewer and its push gate.
package vouch

import (
	"context"
	"time"

	"github.com/tunedev/foundry/internal/run"
)

type Vouch struct{ ReviewTimeout, CallTimeout time.Duration }

// Review runs vouch review on HEAD, then vouch gate. A failing gate's error carries vouch's findings.
func (v Vouch) Review(ctx context.Context, dir string) error {
	if _, err := run.Cmd(ctx, v.ReviewTimeout, dir, nil, "vouch", "review"); err != nil {
		return run.Classify(err)
	}
	_, err := run.Cmd(ctx, v.CallTimeout, dir, nil, "vouch", "gate")
	return run.Classify(err)
}
