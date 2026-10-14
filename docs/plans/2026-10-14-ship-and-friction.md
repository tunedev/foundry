# foundry ship and friction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Increment 1 of the spec: a Go binary `foundry` whose `ship` command moves a feature branch through fixed, rerunnable steps to a green pull request, and whose `friction` commands file agent-reported friction as labelled GitHub issues.

**Architecture:** Hexagonal, the same shape as vouch. Pure rules live in `internal/core/{markdown,ship,friction}`. `internal/core/ports` holds the one real port, `Forge` (GitHub now, Gitea plausible). `internal/core/app` composes use cases from step functions that `cmd/foundry/wire.go` injects. Adapters (`gitx`, `github`, `vouch`, `store`) shell out through `internal/run`. An architecture test keeps drivers out of the core.

**Tech Stack:** Go 1.27 stdlib (`flag`, `os/exec`, `encoding/json`, `log/slog`, `math/rand/v2`, `regexp`), `github.com/BurntSushi/toml` (latest), external CLIs `git`, `gh`, `vouch`.

**Spec:** `docs/specs/2026-10-13-ship-and-friction-loop-design.md`

## Global Constraints

- Go 1.27; module path `github.com/tunedev/foundry`; one binary `cmd/foundry`.
- Install location: `~/.local/bin/foundry` via `make install`; skill at `~/.claude/skills/foundry-ship` via `make install-skill`.
- Home: `${FOUNDRY_HOME:-~/.foundry}`. State: `repos/<owner>/<repo>/friction/<url-escaped branch>.json`, `runs.jsonl`, `logs/foundry.jsonl`.
- Config precedence: defaults, then `<home>/config.toml`, then `FOUNDRY_<KEY>` env. Unknown keys and non-positive durations fail at startup with exit 2. Command flags (`--recap`, `--title`, ...) are inputs, not config.
- Per repo: `foundry.toml` at the repo root with `base` (default `main`), `checks` (required, non-empty), `verify`, `max_friction_per_ship` (default 3). A repo without it is not opted in.
- Every external call carries a timeout from config. Only errors wrapped with `ports.ErrTransient` are retried, once, after a jittered pause around `retry_pause`.
- Exit codes: 0 ready for human (or command done), 1 stop (agent action or persistent transient), 2 usage or config.
- Ship step names, in order: `preflight`, `checks`, `review`, `push-pr`, `ci`, `friction`, then `hand-off`.
- Recap headings: `What and why`, `Touched`, `Risk`, `Verified`, `Links`. Friction headings: `What slowed me down`, `Cost`, `Proposed fix`, `Cleanup`, `Source`. A heading is a line starting `## ` or `### `, matched case-insensitively, trailing `:` ignored.
- Labels: `friction`, `friction:ready`, `friction:needs-decision`, `friction:cleanup`, `friction:in-progress`, `friction:stuck`.
- A repo with no CI never reaches green: `ci` stops after `ci_timeout` with "still pending".
- `vouch gate` exits 0 in a repo with no activated rules, so foundry needs no special case for un-opted repos.
- No merge step anywhere. No emojis in code, output or logs. Comments describe current behaviour only.

## Review Focus

1. **Branch names with `/`** (`friction/12-slow-tests`): the friction answer file must be keyed so `friction` and `friction/12-x` never share state. Pinned in Task 5 (`TestAnswerRoundTripsForSlashedBranch`) and Task 7 (`TestShipHandsOffAfterFrictionIsReported` uses a slashed branch).
2. **A PR body edited by a human:** `ship` must not overwrite it unless the agent passes `--recap`. Pinned in Task 7 (`TestShipReplacesInvalidBodyOnlyWithRecap`).
3. **No checks registered yet** right after a push: an empty check list is pending, never green. Pinned in Task 3 (`TestVerdict`, "no checks yet") and Task 7 (`TestShipWaitsOutPendingCIThenReportsFailure`).
4. **New commits after the PR exists:** a rerun pushes them and reads CI for the new head. Pinned in Task 7 (`TestShipPushesNewCommitsOnRerun`).
5. **A transient failure on a non-idempotent write** (create PR, create issue): the retry must look first, so no duplicate is created. Pinned in Task 7 (`TestShipRetriesTransientPRCreateOnce`).

---

### Task 1: Module, command runner and error classification

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`
- Create: `internal/core/ports/ports.go`
- Create: `internal/run/run.go`
- Create: `internal/run/classify.go`
- Test: `internal/run/classify_test.go`
- Create: `internal/arch_test.go`

**Interfaces:**
- Produces: `ports.ErrTransient`; `ports.PR{Number int; URL, Body, HeadSHA string}`; `ports.Check{Name, Bucket, Link string}`; `ports.Issue{Number int; Title, URL string}`; `ports.NewIssue{Title, Body string; Labels []string}`; `ports.Forge` interface (below); `run.Cmd(ctx, timeout, dir, stdin, name, args...) ([]byte, error)`; `run.ErrTimeout`; `run.Classify(err error) error`.

- [ ] **Step 1: Create the module**

```bash
cd ~/forge/foundry
go mod init github.com/tunedev/foundry
go get github.com/BurntSushi/toml@latest
```

Edit `go.mod` so the `go` line reads `go 1.27.0`.

`Makefile`:

```make
BIN := $(HOME)/.local/bin/foundry
SKILL := $(HOME)/.claude/skills/foundry-ship

.PHONY: build test install install-skill
build:
	go build -o bin/foundry ./cmd/foundry
test:
	go test ./...
install:
	go build -o $(BIN) ./cmd/foundry
	@echo installed $(BIN)
install-skill:
	mkdir -p $(SKILL)
	cp skills/foundry-ship/SKILL.md $(SKILL)/SKILL.md
	@echo installed $(SKILL)
```

`.gitignore`:

```
bin/
```

- [ ] **Step 2: Write the ports**

`internal/core/ports/ports.go`:

```go
// Package ports declares what the core needs from a code forge.
package ports

import (
	"context"
	"errors"
)

// ErrTransient marks a failure worth one retry: a timeout, a network error, a forge 5xx or rate limit.
var ErrTransient = errors.New("transient")

type PR struct {
	Number  int
	URL     string
	Body    string
	HeadSHA string
}

// Check is one CI check on a pull request head. Bucket is pass, fail, pending, skipping or cancel.
type Check struct {
	Name   string
	Bucket string
	Link   string
}

type Issue struct {
	Number int
	Title  string
	URL    string
}

type NewIssue struct {
	Title  string
	Body   string
	Labels []string
}

// Forge is the code host: pull requests, their checks, and issues. dir is a checkout of the repo.
type Forge interface {
	FindPR(ctx context.Context, dir, branch string) (PR, bool, error)
	CreatePR(ctx context.Context, dir, base, branch, title, body string) (PR, error)
	EditPRBody(ctx context.Context, dir string, number int, body string) error
	Checks(ctx context.Context, dir string, number int) ([]Check, error)
	EnsureLabels(ctx context.Context, dir string, labels []string) error
	OpenIssues(ctx context.Context, dir, label string) ([]Issue, error)
	CreateIssue(ctx context.Context, dir string, in NewIssue) (Issue, error)
	CommentIssue(ctx context.Context, dir string, number int, body string) error
}
```

- [ ] **Step 3: Write the command runner**

`internal/run/run.go`:

```go
// Package run executes external commands with a deadline.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

var ErrTimeout = errors.New("timed out")

// Cmd runs name in dir and returns stdout. Errors carry the command line, stderr and a stdout excerpt.
// After the timeout it waits at most 5s more for children still holding the output pipes.
// Every run is logged: DEBUG on success, WARN with stderr and stdout excerpts on failure.
func Cmd(ctx context.Context, timeout time.Duration, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env = dir, Env()
	c.WaitDelay = 5 * time.Second
	c.Stdin = stdin
	var stderr bytes.Buffer
	c.Stderr = &stderr
	start := time.Now()
	out, err := c.Output()
	exit := -1
	if c.ProcessState != nil {
		exit = c.ProcessState.ExitCode()
	}
	attrs := []any{"argv", append([]string{name}, args...), "dir", dir, "duration_ms", time.Since(start).Milliseconds(), "exit", exit}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.WarnContext(ctx, "exec timed out", append(attrs, "stderr", tail(stderr.String()))...)
		return out, fmt.Errorf("%s %s: %w after %s", name, strings.Join(args, " "), ErrTimeout, timeout)
	}
	if err != nil {
		slog.WarnContext(ctx, "exec failed", append(attrs, "stderr", tail(stderr.String()), "stdout", tail(string(out)))...)
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		if s := tail(string(out)); s != "" {
			err = fmt.Errorf("%w\nstdout: %s", err, s)
		}
		return out, err
	}
	slog.DebugContext(ctx, "exec", attrs...)
	return out, nil
}

// repoLocal is what `git rev-parse --local-env-vars` prints: the variables that pin git to one repository.
var repoLocal = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// Env is this process's environment without git's repo-local variables, so a child acts on the
// directory it runs in even when foundry runs from inside a git hook.
func Env() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(repoLocal, name)
	})
}

// tail keeps the first and last 2 KB of s: tools print their reason first and their stack last.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4096 {
		return s[:2048] + "\n...\n" + s[len(s)-2048:]
	}
	return s
}
```

- [ ] **Step 4: Write the failing classification test**

`internal/run/classify_test.go`:

```go
package run

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tunedev/foundry/internal/core/ports"
)

