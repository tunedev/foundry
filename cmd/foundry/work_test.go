package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// newWorkWorld is a clone of origin on main, as the worker's dedicated checkout, with secrets, a
// config.toml for the agent's ship runs, and a local Discord webhook.
func newWorkWorld(t *testing.T) *world {
	t.Helper()
	w := seedWorld(t)
	secrets := filepath.Join(w.home, "secrets")
	os.MkdirAll(secrets, 0o700)
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		w.discord = append(w.discord, string(b))
		mu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	w.discordMu = &mu
	w.write(secrets, "github-token", "bot-token\n")
	w.write(secrets, "claude-oauth-token", "claude-token\n")
	w.write(secrets, "discord-webhook", srv.URL+"/api/webhooks/1/tok\n")
	w.write(w.home, "config.toml", `ci_poll = "10ms"
retry_pause = "1ms"
ci_timeout = "5s"
issue_timeout = "30s"
bot_name = "foundry-bot"
bot_email = "bot@example.invalid"
agent_pass_env = ["FAKE_STATE", "FAKE_LOGS"]
`)
	return w
}

func (w *world) work() (string, string, int) { return w.foundry("work") }

func (w *world) scenario(issue int, name string) {
	w.write(w.root, "claude-"+strconv.Itoa(issue)+".scenario", name)
}

func (w *world) discordBodies() []string {
	w.discordMu.Lock()
	defer w.discordMu.Unlock()
	return append([]string{}, w.discord...)
}

