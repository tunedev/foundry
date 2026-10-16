package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

func TestRenderGroupsIssuesByOutcome(t *testing.T) {
	s := ports.Summary{RunID: "20261016-020000", Repo: "acme/widget", CostUSD: 1.25, Wall: 42 * time.Minute,
		Issues: []ports.IssueReport{
			{Number: 1, Title: "Slow tests", Outcome: "ready-for-human", PRURL: "https://github.com/acme/widget/pull/9"},
			{Number: 2, Title: "Flaky e2e", Outcome: "stuck", Reason: "timeout"},
			{Number: 3, Title: "Rename flag", Outcome: "needs-decision"},
		}}
	got := Render(s)
	for _, want := range []string{"acme/widget", "$1.25", "Ready for review:\n- #1 Slow tests: https://github.com/acme/widget/pull/9",
		"Stuck:\n- #2 Flaky e2e: timeout", "Needs a decision:\n- #3 Rename flag"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	s.Issues[0].Title = strings.Repeat("é", 3000)
	if r := []rune(Render(s)); len(r) > 2000 {
		t.Errorf("%d characters, want at most 2000", len(r))
	}
}
