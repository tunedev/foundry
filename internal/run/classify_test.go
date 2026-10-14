package run

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tunedev/foundry/internal/core/ports"
)

func TestClassifyMarksOnlyRetryableFailures(t *testing.T) {
	cases := []struct {
		msg       string
		transient bool
	}{
		{"gh pr create: exit status 1: HTTP 502: Bad Gateway", true},
		{"gh: API rate limit exceeded for user", true},
		{"fatal: unable to access 'https://github.com/x/': Could not resolve host: github.com", true},
		{"read tcp 10.0.0.2:5555: i/o timeout", true},
		{`gh pr create: exit status 1: a pull request for branch "feat" already exists`, false},
		{"HTTP 422: Validation Failed", false},
		{"sh -c go test ./...: exit status 1: FAIL calc", false},
	}
	for _, c := range cases {
		got := errors.Is(Classify(errors.New(c.msg)), ports.ErrTransient)
		if got != c.transient {
			t.Errorf("%q: transient=%v, want %v", c.msg, got, c.transient)
		}
	}
	if !errors.Is(Classify(fmt.Errorf("git fetch: %w after 2m", ErrTimeout)), ports.ErrTransient) {
		t.Error("timeout not transient")
	}
	if Classify(nil) != nil {
		t.Error("nil classified as an error")
	}
}