func (w *world) agentEnv(issue int) []string {
	b, _ := os.ReadFile(filepath.Join(w.root, "claude-"+strconv.Itoa(issue)+".env"))
	var names []string
	for _, kv := range strings.Split(string(b), "\n") {
		if name, _, _ := strings.Cut(kv, "="); name != "" && name != "PWD" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (w *world) labels(issue int) []string {
	for _, is := range w.gh().Issues {
		if is.Number == issue {
			return is.Labels
		}
	}
	return nil
}

func (w *world) comments(issue int) []string {
	var out []string
	for _, c := range w.gh().Comments {
		if c.Issue == issue {
			out = append(out, c.Body)
		}
	}
	return out
}

const readyIssue = `{"number":%d,"title":"Issue %d","body":"## What slowed me down\nslow","labels":["friction","friction:ready"],"created_at":"%s"}`

func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

// running reports whether pid is still a live process after up to wait. A zombie counts as dead: a killed
// process reparented to a PID 1 that does not reap stays a zombie.
func running(pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		if f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:])); len(f) > 0 && f[0] == "Z" {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWorkShipsReadyIssueToHandOff(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	out, errOut, code := w.work()
	if code != 0 {
		t.Fatalf("code %d\n%s%s", code, out, errOut)
	}
	s := w.gh()
	if len(s.PRs) != 1 || !strings.Contains(s.PRs[0].Body, "Closes #1") || !strings.HasPrefix(s.PRs[0].Head, "friction/1-") {
		t.Fatalf("prs %+v", s.PRs)
	}
	if l := w.labels(1); !slices.Contains(l, "friction:in-progress") || !slices.Contains(l, "friction:ready") {
		t.Fatalf("labels %v", l)
	}
	cs := w.comments(1)
	if len(cs) != 2 || !strings.HasPrefix(cs[0], "foundry: claimed by run ") || !strings.Contains(cs[1], "pull/1") {
		t.Fatalf("comments %q", cs)
	}
	want := []string{"CLAUDE_CODE_ENABLE_TELEMETRY", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA", "CLAUDE_CODE_OAUTH_TOKEN", "FAKE_LOGS",
		"FAKE_STATE", "FOUNDRY_HOME", "GH_TOKEN", "HOME", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_LOGS_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_TRACES_EXPORTER", "PATH", "VOUCH_HOME"}
	if got := w.agentEnv(1); !slices.Equal(got, want) {
		t.Fatalf("agent env %v", got)
	}
	if d := w.discordBodies(); len(d) != 1 || !strings.Contains(d[0], "pull/1") || !strings.Contains(d[0], `"allowed_mentions":{"parse":[]}`) {
		t.Fatalf("discord %q", d)
	}
	if b, _ := os.ReadFile(filepath.Join(w.home, "agent", ".claude", "settings.json")); !strings.Contains(string(b), `"Bash(gh:*)"`) {
		t.Fatalf("agent settings do not deny gh:\n%s", b)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.home, "worktrees")); len(entries) != 0 {
		t.Fatalf("worktrees left: %v", entries)
	}
}

func TestWorkRelabelsNeedsDecision(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	w.scenario(1, "needs-decision")
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	l := w.labels(1)
	if !slices.Contains(l, "friction:needs-decision") || slices.Contains(l, "friction:ready") || slices.Contains(l, "friction:in-progress") {
		t.Fatalf("labels %v", l)
	}
	if cs := w.comments(1); !strings.Contains(cs[len(cs)-1], "I recommend keeping it") || len(w.gh().PRs) != 0 {
		t.Fatalf("comments %q prs %d", cs, len(w.gh().PRs))
	}
}

func TestWorkKillsHungAgent(t *testing.T) {
	w := newWorkWorld(t)
	w.env = append(w.env, "FOUNDRY_ISSUE_TIMEOUT=2s")
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	w.scenario(1, "hang")
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if l := w.labels(1); !slices.Contains(l, "friction:stuck") || slices.Contains(l, "friction:in-progress") {
		t.Fatalf("labels %v", l)
	}
	if cs := w.comments(1); !strings.Contains(cs[len(cs)-1], "(timeout)") {
		t.Fatalf("comments %q", cs)
	}
	b, _ := os.ReadFile(filepath.Join(w.root, "claude-1.child"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 || running(pid, 3*time.Second) {
		t.Fatalf("child %d still running", pid)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.home, "worktrees")); len(entries) != 0 {
		t.Fatalf("worktrees left: %v", entries)
	}
}

func TestWorkStuckWhenReviewNeverConverges(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	w.scenario(1, "review-loop")
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	cs := w.comments(1)
	if last := cs[len(cs)-1]; !strings.Contains(last, "(review_not_converged)") || !strings.Contains(last, "unbounded retry") {
		t.Fatalf("comments %q", cs)
	}
}

func TestWorkRunsNextIssueAfterACrash(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(3*time.Hour)) + `,` + fmt.Sprintf(readyIssue, 2, 2, ago(time.Hour)) + `]}`)
	w.scenario(1, "crash")
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !slices.Contains(w.labels(1), "friction:stuck") || len(w.gh().PRs) != 1 {
		t.Fatalf("labels %v prs %d", w.labels(1), len(w.gh().PRs))
	}
	if d := w.discordBodies(); len(d) != 1 || !strings.Contains(d[0], "#1 Issue 1: crash") || !strings.Contains(d[0], "pull/1") {
		t.Fatalf("discord %q", d)
	}
}

func TestWorkReleasesExpiredClaimThenPicksIt(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[{"number":1,"title":"Issue 1","labels":["friction","friction:ready","friction:in-progress"]}],
		"comments":[{"issue":1,"author":"foundry-bot","body":"foundry: claimed by run r0","created_at":"` + ago(3*time.Hour) + `"}]}`)
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	cs := w.comments(1)
	if len(cs) < 3 || !strings.Contains(cs[1], "expired") || !strings.HasPrefix(cs[2], "foundry: claimed by run ") || len(w.gh().PRs) != 1 {
		t.Fatalf("comments %q prs %d", cs, len(w.gh().PRs))
	}
}

func TestWorkNeverPicksAnUntrustedAuthorsIssue(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[{"number":1,"title":"Issue 1","author":"stranger","labels":["friction","friction:ready"]}]}`)
	if _, _, code := w.work(); code != 0 || w.gh().Writes != 0 || len(w.discordBodies()) != 0 {
		t.Fatalf("code %d writes %d", code, w.gh().Writes)
	}
}

