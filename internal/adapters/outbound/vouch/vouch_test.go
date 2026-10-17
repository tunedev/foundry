package vouch

import (
	"maps"
	"testing"
)

func TestSeverityCounts(t *testing.T) {
	got := severityCounts("vouch: review r1 has 4 finding(s) at minor or above:\n- minor a.go:1: retry\n  - major continuation of the finding\n- Major b.go:2: leak\n- foo c.go:3: other\n- nit d.go:4: name\n- minor e.go:5: x\n")
	want := map[string]int{"minor": 2, "major": 1, "nit": 1}
	if !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
