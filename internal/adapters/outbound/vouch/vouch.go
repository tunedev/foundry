// Package vouch runs the vouch reviewer and its push gate.
package vouch

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
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
		return &ports.FindingsError{Counts: severityCounts(e.Stderr), Text: e.Stderr}
	}
	return run.Classify(err)
}

func exitCode(e *run.Error) int {
	if x, ok := errors.AsType[*exec.ExitError](e.Err); ok {
		return x.ExitCode()
	}
	return -1
}

var severities = []string{"blocker", "critical", "major", "minor", "nit"}

// severityCounts counts the finding lines of vouch's output, "- <severity> <location>: ...", per severity. Header and
// indented continuation lines are not counted.
func severityCounts(stderr string) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(stderr, "\n") {
		item, ok := strings.CutPrefix(line, "- ")
		w, _, _ := strings.Cut(item, " ")
		if w = strings.ToLower(w); ok && slices.Contains(severities, w) {
			counts[w]++
		}
	}
	return counts
}