func TestWorkKeepsAYoungClaim(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[{"number":1,"title":"Issue 1","labels":["friction","friction:ready","friction:in-progress"]}],
		"comments":[{"issue":1,"author":"foundry-bot","body":"foundry: claimed by run r0","created_at":"` + ago(10*time.Minute) + `"}]}`)
	if _, _, code := w.work(); code != 0 || w.gh().Writes != 0 || len(w.discordBodies()) != 0 {
		t.Fatalf("code %d writes %d", code, w.gh().Writes)
	}
}

func TestWorkReleasesInProgressWithoutAClaim(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[{"number":1,"title":"Issue 1","labels":["friction","friction:ready","friction:in-progress"]}],
		"comments":[{"issue":1,"author":"someone","body":"foundry: claimed by run r9","created_at":"` + ago(10*time.Minute) + `"}]}`)
	w.scenario(1, "needs-decision")
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if cs := w.comments(1); len(cs) < 2 || !strings.Contains(cs[1], "no claim") {
		t.Fatalf("comments %q", cs)
	}
}

func TestWorkSendsARejectedFixToDecision(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[{"number":1,"title":"Issue 1","labels":["friction","friction:ready","friction:in-progress"]}],
		"comments":[{"issue":1,"author":"foundry-bot","body":"foundry: claimed by run r0","created_at":"` + ago(time.Hour) + `"}],
		"prs":[{"number":4,"url":"https://github.com/acme/widget/pull/4","head":"friction/1-r0","state":"CLOSED"}]}`)
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	l := w.labels(1)
	if !slices.Contains(l, "friction:needs-decision") || slices.Contains(l, "friction:ready") || slices.Contains(l, "friction:in-progress") {
		t.Fatalf("labels %v", l)
	}
	if cs := w.comments(1); !strings.Contains(cs[len(cs)-1], "pull/4") {
		t.Fatalf("comments %q", cs)
	}
}

func TestWorkRepicksWhenTheNewClaimExpiresAfterARejection(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[{"number":1,"title":"Issue 1","labels":["friction","friction:ready","friction:in-progress"]}],
		"comments":[{"issue":1,"author":"foundry-bot","body":"foundry: claimed by run r0","created_at":"` + ago(5*time.Hour) + `"},
		            {"issue":1,"author":"foundry-bot","body":"foundry: claimed by run r1","created_at":"` + ago(3*time.Hour) + `"}],
		"prs":[{"number":4,"url":"https://github.com/acme/widget/pull/4","head":"friction/1-r0","state":"CLOSED"}]}`)
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if l := w.labels(1); slices.Contains(l, "friction:needs-decision") || !slices.Contains(l, "friction:ready") {
		t.Fatalf("labels %v", l)
	}
	if prs := w.gh().PRs; len(prs) != 2 {
		t.Fatalf("prs %+v", prs)
	}
}

func TestWorkSkipsWhileAnotherRunHoldsTheLock(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	f, _ := os.OpenFile(filepath.Join(w.home, "work.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	out, _, code := w.work()
	if code != 0 || !strings.Contains(out, "skip") || w.gh().Writes != 0 {
		t.Fatalf("code %d writes %d\n%s", code, w.gh().Writes, out)
	}
}

func TestWorkTakesAStaleLockFile(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	w.write(w.home, "work.lock", "")
	if _, _, code := w.work(); code != 0 || len(w.gh().PRs) != 1 {
		t.Fatalf("code %d prs %d", code, len(w.gh().PRs))
	}
}

func TestWorkWithNothingReadyDoesNothing(t *testing.T) {
	w := newWorkWorld(t)
	if _, _, code := w.work(); code != 0 || w.gh().Writes != 0 || len(w.discordBodies()) != 0 {
		t.Fatalf("code %d writes %d discord %d", code, w.gh().Writes, len(w.discordBodies()))
	}
}

func TestWorkRerunOverFinishedIssuesWritesNothing(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("first run: code %d", code)
	}
	writes := w.gh().Writes
	if _, _, code := w.work(); code != 0 || w.gh().Writes != writes || len(w.discordBodies()) != 1 {
		t.Fatalf("rerun: code %d writes %d -> %d", code, writes, w.gh().Writes)
	}
}

func TestWorkDoesNotDuplicateACommentWhenTheResponseIsLost(t *testing.T) {
	w := newWorkWorld(t)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `],
		"lose_once":{"issue comment":"HTTP 502: Bad Gateway (https://api.github.com/graphql)"}}`)
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if cs := w.comments(1); len(cs) != 2 {
		t.Fatalf("comments %q, want one claim and one ready", cs)
	}
}

