package ship

import "github.com/tunedev/foundry/internal/core/ports"

type CI int

const (
	CIPending CI = iota
	CIGreen
	CIRed
)

// Verdict reads the checks on a pull request head and returns the failing ones when red.
// No checks counts as pending, since the forge registers a new head's checks after a delay.
// fail and cancel are red; pass and skipping are green; anything else is pending.
func Verdict(checks []ports.Check) (CI, []ports.Check) {
	if len(checks) == 0 {
		return CIPending, nil
	}
	var red []ports.Check
	pending := false
	for _, c := range checks {
		switch c.Bucket {
		case "fail", "cancel":
			red = append(red, c)
		case "pass", "skipping":
		default:
			pending = true
		}
	}
	switch {
	case len(red) > 0:
		return CIRed, red
	case pending:
		return CIPending, nil
	}
	return CIGreen, nil
}
