package work

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
)

// Hits is how often the friction was reported: the filing plus each later report's comment by a trusted author.
func Hits(is ports.Issue, trusted []string) int {
	n := 1
	for _, c := range is.Comments {
		if Trusted(trusted, c.Author) && strings.HasPrefix(c.Body, friction.HitAgain) {
			n++
		}
	}
	return n
}

// Pick returns up to max ready issues to work: opened by a trusted author, not claimed, not stuck and
// without an open pull request for them, most hits first, then oldest first. The issue body becomes the
// agent's prompt, so an untrusted author's issue is never picked, whatever its labels.
func Pick(ready []ports.Issue, open []ports.PR, max int, trusted []string) []ports.Issue {
	var out []ports.Issue
	for _, is := range ready {
		if !Trusted(trusted, is.Author) || slices.Contains(is.Labels, friction.InProgress) ||
			slices.Contains(is.Labels, friction.Stuck) || hasPR(is.Number, open) {
			continue
		}
		out = append(out, is)
	}
	slices.SortStableFunc(out, func(a, b ports.Issue) int {
		if c := cmp.Compare(Hits(b, trusted), Hits(a, trusted)); c != 0 {
			return c
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return out[:min(max, len(out))]
}

// Trusted reports whether login is one of trusted. GitHub logins are case-insensitive.
func Trusted(trusted []string, login string) bool {
	return slices.ContainsFunc(trusted, func(t string) bool { return strings.EqualFold(t, login) })
}

func hasPR(n int, open []ports.PR) bool {
	closes := regexp.MustCompile(fmt.Sprintf(`(?i)\b(close[sd]?|fix(e[sd])?|resolve[sd]?) #%d\b`, n))
	prefix := fmt.Sprintf("friction/%d-", n)
	return slices.ContainsFunc(open, func(p ports.PR) bool { return strings.HasPrefix(p.Head, prefix) || closes.MatchString(p.Body) })
}
