// Package app holds foundry's use cases, composed from steps that cmd/foundry injects.
package app

import "fmt"

// NeedsAction is a stop the agent resolves by changing its branch or its report, then rerunning.
type NeedsAction struct{ Step, Detail string }

func (e *NeedsAction) Error() string { return e.Step + ": " + e.Detail }

func needs(step, format string, a ...any) error {
	return &NeedsAction{Step: step, Detail: fmt.Sprintf(format, a...)}
}
