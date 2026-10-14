package store

import (
	"slices"
	"testing"

	"github.com/tunedev/foundry/internal/core/friction"
)

func TestAnswerRoundTripsForSlashedBranch(t *testing.T) {
	s := Store{Home: t.TempDir()}
	if a, err := s.Answer("acme/widget", "friction/12-slow"); err != nil || a.Given() {
		t.Fatalf("fresh branch: %+v %v", a, err)
	}
	if err := s.SaveAnswer("acme/widget", "friction/12-slow", friction.Answer{Issues: []int{3}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Answer("acme/widget", "friction/12-slow")
	if err != nil || !slices.Equal(got.Issues, []int{3}) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if a, err := s.Answer("acme/widget", "friction"); err != nil || a.Given() {
		t.Fatalf("branch prefix shares state: %+v %v", a, err)
	}
}
