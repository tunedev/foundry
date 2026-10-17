// Package ship holds the rules a branch meets before it is handed to a human.
package ship

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tunedev/foundry/internal/core/markdown"
)

var RecapHeadings = []string{"What and why", "Touched", "Risk", "Verified", "Links"}

// ValidateRecap returns an error naming every missing heading, or a Risk section that does not
// start with low, medium or high.
func ValidateRecap(body string) error {
	s := markdown.Sections(body)
	if m := markdown.Missing(s, RecapHeadings); len(m) > 0 {
		return fmt.Errorf("recap is missing headings: %s", strings.Join(m, ", "))
	}
	words := strings.Fields(strings.ToLower(s["risk"]))
	if len(words) == 0 {
		return errors.New("recap Risk must start with low, medium or high, then one reason")
	}
	if level := strings.Trim(words[0], ".,:;-*_"); !slices.Contains([]string{"low", "medium", "high"}, level) {
		return fmt.Errorf("recap Risk must start with low, medium or high, got %q", s["risk"])
	}
	return nil
}

// NotConverged starts the review stop ship gives once a branch has used its review rounds.
const NotConverged = "review did not converge"

// Rounds is how many reviews of a branch ended in findings, and the commit the last of them reviewed.
type Rounds struct {
	Count   int
	LastSHA string
}
