package work

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

var t0 = time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)

func TestWindow(t *testing.T) {
	w, err := ParseWindow("22:00-07:00")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 16, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		t    time.Time
		open bool
	}{{at(23, 30), true}, {at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false}, {at(22, 0), true}} {
		if w.Contains(c.t) != c.open {
			t.Errorf("%s: open=%v", c.t.Format("15:04"), !c.open)
		}
	}
	if always, _ := ParseWindow(""); !always.Contains(at(12, 0)) {
		t.Error("empty window closed")
	}
	for _, bad := range []string{"22:00", "25:00-07:00", "07:00-07:00"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCurrentClaimIgnoresOtherAuthors(t *testing.T) {
	cs := []ports.Comment{
		{Author: "bot", Body: ClaimComment("r1"), CreatedAt: t0},
		{Author: "bot", Body: ClaimComment("r2") + "\nmore", CreatedAt: t0.Add(time.Hour)},
		{Author: "someone", Body: ClaimComment("r9"), CreatedAt: t0.Add(2 * time.Hour)},
	}
	c, ok := CurrentClaim(cs, "bot")
	if !ok || c.RunID != "r2" || !c.At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := CurrentClaim(cs[2:], "bot"); ok {
		t.Error("a claim by another author counted")
	}
}

func TestDecide(t *testing.T) {
	c := Claim{RunID: "r", At: t0}
	lease := 2 * time.Hour
	cases := []struct {
		name    string
		claimed bool
		pr      ports.PR
		found   bool
		now     time.Time
		want    Release
	}{
		{"no claim", false, ports.PR{}, false, t0, NoClaim},
		{"closed unmerged", true, ports.PR{State: "CLOSED"}, true, t0, Rejected},
		{"open PR", true, ports.PR{State: "OPEN"}, true, t0.Add(5 * time.Hour), Keep},
		{"merged PR", true, ports.PR{State: "MERGED"}, true, t0.Add(5 * time.Hour), Keep},
		{"young, no PR", true, ports.PR{}, false, t0.Add(time.Hour), Keep},
		{"expired, no PR", true, ports.PR{}, false, t0.Add(3 * time.Hour), Expired},
	}
	for _, tc := range cases {
		if got := Decide(c, tc.claimed, tc.pr, tc.found, tc.now, lease); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPickOrdersByHitsThenAgeAndSkipsTaken(t *testing.T) {
	trusted := []string{"bot", "Owner"}
	hit := ports.Comment{Author: "bot", Body: "Hit again from branch `x`."}
	spam := ports.Comment{Author: "stranger", Body: "Hit again from branch `y`."}
	ready := []ports.Issue{
		{Number: 1, Author: "owner", CreatedAt: t0, Labels: []string{"friction:ready"}},
		{Number: 2, Author: "bot", CreatedAt: t0.Add(time.Hour), Labels: []string{"friction:ready"}, Comments: []ports.Comment{hit, hit}},
		{Number: 3, Author: "owner", CreatedAt: t0.Add(-time.Hour), Labels: []string{"friction:ready"}},
		{Number: 8, Author: "owner", CreatedAt: t0.Add(2 * time.Hour), Labels: []string{"friction:ready"}, Comments: []ports.Comment{spam, spam, spam}},
		{Number: 9, Author: "stranger", CreatedAt: t0.Add(-5 * time.Hour), Labels: []string{"friction:ready"}},
		{Number: 4, Author: "owner", Labels: []string{"friction:ready", "friction:in-progress"}},
		{Number: 5, Author: "owner", Labels: []string{"friction:ready", "friction:stuck"}},
		{Number: 6, Author: "owner", Labels: []string{"friction:ready"}},
		{Number: 7, Author: "owner", Labels: []string{"friction:ready"}},
	}
	open := []ports.PR{{Head: "friction/6-20261015-020000"}, {Head: "fix-docs", Body: "Fixes #7"}}
	got := Pick(ready, open, 3, trusted)
	if Hits(ready[3], trusted) != 1 {
		t.Errorf("untrusted hit comments counted: %d", Hits(ready[3], trusted))
	}
	var nums []int
	for _, is := range got {
		nums = append(nums, is.Number)
	}
	if len(nums) != 3 || nums[0] != 2 || nums[1] != 3 || nums[2] != 1 {
		t.Fatalf("picked %v, want [2 3 1]", nums)
	}
}

func TestClassify(t *testing.T) {
	ok := ports.AgentResult{Subtype: "success"}
	cases := []struct {
		name   string
		e      Evidence
		kind   Kind
		reason Reason
	}{
		{"hand-off", Evidence{Result: ok, Shipped: true, Ship: ShipRecord{Step: "hand-off", Outcome: "ready-for-human", PRURL: "u"}}, ReadyForHuman, ""},
		{"hand-off then timeout", Evidence{TimedOut: true, RunErr: errors.New("killed"), Shipped: true, Ship: ShipRecord{Outcome: "ready-for-human"}}, ReadyForHuman, ""},
		{"decision", Evidence{Result: ports.AgentResult{Text: "NEEDS-DECISION: A or B; A."}}, NeedsDecision, ""},
		{"timeout", Evidence{TimedOut: true, RunErr: errors.New("killed")}, Stuck, ReasonTimeout},
		{"turns", Evidence{Result: ports.AgentResult{Subtype: "error_max_turns"}}, Stuck, ReasonMaxTurns},
		{"review", Evidence{Result: ports.AgentResult{Text: "STUCK: no"}, Shipped: true, Ship: ShipRecord{Step: "review", Detail: "review did not converge after 3 rounds"}}, Stuck, ReasonReviewNotConverged},
		{"agent stuck", Evidence{Result: ports.AgentResult{Text: "STUCK: vouch failed"}}, Stuck, ReasonAgentStuck},
		{"crash", Evidence{RunErr: errors.New("exit 3")}, Stuck, ReasonCrash},
		{"ship stop", Evidence{Result: ok, Shipped: true, Ship: ShipRecord{Step: "ci", Outcome: "needs-action"}}, Stuck, ReasonShipStop},
		{"no ship", Evidence{Result: ok}, Stuck, ReasonNoShip},
	}
	for _, c := range cases {
		o := Classify(c.e)
		if o.Kind != c.kind || o.Reason != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, o.Kind, o.Reason, c.kind, c.reason)
		}
	}
	if o := Classify(cases[2].e); o.Detail != "A or B; A." {
		t.Errorf("decision detail %q", o.Detail)
	}
}

func TestPromptCarriesIssueAndOverrides(t *testing.T) {
	p := Prompt(ports.Issue{Number: 7, Title: "Slow tests", Body: "## What slowed me down\n4 minutes"}, []string{"go test ./..."})
	for _, want := range []string{"#7", "Slow tests", "4 minutes", "`go test ./...`", "Closes #7", "NEEDS-DECISION:", "STUCK:", "foundry ship"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
