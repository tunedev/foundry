package work

import (
	"strings"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/core/ship"
)

type Kind string

const (
	ReadyForHuman Kind = "ready-for-human"
	NeedsDecision Kind = "needs-decision"
	Stuck         Kind = "stuck"
)

type Reason string

const (
	ReasonTimeout            Reason = "timeout"
	ReasonMaxTurns           Reason = "max_turns"
	ReasonReviewNotConverged Reason = "review_not_converged"
	ReasonAgentStuck         Reason = "agent_stuck"
	ReasonShipStop           Reason = "ship_stop"
	ReasonCrash              Reason = "crash"
	ReasonNoShip             Reason = "no_ship"
)

// ShipRecord is the newest line ship wrote for the run's branch.
type ShipRecord struct{ Step, Outcome, PRURL, Detail string }

// Evidence is what a finished agent run left behind.
type Evidence struct {
	Result   ports.AgentResult
	RunErr   error
	TimedOut bool
	Ship     ShipRecord
	Shipped  bool
}

type Outcome struct {
	Kind   Kind
	Reason Reason
	PRURL  string
	Detail string
}

const (
	decisionPrefix = "NEEDS-DECISION:"
	stuckPrefix    = "STUCK:"
)

// Classify reads an issue's outcome from evidence: ship's hand-off record first, then the agent's own
// NEEDS-DECISION reply, and otherwise the first stuck reason that holds.
func Classify(e Evidence) Outcome {
	text := strings.TrimSpace(e.Result.Text)
	stuck := func(r Reason, detail string) Outcome { return Outcome{Kind: Stuck, Reason: r, Detail: detail} }
	switch {
	case e.Shipped && e.Ship.Outcome == string(ReadyForHuman):
		return Outcome{Kind: ReadyForHuman, PRURL: e.Ship.PRURL}
	case !e.TimedOut && e.RunErr == nil && strings.HasPrefix(text, decisionPrefix):
		return Outcome{Kind: NeedsDecision, Detail: strings.TrimSpace(strings.TrimPrefix(text, decisionPrefix))}
	case e.TimedOut:
		return stuck(ReasonTimeout, lastStep(e))
	case e.Result.Subtype == "error_max_turns":
		return stuck(ReasonMaxTurns, lastStep(e))
	case e.Shipped && e.Ship.Step == "review" && strings.HasPrefix(e.Ship.Detail, ship.NotConverged):
		return stuck(ReasonReviewNotConverged, e.Ship.Detail)
	case strings.HasPrefix(text, stuckPrefix):
		return stuck(ReasonAgentStuck, strings.TrimSpace(strings.TrimPrefix(text, stuckPrefix)))
	case e.RunErr != nil:
		return stuck(ReasonCrash, e.RunErr.Error())
	case e.Shipped:
		return stuck(ReasonShipStop, lastStep(e))
	}
	return stuck(ReasonNoShip, text)
}

func lastStep(e Evidence) string {
	if !e.Shipped {
		return "foundry ship never ran"
	}
	return "stop at " + e.Ship.Step + ": " + e.Ship.Detail
}
