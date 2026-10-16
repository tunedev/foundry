package internal

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCoreImportsNoDriver fails when anything under internal/core depends on an adapter, the runner or os/exec.
func TestCoreImportsNoDriver(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/tunedev/foundry/internal/core/...").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.Contains(dep, "/internal/adapters/") || strings.HasSuffix(dep, "/internal/run") || dep == "os/exec" ||
			dep == "github.com/tunedev/foundry/internal/telemetry" || strings.HasPrefix(dep, "go.opentelemetry.io/otel/sdk") ||
			strings.HasPrefix(dep, "go.opentelemetry.io/otel/exporters") {
			t.Errorf("core depends on %s", dep)
		}
	}
}

// TestCoreReachesNoOSThroughOurPackages fails when a foundry package that internal/core depends on imports os.
func TestCoreReachesNoOSThroughOurPackages(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", `{{if not .Standard}}{{.ImportPath}} {{join .Imports " "}}{{end}}`,
		"github.com/tunedev/foundry/internal/core/...").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if !strings.HasPrefix(fields[0], "github.com/tunedev/foundry/") {
			continue
		}
		for _, imp := range fields[1:] {
			if imp == "os" || imp == "os/exec" {
				t.Errorf("%s imports %s", fields[0], imp)
			}
		}
	}
}
