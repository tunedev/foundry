# Ship and friction loop — Design

> Governed by `../../../CLAUDE.md`. Agreed in conversation on 2026-10-13.

## Goal

Agents working in an opted-in repo ship through one deterministic command, report the
friction they hit as GitHub issues, and an unattended worker on the workstation turns
ready friction into pull requests overnight. The human makes product decisions and merges;
agents do everything else up to a green PR.

Pilot repo: `tunedev/atlas`. Other repos join through config.

Success is the end-to-end test in [Testing](#testing) passing against a sandbox repo, then
one real friction issue in atlas going from filed to a green PR with no human action
between those two points.

## Decisions

| Decision | Choice | Rejected |
|---|---|---|
| Where the factory lives | New Go CLI `foundry`, own repo, same shape as vouch | Scripts inside atlas (second repo means a copy that drifts); extending vouch (a reviewer that also dispatches agents is two tools) |
| Friction store | GitHub issues on the repo, labelled | Gitea issues (unreachable from the phone); markdown files in the repo (no status or assignment) |
| Merge authority | Agent stops at a green PR; the human merges | Auto-merge when green (no flags or restore drill yet, so blast radius is uncapped); auto-merge for low-risk paths |
| AI review | `vouch review` + `vouch gate` | Hosted review bots (cost, and vouch already reviews against repo rules) |
| Dispatcher | systemd user timer running `foundry work` | Gas Town; Kody jobs |
| Agent runtime | Headless `claude -p --output-format json` | — |
| Phone channel | Discord webhook behind a `Notifier` port | ntfy (the named second implementation) |
| Sandbox | Branch protection + scoped token + worktree + scrubbed env + permission allowlist | Container sandbox (deferred; see [Not built](#not-built)) |

## Increments

1. **`foundry ship` and `foundry friction`**, used from interactive sessions. Nothing runs
   unattended.
2. **`foundry work` and the timer.** Adds the one new unknown: headless `claude`
   running unattended.

## `foundry ship`

Run from a feature branch. Each step reads current state, so a rerun resumes at the first
unmet step and a rerun on a finished branch is a no-op that reports the same state. Output
is only the current step's result and instruction.

| # | Step | Passes when | On failure |
|---|---|---|---|
| 1 | Preflight | Not on the base branch; clean tree; commits ahead of `origin/<base>` | Fail fast with the fix |
| 2 | Local checks | Every command in `checks` exits 0 | Fail fast with the output tail |
| 3 | Review | `vouch review` then `vouch gate` exits 0 | Print findings; agent fixes and reruns |
| 4 | Push and PR | Branch pushed; PR open with a valid recap body | Retry once if transient, else fail |
| 5 | CI | Every required check is green on the PR head SHA | Print the failing job's log tail |
| 6 | Friction | `foundry friction file` or `foundry friction none` ran for this branch | Block with the instruction |
| 7 | Hand-off | — | Print the PR URL; record `ready-for-human` |

There is no merge step.

### System recap

The PR body carries these headings, and step 4 rejects a body missing any of them:

- **What and why**
- **Touched**: packages or modules changed
- **Risk**: `low`, `medium` or `high`, with one reason
- **Verified**: commands run, with pasted output
- **Links**: issues closed or friction filed

### Error classification

Every remote call (`git`, `gh`, `vouch`, GitHub API) carries a timeout from config.
Errors are sorted into:

- **fix-and-rerun**: failing checks, review findings, CI failures. Never retried.
- **transient**: network errors, timeouts, GitHub 5xx and rate limits. Retried once with
  jitter.

## Friction issues

### Commands

```
foundry friction file --title <t> --body-file <f> --kind ready|needs-decision|cleanup
foundry friction none --reason <r>
```

### Labels

Every friction issue carries `friction` plus exactly one kind label.

| Label | Meaning | Acted on by |
|---|---|---|
| `friction:ready` | Implementation-only fix: tooling, tests, docs, dev scripts, internal code | The worker |
| `friction:needs-decision` | The fix changes what users see or how the product behaves | The human, who relabels to `ready` once decided |
| `friction:cleanup` | Something to delete once a stated condition holds | Nobody yet |
| `friction:in-progress` | Claimed by a worker run | The worker |
| `friction:stuck` | A worker run failed or hit its budget | The human |

### Body

The command rejects a body missing any of these headings:

- **What slowed me down**: symptom, exact command, exact error.
- **Cost**: rough time lost, and the workaround used.
- **Proposed fix**: one change; for `needs-decision`, two options and a recommendation.
- **Cleanup**: what the fix would let us delete later, or `none`.
- **Source**: PR and branch.

### Rules

- **Dedupe.** An open `friction` issue with a similar title gets a comment
  `hit again from <PR>` instead of a new issue. The comment count is the hit count.
- **Public-safe.** A body is rejected if it contains an email address, a token or key
  pattern, or a path under a configured user-data directory.
- **Bounded.** At most `max_friction_per_ship` issues per branch (default 3).

## `foundry work`

### Picking work

1. Release claims: an issue labelled `friction:in-progress` for longer than `claim_lease`
   (default 2h) with no open PR loses the label and gets a comment.
2. Query open `friction:ready` issues with no linked open PR and not labelled `stuck`.
3. Order by hit count descending, then oldest first.
4. Claim the first: add `friction:in-progress` and a comment naming the run.

Stop when `max_issues_per_run` (default 3) is reached or the window closes.

### Running the agent

For each claimed issue:

1. Create a worktree at `~/.foundry/work/<owner>/<repo>/<issue>` from `origin/<base>` on a
   branch `friction/<issue>-<slug>`.
2. Run `claude -p --output-format json` in that worktree, with a scrubbed environment and
   a prompt that names the issue, the repo's `verify` tools and the instruction to finish
   with `foundry ship`.
3. Remove the worktree when the run ends, whatever the outcome.

### Isolation

| Guard | Guarantees |
|---|---|
| Branch protection on the base branch requiring an approving review | The agent cannot merge |
| Fine-grained GitHub token scoped to opted-in repos: contents, pull requests and issues write; no admin | No access to other repos, settings or secrets |
| Fresh worktree, deleted afterwards | The human's checkouts are never touched |
| Environment reduced to `PATH`, `HOME`, the scoped token and the model credential | No cloud, prod or personal credentials reach the agent |
| Claude Code permission allowlist: edits inside the worktree; Bash limited to `go`, `git`, `foundry`, `vouch` and the repo's `verify` commands | Prevents accidents; a guardrail, not a boundary |

### Budgets

`issue_timeout` (default 45m), `max_turns`, and `max_issues_per_run`. The timeout is
enforced by cancelling the context that owns the `claude` process.

### Outcomes

| Outcome | Effect on the issue |
|---|---|
| `ship` reaches hand-off | PR linked; `in-progress` stays until the PR closes |
| Failure or budget hit | Comment with the classified reason and last step; `in-progress` removed; `stuck` added |
| Agent finds a product question | Relabelled `needs-decision` with its recommendation; `in-progress` removed |

An issue is attempted at most once per run.

## Run record and summary

Every `ship` and every issue attempt appends one JSON line to `~/.foundry/runs.jsonl`:
repo, issue, branch, last step, outcome, PR URL, wall time, turns, cost in USD, model.
Cost, turns and model come from the `claude` JSON output; an interactive `ship` records
them as absent.

When a `work` run ends, foundry sends one summary through the `Notifier` port: issues
picked, PRs ready (with links), stuck issues with reasons, new `needs-decision` issues
and total cost. v1 implements the port with a Discord webhook.

GitHub stays the interface for PRs and issues; foundry has no dashboard.

## Configuration

Precedence: defaults, then `~/.foundry/config.toml`, then `FOUNDRY_*` environment, then
flags. Validated at startup; invalid config exits non-zero before any remote call.

- **Global** (`~/.foundry/config.toml`): opted-in repos and their local paths, the work
  window, budgets, timeouts, claim lease, user-data paths for the public-safe scan.
- **Per repo** (`foundry.toml` at the repo root): `base`, `checks` (commands), `verify`
  (commands the agent may run to verify its work), `max_friction_per_ship`.
- **Secrets** (the GitHub token and the Discord webhook URL) are read from files named
  in config, never logged and never written to the run record.

For atlas, `checks` and `verify` start as `go vet ./...` and `go test ./...`. The atlas
verify CLI and preview environment join `verify` in the next spec without changing
foundry.

## Prerequisites (human actions)

- Opt atlas into vouch: `vouch init`, then `vouch rules accept`.
- Enable branch protection on atlas `main` requiring one approving review.
- Create the fine-grained token and the Discord webhook, and save each to a file.
- Create the sandbox repo `tunedev/foundry-sandbox` for the end-to-end test.

## Testing

- **End to end** against `tunedev/foundry-sandbox`: file a friction issue; run
  `foundry work` with a stub agent command that commits a trivial fix; assert a green PR
  exists, the labels moved as specified, and the run record has one line with outcome
  `ready-for-human`.
- **Rerun**: run `ship` twice on the finished branch; the second run makes no remote
  writes and reports the same state.
- **Unit**, only for pure logic: error classification, dedupe matching, the public-safe
  scan, queue ordering, recap and friction body validation.

The agent command is set in config, which is how the end-to-end test substitutes a stub
for `claude`.

## Not built

| Left out | Revisit when |
|---|---|
| Auto-merge | Flags, rollout conditions and a tested restore exist, and the loop's PRs have a track record |
| Container sandbox | The allowlist is shown to be insufficient |
| Dashboard | GitHub plus the Discord summary stop answering "what happened overnight" |
| Gas Town or Kody as dispatcher | The timer demonstrably limits the loop |
| Cron agents for docs, tests and dead code; error-tracker webhooks | The friction loop is proven |
| `friction:cleanup` sweep | Cleanup issues accumulate |

## Next specs

1. Atlas verify CLI and a per-PR preview on the workstation.
2. Feature flags with success metrics and rollout conditions.