func TestClassifyMarksOnlyRetryableFailures(t *testing.T) {
	cases := []struct {
		msg       string
		transient bool
	}{
		{"gh pr create: exit status 1: HTTP 502: Bad Gateway", true},
		{"gh: API rate limit exceeded for user", true},
		{"fatal: unable to access 'https://github.com/x/': Could not resolve host: github.com", true},
		{"read tcp 10.0.0.2:5555: i/o timeout", true},
		{`gh pr create: exit status 1: a pull request for branch "feat" already exists`, false},
		{"HTTP 422: Validation Failed", false},
		{"sh -c go test ./...: exit status 1: FAIL calc", false},
	}
	for _, c := range cases {
		got := errors.Is(Classify(errors.New(c.msg)), ports.ErrTransient)
		if got != c.transient {
			t.Errorf("%q: transient=%v, want %v", c.msg, got, c.transient)
		}
	}
	if !errors.Is(Classify(fmt.Errorf("git fetch: %w after 2m", ErrTimeout)), ports.ErrTransient) {
		t.Error("timeout not transient")
	}
	if Classify(nil) != nil {
		t.Error("nil classified as an error")
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `go test ./internal/run/`
Expected: FAIL, `undefined: Classify`.

- [ ] **Step 6: Implement Classify**

`internal/run/classify.go`:

```go
package run

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/tunedev/foundry/internal/core/ports"
)

var transient = regexp.MustCompile(`(?i)HTTP 5\d\d|rate limit|could not resolve host|connection (reset|refused)|i/o timeout|TLS handshake|unexpected EOF`)

// Classify wraps err with ports.ErrTransient when it is a timeout, a network failure, or a forge-side
// failure worth one retry. Every other error is returned unchanged.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTimeout) || transient.MatchString(err.Error()) {
		return fmt.Errorf("%w: %w", ports.ErrTransient, err)
	}
	return err
}
```

- [ ] **Step 7: Write the architecture test**

`internal/arch_test.go`:

```go
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
		if strings.Contains(dep, "/internal/adapters/") || strings.HasSuffix(dep, "/internal/run") || dep == "os/exec" {
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
		for _, imp := range fields[1:] {
			if imp == "os" || imp == "os/exec" {
				t.Errorf("%s imports %s", fields[0], imp)
			}
		}
	}
}
```

- [ ] **Step 8: Run all tests to verify they pass**

Run: `go mod tidy && go vet ./... && go test ./...`
Expected: PASS for `internal` and `internal/run`.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum Makefile .gitignore internal
git commit -m "Add the command runner, transient classification and the forge port"
```

---

### Task 2: Configuration

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/repo.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Duration{time.Duration}`; `config.Config{CallTimeout, ReviewTimeout, CheckTimeout, CITimeout, CIPoll, RetryPause Duration; UserDataPaths []string; LogLevel, Home string}`; `config.Home(getenv func(string) string) (string, error)`; `config.Load(home string, getenv func(string) string) (Config, error)`; `(Config).Level() (slog.Level, error)`; `config.Repo{Base string; Checks, Verify []string; MaxFrictionPerShip int}`; `config.LoadRepo(dir string) (Repo, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadLayersDefaultsFileThenEnv(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("ci_poll = \"5s\"\ncall_timeout = \"1m\"\n"), 0o644)
	c, err := Load(home, env(map[string]string{"FOUNDRY_CI_POLL": "1s"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.CIPoll.Duration != time.Second || c.CallTimeout.Duration != time.Minute || c.ReviewTimeout.Duration != 30*time.Minute {
		t.Fatalf("ci_poll=%s call_timeout=%s review_timeout=%s", c.CIPoll, c.CallTimeout, c.ReviewTimeout)
	}
}

func TestLoadFailsLoudlyOnBadConfig(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("ci_pol = \"5s\"\n"), 0o644)
	if _, err := Load(home, env(nil)); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Errorf("unknown key: %v", err)
	}
	clean := t.TempDir()
	if _, err := Load(clean, env(map[string]string{"FOUNDRY_CI_POLL": "0s"})); err == nil || !strings.Contains(err.Error(), "ci_poll must be positive") {
		t.Errorf("zero duration: %v", err)
	}
	if _, err := Load(clean, env(map[string]string{"FOUNDRY_LOG_LEVEL": "loud"})); err == nil || !strings.Contains(err.Error(), "log_level") {
		t.Errorf("bad level: %v", err)
	}
}

func TestLoadRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadRepo(dir); err == nil || !strings.Contains(err.Error(), "no foundry.toml") {
		t.Errorf("missing file: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "foundry.toml"), []byte("base = \"main\"\n"), 0o644)
	if _, err := LoadRepo(dir); err == nil || !strings.Contains(err.Error(), "checks") {
		t.Errorf("no checks: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "foundry.toml"), []byte("checks = [\"go test ./...\"]\n"), 0o644)
	r, err := LoadRepo(dir)
	if err != nil || r.Base != "main" || r.MaxFrictionPerShip != 3 {
		t.Fatalf("%+v %v", r, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL, `undefined: Load`.

- [ ] **Step 3: Implement the global config**

`internal/config/config.go`:

```go
// Package config resolves foundry configuration: defaults, then config.toml, then FOUNDRY_* env.
// A repo's own settings come from foundry.toml at its root.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	CallTimeout   Duration `toml:"call_timeout"`
	ReviewTimeout Duration `toml:"review_timeout"`
	CheckTimeout  Duration `toml:"check_timeout"`
	CITimeout     Duration `toml:"ci_timeout"`
	CIPoll        Duration `toml:"ci_poll"`
	RetryPause    Duration `toml:"retry_pause"`
	UserDataPaths []string `toml:"user_data_paths"`
	LogLevel      string   `toml:"log_level"`
	Home          string   `toml:"-"`
}

func Defaults() Config {
	return Config{
		CallTimeout:   Duration{2 * time.Minute},
		ReviewTimeout: Duration{30 * time.Minute},
		CheckTimeout:  Duration{30 * time.Minute},
		CITimeout:     Duration{45 * time.Minute},
		CIPoll:        Duration{30 * time.Second},
		RetryPause:    Duration{2 * time.Second},
		LogLevel:      "info",
	}
}

// Home returns FOUNDRY_HOME, or ~/.foundry.
func Home(getenv func(string) string) (string, error) {
	if h := getenv("FOUNDRY_HOME"); h != "" {
		return h, nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, ".foundry"), nil
}

// Load resolves defaults, then <home>/config.toml, then FOUNDRY_<KEY> env, and validates the result.
func Load(home string, getenv func(string) string) (Config, error) {
	c := Defaults()
	c.Home = home
	md, err := toml.DecodeFile(filepath.Join(home, "config.toml"), &c)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, fmt.Errorf("config.toml: %w", err)
	case len(md.Undecoded()) > 0:
		return c, fmt.Errorf("config.toml: unknown keys %v", md.Undecoded())
	}
	for key, d := range c.durations() {
		name := "FOUNDRY_" + strings.ToUpper(key)
		if v := getenv(name); v != "" {
			if err := d.UnmarshalText([]byte(v)); err != nil {
				return c, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if v := getenv("FOUNDRY_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	return c, c.validate()
}

func (c *Config) durations() map[string]*Duration {
	return map[string]*Duration{
		"call_timeout": &c.CallTimeout, "review_timeout": &c.ReviewTimeout, "check_timeout": &c.CheckTimeout,
		"ci_timeout": &c.CITimeout, "ci_poll": &c.CIPoll, "retry_pause": &c.RetryPause,
	}
}

func (c *Config) validate() error {
	var errs []error
	for key, d := range c.durations() {
		if d.Duration <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", key))
		}
	}
	if _, err := c.Level(); err != nil {
		errs = append(errs, fmt.Errorf("log_level: %w", err))
	}
	return errors.Join(errs...)
}

func (c Config) Level() (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(c.LogLevel))
	return l, err
}
```

- [ ] **Step 4: Implement the repo config**

`internal/config/repo.go`:

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Repo is foundry.toml at a repo's root.
type Repo struct {
	Base               string   `toml:"base"`
	Checks             []string `toml:"checks"`
	Verify             []string `toml:"verify"`
	MaxFrictionPerShip int      `toml:"max_friction_per_ship"`
}

// LoadRepo reads and validates foundry.toml in dir. A directory without one is not opted in.
func LoadRepo(dir string) (Repo, error) {
	r := Repo{Base: "main", MaxFrictionPerShip: 3}
	md, err := toml.DecodeFile(filepath.Join(dir, "foundry.toml"), &r)
	if errors.Is(err, os.ErrNotExist) {
		return r, fmt.Errorf("%s has no foundry.toml; add one to opt the repo in", dir)
	}
	if err != nil {
		return r, fmt.Errorf("foundry.toml: %w", err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return r, fmt.Errorf("foundry.toml: unknown keys %v", u)
	}
	var errs []error
	if r.Base == "" {
		errs = append(errs, errors.New("foundry.toml: base must not be empty"))
	}
	if len(r.Checks) == 0 {
		errs = append(errs, errors.New("foundry.toml: checks must list at least one command"))
	}
	if r.MaxFrictionPerShip < 1 {
		errs = append(errs, errors.New("foundry.toml: max_friction_per_ship must be at least 1"))
	}
	return r, errors.Join(errs...)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config
git commit -m "Resolve layered config and per-repo foundry.toml, failing at startup when invalid"
```

---

### Task 3: Ship rules: markdown sections, recap and CI verdict

**Files:**
- Create: `internal/core/markdown/markdown.go`
- Test: `internal/core/markdown/markdown_test.go`
- Create: `internal/core/ship/recap.go`
- Create: `internal/core/ship/ci.go`
- Test: `internal/core/ship/ship_test.go`

**Interfaces:**
- Consumes: `ports.Check`.
- Produces: `markdown.Sections(body string) map[string]string` (keys lowercased); `markdown.Missing(sections map[string]string, want []string) []string`; `ship.RecapHeadings []string`; `ship.ValidateRecap(body string) error`; `ship.CI` with `ship.CIPending`, `ship.CIGreen`, `ship.CIRed`; `ship.Verdict(checks []ports.Check) (ship.CI, []ports.Check)`.

- [ ] **Step 1: Write the failing tests**

`internal/core/markdown/markdown_test.go`:

```go
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
```

`internal/core/ship/ship_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/...`
Expected: FAIL, `undefined: Sections` and `undefined: ValidateRecap`.

- [ ] **Step 3: Implement markdown sections**

`internal/core/markdown/markdown.go`:

```go
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
```

- [ ] **Step 4: Implement the recap rule**

`internal/core/ship/recap.go`:

```go
// Package ship holds the rules a branch meets before it is handed to a human.
package ship

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tunedev/foundry/internal/core/markdown"
)

var RecapHeadings = []string{"What and why", "Touched", "Risk", "Verified", "Links"}

// ValidateRecap returns an error naming every missing heading, or a Risk section that does not
// start with low, medium or high.
func ValidateRecap(body string) error {
	s := markdown.Sections(body)
	if m := markdown.Missing(s, RecapHeadings); len(m) > 0 {
		return fmt.Errorf("recap is missing headings: %s", strings.Join(m, ", "))
	}
	words := strings.Fields(strings.ToLower(s["risk"]))
	if len(words) == 0 {
		return errors.New("recap Risk must start with low, medium or high, then one reason")
	}
	if level := strings.Trim(words[0], ".,:;-*_"); !slices.Contains([]string{"low", "medium", "high"}, level) {
		return fmt.Errorf("recap Risk must start with low, medium or high, got %q", s["risk"])
	}
	return nil
}
```

- [ ] **Step 5: Implement the CI verdict**

`internal/core/ship/ci.go`:

```go
package ship

import "github.com/tunedev/foundry/internal/core/ports"

type CI int

const (
	CIPending CI = iota
	CIGreen
	CIRed
)

// Verdict reads the checks on a pull request head and returns the failing ones when red.
// No checks counts as pending, since the forge registers a new head's checks after a delay.
// fail and cancel are red; pass and skipping are green; anything else is pending.
func Verdict(checks []ports.Check) (CI, []ports.Check) {
	if len(checks) == 0 {
		return CIPending, nil
	}
	var red []ports.Check
	pending := false
	for _, c := range checks {
		switch c.Bucket {
		case "fail", "cancel":
			red = append(red, c)
		case "pass", "skipping":
		default:
			pending = true
		}
	}
	switch {
	case len(red) > 0:
		return CIRed, red
	case pending:
		return CIPending, nil
	}
	return CIGreen, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/...`
Expected: PASS, including the architecture tests.

- [ ] **Step 7: Commit**

```bash
git add internal/core/markdown internal/core/ship
git commit -m "Validate the system recap and read CI checks, treating no checks as pending"
```

---

### Task 4: Friction rules: kinds, body, public-safe scan, dedupe

**Files:**
- Create: `internal/core/friction/friction.go`
- Create: `internal/core/friction/dedupe.go`
- Test: `internal/core/friction/friction_test.go`

**Interfaces:**
- Consumes: `markdown.Sections`, `markdown.Missing`, `ports.Issue`.
- Produces: `friction.Kind` with `friction.Ready`, `friction.NeedsDecision`, `friction.Cleanup`; `friction.ParseKind(s string) (Kind, error)`; `(Kind).Label() string`; `friction.Label = "friction"`; `friction.AllLabels []string`; `friction.BodyHeadings []string`; `friction.ValidateBody(body string) error`; `friction.Unsafe(text string, userDataPaths []string) []string`; `friction.Answer{Issues []int; None string}` with `(Answer).Given() bool`; `friction.Similar(a, b string) bool`; `friction.Match(title string, open []ports.Issue) (ports.Issue, bool)`.

- [ ] **Step 1: Write the failing tests**

`internal/core/friction/friction_test.go`:

```go
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
		"contact jane.doe@example.com":              "email address",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123":  "GitHub token",
		"key sk-proj-abcdefghijklmnopqrstuv":        "API key",
		"-----BEGIN OPENSSH PRIVATE KEY-----":       "private key",
		"see /var/lib/atlas/users/42/cv.pdf":        "user-data path /var/lib/atlas/users",
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/core/friction/`
Expected: FAIL, `undefined: Unsafe`.

- [ ] **Step 3: Implement the friction rules**

`internal/core/friction/friction.go`:

```go
// Package friction holds the rules for the friction reports agents file as issues.
package friction

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tunedev/foundry/internal/core/markdown"
)

type Kind string

const (
	Ready         Kind = "ready"
	NeedsDecision Kind = "needs-decision"
	Cleanup       Kind = "cleanup"
)

const Label = "friction"

// AllLabels is every label the loop uses; filing a report makes sure they exist.
var AllLabels = []string{Label, "friction:ready", "friction:needs-decision", "friction:cleanup", "friction:in-progress", "friction:stuck"}

func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case Ready, NeedsDecision, Cleanup:
		return k, nil
	}
	return "", fmt.Errorf("--kind must be ready, needs-decision or cleanup, got %q", s)
}

func (k Kind) Label() string { return Label + ":" + string(k) }

var BodyHeadings = []string{"What slowed me down", "Cost", "Proposed fix", "Cleanup", "Source"}

func ValidateBody(body string) error {
	if m := markdown.Missing(markdown.Sections(body), BodyHeadings); len(m) > 0 {
		return fmt.Errorf("report is missing headings: %s", strings.Join(m, ", "))
	}
	return nil
}

var secrets = []struct {
	name string
	re   *regexp.Regexp
}{
	{"email address", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)},
	{"GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)},
	{"API key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// Unsafe returns why text may not be posted publicly: emails, token or key patterns, or a user-data path.
func Unsafe(text string, userDataPaths []string) []string {
	var why []string
	for _, s := range secrets {
		if s.re.MatchString(text) {
			why = append(why, s.name)
		}
	}
	for _, p := range userDataPaths {
		if p != "" && strings.Contains(text, p) {
			why = append(why, "user-data path "+p)
		}
	}
	return why
}

// Answer is what a branch reported at the friction step: the issues it filed or commented on, or why it had none.
type Answer struct {
	Issues []int  `json:"issues,omitempty"`
	None   string `json:"none,omitempty"`
}

func (a Answer) Given() bool { return len(a.Issues) > 0 || a.None != "" }
```

- [ ] **Step 4: Implement dedupe**

`internal/core/friction/dedupe.go`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l . ; go test ./internal/...`
Expected: no files listed by gofmt (run `gofmt -w internal` if the test-table alignment differs); PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/core/friction
git commit -m "Add friction kinds, report validation, the public-safe scan and title dedupe"
```

---

### Task 5: Adapters: git, GitHub, vouch and the local store

**Files:**
- Create: `internal/adapters/outbound/gitx/gitx.go`
- Create: `internal/adapters/outbound/github/github.go`
- Create: `internal/adapters/outbound/vouch/vouch.go`
- Create: `internal/adapters/outbound/store/store.go`
- Test: `internal/adapters/outbound/store/store_test.go`

**Interfaces:**
- Consumes: `run.Cmd`, `run.Classify`, `ports.*`, `friction.Answer`.
- Produces:
  - `gitx.Git{Timeout time.Duration}` with `Toplevel(ctx, dir) (string, error)`, `RepoID(ctx, dir) (string, error)` (returns `owner/name`), `Branch(ctx, dir) (string, error)`, `Clean(ctx, dir) (bool, error)`, `Ahead(ctx, dir, base string) (int, error)`, `HeadSHA(ctx, dir) (string, error)`, `RemoteSHA(ctx, dir, branch string) (string, error)` (`""` when absent), `Push(ctx, dir, branch string) error`, `Subject(ctx, dir) (string, error)`.
  - `github.GitHub{Timeout time.Duration}` implementing `ports.Forge`.
  - `vouch.Vouch{ReviewTimeout, CallTimeout time.Duration}` with `Review(ctx, dir string) error`.
  - `store.Store{Home string}` with `Answer(repo, branch string) (friction.Answer, error)`, `SaveAnswer(repo, branch string, a friction.Answer) error`, `AppendRun(r store.Run) error`; `store.Run{Time time.Time; Kind, Repo, Branch, Step, Outcome, PRURL string; WallMS int64}` with JSON keys `time, kind, repo, branch, step, outcome, pr_url, wall_ms`.

- [ ] **Step 1: Write the failing store test**

`internal/adapters/outbound/store/store_test.go`:

```go
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/adapters/...`
Expected: FAIL, `undefined: Store`.

- [ ] **Step 3: Implement the store**

`internal/adapters/outbound/store/store.go`:

```go
// Package store keeps foundry's local state under its home directory.
package store

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/tunedev/foundry/internal/core/friction"
)

type Store struct{ Home string }

func (s Store) answerPath(repo, branch string) string {
	return filepath.Join(s.Home, "repos", filepath.FromSlash(repo), "friction", url.PathEscape(branch)+".json")
}

// Answer returns what branch reported at the friction step; a branch that reported nothing has a zero Answer.
func (s Store) Answer(repo, branch string) (friction.Answer, error) {
	var a friction.Answer
	b, err := os.ReadFile(s.answerPath(repo, branch))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	return a, json.Unmarshal(b, &a)
}

func (s Store) SaveAnswer(repo, branch string, a friction.Answer) error {
	p := s.answerPath(repo, branch)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Run is one line of runs.jsonl.
type Run struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	Step    string    `json:"step"`
	Outcome string    `json:"outcome"`
	PRURL   string    `json:"pr_url,omitempty"`
	WallMS  int64     `json:"wall_ms"`
}

func (s Store) AppendRun(r Run) error {
	if err := os.MkdirAll(s.Home, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Home, "runs.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
```

- [ ] **Step 4: Run the store test to verify it passes**

Run: `go test ./internal/adapters/outbound/store/`
Expected: PASS.

- [ ] **Step 5: Implement the git adapter**

`internal/adapters/outbound/gitx/gitx.go`:

```go
// Package gitx runs git in a checkout.
package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/run"
)

type Git struct{ Timeout time.Duration }

func (g Git) out(ctx context.Context, dir string, args ...string) (string, error) {
	b, err := run.Cmd(ctx, g.Timeout, dir, nil, "git", args...)
	return strings.TrimSpace(string(b)), run.Classify(err)
}

func (g Git) Toplevel(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "rev-parse", "--show-toplevel")
}

// RepoID returns owner/name from the origin URL, for https, ssh and local-path remotes.
func (g Git) RepoID(ctx context.Context, dir string) (string, error) {
	u, err := g.out(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	parts := strings.Split(strings.ReplaceAll(s, ":", "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("origin %q does not name an owner and repo", u)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

func (g Git) Branch(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// Clean reports whether the checkout has no staged, unstaged or untracked changes.
func (g Git) Clean(ctx context.Context, dir string) (bool, error) {
	s, err := g.out(ctx, dir, "status", "--porcelain")
	return s == "", err
}

// Ahead fetches base from origin and counts the commits on HEAD that origin/base lacks.
func (g Git) Ahead(ctx context.Context, dir, base string) (int, error) {
	if _, err := g.out(ctx, dir, "fetch", "-q", "origin", base); err != nil {
		return 0, err
	}
	s, err := g.out(ctx, dir, "rev-list", "--count", "origin/"+base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func (g Git) HeadSHA(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "rev-parse", "HEAD")
}

// RemoteSHA returns the commit origin holds for branch, or "" when origin has no such branch.
func (g Git) RemoteSHA(ctx context.Context, dir, branch string) (string, error) {
	s, err := g.out(ctx, dir, "ls-remote", "origin", "refs/heads/"+branch)
	if f := strings.Fields(s); err == nil && len(f) > 0 {
		return f[0], nil
	}
	return "", err
}

func (g Git) Push(ctx context.Context, dir, branch string) error {
	_, err := g.out(ctx, dir, "push", "-q", "-u", "origin", "HEAD:refs/heads/"+branch)
	return err
}

func (g Git) Subject(ctx context.Context, dir string) (string, error) {
	return g.out(ctx, dir, "log", "-1", "--format=%s")
}
```

- [ ] **Step 6: Implement the GitHub adapter**

`internal/adapters/outbound/github/github.go`:

```go
// Package github implements the forge port with the gh CLI.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/run"
)

type GitHub struct{ Timeout time.Duration }

func (g GitHub) gh(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	out, err := run.Cmd(ctx, g.Timeout, dir, in, "gh", args...)
	return out, run.Classify(err)
}

type prJSON struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Body       string `json:"body"`
	HeadRefOid string `json:"headRefOid"`
}

func (g GitHub) FindPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) {
	out, err := g.gh(ctx, dir, "", "pr", "list", "--head", branch, "--state", "open",
		"--json", "number,url,body,headRefOid", "--limit", "1")
	if err != nil {
		return ports.PR{}, false, err
	}
	var prs []prJSON
	if err := json.Unmarshal(out, &prs); err != nil {
		return ports.PR{}, false, fmt.Errorf("gh pr list: %w", err)
	}
	if len(prs) == 0 {
		return ports.PR{}, false, nil
	}
	p := prs[0]
	return ports.PR{Number: p.Number, URL: p.URL, Body: p.Body, HeadSHA: p.HeadRefOid}, true, nil
}

func (g GitHub) CreatePR(ctx context.Context, dir, base, branch, title, body string) (ports.PR, error) {
	if _, err := g.gh(ctx, dir, body, "pr", "create", "--base", base, "--head", branch, "--title", title, "--body-file", "-"); err != nil {
		return ports.PR{}, err
	}
	pr, found, err := g.FindPR(ctx, dir, branch)
	if err == nil && !found {
		err = fmt.Errorf("gh pr create succeeded but no open pull request lists head %s", branch)
	}
	return pr, err
}

func (g GitHub) EditPRBody(ctx context.Context, dir string, number int, body string) error {
	_, err := g.gh(ctx, dir, body, "pr", "edit", strconv.Itoa(number), "--body-file", "-")
	return err
}

type checkJSON struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// Checks lists every check on the pull request head. gh exits non-zero while checks fail or are
// pending but still prints them, so its output is read before its error.
func (g GitHub) Checks(ctx context.Context, dir string, number int) ([]ports.Check, error) {
	out, err := g.gh(ctx, dir, "", "pr", "checks", strconv.Itoa(number), "--json", "name,bucket,link")
	var raw []checkJSON
	if jerr := json.Unmarshal(out, &raw); jerr == nil {
		checks := make([]ports.Check, len(raw))
		for i, c := range raw {
			checks[i] = ports.Check{Name: c.Name, Bucket: c.Bucket, Link: c.Link}
		}
		return checks, nil
	}
	if err != nil && strings.Contains(err.Error(), "no checks reported") {
		return nil, nil
	}
	if err == nil {
		err = fmt.Errorf("gh pr checks: unreadable output %q", out)
	}
	return nil, err
}

func (g GitHub) EnsureLabels(ctx context.Context, dir string, labels []string) error {
	for _, l := range labels {
		if _, err := g.gh(ctx, dir, "", "label", "create", l, "--force", "--color", "ededed", "--description", "foundry"); err != nil {
			return err
		}
	}
	return nil
}

func (g GitHub) OpenIssues(ctx context.Context, dir, label string) ([]ports.Issue, error) {
	out, err := g.gh(ctx, dir, "", "issue", "list", "--label", label, "--state", "open", "--json", "number,title,url", "--limit", "500")
	if err != nil {
		return nil, err
	}
	var issues []ports.Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	return issues, nil
}

func (g GitHub) CreateIssue(ctx context.Context, dir string, in ports.NewIssue) (ports.Issue, error) {
	args := []string{"issue", "create", "--title", in.Title, "--body-file", "-"}
	for _, l := range in.Labels {
		args = append(args, "--label", l)
	}
	out, err := g.gh(ctx, dir, in.Body, args...)
	if err != nil {
		return ports.Issue{}, err
	}
	url := strings.TrimSpace(string(out))
	url = url[strings.LastIndex(url, "\n")+1:]
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		return ports.Issue{}, fmt.Errorf("gh issue create: no issue number in %q", url)
	}
	return ports.Issue{Number: n, Title: in.Title, URL: url}, nil
}

func (g GitHub) CommentIssue(ctx context.Context, dir string, number int, body string) error {
	_, err := g.gh(ctx, dir, body, "issue", "comment", strconv.Itoa(number), "--body-file", "-")
	return err
}
```

`ports.Issue` has no JSON tags; `encoding/json` matches `number`, `title`, `url` case-insensitively.

- [ ] **Step 7: Implement the vouch adapter**

`internal/adapters/outbound/vouch/vouch.go`:

```go
// Package vouch runs the vouch reviewer and its push gate.
package vouch

import (
	"context"
	"time"

	"github.com/tunedev/foundry/internal/run"
)

type Vouch struct{ ReviewTimeout, CallTimeout time.Duration }

// Review runs vouch review on HEAD, then vouch gate. A failing gate's error carries vouch's findings.
func (v Vouch) Review(ctx context.Context, dir string) error {
	if _, err := run.Cmd(ctx, v.ReviewTimeout, dir, nil, "vouch", "review"); err != nil {
		return run.Classify(err)
	}
	_, err := run.Cmd(ctx, v.CallTimeout, dir, nil, "vouch", "gate")
	return run.Classify(err)
}
```

- [ ] **Step 8: Verify everything builds and passes**

Run: `go vet ./... && go test ./...`
Expected: PASS. The GitHub, git and vouch adapters are exercised end to end in Task 7.

- [ ] **Step 9: Commit**

```bash
git add internal/adapters
git commit -m "Add git, GitHub, vouch and local-store adapters"
```

---

### Task 6: Friction use cases, the CLI, and the end-to-end harness

**Files:**
- Create: `internal/core/app/step.go`
- Create: `internal/core/app/retry.go`
- Create: `internal/core/app/errors.go`
- Create: `internal/core/app/friction.go`
- Create: `cmd/foundry/main.go`
- Create: `cmd/foundry/wire.go`
- Create: `cmd/foundry/testdata/fakegh/main.go`
- Create: `cmd/foundry/testdata/fakevouch`
- Test: `cmd/foundry/e2e_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1-5.
- Produces: `app.NeedsAction{Step, Detail string}` (error); `app.FrictionDeps{Forge ports.Forge; Load func(repo, branch string) (friction.Answer, error); Save func(repo, branch string, a friction.Answer) error}`; `app.FileInput{Dir, Repo, Branch, Title, Body string; Kind friction.Kind; UserDataPaths []string; Max int; RetryPause time.Duration}`; `app.Filed{Issue ports.Issue; Duplicate bool}`; `app.FileFriction(ctx, d FrictionDeps, in FileInput) (Filed, error)`; `app.NoFriction(d FrictionDeps, repo, branch, reason string) error`; unexported `step`, `retryOnce`, `needs` used by Task 7; in `cmd/foundry`: `repoEnv`, `wireRepo`, `stopWith`, `outcome`, `exitOK/exitStop/exitUsage`; the e2e `world` helpers.

- [ ] **Step 1: Write the app helpers**

`internal/core/app/errors.go`:

```go
// Package app holds foundry's use cases, composed from steps that cmd/foundry injects.
package app

import "fmt"

// NeedsAction is a stop the agent resolves by changing its branch or its report, then rerunning.
type NeedsAction struct{ Step, Detail string }

func (e *NeedsAction) Error() string { return e.Step + ": " + e.Detail }

func needs(step, format string, a ...any) error {
	return &NeedsAction{Step: step, Detail: fmt.Sprintf(format, a...)}
}
```

`internal/core/app/step.go`:

```go
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// step runs fn as the named use-case step, logging its start, outcome and duration.
func step(ctx context.Context, name string, fn func() error) error {
	slog.InfoContext(ctx, "step start", "step", name)
	start := time.Now()
	err := fn()
	attrs := []any{"step", name, "duration_ms", time.Since(start).Milliseconds()}
	if err != nil {
		slog.ErrorContext(ctx, "step failed", append(attrs, "error", err.Error(), "class", class(err))...)
		return err
	}
	slog.InfoContext(ctx, "step done", attrs...)
	return nil
}

func class(err error) string {
	var na *NeedsAction
	switch {
	case errors.As(err, &na):
		return "needs_action"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	}
	return "fail_fast"
}
```

`internal/core/app/retry.go`:

```go
package app

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// retryOnce runs fn, and runs it once more after a pause of pause/2 plus up to pause of jitter
// when the first error is transient. Every other error is returned at once.
func retryOnce(ctx context.Context, pause time.Duration, fn func() error) error {
	err := fn()
	if !errors.Is(err, ports.ErrTransient) {
		return err
	}
	slog.WarnContext(ctx, "retrying transient failure", "error", err.Error())
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(pause/2 + rand.N(pause)):
	}
	return fn()
}
```

- [ ] **Step 2: Write the friction use cases**

`internal/core/app/friction.go`:

```go
package app

import (
	"context"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
)

type FrictionDeps struct {
	Forge ports.Forge
	Load  func(repo, branch string) (friction.Answer, error)
	Save  func(repo, branch string, a friction.Answer) error
}

type FileInput struct {
	Dir, Repo, Branch, Title, Body string
	Kind                           friction.Kind
	UserDataPaths                  []string
	Max                            int
	RetryPause                     time.Duration
}

// Filed is the issue a report landed on. Duplicate means an open issue already named the friction and got a comment.
type Filed struct {
	Issue     ports.Issue
	Duplicate bool
}

// FileFriction validates a report, then comments on a similar open friction issue or creates a labelled one,
// and records the issue against the branch.
func FileFriction(ctx context.Context, d FrictionDeps, in FileInput) (Filed, error) {
	if err := friction.ValidateBody(in.Body); err != nil {
		return Filed{}, needs("friction", "%v", err)
	}
	if why := friction.Unsafe(in.Title+"\n"+in.Body, in.UserDataPaths); len(why) > 0 {
		return Filed{}, needs("friction", "report is not public-safe (%s); describe the problem without user data or secrets", strings.Join(why, ", "))
	}
	a, err := d.Load(in.Repo, in.Branch)
	if err != nil {
		return Filed{}, err
	}
	if len(a.Issues) >= in.Max {
		return Filed{}, needs("friction", "this branch already reported %d friction issues, the maximum", len(a.Issues))
	}
	var f Filed
	err = step(ctx, "file friction", func() error {
		return retryOnce(ctx, in.RetryPause, func() (err error) {
			f, err = fileOnce(ctx, d.Forge, in)
			return err
		})
	})
	if err != nil {
		return f, err
	}
	a.Issues = append(a.Issues, f.Issue.Number)
	a.None = ""
	return f, d.Save(in.Repo, in.Branch, a)
}

// fileOnce lists open friction issues before writing, so a retry after a lost response comments
// on the issue the first attempt created instead of creating a second one.
func fileOnce(ctx context.Context, forge ports.Forge, in FileInput) (Filed, error) {
	if err := forge.EnsureLabels(ctx, in.Dir, friction.AllLabels); err != nil {
		return Filed{}, err
	}
	open, err := forge.OpenIssues(ctx, in.Dir, friction.Label)
	if err != nil {
		return Filed{}, err
	}
	if m, ok := friction.Match(in.Title, open); ok {
		return Filed{Issue: m, Duplicate: true}, forge.CommentIssue(ctx, in.Dir, m.Number, "Hit again from branch `"+in.Branch+"`.\n\n"+in.Body)
	}
	issue, err := forge.CreateIssue(ctx, in.Dir, ports.NewIssue{Title: in.Title, Body: in.Body, Labels: []string{friction.Label, in.Kind.Label()}})
	return Filed{Issue: issue}, err
}

// NoFriction records that branch hit no friction worth reporting.
func NoFriction(d FrictionDeps, repo, branch, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return needs("friction", "--reason is required")
	}
	a, err := d.Load(repo, branch)
	if err != nil {
		return err
	}
	a.None = reason
	return d.Save(repo, branch, a)
}
```

- [ ] **Step 3: Write the CLI**

`cmd/foundry/main.go`:

```go
// Command foundry ships a branch to a green pull request and files the friction met on the way.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
)

