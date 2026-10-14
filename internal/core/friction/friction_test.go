package friction

import (
	"slices"
	"strings"
	"testing"

	"github.com/tunedev/foundry/internal/core/ports"
)

func TestUnsafeFlagsSecretsAndUserData(t *testing.T) {
	paths := []string{"/var/lib/atlas/users"}
	cases := map[string]string{
		"contact jane.doe@example.com":             "email address",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123": "GitHub token",
		"key sk-proj-abcdefghijklmnopqrstuv":       "API key",
		"-----BEGIN OPENSSH PRIVATE KEY-----":      "private key",
		"see /var/lib/atlas/users/42/cv.pdf":       "user-data path /var/lib/atlas/users",
	}
	for body, want := range cases {
		if why := Unsafe(body, paths); !slices.Contains(why, want) {
			t.Errorf("%q: got %v, want %q", body, why, want)
		}
	}
	if why := Unsafe("go test ./... takes 4m; pinned github.com/x/y@v1.2.3 in go.mod", paths); len(why) != 0 {
		t.Errorf("false positive: %v", why)
	}
}

func TestValidateBodyNamesMissingHeadings(t *testing.T) {
	body := "## What slowed me down\nx\n## Cost\n5m\n## Proposed fix\ny\n## Source\nPR 3\n"
	if err := ValidateBody(body); err == nil || !strings.Contains(err.Error(), "Cleanup") {
		t.Fatalf("got %v", err)
	}
}

func TestMatchFindsSimilarOpenTitle(t *testing.T) {
	open := []ports.Issue{{Number: 4, Title: "Flaky TestCrawl in CI"}, {Number: 9, Title: "Dev server takes 40s to start"}}
	if m, ok := Match("Server takes 40s to start in dev", open); !ok || m.Number != 9 {
		t.Errorf("similar title: got %+v %v", m, ok)
	}
	if _, ok := Match("vouch gate hangs on large diffs", open); ok {
		t.Error("unrelated title matched")
	}
	if _, ok := Match("CI", open); ok {
		t.Error("a title of short words matched")
	}
}
