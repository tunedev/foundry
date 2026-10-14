package ship

import (
	"strings"
	"testing"

	"github.com/tunedev/foundry/internal/core/ports"
)

const good = "## What and why\nx\n## Touched\ncalc\n## Risk\nMedium - touches the parser\n## Verified\nok\n## Links\nnone\n"

func TestValidateRecap(t *testing.T) {
	if err := ValidateRecap(good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecap(strings.Replace(good, "## Touched\ncalc\n", "", 1)); err == nil || !strings.Contains(err.Error(), "Touched") {
		t.Errorf("missing heading: %v", err)
	}
	if err := ValidateRecap(strings.Replace(good, "Medium - touches", "probably fine, touches", 1)); err == nil || !strings.Contains(err.Error(), "low, medium or high") {
		t.Errorf("bad risk: %v", err)
	}
	if err := ValidateRecap(strings.Replace(good, "Medium - touches the parser", "", 1)); err == nil {
		t.Error("empty risk accepted")
	}
}

func TestVerdict(t *testing.T) {
	pass := ports.Check{Name: "build", Bucket: "pass"}
	cases := []struct {
		name string
		in   []ports.Check
		want CI
		red  int
	}{
		{"no checks yet", nil, CIPending, 0},
		{"all pass or skipped", []ports.Check{pass, {Name: "docs", Bucket: "skipping"}}, CIGreen, 0},
		{"one pending", []ports.Check{pass, {Name: "e2e", Bucket: "pending"}}, CIPending, 0},
		{"fail beats pending", []ports.Check{{Name: "e2e", Bucket: "pending"}, {Name: "lint", Bucket: "fail"}, {Name: "x", Bucket: "cancel"}}, CIRed, 2},
	}
	for _, c := range cases {
		got, red := Verdict(c.in)
		if got != c.want || len(red) != c.red {
			t.Errorf("%s: got %v with %d red, want %v with %d", c.name, got, len(red), c.want, c.red)
		}
	}
}