const usage = `usage: foundry <command>

  ship [--recap <file>] [--title <t>]
        move this branch through preflight, checks, review, push-pr, ci and friction to a green PR
  friction file --title <t> --body-file <f> --kind ready|needs-decision|cleanup
        file a friction report, or comment on a similar open one
  friction none --reason <r>
        record that this branch hit no friction
  config
        print the resolved config
`

const (
	exitOK    = 0
	exitStop  = 1
	exitUsage = 2
)

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	home, err := config.Home(os.Getenv)
	if err != nil {
		return usageErr(err)
	}
	cfg, err := config.Load(home, os.Getenv)
	if err != nil {
		return usageErr(err)
	}
	logs, err := openLogs(cfg)
	if err != nil {
		return usageErr(err)
	}
	defer logs.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "ship":
		return cmdShip(ctx, cfg, args[1:])
	case "friction":
		return cmdFriction(ctx, cfg, args[1:])
	case "config":
		return cmdConfig(cfg)
	}
	fmt.Fprint(os.Stderr, usage)
	return exitUsage
}

func usageErr(err error) int {
	fmt.Fprintln(os.Stderr, "foundry:", err)
	return exitUsage
}

// openLogs appends structured logs to <home>/logs/foundry.jsonl.
func openLogs(cfg config.Config) (*os.File, error) {
	level, _ := cfg.Level()
	dir := filepath.Join(cfg.Home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "foundry.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})))
	return f, nil
}

func cmdFriction(ctx context.Context, cfg config.Config, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	w, err := wireRepo(ctx, cfg)
	if err != nil {
		return usageErr(err)
	}
	branch, err := w.git.Branch(ctx, w.dir)
	if err != nil {
		return usageErr(err)
	}
	switch args[0] {
	case "file":
		return frictionFile(ctx, w, branch, args[1:])
	case "none":
		fs := flag.NewFlagSet("friction none", flag.ContinueOnError)
		reason := fs.String("reason", "", "why this branch hit no friction")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if err := app.NoFriction(w.frictionDeps(), w.id, branch, *reason); err != nil {
			return stopWith(err)
		}
		fmt.Println("recorded: no friction on", branch)
		return exitOK
	}
	fmt.Fprint(os.Stderr, usage)
	return exitUsage
}

func frictionFile(ctx context.Context, w repoEnv, branch string, args []string) int {
	fs := flag.NewFlagSet("friction file", flag.ContinueOnError)
	title := fs.String("title", "", "issue title naming the symptom")
	bodyFile := fs.String("body-file", "", "file holding the report body")
	kindFlag := fs.String("kind", "", "ready, needs-decision or cleanup")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	kind, err := friction.ParseKind(*kindFlag)
	if err != nil {
		return usageErr(err)
	}
	if *title == "" || *bodyFile == "" {
		return usageErr(errors.New("--title and --body-file are required"))
	}
	body, err := os.ReadFile(*bodyFile)
	if err != nil {
		return usageErr(err)
	}
	f, err := app.FileFriction(ctx, w.frictionDeps(), app.FileInput{
		Dir: w.dir, Repo: w.id, Branch: branch, Title: *title, Body: string(body), Kind: kind,
		UserDataPaths: w.cfg.UserDataPaths, Max: w.repo.MaxFrictionPerShip, RetryPause: w.cfg.RetryPause.Duration,
	})
	if err != nil {
		return stopWith(err)
	}
	if f.Duplicate {
		fmt.Printf("commented on #%d (similar open issue): %s\n", f.Issue.Number, f.Issue.URL)
	} else {
		fmt.Printf("filed #%d: %s\n", f.Issue.Number, f.Issue.URL)
	}
	return exitOK
}

func cmdConfig(cfg config.Config) int {
	fmt.Printf("# home = %s\n", cfg.Home)
	if err := toml.NewEncoder(os.Stdout).Encode(cfg); err != nil {
		return usageErr(err)
	}
	return exitOK
}

// stopWith prints why a command stopped, as the last line an agent reads, and returns exitStop.
func stopWith(err error) int {
	var na *app.NeedsAction
	switch {
	case errors.As(err, &na):
		fmt.Printf("stop at %s: %s\n", na.Step, na.Detail)
	case errors.Is(err, ports.ErrTransient):
		fmt.Printf("stop: transient failure survived one retry; rerun later: %v\n", err)
	default:
		fmt.Printf("stop: %v\n", err)
	}
	return exitStop
}

func outcome(err error) string {
	var na *app.NeedsAction
	switch {
	case err == nil:
		return "ready-for-human"
	case errors.As(err, &na):
		return "needs-action"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	}
	return "error"
}
```

`cmd/foundry/wire.go`:

```go
package main

import (
	"context"
	"os"

	"github.com/tunedev/foundry/internal/adapters/outbound/github"
	"github.com/tunedev/foundry/internal/adapters/outbound/gitx"
	"github.com/tunedev/foundry/internal/adapters/outbound/store"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
)

// repoEnv is the opted-in repo foundry runs in, with the adapters every command uses.
type repoEnv struct {
	cfg     config.Config
	dir, id string
	repo    config.Repo
	git     gitx.Git
	forge   github.GitHub
	store   store.Store
}

// wireRepo resolves the repo containing the working directory and loads its foundry.toml.
func wireRepo(ctx context.Context, cfg config.Config) (repoEnv, error) {
	w := repoEnv{
		cfg:   cfg,
		git:   gitx.Git{Timeout: cfg.CallTimeout.Duration},
		forge: github.GitHub{Timeout: cfg.CallTimeout.Duration},
		store: store.Store{Home: cfg.Home},
	}
	cwd, err := os.Getwd()
	if err != nil {
		return w, err
	}
	if w.dir, err = w.git.Toplevel(ctx, cwd); err != nil {
		return w, err
	}
	if w.repo, err = config.LoadRepo(w.dir); err != nil {
		return w, err
	}
	w.id, err = w.git.RepoID(ctx, w.dir)
	return w, err
}

func (w repoEnv) frictionDeps() app.FrictionDeps {
	return app.FrictionDeps{Forge: w.forge, Load: w.store.Answer, Save: w.store.SaveAnswer}
}
```

Add a temporary `cmdShip` so the package compiles; Task 7 replaces it:

```go
// in cmd/foundry/main.go
func cmdShip(ctx context.Context, cfg config.Config, args []string) int {
	return usageErr(errors.New("ship is not built yet"))
}
```

- [ ] **Step 4: Write the fakes**

`cmd/foundry/testdata/fakevouch` (mode 0755):

```sh
#!/bin/sh
# Fake vouch: review passes; gate fails with the contents of $FAKE_LOGS/vouch-gate-fail when that file exists.
echo "$@" >> "$FAKE_LOGS/vouch.calls"
case "$1" in
  review) exit 0 ;;
  gate)
    if [ -f "$FAKE_LOGS/vouch-gate-fail" ]; then cat "$FAKE_LOGS/vouch-gate-fail"; exit 1; fi
    exit 0 ;;
