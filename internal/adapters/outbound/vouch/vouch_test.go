package vouch

import (
	"maps"
	"testing"
)

func TestSeverityCounts(t *testing.T) {
	got := severityCounts("minor a.go:1 retry\n  major continuation of the finding\nMajor b.go:2 leak\nfoo c.go:3 other\nnit d.go:4 name\nminor e.go:5 x\n")
	want := map[string]int{"minor": 2, "major": 1, "nit": 1}
	if !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
