---
name: foundry-ship
description: Use when work on a feature branch is committed and ready for review in a repo with a foundry.toml - ships it to a green pull request and reports the friction met on the way
---

# Ship with foundry

Run `foundry ship` from the repo. Read its last line, do what it says, and rerun until it prints
`ready for human: <url>`. Do not push, open pull requests or merge any other way.

## When it stops

- `stop at preflight`, `stop at checks` or `stop at ci`: fix the cause, commit, rerun.
- `stop at review: vouch did not pass`: fix vouch's findings, commit, rerun. Never run `vouch ack` or
  `vouch rules accept`.
- `stop at review: vouch failed to run`: report the error to the user and stop.
- `stop at review: review did not converge`: report the last findings to the user and stop.
- `stop at push-pr` asking for a recap: write the pull request body to a file and rerun with
  `--recap <file>`. Use these `## ` headings:
  - `What and why`
  - `Touched`: packages or modules changed
  - `Risk`: starts with `low`, `medium` or `high`, then one reason
  - `Verified`: commands run, with their output pasted
  - `Links`: issues closed, or `none`
- `stop at push-pr` saying origin's branch has diverged: ask the user whether to overwrite it. Never
  force-push.
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