esac
echo "fake vouch: unhandled $*" >&2; exit 1
```

`cmd/foundry/testdata/fakegh/main.go`:

```go
// Command fakegh stands in for gh in foundry's end-to-end tests. Its state is the JSON file named by
// FAKE_STATE. A pull request's head is read from the checkout's origin, as GitHub would report it.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

type pr struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Body   string `json:"body"`
	Head   string `json:"head"`
	Title  string `json:"title"`
}

type issue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

type comment struct {
	Issue int    `json:"issue"`
	Body  string `json:"body"`
}

type check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

type state struct {
	PRs         []pr              `json:"prs"`
	Issues      []issue           `json:"issues"`
	Comments    []comment         `json:"comments"`
	Labels      []string          `json:"labels"`
	Checks      [][]check         `json:"checks"`
	ChecksCalls int               `json:"checks_calls"`
	FailOnce    map[string]string `json:"fail_once"`
	Writes      int               `json:"writes"`
}

func main() {
	path := os.Getenv("FAKE_STATE")
	var s state
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	args := os.Args[1:]
	cmd := strings.Join(args[:min(2, len(args))], " ")
	if msg, ok := s.FailOnce[cmd]; ok {
		delete(s.FailOnce, cmd)
		save(path, s)
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
	code := handle(&s, cmd, args[min(2, len(args)):])
	save(path, s)
	os.Exit(code)
}

func handle(s *state, cmd string, rest []string) int {
	switch cmd {
	case "pr list":
		head := flagVal(rest, "--head")
		out := []map[string]any{}
		for _, p := range s.PRs {
			if p.Head == head {
				out = append(out, map[string]any{"number": p.Number, "url": p.URL, "body": p.Body, "headRefOid": remoteHead(head)})
			}
		}
		printJSON(out)
	case "pr create":
		n := len(s.PRs) + 1
		p := pr{Number: n, URL: fmt.Sprintf("https://github.com/acme/widget/pull/%d", n), Body: stdin(),
			Head: flagVal(rest, "--head"), Title: flagVal(rest, "--title")}
		s.PRs = append(s.PRs, p)
		s.Writes++
		fmt.Println(p.URL)
	case "pr edit":
		n, _ := strconv.Atoi(rest[0])
		s.PRs[n-1].Body = stdin()
		s.Writes++
	case "pr checks":
		cs := []check{{Name: "build", Bucket: "pass"}}
		if len(s.Checks) > 0 {
			cs = s.Checks[min(s.ChecksCalls, len(s.Checks)-1)]
		}
		s.ChecksCalls++
		printJSON(cs)
		for _, c := range cs {
			if c.Bucket == "fail" {
				return 1
			}
		}
		for _, c := range cs {
			if c.Bucket == "pending" {
				return 8
			}
		}
	case "label create":
		if !slices.Contains(s.Labels, rest[0]) {
			s.Labels = append(s.Labels, rest[0])
		}
		s.Writes++
	case "issue list":
		label := flagVal(rest, "--label")
		out := []map[string]any{}
		for _, is := range s.Issues {
			if slices.Contains(is.Labels, label) {
				out = append(out, map[string]any{"number": is.Number, "title": is.Title, "url": is.URL})
			}
		}
		printJSON(out)
	case "issue create":
		n := len(s.Issues) + 1
		url := fmt.Sprintf("https://github.com/acme/widget/issues/%d", n)
		s.Issues = append(s.Issues, issue{Number: n, Title: flagVal(rest, "--title"), URL: url, Body: stdin(), Labels: flagVals(rest, "--label")})
		s.Writes++
		fmt.Println(url)
	case "issue comment":
		n, _ := strconv.Atoi(rest[0])
		s.Comments = append(s.Comments, comment{Issue: n, Body: stdin()})
		s.Writes++
	default:
		fmt.Fprintln(os.Stderr, "fake gh: unhandled", cmd)
		return 1
	}
	return 0
}

func flagVal(args []string, name string) string {
	if v := flagVals(args, name); len(v) > 0 {
		return v[0]
	}
	return ""
}

func flagVals(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func stdin() string {
	b, _ := io.ReadAll(os.Stdin)
	return string(b)
}

func remoteHead(branch string) string {
	out, _ := exec.Command("git", "ls-remote", "origin", "refs/heads/"+branch).Output()
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0]
	}
	return ""
}

func printJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func save(path string, s state) {
	b, _ := json.Marshal(s)
	os.WriteFile(path, b, 0o644)
}
```

- [ ] **Step 5: Write the harness and the failing friction tests**

`cmd/foundry/e2e_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var bin, fakebin string

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "foundry-e2e-")
	bin, fakebin = filepath.Join(dir, "foundry"), filepath.Join(dir, "fakebin")
	for out, pkg := range map[string]string{bin: ".", filepath.Join(fakebin, "gh"): "./testdata/fakegh"} {
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			os.Stderr.Write(b)
			os.Exit(1)
		}
	}
	vouch, _ := os.ReadFile("testdata/fakevouch")
	os.WriteFile(filepath.Join(fakebin, "vouch"), vouch, 0o755)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const goodRecap = "## What and why\nAdds Sub.\n## Touched\ncalc\n## Risk\nlow: one pure function\n## Verified\ngo test ./... ok\n## Links\nnone\n"

const goodFriction = "## What slowed me down\n`go test ./...` took 4 minutes.\n## Cost\n10 minutes waiting.\n" +
	"## Proposed fix\nCache the fixture build.\n## Cleanup\nnone\n## Source\nbranch feat\n"

type world struct {
	t                                   *testing.T
	root, home, checkout, origin, state string
	env                                 []string
}

