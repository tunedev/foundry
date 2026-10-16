package work

import (
	"fmt"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

const claimPrefix = "foundry: claimed by run "

func ClaimComment(runID string) string { return claimPrefix + runID }

type Claim struct {
	RunID string
	At    time.Time
}

// CurrentClaim returns the newest claim comment that bot posted on an issue.
func CurrentClaim(comments []ports.Comment, bot string) (Claim, bool) {
	var c Claim
	found := false
	for _, cm := range comments {
		first, _, _ := strings.Cut(cm.Body, "\n")
		id, ok := strings.CutPrefix(strings.TrimSpace(first), claimPrefix)
		if ok && id != "" && strings.EqualFold(cm.Author, bot) && (!found || !cm.CreatedAt.Before(c.At)) {
			c, found = Claim{RunID: id, At: cm.CreatedAt}, true
		}
	}
	return c, found
}

// Branch names the branch one run works an issue on.
func Branch(issue int, runID string) string { return fmt.Sprintf("friction/%d-%s", issue, runID) }

type Release int

const (
	Keep Release = iota
	Expired
	NoClaim
	Rejected
)

// Decide says what happens to an issue labelled in-progress. pr is the newest pull request on the current
// claim's branch, and found says whether there is one. A closed, unmerged pull request was turned down;
// a claim older than lease with no pull request has expired.
func Decide(c Claim, claimed bool, pr ports.PR, found bool, now time.Time, lease time.Duration) Release {
	switch {
	case !claimed:
		return NoClaim
	case found && pr.State == "CLOSED":
		return Rejected
	case found:
		return Keep
	case now.Sub(c.At) > lease:
		return Expired
	}
	return Keep
}
