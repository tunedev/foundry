package markdown

import (
	"slices"
	"testing"
)

func TestSectionsReadsH2AndH3HeadingsAcrossLineEndings(t *testing.T) {
	body := "intro\r\n## What and why\r\nAdds Sub.\r\n### Risk:\r\nlow: tiny\r\n"
	s := Sections(body)
	if s["what and why"] != "Adds Sub." || s["risk"] != "low: tiny" {
		t.Fatalf("sections %q", s)
	}
	if m := Missing(s, []string{"What and why", "Risk", "Links"}); !slices.Equal(m, []string{"Links"}) {
		t.Fatalf("missing %v", m)
	}
}
