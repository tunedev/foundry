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
foundry work
foundry config
```

`ship` steps: preflight, checks, review, push-pr, ci, friction, hand-off. Exit 0 is ready for a
person, 1 is a stop to act on (the last line says what), 2 is a usage or config error.

## Unattended work

`foundry work` runs in a dedicated clone under `~/.foundry/checkouts/`. It releases expired claims, picks up
to `max_issues_per_run` `friction:ready` issues opened by the bot or the repo owner, and runs `claude -p` on
each in a fresh worktree as the bot, under `~/.foundry/agent`. Each issue ends `ready-for-human`,
`needs-decision` or `stuck`, and the run summary goes to Discord. One run at a time; a second exits at once.
The timer works every checkout in turn, every 2 hours.

    make install-timer
    systemctl --user enable --now foundry-work.timer

Needs `bot_name`, `bot_email` and the secret files `github_token_file`, `claude_token_file`,
`discord_webhook_file` (defaults under `~/.foundry/secrets/`), and `loginctl enable-linger`.

## Configuration

`~/.foundry/config.toml` (or `$FOUNDRY_HOME`), each key overridable by `FOUNDRY_<KEY>`:
`call_timeout`, `review_timeout`, `check_timeout`, `ci_timeout`, `ci_poll`, `retry_pause`,
`user_data_paths`, `log_level`, `work_window`, `claim_lease`, `issue_timeout`, `max_issues_per_run`,
`max_turns`, `max_review_rounds`, `agent_pass_env` (comma-separated in the environment), `bot_name`,
`bot_email`, `github_token_file`, `claude_token_file`, `discord_webhook_file`. `foundry config` prints the resolved values.

Telemetry goes to `OTEL_EXPORTER_OTLP_ENDPOINT` (default `http://localhost:4318`);
`OTEL_TRACES_EXPORTER=none` turns traces off.

State lives under the same home: `repos/` (friction answers per branch), `runs.jsonl` (one line
per ship), `logs/foundry.jsonl`.