func newWorld(t *testing.T, branch string) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, root: root, home: filepath.Join(root, "foundry-home"), checkout: filepath.Join(root, "work", "widget"),
		origin: filepath.Join(root, "remotes", "acme", "widget.git"), state: filepath.Join(root, "gh-state.json")}
	w.env = append(os.Environ(),
		"PATH="+fakebin+":"+os.Getenv("PATH"), "HOME="+root, "FOUNDRY_HOME="+w.home,
		"FAKE_STATE="+w.state, "FAKE_LOGS="+root,
		"FOUNDRY_CI_POLL=10ms", "FOUNDRY_RETRY_PAUSE=1ms", "FOUNDRY_CI_TIMEOUT=5s",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, ".gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	seed := filepath.Join(root, "seed")
	w.git("", "init", "-q", "-b", "main", seed)
	w.write(seed, "foundry.toml", "checks = [\"true\"]\n")
	w.write(seed, "calc.go", "package calc\n")
	w.git(seed, "add", ".")
	w.git(seed, "commit", "-qm", "seed")
	w.git("", "clone", "-q", "--bare", seed, w.origin)
	w.git("", "clone", "-q", w.origin, w.checkout)
	w.git(w.checkout, "checkout", "-qb", branch)
	w.commit("feature work")
	w.write(root, "recap.md", goodRecap)
	return w
}

func (w *world) git(dir string, args ...string) string {
	w.t.Helper()
	c := exec.Command("git", args...)
	c.Dir, c.Env = dir, w.env
	out, err := c.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (w *world) write(dir, name, content string) string {
	w.t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) commit(msg string) string {
	w.t.Helper()
	f := filepath.Join(w.checkout, "calc.go")
	b, _ := os.ReadFile(f)
	os.WriteFile(f, append(b, []byte("// "+msg+"\n")...), 0o644)
	w.git(w.checkout, "commit", "-qam", msg)
	return w.git(w.checkout, "rev-parse", "HEAD")
}

func (w *world) recap() string { return filepath.Join(w.root, "recap.md") }

func (w *world) foundry(args ...string) (string, string, int) {
	w.t.Helper()
	c := exec.Command(bin, args...)
	c.Dir, c.Env = w.checkout, w.env
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode()
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return out.String(), errb.String(), 0
}

func (w *world) setGH(js string) { os.WriteFile(w.state, []byte(js), 0o644) }

type ghState struct {
	PRs []struct {
		Number     int
		Body, Head string
	} `json:"prs"`
	Issues []struct {
		Number int
		Title  string
		Labels []string
	} `json:"issues"`
	Comments    []struct{ Issue int } `json:"comments"`
	Labels      []string              `json:"labels"`
	ChecksCalls int                   `json:"checks_calls"`
	Writes      int                   `json:"writes"`
}

func (w *world) gh() ghState {
	var s ghState
	b, _ := os.ReadFile(w.state)
	json.Unmarshal(b, &s)
	return s
}

type runLine struct {
	Branch, Step, Outcome string
	PRURL                 string `json:"pr_url"`
}

func (w *world) runs() []runLine {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.home, "runs.jsonl"))
	if err != nil {
		w.t.Fatal(err)
	}
	var out []runLine
	for line := range strings.Lines(string(b)) {
		var r runLine
		json.Unmarshal([]byte(line), &r)
		out = append(out, r)
	}
	return out
}

func TestFrictionFileCreatesLabelledIssueThenDedupes(t *testing.T) {
	w := newWorld(t, "feat")
	body := w.write(w.root, "f.md", goodFriction)
	out, errOut, code := w.foundry("friction", "file", "--title", "Test suite takes 4 minutes to run", "--body-file", body, "--kind", "ready")
	if code != 0 || !strings.Contains(out, "filed #1") {
		t.Fatalf("code %d\n%s%s", code, out, errOut)
	}
	s := w.gh()
	if len(s.Issues) != 1 || !slices.Equal(s.Issues[0].Labels, []string{"friction", "friction:ready"}) || !slices.Contains(s.Labels, "friction:needs-decision") {
		t.Fatalf("state %+v", s)
	}
	out, _, code = w.foundry("friction", "file", "--title", "Test suite takes 4 minutes", "--body-file", body, "--kind", "ready")
	if s := w.gh(); code != 0 || !strings.Contains(out, "commented on #1") || len(s.Issues) != 1 || len(s.Comments) != 1 {
		t.Fatalf("code %d\n%s\nstate %+v", code, out, s)
	}
}

