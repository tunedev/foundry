package friction

import (
	"strings"
	"unicode"

	"github.com/tunedev/foundry/internal/core/ports"
)

// Similar reports whether two titles name the same friction: the Jaccard similarity of their
// lowercased words of three or more characters is at least 0.6.
func Similar(a, b string) bool {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return float64(shared)/float64(len(wa)+len(wb)-shared) >= 0.6
}

// Match returns the first open issue whose title is Similar to title.
func Match(title string, open []ports.Issue) (ports.Issue, bool) {
	for _, is := range open {
		if Similar(title, is.Title) {
			return is, true
		}
	}
	return ports.Issue{}, false
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 3 {
			out[w] = true
		}
	}
	return out
}
