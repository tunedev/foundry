// Package claude runs Claude Code headless as foundry's agent, in a fresh worktree per job.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/run"
	"github.com/tunedev/foundry/internal/telemetry"
)

var tracer = otel.Tracer(telemetry.Scope)

// Runner runs Bin (claude) for one job in a worktree under WorktreeRoot, with exactly Env plus the trace
// context. The worktree and its local branch are removed afterwards, whatever the outcome.
type Runner struct {
	Bin          string
	WorktreeRoot string
	GitTimeout   time.Duration
	Env          []string
}

func (r Runner) Run(ctx context.Context, job ports.AgentJob) (ports.AgentResult, error) {
	dir := filepath.Join(r.WorktreeRoot, strings.ReplaceAll(job.Branch, "/", "-"))
	if err := r.addWorktree(ctx, job, dir); err != nil {
		return ports.AgentResult{}, err
	}
	defer r.removeWorktree(context.WithoutCancel(ctx), job, dir)
	ctx, span := tracer.Start(ctx, "foundry.agent")
	defer span.End()
	res, err := r.claude(ctx, dir, job)
	span.SetAttributes(attribute.String("session.id", res.SessionID), attribute.Int("turns", res.Turns),
		attribute.Float64("cost_usd", res.CostUSD), attribute.String("model", res.Model), attribute.String("subtype", res.Subtype))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	return res, err
}

func (r Runner) addWorktree(ctx context.Context, job ports.AgentJob, dir string) error {
	ctx, span := tracer.Start(ctx, "foundry.worktree")
	defer span.End()
	if _, err := run.Cmd(ctx, r.GitTimeout, job.RepoDir, nil, "git", "fetch", "-q", "origin", job.Base); err != nil {
		return run.Classify(err)
	}
	_, err := run.Cmd(ctx, r.GitTimeout, job.RepoDir, nil, "git", "worktree", "add", "-q", "-b", job.Branch, dir, "origin/"+job.Base)
	return err
}

func (r Runner) removeWorktree(ctx context.Context, job ports.AgentJob, dir string) {
	for _, args := range [][]string{{"worktree", "remove", "--force", dir}, {"branch", "-D", job.Branch}} {
		if _, err := run.Cmd(ctx, r.GitTimeout, job.RepoDir, nil, "git", args...); err != nil {
			slog.WarnContext(ctx, "worktree cleanup", "error", err.Error())
		}
	}
}

// claude runs the agent as a process group, so ending ctx kills every process it started. A result on
// stdout counts even when claude exits non-zero, as it does at the turn limit.
func (r Runner) claude(ctx context.Context, dir string, job ports.AgentJob) (ports.AgentResult, error) {
	env := append(slices.Clone(r.Env), telemetry.Env(ctx)...)
	out, err := run.Group(ctx, dir, env, strings.NewReader(job.Prompt), r.Bin, "-p", "--output-format", "json",
		"--max-turns", strconv.Itoa(job.MaxTurns), "--permission-mode", "dontAsk", "--setting-sources", "user", "--strict-mcp-config")
	res, perr := parse(out)
	if perr == nil {
		return res, nil
	}
	if err == nil {
		err = perr
	}
	return res, fmt.Errorf("claude: %w", err)
}

type resultJSON struct {
	Type         string                     `json:"type"`
	Subtype      string                     `json:"subtype"`
	Result       string                     `json:"result"`
	SessionID    string                     `json:"session_id"`
	NumTurns     int                        `json:"num_turns"`
	TotalCostUSD float64                    `json:"total_cost_usd"`
	ModelUsage   map[string]json.RawMessage `json:"modelUsage"`
}

func parse(out []byte) (ports.AgentResult, error) {
	var j resultJSON
	if err := json.Unmarshal(out, &j); err != nil || j.Type != "result" {
		return ports.AgentResult{}, fmt.Errorf("no result in claude output %q", out[:min(len(out), 200)])
	}
	return ports.AgentResult{Text: j.Result, Subtype: j.Subtype, SessionID: j.SessionID, Turns: j.NumTurns,
		CostUSD: j.TotalCostUSD, Model: strings.Join(slices.Sorted(maps.Keys(j.ModelUsage)), ",")}, nil
}
