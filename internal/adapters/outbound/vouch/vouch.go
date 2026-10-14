// Package vouch runs the vouch reviewer and its push gate.
package vouch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/run"
)

type Vouch struct{ ReviewTimeout, CallTimeout time.Duration }

// Review runs vouch review on HEAD, then vouch gate. A gate that exits 2 denied the code: its error is
// ports.ErrFindings carrying the findings vouch printed on stderr.
func (v Vouch) Review(ctx context.Context, dir string) error {
	if _, err := run.Cmd(ctx, v.ReviewTimeout, dir, nil, "vouch", "review"); err != nil {
		return run.Classify(err)
	}
	_, err := run.Cmd(ctx, v.CallTimeout, dir, nil, "vouch", "gate")
	if e, ok := errors.AsType[*run.Error](err); ok && exitCode(e) == 2 {
		return fmt.Errorf("%w:\n%s", ports.ErrFindings, e.Stderr)
	}
	return run.Classify(err)
}

func exitCode(e *run.Error) int {
	if x, ok := errors.AsType[*exec.ExitError](e.Err); ok {
		return x.ExitCode()
	}
	return -1
}
