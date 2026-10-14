// Package markdown reads the headed sections of a pull request or issue body.
package markdown

import "strings"

// Sections maps each heading name, lowercased and without a trailing colon, to the trimmed text up
// to the next heading. A heading is a line starting with "## " or "### ".
func Sections(body string) map[string]string {
	out := map[string]string{}
	name := ""
	var buf strings.Builder
	flush := func() {
		if name != "" {
			out[name] = strings.TrimSpace(buf.String())
		}
		buf.Reset()
	}
	for line := range strings.Lines(body) {
		if h, ok := heading(strings.TrimSpace(line)); ok {
			flush()
			name = h
			continue
		}
		buf.WriteString(line)
	}
	flush()
	return out
}

func heading(line string) (string, bool) {
	for _, p := range []string{"### ", "## "} {
		if rest, ok := strings.CutPrefix(line, p); ok {
			return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rest), ":")), true
		}
	}
	return "", false
}

// Missing returns the names in want that have no section.
func Missing(sections map[string]string, want []string) []string {
	var missing []string
	for _, w := range want {
		if _, ok := sections[strings.ToLower(w)]; !ok {
			missing = append(missing, w)
		}
	}
	return missing
}