func TestFrictionRejectsUnsafeReports(t *testing.T) {
	w := newWorld(t, "feat")
	body := w.write(w.root, "f.md", goodFriction+"Reproduced with jane@example.com's account.\n")
	out, _, code := w.foundry("friction", "file", "--title", "Login loops", "--body-file", body, "--kind", "ready")
	if code != 1 || !strings.Contains(out, "not public-safe (email address)") || len(w.gh().Issues) != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestFrictionCapsReportsPerBranch(t *testing.T) {
	w := newWorld(t, "feat")
	w.write(w.checkout, "foundry.toml", "checks = [\"true\"]\nmax_friction_per_ship = 1\n")
	body := w.write(w.root, "f.md", goodFriction)
	if _, _, code := w.foundry("friction", "file", "--title", "Slow tests", "--body-file", body, "--kind", "ready"); code != 0 {
		t.Fatalf("first report: code %d", code)
	}
	out, _, code := w.foundry("friction", "file", "--title", "Broken docs link", "--body-file", body, "--kind", "ready")
	if code != 1 || !strings.Contains(out, "the maximum") || len(w.gh().Issues) != 1 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestFrictionRejectsUnknownKind(t *testing.T) {
	w := newWorld(t, "feat")
	body := w.write(w.root, "f.md", goodFriction)
	if _, errOut, code := w.foundry("friction", "file", "--title", "x", "--body-file", body, "--kind", "severe"); code != 2 || !strings.Contains(errOut, "--kind") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `chmod +x cmd/foundry/testdata/fakevouch && go vet ./... && go test ./...`
Expected: PASS. If a friction test fails, the harness or the use case is wrong; fix the cause, not the assertion.

- [ ] **Step 7: Commit**

```bash
git add internal/core/app cmd/foundry
git commit -m "File friction reports from the CLI with dedupe, a public-safe scan and a per-branch cap"
```

---

### Task 7: The ship use case

**Files:**
- Create: `internal/core/app/ship.go`
- Modify: `cmd/foundry/main.go` (replace the temporary `cmdShip`)
- Modify: `cmd/foundry/wire.go` (add `shipDeps`)
- Test: `cmd/foundry/e2e_test.go` (append)

**Interfaces:**
- Consumes: `ship.ValidateRecap`, `ship.RecapHeadings`, `ship.Verdict`, `ports.Forge`, `friction.Answer`, `step`, `retryOnce`, `needs`, the e2e harness.
- Produces: `app.ShipDeps`, `app.ShipInput`, `app.Report{Repo, Branch string; Passed []string; Step, PRURL string}`, `app.HandOff = "hand-off"`, `app.Ship(ctx, d ShipDeps, in ShipInput) (Report, error)`.

- [ ] **Step 1: Write the failing ship tests**

Append to `cmd/foundry/e2e_test.go`:

```go
func TestShipRefusesBaseBranch(t *testing.T) {
	w := newWorld(t, "feat")
	w.git(w.checkout, "checkout", "-q", "main")
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 1 || !strings.Contains(out, "stop at preflight") {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestShipHandsOffAfterFrictionIsReported(t *testing.T) {
	branch := "friction/12-slow-tests"
	w := newWorld(t, branch)
	out, errOut, code := w.foundry("ship", "--recap", w.recap())
	if code != 1 || !strings.Contains(out, "stop at friction") {
		t.Fatalf("code %d\n%s%s", code, out, errOut)
	}
	if prs := w.gh().PRs; len(prs) != 1 || !strings.Contains(prs[0].Body, "## Risk") {
		t.Fatalf("prs %+v", prs)
	}
	if w.git(w.origin, "rev-parse", "refs/heads/"+branch) != w.git(w.checkout, "rev-parse", "HEAD") {
		t.Fatal("branch not pushed")
	}
	if _, _, code := w.foundry("friction", "none", "--reason", "smooth run"); code != 0 {
		t.Fatalf("friction none: code %d", code)
	}
	out, _, code = w.foundry("ship")
	if code != 0 || !strings.Contains(out, "ready for human: https://github.com/acme/widget/pull/1") {
		t.Fatalf("code %d\n%s", code, out)
	}
	runs := w.runs()
	if last := runs[len(runs)-1]; last.Outcome != "ready-for-human" || last.Branch != branch || last.Step != "hand-off" || last.PRURL == "" {
		t.Fatalf("run record %+v", last)
	}
}

func TestShipRerunMakesNoRemoteWrites(t *testing.T) {
	w := newWorld(t, "feat")
	w.foundry("friction", "none", "--reason", "smooth run")
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 {
		t.Fatalf("first ship: code %d\n%s", code, out)
	}
	writes, remote := w.gh().Writes, w.git(w.origin, "rev-parse", "refs/heads/feat")
	out, _, code := w.foundry("ship", "--recap", w.recap())
	if code != 0 || w.gh().Writes != writes || w.git(w.origin, "rev-parse", "refs/heads/feat") != remote {
		t.Fatalf("rerun: code %d, writes %d -> %d\n%s", code, writes, w.gh().Writes, out)
	}
}

func TestShipNeedsRecapWithRiskLevel(t *testing.T) {
	w := newWorld(t, "feat")
	out, _, code := w.foundry("ship")
	if code != 1 || !strings.Contains(out, "stop at push-pr") || !strings.Contains(out, "--recap") {
		t.Fatalf("no recap: code %d\n%s", code, out)
	}
	bad := w.write(w.root, "bad.md", strings.Replace(goodRecap, "low: one pure function", "fine", 1))
	out, _, code = w.foundry("ship", "--recap", bad)
	if code != 1 || !strings.Contains(out, "Risk must start with low, medium or high") || len(w.gh().PRs) != 0 {
		t.Fatalf("bad risk: code %d\n%s", code, out)
	}
}

func TestShipReplacesInvalidBodyOnlyWithRecap(t *testing.T) {
	w := newWorld(t, "feat")
	w.setGH(`{"prs":[{"number":1,"url":"https://github.com/acme/widget/pull/1","body":"quick fix","head":"feat"}]}`)
	out, _, code := w.foundry("ship")
	if code != 1 || !strings.Contains(out, "PR #1 body") || w.gh().PRs[0].Body != "quick fix" {
		t.Fatalf("code %d\n%s", code, out)
	}
	w.foundry("friction", "none", "--reason", "smooth run")
	out, _, code = w.foundry("ship", "--recap", w.recap())
	if s := w.gh(); code != 0 || len(s.PRs) != 1 || !strings.Contains(s.PRs[0].Body, "## What and why") {
		t.Fatalf("code %d\n%s\nstate %+v", code, out, s)
	}
}

func TestShipStopsAtFailingCheckWithItsOutput(t *testing.T) {
	w := newWorld(t, "feat")
	w.write(w.checkout, "foundry.toml", "checks = [\"echo TestAdd failed; exit 1\"]\n")
	w.git(w.checkout, "commit", "-qam", "break the check")
	out, _, code := w.foundry("ship", "--recap", w.recap())
	if code != 1 || !strings.Contains(out, "stop at checks") || !strings.Contains(out, "TestAdd failed") || len(w.gh().PRs) != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestShipStopsAtVouchFindings(t *testing.T) {
	w := newWorld(t, "feat")
	w.write(w.root, "vouch-gate-fail", "blocker calc.go:1 unchecked overflow\n")
	out, _, code := w.foundry("ship", "--recap", w.recap())
	if code != 1 || !strings.Contains(out, "stop at review") || !strings.Contains(out, "unchecked overflow") {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestShipWaitsOutPendingCIThenReportsFailure(t *testing.T) {
	w := newWorld(t, "feat")
	w.setGH(`{"checks":[[],[{"name":"build","bucket":"pending"}],[{"name":"build","bucket":"fail","link":"https://ci/run/9"}]]}`)
	out, _, code := w.foundry("ship", "--recap", w.recap())
	if code != 1 || !strings.Contains(out, "stop at ci") || !strings.Contains(out, "https://ci/run/9") || w.gh().ChecksCalls != 3 {
		t.Fatalf("code %d, checks calls %d\n%s", code, w.gh().ChecksCalls, out)
	}
}

func TestShipStopsWhenCIStaysPending(t *testing.T) {
	w := newWorld(t, "feat")
	w.env = append(w.env, "FOUNDRY_CI_TIMEOUT=50ms")
	w.setGH(`{"checks":[[{"name":"build","bucket":"pending"}]]}`)
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 1 || !strings.Contains(out, "still pending") {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestShipRetriesTransientPRCreateOnce(t *testing.T) {
	w := newWorld(t, "feat")
	w.foundry("friction", "none", "--reason", "smooth run")
	w.setGH(`{"fail_once":{"pr create":"HTTP 502: Bad Gateway (https://api.github.com/graphql)"}}`)
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 || len(w.gh().PRs) != 1 {
		t.Fatalf("code %d, prs %d\n%s", code, len(w.gh().PRs), out)
	}
}

func TestShipPushesNewCommitsOnRerun(t *testing.T) {
	w := newWorld(t, "feat")
	w.foundry("friction", "none", "--reason", "smooth run")
	if _, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 {
		t.Fatalf("first ship: code %d", code)
	}
	head := w.commit("review follow-up")
	out, _, code := w.foundry("ship")
	if code != 0 || w.git(w.origin, "rev-parse", "refs/heads/feat") != head {
		t.Fatalf("code %d\n%s", code, out)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/foundry/ -run TestShip`
Expected: FAIL; every ship test exits 2 with "ship is not built yet".

- [ ] **Step 3: Implement the ship use case**

`internal/core/app/ship.go`:

```go
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/core/ship"
)

// ShipDeps are the concrete steps cmd/foundry injects. Forge is the one port with a second implementation.
type ShipDeps struct {
	Forge     ports.Forge
	Branch    func(ctx context.Context, dir string) (string, error)
	Clean     func(ctx context.Context, dir string) (bool, error)
	Ahead     func(ctx context.Context, dir, base string) (int, error)
	HeadSHA   func(ctx context.Context, dir string) (string, error)
	RemoteSHA func(ctx context.Context, dir, branch string) (string, error)
	Push      func(ctx context.Context, dir, branch string) error
	Subject   func(ctx context.Context, dir string) (string, error)
	RunCheck  func(ctx context.Context, dir, command string) error
	Review    func(ctx context.Context, dir string) error
	Answer    func(repo, branch string) (friction.Answer, error)
}

type ShipInput struct {
	Dir, Repo, Base               string
	Checks                        []string
	Recap, Title                  string
	CITimeout, CIPoll, RetryPause time.Duration
}

// Report is how far a ship run got: the steps that passed and the step it stopped at.
type Report struct {
	Repo, Branch string
	Passed       []string
	Step         string
	PRURL        string
}

const HandOff = "hand-off"

// Ship moves the branch checked out at in.Dir through every step up to a green pull request. Each step
// reads current state, so a rerun resumes at the first unmet step and makes no remote writes once all
// steps are met. It returns a *NeedsAction for a stop the agent fixes, and an ErrTransient error when a
// transient failure survived its retry.
func Ship(ctx context.Context, d ShipDeps, in ShipInput) (Report, error) {
	r := Report{Repo: in.Repo}
	var head string
	var pr ports.PR
	steps := []struct {
		name string
		fn   func() error
	}{
		{"preflight", func() (err error) { r.Branch, err = preflight(ctx, d, in); return err }},
		{"checks", func() error { return runChecks(ctx, d, in) }},
		{"review", func() error { return review(ctx, d, in) }},
		{"push-pr", func() (err error) {
			head, pr, err = pushAndPR(ctx, d, in, r.Branch)
			r.PRURL = pr.URL
			return err
		}},
		{"ci", func() error { return waitCI(ctx, d, in, r.Branch, head, pr) }},
		{"friction", func() error { return frictionGiven(d, in.Repo, r.Branch) }},
	}
	for _, s := range steps {
		r.Step = s.name
		if err := step(ctx, s.name, s.fn); err != nil {
			return r, err
		}
		r.Passed = append(r.Passed, s.name)
	}
	r.Step = HandOff
	return r, nil
}

func preflight(ctx context.Context, d ShipDeps, in ShipInput) (string, error) {
	branch, err := d.Branch(ctx, in.Dir)
	if err != nil {
		return "", err
	}
	if branch == in.Base || branch == "HEAD" {
		return branch, needs("preflight", "on %s; ship from a feature branch", branch)
	}
	clean, err := d.Clean(ctx, in.Dir)
	if err != nil {
		return branch, err
	}
	if !clean {
		return branch, needs("preflight", "uncommitted changes; commit or stash them, then rerun")
	}
	var ahead int
	if err := retryOnce(ctx, in.RetryPause, func() (err error) { ahead, err = d.Ahead(ctx, in.Dir, in.Base); return err }); err != nil {
		return branch, err
	}
	if ahead == 0 {
		return branch, needs("preflight", "no commits ahead of origin/%s", in.Base)
	}
	return branch, nil
}

func runChecks(ctx context.Context, d ShipDeps, in ShipInput) error {
	for _, c := range in.Checks {
		if err := d.RunCheck(ctx, in.Dir, c); err != nil {
			return needs("checks", "%s failed; fix it, commit, rerun:\n%v", c, err)
		}
	}
	return nil
}

func review(ctx context.Context, d ShipDeps, in ShipInput) error {
	err := retryOnce(ctx, in.RetryPause, func() error { return d.Review(ctx, in.Dir) })
	if err == nil || isTransient(err) {
		return err
	}
	return needs("review", "vouch did not pass this code; fix the findings, commit, rerun:\n%v", err)
}

// pushAndPR pushes HEAD when origin lacks it and makes sure an open pull request carries a valid recap.
// It edits the body only when the agent passed a different recap, and checks for an existing pull
// request before every create, so a retried create never opens a second one.
func pushAndPR(ctx context.Context, d ShipDeps, in ShipInput, branch string) (string, ports.PR, error) {
	var pr ports.PR
	head, err := d.HeadSHA(ctx, in.Dir)
	if err != nil {
		return "", pr, err
	}
	var remote string
	if err := retryOnce(ctx, in.RetryPause, func() (err error) { remote, err = d.RemoteSHA(ctx, in.Dir, branch); return err }); err != nil {
		return head, pr, err
	}
	if remote != head {
		if err := retryOnce(ctx, in.RetryPause, func() error { return d.Push(ctx, in.Dir, branch) }); err != nil {
			return head, pr, err
		}
	}
	var found bool
	find := func() (err error) { pr, found, err = d.Forge.FindPR(ctx, in.Dir, branch); return err }
	if err := retryOnce(ctx, in.RetryPause, find); err != nil {
		return head, pr, err
	}
	switch {
	case !found:
		if err := recapReady(in.Recap); err != nil {
			return head, pr, err
		}
		title := in.Title
		if title == "" {
			if title, err = d.Subject(ctx, in.Dir); err != nil {
				return head, pr, err
			}
		}
		create := func() error {
			if err := find(); err != nil || found {
				return err
			}
			pr, err = d.Forge.CreatePR(ctx, in.Dir, in.Base, branch, title, in.Recap)
			return err
		}
		return head, pr, retryOnce(ctx, in.RetryPause, create)
	case in.Recap != "" && !sameBody(in.Recap, pr.Body):
		if err := recapReady(in.Recap); err != nil {
			return head, pr, err
		}
		err := retryOnce(ctx, in.RetryPause, func() error { return d.Forge.EditPRBody(ctx, in.Dir, pr.Number, in.Recap) })
		pr.Body = in.Recap
		return head, pr, err
	}
	if err := ship.ValidateRecap(pr.Body); err != nil {
		return head, pr, needs("push-pr", "PR #%d body: %v; pass --recap <file> to replace it", pr.Number, err)
	}
	return head, pr, nil
}

func recapReady(recap string) error {
	if recap == "" {
		return needs("push-pr", "no PR yet; write the system recap to a file with headings %s and pass --recap <file>",
			strings.Join(ship.RecapHeadings, ", "))
	}
	if err := ship.ValidateRecap(recap); err != nil {
		return needs("push-pr", "%v", err)
	}
	return nil
}

func sameBody(a, b string) bool {
	norm := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	return norm(a) == norm(b)
}

// waitCI polls the pull request until the forge reports head as its head and every check on it is green,
// a check fails, or in.CITimeout passes.
func waitCI(ctx context.Context, d ShipDeps, in ShipInput, branch, head string, pr ports.PR) error {
	deadline := time.Now().Add(in.CITimeout)
	for {
		if pr.HeadSHA == head {
			var checks []ports.Check
			if err := retryOnce(ctx, in.RetryPause, func() (err error) { checks, err = d.Forge.Checks(ctx, in.Dir, pr.Number); return err }); err != nil {
				return err
			}
			switch state, red := ship.Verdict(checks); state {
			case ship.CIGreen:
				return nil
			case ship.CIRed:
				return needs("ci", "failing checks on %s; fix them, commit, rerun:\n%s", short(head), describe(red))
			}
		}
		if time.Now().After(deadline) {
			return needs("ci", "checks on %s still pending after %s; rerun foundry ship", short(head), in.CITimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(in.CIPoll):
		}
		if pr.HeadSHA != head {
			if err := retryOnce(ctx, in.RetryPause, func() (err error) { pr, _, err = d.Forge.FindPR(ctx, in.Dir, branch); return err }); err != nil {
				return err
			}
		}
	}
}

func describe(checks []ports.Check) string {
	var b strings.Builder
	for _, c := range checks {
		fmt.Fprintf(&b, "- %s (%s) %s\n", c.Name, c.Bucket, c.Link)
	}
	return strings.TrimSpace(b.String())
}

func short(sha string) string { return sha[:min(7, len(sha))] }

func frictionGiven(d ShipDeps, repo, branch string) error {
	a, err := d.Answer(repo, branch)
	if err != nil || a.Given() {
		return err
	}
	return needs("friction", "report what slowed you down on this branch: foundry friction file --title <t> --body-file <f> "+
		"--kind ready|needs-decision|cleanup, or foundry friction none --reason <r>")
}
```

Add to `internal/core/app/retry.go`:

```go
func isTransient(err error) bool { return errors.Is(err, ports.ErrTransient) }
```

- [ ] **Step 4: Wire it into the CLI**

In `cmd/foundry/main.go`, replace the temporary `cmdShip` with the version below, and add `"time"` and `"github.com/tunedev/foundry/internal/adapters/outbound/store"` to the imports:

```go
func cmdShip(ctx context.Context, cfg config.Config, args []string) int {
	fs := flag.NewFlagSet("ship", flag.ContinueOnError)
	recapFile := fs.String("recap", "", "file holding the pull request's system recap")
	title := fs.String("title", "", "pull request title; defaults to the HEAD commit subject")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	var recap string
	if *recapFile != "" {
		b, err := os.ReadFile(*recapFile)
		if err != nil {
			return usageErr(err)
		}
		recap = string(b)
	}
	w, err := wireRepo(ctx, cfg)
	if err != nil {
		return usageErr(err)
	}
	start := time.Now()
	rep, err := app.Ship(ctx, w.shipDeps(), app.ShipInput{
		Dir: w.dir, Repo: w.id, Base: w.repo.Base, Checks: w.repo.Checks, Recap: recap, Title: *title,
		CITimeout: cfg.CITimeout.Duration, CIPoll: cfg.CIPoll.Duration, RetryPause: cfg.RetryPause.Duration,
	})
	for _, s := range rep.Passed {
		fmt.Println("ok", s)
	}
	run := store.Run{Time: start, Kind: "ship", Repo: rep.Repo, Branch: rep.Branch, Step: rep.Step,
		Outcome: outcome(err), PRURL: rep.PRURL, WallMS: time.Since(start).Milliseconds()}
	if rerr := w.store.AppendRun(run); rerr != nil {
		slog.ErrorContext(ctx, "run record not written", "error", rerr.Error())
	}
	if err != nil {
		return stopWith(err)
	}
	fmt.Println("ready for human:", rep.PRURL)
	return exitOK
}
```

In `cmd/foundry/wire.go`, add the imports `"github.com/tunedev/foundry/internal/adapters/outbound/vouch"` and `runner "github.com/tunedev/foundry/internal/run"`, and:

```go
func (w repoEnv) shipDeps() app.ShipDeps {
	v := vouch.Vouch{ReviewTimeout: w.cfg.ReviewTimeout.Duration, CallTimeout: w.cfg.CallTimeout.Duration}
	return app.ShipDeps{
		Forge: w.forge, Branch: w.git.Branch, Clean: w.git.Clean, Ahead: w.git.Ahead, HeadSHA: w.git.HeadSHA,
		RemoteSHA: w.git.RemoteSHA, Push: w.git.Push, Subject: w.git.Subject, Review: v.Review, Answer: w.store.Answer,
		RunCheck: func(ctx context.Context, dir, command string) error {
			_, err := runner.Cmd(ctx, w.cfg.CheckTimeout.Duration, dir, nil, "sh", "-c", command)
			return err
		},
	}
}
```

- [ ] **Step 5: Run the whole suite to verify it passes**

Run: `go vet ./... && go test ./...`
Expected: PASS, every package. `TestShipStopsWhenCIStaysPending` takes about 50ms; the full e2e suite stays under a few seconds.

- [ ] **Step 6: Commit**

```bash
git add internal/core/app cmd/foundry
git commit -m "Ship a branch through checks, vouch, PR recap, CI and friction to a green PR"
```

---

### Task 8: Skill, README, CI and dogfood config

**Files:**
- Create: `skills/foundry-ship/SKILL.md`
- Create: `README.md`
- Create: `foundry.toml`
- Create: `.github/workflows/test.yml`

**Interfaces:**
- Consumes: the CLI's output contract: lines `ok <step>`, a final `stop at <step>: ...`, `stop: ...` or `ready for human: <url>`.

- [ ] **Step 1: Write the skill**

`skills/foundry-ship/SKILL.md`:

````markdown
---
name: foundry-ship
description: Use when work on a feature branch is committed and ready for review in a repo with a foundry.toml - ships it to a green pull request and reports the friction met on the way
---

# Ship with foundry

Run `foundry ship` from the repo. Read its last line, do what it says, and rerun until it prints
`ready for human: <url>`. Do not push, open pull requests or merge any other way.

## When it stops

- `stop at preflight`, `stop at checks` or `stop at ci`: fix the cause, commit, rerun.
- `stop at review`: fix vouch's findings, commit, rerun. Never run `vouch ack` or `vouch rules accept`.
- `stop at push-pr` asking for a recap: write the pull request body to a file and rerun with
  `--recap <file>`. Use these `## ` headings:
  - `What and why`
  - `Touched`: packages or modules changed
  - `Risk`: starts with `low`, `medium` or `high`, then one reason
  - `Verified`: commands run, with their output pasted
  - `Links`: issues closed, or `none`
- `stop at friction`: report friction as below, then rerun.
- `stop: transient failure`: rerun once; if it stops the same way, report the error to the user.

## Reporting friction

Look back over this session for what slowed you down: slow or flaky commands, missing tooling,
confusing code, wrong docs, a verification you had to improvise. For each, at most 3, write a body
file with these `## ` headings and run
`foundry friction file --title "<symptom>" --body-file <file> --kind <kind>`:

- `What slowed me down`: symptom, exact command, exact error
- `Cost`: rough time lost, and the workaround used
- `Proposed fix`: one change; for `needs-decision`, two options and your recommendation
- `Cleanup`: what the fix would let us delete later, or `none`
- `Source`: this pull request and branch

Kind:
- `ready`: the fix is implementation-only: tooling, tests, docs, dev scripts, internal code.
- `needs-decision`: the fix changes what users see or how the product behaves.
- `cleanup`: something can be deleted once a stated condition holds.

Never put user data, emails, tokens or keys in a report. If nothing slowed you down, run
`foundry friction none --reason "<one line>"`.

Stop at `ready for human`. The user merges.
````

- [ ] **Step 2: Write the README**

`README.md`:

````markdown
# foundry

Ships a branch to a green pull request in fixed, rerunnable steps, and files the friction met on the
way as GitHub issues for agents to fix. It stops at a green pull request; a person merges.

## Install

```
make install install-skill
```

Requires `git`, `gh` (logged in) and `vouch`.

## Opt a repo in

Add `foundry.toml` at the repo root:

```toml
base = "main"                                # default main
checks = ["go vet ./...", "go test ./..."]   # required; run before review
verify = ["go test ./..."]                   # commands an agent may run to verify its work
max_friction_per_ship = 3                    # default 3
```

Activate vouch rules for the repo with `vouch init`, then `vouch rules accept`. The repo needs CI:
`ship` waits for every check on the pull request head to pass.

## Commands

```
foundry ship [--recap <file>] [--title <t>]
foundry friction file --title <t> --body-file <f> --kind ready|needs-decision|cleanup
foundry friction none --reason <r>
foundry config
```

`ship` steps: preflight, checks, review, push-pr, ci, friction, hand-off. Exit 0 is ready for a
person, 1 is a stop to act on (the last line says what), 2 is a usage or config error.

## Configuration

`~/.foundry/config.toml` (or `$FOUNDRY_HOME`), each key overridable by `FOUNDRY_<KEY>`:
`call_timeout`, `review_timeout`, `check_timeout`, `ci_timeout`, `ci_poll`, `retry_pause`,
`user_data_paths`, `log_level`. `foundry config` prints the resolved values.

State lives under the same home: `repos/` (friction answers per branch), `runs.jsonl` (one line
per ship), `logs/foundry.jsonl`.
````

- [ ] **Step 3: Add CI and the dogfood config**

`.github/workflows/test.yml`:

```yaml
name: test

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
        with:
          go-version: "1.27"
      - run: go vet ./...
      - run: go test ./...
```

`foundry.toml`:

```toml
checks = ["go vet ./...", "go test ./..."]
verify = ["go test ./..."]
```

- [ ] **Step 4: Verify locally**

Run: `go test ./... && make install install-skill && foundry config`
Expected: PASS; `installed ~/.local/bin/foundry`; `foundry config` prints the defaults with `ci_timeout = "45m0s"`.

- [ ] **Step 5: Commit**

```bash
git add skills README.md foundry.toml .github
git commit -m "Add the foundry-ship skill, README, CI and foundry's own foundry.toml"
```

---

### Task 9: Real end-to-end verification and the increment note

**Files:**
- Create: `docs/notes/2026-10-14-increment-1.md`
- In atlas (separate branch and PR): create `foundry.toml`

This task runs against real GitHub. The vouch steps for atlas are the user's: vouch refuses to let an agent accept rules.

- [ ] **Step 1: Ship this implementation branch with foundry itself**

From the implementation branch in `~/forge/foundry`, write the recap to a scratch file and run:

```bash
foundry ship --recap <scratch>/recap.md
```

Expected: `ok preflight` through `ok ci` against the real `test` workflow, then `stop at friction`. File real friction from this increment with `foundry friction file`, or run `foundry friction none --reason ...`, then rerun `foundry ship`.
Expected: `ready for human: https://github.com/tunedev/foundry/pull/<n>`. Paste the full output into the PR conversation as evidence. Run `foundry ship` once more and confirm it prints the same URL and makes no new commits, comments or pushes.

- [ ] **Step 2: Opt atlas in (user runs vouch)**

Ask the user to run, in `~/forge/atlas`: `vouch init`, then `vouch rules accept`. Wait for confirmation.

Then on a new atlas branch `foundry-opt-in`, add `foundry.toml`:

```toml
checks = ["go vet ./...", "go test ./..."]
verify = ["go test ./..."]
```

Commit it, then ship it with `foundry ship --recap <file>` and report friction. Expected: `ready for human:` with the atlas pull request URL. The user merges both pull requests.

- [ ] **Step 3: Write the increment note**

`docs/notes/2026-10-14-increment-1.md`, under four headings, from what actually happened in Steps 1-2:
`## The pattern`, `## What surprised me`, `## What I would do differently`, `## What I still do not understand`.

- [ ] **Step 4: Commit and ship the note**

```bash
git add docs/notes
git commit -m "Note what increment 1 of foundry taught"
foundry ship
```

Expected: `ready for human:` on the same implementation pull request.
