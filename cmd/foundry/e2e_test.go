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

func TestShipDoesNotDuplicatePRWhenCreateResponseIsLost(t *testing.T) {
	w := newWorld(t, "feat")
	w.foundry("friction", "none", "--reason", "smooth run")
	w.setGH(`{"lose_once":{"pr create":"HTTP 502: Bad Gateway (https://api.github.com/graphql)"}}`)
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 || len(w.gh().PRs) != 1 {
		t.Fatalf("code %d, prs %d\n%s", code, len(w.gh().PRs), out)
	}
}

func TestFrictionDoesNotDuplicateIssueWhenCreateResponseIsLost(t *testing.T) {
	w := newWorld(t, "feat")
	w.setGH(`{"lose_once":{"issue create":"HTTP 502: Bad Gateway (https://api.github.com/graphql)"}}`)
	body := w.write(w.root, "f.md", goodFriction)
	out, _, code := w.foundry("friction", "file", "--title", "Slow tests", "--body-file", body, "--kind", "ready")
	if s := w.gh(); code != 0 || !strings.Contains(out, "filed #1") || len(s.Issues) != 1 || len(s.Comments) != 0 {
		t.Fatalf("code %d\n%s\nstate %+v", code, out, s)
	}
}

func TestShipStopsWithoutForcePushingADivergedBranch(t *testing.T) {
	w := newWorld(t, "feat")
	w.foundry("friction", "none", "--reason", "smooth run")
	if _, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 {
		t.Fatalf("first ship: code %d", code)
	}
	remote := w.git(w.origin, "rev-parse", "refs/heads/feat")
	w.git(w.checkout, "commit", "-q", "--amend", "-m", "rewritten")
	out, _, code := w.foundry("ship")
	if code != 1 || !strings.Contains(out, "stop at push-pr") || !strings.Contains(out, "never force-pushes") ||
		w.git(w.origin, "rev-parse", "refs/heads/feat") != remote {
		t.Fatalf("code %d\n%s", code, out)
	}
}