func TestWorkNeverLeaksTheWebhookURL(t *testing.T) {
	const marker = "s3cretWEBHOOKtoken"
	for name, url := range map[string]string{
		"refused": "http://127.0.0.1:1/api/webhooks/1/" + marker,
		"404":     "", // filled below with a server that answers 404
	} {
		w := newWorkWorld(t)
		if url == "" {
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusNotFound) }))
			t.Cleanup(srv.Close)
			url = srv.URL + "/api/webhooks/1/" + marker
		}
		w.write(filepath.Join(w.home, "secrets"), "discord-webhook", url+"\n")
		w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
		out, errOut, code := w.work()
		logs, _ := os.ReadFile(filepath.Join(w.home, "logs", "foundry.jsonl"))
		runs, _ := os.ReadFile(filepath.Join(w.home, "runs.jsonl"))
		if code != 0 || strings.Contains(out+errOut+string(logs)+string(runs), marker) {
			t.Fatalf("%s: code %d, or the webhook token leaked\nstdout %s\nstderr %s", name, code, out, errOut)
		}
		if !strings.Contains(string(logs), "discord webhook") {
			t.Fatalf("%s: the failed summary was not logged", name)
		}
	}
}

func TestWorkFailsAtStartupWithoutASecretFile(t *testing.T) {
	const claudeSecret, webhookSecret = "claudeSECRETvalue", "webhookSECRETvalue"
	w := newWorkWorld(t)
	secrets := filepath.Join(w.home, "secrets")
	w.write(secrets, "claude-oauth-token", claudeSecret+"\n")
	w.write(secrets, "discord-webhook", "http://127.0.0.1:1/api/webhooks/1/"+webhookSecret+"\n")
	os.Remove(filepath.Join(secrets, "github-token"))
	out, errOut, code := w.work()
	if code != 2 || !strings.Contains(errOut, "github-token") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
	if strings.Contains(out+errOut, claudeSecret) || strings.Contains(out+errOut, webhookSecret) {
		t.Fatalf("a secret value was printed\nstdout %s\nstderr %s", out, errOut)
	}
}

func TestWorkTraceCoversOneIssueThroughShip(t *testing.T) {
	sink := startSink(t)
	w := newWorkWorld(t)
	w.exportTo(sink)
	w.setGH(`{"issues":[` + fmt.Sprintf(readyIssue, 1, 1, ago(time.Hour)) + `]}`)
	if _, _, code := w.work(); code != 0 {
		t.Fatalf("code %d", code)
	}
	spans := sink.spans()
	one := func(name string) sinkSpan {
		got := named(spans, name)
		if len(got) != 1 {
			t.Fatalf("%s: %d spans; all %+v", name, len(got), spans)
		}
		return got[0]
	}
	root, issue, agent, ship := one("foundry.work"), one("foundry.issue"), one("foundry.agent"), one("foundry.ship")
	for child, parent := range map[string]sinkSpan{
		"foundry.claims.release": root, "foundry.pick": root, "foundry.issue": root,
		"foundry.worktree": issue, "foundry.agent": issue, "foundry.ship": agent, "foundry.ship.review": ship,
	} {
		if c := one(child); c.Parent != parent.ID || c.Trace != root.Trace {
			t.Errorf("%s: parent %s trace %s, want %s in %s", child, c.Parent, c.Trace, parent.ID, root.Trace)
		}
	}
	if !strings.Contains(agent.Attrs["session.id"], "fake-session") || !strings.Contains(issue.Attrs["outcome"], "ready-for-human") {
		t.Errorf("agent %v issue %v", agent.Attrs, issue.Attrs)
	}
	names := sink.metricNames()
	for _, m := range []string{"foundry_issue_outcomes_total", "foundry_agent_cost_usd_total", "foundry_step_duration_seconds",
		"foundry_review_rounds"} {
		if !slices.Contains(names, m) {
			t.Errorf("metric %s not exported; got %v", m, names)
		}
	}
}
