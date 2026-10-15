# foundry work — Design

> Governed by `../../../CLAUDE.md`. Refines the `foundry work` sections of
> [the ship and friction loop design](2026-10-13-ship-and-friction-loop-design.md); where they
> differ, this document wins. Agreed in conversation on 2026-10-15.

## Goal

An unattended worker on the workstation turns `friction:ready` issues into green pull requests
opened by a bot account, reports each run to Discord, and makes every run visible as one trace.
The human approves and merges; the bot cannot.

Success is the three checks in [Success](#success), in order.

## Decisions

| Decision | Choice | Rejected |
|---|---|---|
| Where agents run | Workstation, behind an `AgentRunner` port | Kubernetes now (two unknowns in one increment; it is increment 3) |
| Agent identity | Bot account `tunedev-foundry`, Write collaborator, not admin | Fine-grained token on `tunedev` (self-authored PRs cannot be approved; admin bypass unverified); GitHub App (most setup) |
| Merge guard | Branch protection on `main` requiring one approving review | Token permissions alone |
| Review findings | Per-repo vouch policy: `gate_severity = minor`, `ack_below = false`, `post_reviews = true` | A `needs-ack` outcome that waits for the human; fixing every finding at any severity |
| Telemetry | foundry exports OTLP traces and metrics to an OTel Collector; traces to Tempo, metrics to the existing Prometheus | Prometheus textfile metrics only (no per-run trace); full LGTM now (Loki not needed yet) |
| Phone channel | Discord webhook behind a `Notifier` port | ntfy (the named second implementation) |

## Delivery

Three pieces, each its own pull request or checked setup:

1. **`infra`: workstation telemetry.** Add an OTel Collector (OTLP on `localhost:4318`) and Tempo
   to the workstation stack. The collector sends metrics to Prometheus by remote write and traces
   to Tempo; Grafana gains Tempo as a data source and the foundry dashboard. Verified by a test
   span and a test metric reaching Grafana before foundry depends on it.
2. **`foundry`: `foundry work`, the timer, the summary and telemetry**, as specified below.
3. **Human setup**, guided and checked one step at a time:
   - Create `tunedev-foundry`; add it to atlas, foundry and `foundry-sandbox` with Write.
   - Protect `main` on each opted-in repo: one approving review required.
   - Save the bot's token, a `claude setup-token` OAuth token and the Discord webhook URL to
     files named in config.
   - Create `tunedev/foundry-sandbox` with a `foundry.toml` and vouch rules.
   - Set the vouch policy above on each opted-in repo.

## Architecture

The core gains one use case, `Work`, and three ports. `Work` calls no adapter directly.

| Port | v1 adapter | Second implementation |
|---|---|---|
| `AgentRunner`: run one claimed issue to an outcome | `claude -p` in a fresh worktree on the workstation | Kubernetes Job on `forge-qa` (increment 3) |
| `Notifier`: send the run summary | Discord webhook | ntfy |
| Telemetry | OTLP exporter | In-memory exporter in tests |

### Agent environment

The agent never sees the human's home. Each run gets:

- `HOME=~/.foundry/agent`, holding a git config with the bot's name and email and
  `core.hooksPath` set to vouch's githooks, so vouch's pre-push gate and review posting fire.
- `GH_TOKEN` (the bot's), `CLAUDE_CODE_OAUTH_TOKEN`, `VOUCH_HOME` (the shared `~/.vouch`, so the
  index and rules are not duplicated), `PATH`, and the `OTEL_*` variables below. Nothing else.
- A Claude Code settings file with the permission allowlist: edits inside the worktree; Bash
  limited to `go`, `git`, `gh`, `foundry`, `vouch` and the repo's `verify` commands. The human's
  hooks, MCP servers, plugins and `CLAUDE.md` do not load.

## A `foundry work` run

A systemd user timer starts `foundry work` every 2 hours inside the configured window. A lock
file allows one run at a time; a second start exits 0 and logs the skip.

1. **Release stale claims.** An issue labelled `friction:in-progress` for longer than
   `claim_lease` (default 2h) with no open PR loses the label and gets a comment.
2. **Pick.** Open `friction:ready` issues with no linked open PR and no `friction:stuck` label,
   ordered by hit count descending, then oldest first. Stop at `max_issues_per_run` (default 3)
   or when the window closes. Each issue is attempted at most once per run.
3. **Claim.** Add `friction:in-progress` and a comment naming the run ID.
4. **Run**, one issue at a time, through `AgentRunner`:
   - Create a worktree from `origin/<base>` on `friction/<n>-<slug>`.
   - Run `claude -p --output-format json --max-turns <max_turns>` with the agent environment.
     The prompt holds the issue body, the repo's `verify` commands, the instruction to finish by
     running `foundry ship` until it prints `ready for human`, and the instruction to reply
     starting `NEEDS-DECISION:` with a recommendation when the fix is a product decision.
   - `issue_timeout` (default 45m) cancels the context that owns the process.
   - Read the outcome from evidence: the branch's latest `runs.jsonl` line written by ship, and
     cost, turns, model and session ID from Claude's JSON output.
   - Remove the worktree, whatever the outcome.
5. **Summarise** through `Notifier`: issues picked, ready PR links, stuck issues with reasons,
   new `needs-decision` issues, total cost and wall time. A run that picked nothing sends nothing.

### Outcomes

| Outcome | Detected by | Effect on the issue |
|---|---|---|
| `ready-for-human` | ship's record shows hand-off | PR linked; `in-progress` stays until the PR closes |
| `needs-decision` | Claude's final result starts `NEEDS-DECISION:` | `in-progress` removed, `friction:needs-decision` added, recommendation commented |
| `stuck` | anything else: timeout, turn limit, `max_review_rounds` (default 3) exhausted, a ship stop, a crash | `in-progress` removed, `friction:stuck` added, reason and last step commented |

With the vouch policy above, `stop at review: vouch did not pass` always means findings at minor
or above, which the agent fixes. ship counts review rounds per branch and stops with
`review did not converge` after `max_review_rounds`.

### Failure isolation

One issue's failure never stops the next. A forge outage during pick or claim ends the run and
the summary says so. Remote writes are retried once on transient errors, as in increment 1.

## Telemetry

One trace per `work` run:

```
foundry.work                     run id, repo, issues picked
├─ foundry.claims.release
├─ foundry.pick
└─ foundry.issue                 issue, branch, outcome, reason
   ├─ foundry.worktree
   └─ foundry.agent              session.id, turns, cost_usd, model, exit
      └─ claude_code.interaction        (Claude Code beta spans)
         ├─ claude_code.llm_request
         └─ claude_code.tool  (Bash: foundry ship)
            └─ foundry.ship               (parent from TRACEPARENT)
               ├─ foundry.ship.checks
               ├─ foundry.ship.review     round, findings by severity
               ├─ foundry.ship.push_pr
               └─ foundry.ship.ci
```

Claude Code passes `TRACEPARENT` to its Bash subprocesses, so `foundry ship` run by the agent
parents its spans under Claude's tool span. Whether `claude -p` accepts an inbound
`TRACEPARENT` from foundry is settled by a spike before the plan: if it does, Claude's spans
nest under `foundry.agent`; if not, `foundry.agent` carries `session.id` to link the two.
An interactive `foundry ship` without `TRACEPARENT` starts its own trace.

| Metric | Answers |
|---|---|
| `foundry_issue_outcomes_total{repo,outcome,reason}` | Is the loop working, and why do runs stick? |
| `foundry_step_duration_seconds{step}` | Where does the time go? |
| `foundry_review_rounds{repo}` | Does review converge or churn? |
| `foundry_agent_cost_usd_total{repo,model}` | What does the factory cost? |
| `foundry_friction_filed_total{repo,kind}` | Is friction being found, and of which kind? |

The agent environment sets `CLAUDE_CODE_ENABLE_TELEMETRY=1`,
`CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`, `OTEL_TRACES_EXPORTER=otlp` and
`OTEL_METRICS_EXPORTER=otlp`; Claude's log events stay off. foundry reads the standard
`OTEL_EXPORTER_OTLP_*` variables, default `http://localhost:4318`. Export failure never fails a
run.

## Configuration

Additions to `~/.foundry/config.toml`, with the increment 1 precedence and startup validation:
the work window, `claim_lease`, `max_issues_per_run`, `issue_timeout`, `max_turns`,
`max_review_rounds`, and the paths of the three secret files. Secrets are never logged and never
written to the run record or telemetry.

## Testing

End to end, as in increment 1: real git against a local origin, fake `gh` and `vouch`, and a fake
`claude` that reads its prompt, acts out a scenario and prints Claude-shaped JSON.

| Test | Proves |
|---|---|
| Agent fixes and ships to hand-off | `ready-for-human`, PR linked, `in-progress` kept, one record line, summary lists the PR |
| Agent replies `NEEDS-DECISION:` | relabelled, recommendation commented, no PR |
| Agent hangs past `issue_timeout` | process killed, `stuck` with reason `timeout`, worktree removed |
| Review never converges | `stuck` after `max_review_rounds` with the last findings |
| Issue 1 crashes | issue 2 still runs; summary shows both |
| Expired claim with no PR | released with a comment, then picked in the same run |
| Second `work` while one runs | exits at once, no writes |
| Nothing ready | no claims, no summary |
| Rerun over finished issues | no remote writes |
| Telemetry | in-memory exporter receives the span tree for one issue; ship spans parent under a given `TRACEPARENT` |

Unit tests only for pure rules: ordering, lease expiry, outcome classification. The Discord
adapter is tested once against a local HTTP server: payload shape, and the webhook URL absent from
logs.

## Success

1. **Telemetry:** a test span sent to `localhost:4318` appears in Grafana via Tempo, and a test
   metric via Prometheus.
2. **Sandbox:** a `friction:ready` issue on `tunedev/foundry-sandbox` becomes a green PR opened by
   `tunedev-foundry`; the bot's merge attempt is refused; the Discord summary arrives; the run's
   trace shows every step.
3. **Atlas:** one real atlas friction issue goes from filed to a green bot PR overnight with no
   human action between.

## Not built

| Item | Revisit when |
|---|---|
| Kubernetes Job runner on `forge-qa`, Cilium egress policy, shared `~/.vouch` mount | Increment 3, once success 3 holds |
| Loki and Claude's log events | Traces stop answering "what did the agent do" |
| `friction:cleanup` sweep | Cleanup issues accumulate |
| Auto-merge | Flags and a restore drill exist |
| Dashboards beyond the one Grafana board | That board stops answering "what happened overnight" |
| More than one issue at a time | A run regularly hits the window before the queue empties |
