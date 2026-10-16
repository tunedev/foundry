// Package friction holds the rules for the friction reports agents file as issues.
package friction

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tunedev/foundry/internal/core/markdown"
)

type Kind string

const (
	Ready         Kind = "ready"
	NeedsDecision Kind = "needs-decision"
	Cleanup       Kind = "cleanup"
)

const Label = "friction"

const (
	InProgress = "friction:in-progress"
	Stuck      = "friction:stuck"
	// HitAgain starts the comment a later report of the same friction leaves on its issue.
	HitAgain = "Hit again from branch"
)

// AllLabels is every label the loop uses; filing a report makes sure they exist.
var AllLabels = []string{Label, "friction:ready", "friction:needs-decision", "friction:cleanup", "friction:in-progress", "friction:stuck"}

func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case Ready, NeedsDecision, Cleanup:
		return k, nil
	}
	return "", fmt.Errorf("--kind must be ready, needs-decision or cleanup, got %q", s)
}

func (k Kind) Label() string { return Label + ":" + string(k) }

var BodyHeadings = []string{"What slowed me down", "Cost", "Proposed fix", "Cleanup", "Source"}

func ValidateBody(body string) error {
	if m := markdown.Missing(markdown.Sections(body), BodyHeadings); len(m) > 0 {
		return fmt.Errorf("report is missing headings: %s", strings.Join(m, ", "))
	}
	return nil
}

var secrets = []struct {
	name string
	re   *regexp.Regexp
}{
	{"email address", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)},
	{"GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)},
	{"API key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// Unsafe returns why text may not be posted publicly: emails, token or key patterns, or a user-data path.
func Unsafe(text string, userDataPaths []string) []string {
	var why []string
	for _, s := range secrets {
		if s.re.MatchString(text) {
			why = append(why, s.name)
		}
	}
	for _, p := range userDataPaths {
		if p != "" && strings.Contains(text, p) {
			why = append(why, "user-data path "+p)
		}
	}
	return why
}

// Answer is what a branch reported at the friction step: the issues it filed or commented on, or why it had none.
type Answer struct {
	Issues []int  `json:"issues,omitempty"`
	None   string `json:"none,omitempty"`
}

func (a Answer) Given() bool { return len(a.Issues) > 0 || a.None != "" }
