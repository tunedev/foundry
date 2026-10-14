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
