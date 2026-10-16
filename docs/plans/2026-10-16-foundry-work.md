# foundry work Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Increment 2 of foundry: an unattended `foundry work` run on the workstation turns `friction:ready` issues into green pull requests opened by a bot account, reports each run to Discord, and shows every run as one trace in Grafana.

**Architecture:** The core gains one use case, `app.Work`, pure rules in `internal/core/work`, and two ports in `internal/core/ports`: `AgentRunner` (v1: `claude -p` in a fresh worktree) and `Notifier` (v1: Discord webhook). Telemetry uses the OpenTelemetry API in the core and the SDK with OTLP/HTTP exporters only in `internal/telemetry`, wired by `cmd/foundry`. The workstation's infra stack gains an OTel Collector and Tempo first, as its own pull request.

**Tech Stack:** Go 1.27; `go.opentelemetry.io/otel` (API, SDK, `otlptracehttp`, `otlpmetrichttp`, latest); `github.com/BurntSushi/toml`; external CLIs `git`, `gh`, `vouch`, `claude`. Infra: OpenTofu with the `kreuzwerker/docker` provider, `otel/opentelemetry-collector-contrib` and `grafana/tempo` images.

**Spec:** `docs/specs/2026-10-15-foundry-work-design.md`

**Spike, settled before this plan:** `claude -p` accepts an inbound `TRACEPARENT`. Run with `TRACEPARENT=00-4bf92f35...-00f067aa0ba902b7-01`, it emitted `claude_code.interaction` with trace ID `4bf92f35...` and parent `00f067aa0ba902b7` (`isRemote: true`), and `claude_code.llm_request` as a child of `interaction`. So Claude's spans nest under `foundry.agent` with no linking needed. Also observed: `--max-turns` still works; hitting it exits 1 and prints one JSON object on stdout with `"subtype": "error_max_turns"`, `num_turns`, `total_cost_usd`, `session_id` and `modelUsage` keyed by model.

## Decisions this plan adds to the spec

- **Metrics reach Prometheus by scrape, not remote write.** The collector's `deltatocumulative` processor and `prometheus` exporter on `127.0.0.1:8889` join the existing scrape-job pattern and keep a counter whole across short-lived foundry processes. Remote write would forward each process's counter as a separate series that restarts from zero. Assertion a13 proves the sum.
- **`foundry work` runs in a dedicated clone**, `~/.foundry/checkouts/<repo>`, one systemd template instance per repo. Your working copies are never touched.
- **`agent_pass_env`** names extra variables the agent inherits. It is empty in production. The end-to-end fakes need `FAKE_STATE` and `FAKE_LOGS`.
- **The agent's deny list is advisory.** `Bash(gh:*)` and `Bash(git push:*)` stop the obvious calls, but `git -C . push --force` or a `go run` against the API would get past them. The controls that hold are on GitHub's side. The bot is a collaborator only on opted-in repos. `main` requires an approving review. A ruleset blocks force-pushes and deletions on every branch, so the bot can't rewrite one. A fine-grained token can't be scoped to repos where the bot is only a collaborator under a personal account, so the token is classic `repo`, and the collaborator list is the scope.
- **The second arch test checks foundry's own packages only**, as its comment already says, because the OpenTelemetry API imports `os`. The first arch test now also bars the SDK and exporters from the core.

## Global Constraints

- Go 1.27, module `github.com/tunedev/foundry`, one binary `cmd/foundry`. Comments describe current behaviour only. No emojis anywhere.
- Delivery order and boundaries: Part A is one pull request on the infra Gitea remote (`origin`, `localhost:3300/forge/infra`). Part B is one pull request on `tunedev/foundry`, shipped with `foundry ship`. Part C is your setup, guided and checked step by step. Agents never merge.
- Config precedence stays defaults, then `<home>/config.toml`, then `FOUNDRY_<KEY>`. Unknown keys, non-positive durations and integers below 1 fail at startup with exit 2.
- New config keys and defaults: `work_window` (`""`, meaning always), `claim_lease` (`2h`), `issue_timeout` (`45m`), `max_issues_per_run` (`3`), `max_turns` (`150`), `max_review_rounds` (`3`), `github_token_file` (`<home>/secrets/github-token`), `claude_token_file` (`<home>/secrets/claude-oauth-token`), `discord_webhook_file` (`<home>/secrets/discord-webhook`), `bot_name`, `bot_email` (required by `work`), `agent_pass_env` (`[]`).
- Secrets are read from the files above, trimmed, and never logged, printed, written to the run record or set on a span. Error messages name the file, never its content.
- Labels: `friction`, `friction:ready`, `friction:needs-decision`, `friction:cleanup`, `friction:in-progress`, `friction:stuck`.
- Claim comment: first line `foundry: claimed by run <run id>`, posted by the bot. Run ID: UTC `20060102-150405`. Branch: `friction/<n>-<run id>`.
- Outcomes: `ready-for-human`, `needs-decision`, `stuck`. Stuck reasons: `timeout`, `max_turns`, `review_not_converged`, `agent_stuck`, `ship_stop`, `crash`, `no_ship`.
- Span names: `foundry.<command>` (root, child of `TRACEPARENT` when set), `foundry.claims.release`, `foundry.pick`, `foundry.issue`, `foundry.worktree`, `foundry.agent`, `foundry.notify`, `foundry.ship.<step>` (dashes become underscores), `foundry.friction.file`.
- Metric names, exactly: `foundry_issue_outcomes_total{repo,outcome,reason}`, `foundry_step_duration_seconds{step}`, `foundry_review_rounds{repo}`, `foundry_agent_cost_usd_total{repo,model}`, `foundry_friction_filed_total{repo,kind}`. foundry exports with delta temporality; the collector turns deltas into cumulative counters and publishes them without adding suffixes.
- `OTEL_TRACES_EXPORTER=none` and `OTEL_METRICS_EXPORTER=none` turn the signal off. Export failure never fails a command; shutdown waits at most 5s.
- The core imports the OpenTelemetry API only. It never imports `go.opentelemetry.io/otel/sdk...`, an exporter, `internal/telemetry`, an adapter, `internal/run` or `os/exec`.
- The agent environment is exactly: `HOME`, `PATH`, `GH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `VOUCH_HOME`, `FOUNDRY_HOME`, `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`, `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER=none`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `TRACEPARENT` (when a span is recording), plus the names listed in `agent_pass_env`.
- Infra: every new port binds `127.0.0.1` only; every image is pinned by tag and digest; `verify.sh` asserts every new component.

## Review Focus

1. **A claim comment by someone other than the bot** on a public repo must not steer release. `CurrentClaim` reads only the bot's comments. Pinned in Task B3 (`TestCurrentClaimIgnoresOtherAuthors`).
2. **The agent's grandchildren at timeout**: `foundry ship` waiting on CI under a killed `claude` must die too, or it keeps pushing after the issue is marked stuck. The runner kills the whole process group. Pinned in Task B6 (`TestRunKillsTheProcessGroupAtTimeout`).
3. **The webhook URL in an error**: Go's HTTP errors quote the URL, and Discord's URL is the secret. Pinned in Task B7 (`TestNotifyNeverQuotesTheWebhookURL`).
4. **A missing secret file** must stop `work` at startup with exit 2, naming the file and never printing a value. Pinned in Task B8 (`TestWorkFailsAtStartupWithoutASecretFile`).
5. **A window that wraps midnight** (`22:00-07:00`) must be open at 23:30 and 06:59 and closed at 07:00. Pinned in Task B3 (`TestWindow`).

---

# Part A: workstation telemetry (infra repo)

Work in `~/forge/infra` on branch `workstation-telemetry`. One pull request on the Gitea remote, like infra #4.

### Task A1: OTel Collector and Tempo in the workstation stack

**Files:**
- Modify: `stacks/workstation/exporters.tf` (the `images` map)
- Create: `stacks/workstation/telemetry.tf`
- Modify: `stacks/workstation/storage.tf` (the `data_dirs` command)
- Modify: `stacks/workstation/prometheus.tf` (`scrape_jobs`)
- Modify: `stacks/workstation/grafana.tf` (datasources)
- Modify: `stacks/workstation/verify.sh`

**Interfaces:**
- Produces: OTLP/HTTP on `127.0.0.1:4318` (`/v1/traces`, `/v1/metrics`); Tempo HTTP API on `127.0.0.1:3200`; collector Prometheus endpoint `127.0.0.1:8889`, scraped as job `otel_collector`; Grafana datasource uid `tempo`.

- [ ] **Step 1: Write the failing assertions**

In `verify.sh`, add after `DATASOURCE_URL=...`:

```bash
OTLP=http://127.0.0.1:4318
TEMPO=http://127.0.0.1:3200
```

Add to `HEALTH`:

```bash
  [workstation-otel-collector]=http://127.0.0.1:13133/
  [workstation-tempo]=http://127.0.0.1:3200/ready
```

Add to `PORTS`: `4318 8888 8889 13133 3200 9095 4417`.

Add before `run()`:

```bash
# A span posted to the collector is queryable in Tempo by its trace ID.
a12() {
  trace="$(openssl rand -hex 16)"; span="$(openssl rand -hex 8)"; now="$(date +%s%N)"
  curl -fsS --max-time 10 -H 'Content-Type: application/json' "$OTLP/v1/traces" -d @- >/dev/null <<EOF || fail "collector refused the span"
{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"verify"}}]},
 "scopeSpans":[{"spans":[{"traceId":"$trace","spanId":"$span","name":"verify.a12","kind":1,
 "startTimeUnixNano":"$now","endTimeUnixNano":"$((now + 1000000))"}]}]}]}
EOF
  for _ in $(seq 1 12); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$TEMPO/api/traces/$trace")"
    [ "$code" = 200 ] && { echo "span $trace reached Tempo"; return 0; }
    sleep 5
  done
  fail "span $trace not in Tempo after 60s (last HTTP $code)"
}

# One delta of 1 for metric verify_a13_total, as one short-lived process would send it.
send_delta() {
  curl -fsS --max-time 10 -H 'Content-Type: application/json' "$OTLP/v1/metrics" -d @- >/dev/null <<EOF
{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"verify"}}]},
 "scopeMetrics":[{"metrics":[{"name":"verify_a13_total","sum":{"aggregationTemporality":1,"isMonotonic":true,
 "dataPoints":[{"asInt":"1","startTimeUnixNano":"$2","timeUnixNano":"$3",
 "attributes":[{"key":"nonce","value":{"stringValue":"$1"}}]}]}}]}]}]}
EOF
}

# Two processes each counting 1 add up to 2 in Prometheus, under the name they were sent with.
a13() {
  nonce="$(openssl rand -hex 4)"; t0="$(date +%s%N)"
  send_delta "$nonce" "$t0" "$((t0 + 1000000000))" || fail "collector refused the first delta"
  send_delta "$nonce" "$((t0 + 2000000000))" "$((t0 + 3000000000))" || fail "collector refused the second delta"
  for _ in $(seq 1 12); do
    v="$(curl -fsS --max-time 10 --data-urlencode "query=verify_a13_total{nonce=\"$nonce\"}" "$PROM/api/v1/query" \
      | jq -r '.data.result[0].value[1] // empty')"
    [ "$v" = 2 ] && { echo "two deltas summed to 2 in Prometheus"; return 0; }
    sleep 5
  done
  fail "verify_a13_total{nonce=\"$nonce\"} is '${v:-absent}' after 60s, expected 2"
}

a14() {
  pw="$(tofu -chdir="$STACK_DIR" output -raw grafana_admin_password)" || fail "no grafana_admin_password output"
  status="$(curl -fsS --max-time 10 -u "admin:$pw" "$GRAFANA/api/datasources/uid/tempo/health" | jq -r .status)" \
    || fail "datasource tempo not provisioned"
  [ "$status" = OK ] || fail "tempo datasource health is $status"
  echo "Grafana reaches Tempo"
}
```

Change the default list to `set -- 1 2 3 4 5 6 7 8 10 11 12 13 14`.

- [ ] **Step 2: Run the assertions to watch them fail**

Run: `stacks/workstation/verify.sh 1 12`
Expected: assertion 1 FAILs with `workstation-otel-collector is not running` (or `workstation-tempo is not running`).

- [ ] **Step 3: Pin the two images**

```bash
gh release view -R open-telemetry/opentelemetry-collector-releases --json tagName -q .tagName
gh release view -R grafana/tempo --json tagName -q .tagName
docker buildx imagetools inspect otel/opentelemetry-collector-contrib:<collector tag without the v> --format '{{json .Manifest.Digest}}'
docker buildx imagetools inspect grafana/tempo:<tempo tag without the v> --format '{{json .Manifest.Digest}}'
```

Add two entries to `local.images` in `exporters.tf`, in the existing `name:tag@sha256:digest` form:

```hcl
    otel_collector   = "otel/opentelemetry-collector-contrib:<tag>@sha256:<digest>"
    tempo            = "grafana/tempo:<tag>@sha256:<digest>"
```

- [ ] **Step 4: Add the containers**

In `storage.tf`, append ` ${var.data_root}/tempo` to the `mkdir -p` command.

Create `stacks/workstation/telemetry.tf`:

```hcl
locals {
  # Deltas from short-lived processes such as foundry become cumulative counters that survive
  # between runs; series idle for a week expire.
  collector_yml = yamlencode({
    receivers = {
      otlp = { protocols = { http = { endpoint = "127.0.0.1:4318" } } }
    }
    processors = {
      deltatocumulative = { max_stale = "168h" }
      batch             = {}
    }
    exporters = {
      "otlp/tempo" = { endpoint = "127.0.0.1:4417", tls = { insecure = true } }
      prometheus = {
        endpoint             = "127.0.0.1:8889"
        translation_strategy = "UnderscoreEscapingWithoutSuffixes"
        metric_expiration    = "168h"
      }
    }
    extensions = { health_check = { endpoint = "127.0.0.1:13133" } }
    service = {
      extensions = ["health_check"]
      telemetry = { metrics = { readers = [{ pull = { exporter = { prometheus = { host = "127.0.0.1", port = 8888 } } } }] } }
      pipelines = {
        traces  = { receivers = ["otlp"], processors = ["batch"], exporters = ["otlp/tempo"] }
        metrics = { receivers = ["otlp"], processors = ["deltatocumulative", "batch"], exporters = ["prometheus"] }
      }
    }
  })

  tempo_yml = yamlencode({
    stream_over_http_enabled = true
    server = {
      http_listen_address = "127.0.0.1"
      http_listen_port    = 3200
      grpc_listen_address = "127.0.0.1"
      grpc_listen_port    = 9095
    }
    distributor = { receivers = { otlp = { protocols = { grpc = { endpoint = "127.0.0.1:4417" } } } } }
    memberlist  = { bind_addr = ["127.0.0.1"] }
    compactor   = { compaction = { block_retention = "336h" } }
    storage = { trace = {
      backend = "local"
      wal     = { path = "/var/tempo/wal" }
      local   = { path = "/var/tempo/blocks" }
    } }
    usage_report = { reporting_enabled = false }
  })
}

resource "docker_container" "otel_collector" {
  name         = "workstation-otel-collector"
  image        = local.images.otel_collector
  restart      = "unless-stopped"
  network_mode = "host"
  command      = ["--config=/etc/otelcol-contrib/config.yaml"]

  upload {
    file    = "/etc/otelcol-contrib/config.yaml"
    content = local.collector_yml
  }

  depends_on = [docker_container.tempo]
}

resource "docker_container" "tempo" {
  name         = "workstation-tempo"
  image        = local.images.tempo
  restart      = "unless-stopped"
  network_mode = "host"
  user         = var.run_as
  command      = ["-config.file=/etc/tempo.yaml"]

  mounts {
    type   = "bind"
    source = "${var.data_root}/tempo"
    target = "/var/tempo"
  }

  upload {
    file    = "/etc/tempo.yaml"
    content = local.tempo_yml
  }

  depends_on = [terraform_data.data_dirs]
}
```

In `prometheus.tf`, add to `scrape_jobs`:

```hcl
    otel_collector      = "127.0.0.1:8889"
    otel_collector_self = "127.0.0.1:8888"
    tempo               = "127.0.0.1:3200"
```

In `grafana.tf`, make the datasources list two entries:

```hcl
      datasources = [
        {
          name      = "Workstation"
          uid       = "workstation"
          type      = "prometheus"
          access    = "proxy"
          url       = local.datasource_url
          isDefault = true
          jsonData  = { httpMethod = "POST", timeInterval = var.scrape_interval }
        },
        {
          name   = "Tempo"
          uid    = "tempo"
          type   = "tempo"
          access = "proxy"
          url    = "http://127.0.0.1:3200"
        },
      ]
```

- [ ] **Step 5: Apply, then validate both configs**

Run: `make workstation-plan` and read the plan: two new containers, Prometheus and Grafana replaced for their uploads, nothing destroyed besides those replacements.
Run: `make workstation-up`
Run: `docker exec workstation-otel-collector /otelcol-contrib validate --config=/etc/otelcol-contrib/config.yaml && docker logs workstation-tempo 2>&1 | tail -5`
Expected: validate exits 0; Tempo's log has no `error` lines.
If the collector rejects a component name (`otlp/tempo`, `deltatocumulative`) or Tempo rejects a key, read that version's docs through Context7, use its current name, and record a `Ruling:` in the ledger.

- [ ] **Step 6: Find every listening port**

Run: `ss -ltnpH | grep -E 'otelcol|tempo'`
Expected: only `127.0.0.1` addresses. Any port missing from `PORTS` gets added; any non-loopback bind gets a loopback setting in its config.

- [ ] **Step 7: Run the assertions to watch them pass**

Run: `stacks/workstation/verify.sh 1 2 3 10 12 13 14`
Expected: each assertion prints `PASS`; a13 prints `two deltas summed to 2 in Prometheus`.

- [ ] **Step 8: Commit**

```bash
git add stacks/workstation
git commit -m "Add an OTel Collector and Tempo to the workstation stack"
```

### Task A2: Foundry dashboard, README and note

**Files:**
- Modify: `stacks/workstation/dashboard.tf`
- Modify: `stacks/workstation/grafana.tf` (dashboard uploads)
- Modify: `stacks/workstation/verify.sh` (`a10`)
- Modify: `README.md`
- Create: `docs/notes/2026-10-16-workstation-telemetry.md`

**Interfaces:**
- Consumes: Task A1's `otel_collector` scrape job and the metric names in Global Constraints.
- Produces: Grafana dashboard uid `foundry`.

- [ ] **Step 1: Extend a10 so it fails**

In `a10`, after the workstation dashboard check, add:

```bash
  fpanels="$(curl -fsS --max-time 10 -u "admin:$pw" "$GRAFANA/api/dashboards/uid/foundry" | jq '.dashboard.panels | length')" \
    || fail "dashboard foundry not provisioned"
  [ "$fpanels" -ge 5 ] || fail "foundry dashboard has $fpanels panels, expected 5"
```

Run: `stacks/workstation/verify.sh 10`
Expected: FAIL `dashboard foundry not provisioned`.

- [ ] **Step 2: Render both dashboards from one pattern**

In `dashboard.tf`, rename `panel_specs` to `workstation_panels`, add `foundry_panels`, and replace `local.dashboard` with a map rendered by one expression:

```hcl
  foundry_panels = [
    { title = "Issue outcomes per day", unit = "short", targets = [
      ["sum by (outcome, reason) (increase(foundry_issue_outcomes_total[1d]))", "{{outcome}} {{reason}}"],
    ] },
    { title = "Step duration p90", unit = "s", targets = [
      ["histogram_quantile(0.9, sum by (le, step) (rate(foundry_step_duration_seconds_bucket[6h])))", "{{step}}"],
    ] },
    { title = "Review rounds p50 and p90", unit = "short", targets = [
      ["histogram_quantile(0.5, sum by (le, repo) (rate(foundry_review_rounds_bucket[1d])))", "p50 {{repo}}"],
      ["histogram_quantile(0.9, sum by (le, repo) (rate(foundry_review_rounds_bucket[1d])))", "p90 {{repo}}"],
    ] },
    { title = "Agent cost per day (USD)", unit = "currencyUSD", targets = [
      ["sum by (model) (increase(foundry_agent_cost_usd_total[1d]))", "{{model}}"],
    ] },
    { title = "Friction filed per day", unit = "short", targets = [
      ["sum by (kind) (increase(foundry_friction_filed_total[1d]))", "{{kind}}"],
    ] },
  ]

  boards = {
    workstation = { title = "Workstation", panels = local.workstation_panels }
    foundry     = { title = "Foundry", panels = local.foundry_panels }
  }

  dashboards = { for uid, b in local.boards : uid => {
    uid           = uid
    title         = b.title
    schemaVersion = 39
    refresh       = "30s"
    time          = { from = "now-24h", to = "now" }
    panels = [for i, p in b.panels : {
      id          = i + 1
      type        = "timeseries"
      title       = p.title
      datasource  = local.datasource
      gridPos     = { x = (i % 2) * 12, y = floor(i / 2) * 8, w = 12, h = 8 }
      fieldConfig = { defaults = { unit = p.unit }, overrides = [] }
      targets = [for j, t in p.targets : {
        refId        = substr("ABCDEFGH", j, 1)
        expr         = t[0]
        legendFormat = t[1]
        datasource   = local.datasource
      }]
    }]
  } }
```

In `grafana.tf`, replace the single `workstation.json` upload with:

```hcl
  dynamic "upload" {
    for_each = local.dashboards
    content {
      file    = "/etc/grafana/dashboards/${upload.key}.json"
      content = jsonencode(upload.value)
    }
  }
```

- [ ] **Step 3: Apply and run the assertions**

Run: `make workstation-plan` (only Grafana is replaced), then `make workstation-up`, then `stacks/workstation/verify.sh 10`
Expected: PASS, `dashboard with N panels`.

- [ ] **Step 4: README and note**

In `README.md`, after the `make workstation-up` line, add one sentence: traces go to Tempo through the collector on `127.0.0.1:4318`, and the foundry board is at `http://127.0.0.1:3312/d/foundry`.

Write `docs/notes/2026-10-16-workstation-telemetry.md` with the four Tenet 8 headings: The pattern, What surprised me, What I would do differently, What I still do not understand. Use what Steps 5 to 7 of A1 actually showed. One item that belongs under "What I still do not understand": whether 168h of `max_stale` and `metric_expiration` loses the first increment of a counter that wakes after a longer gap.

- [ ] **Step 5: Run the full verify and commit**

Run: `stacks/workstation/verify.sh`
Expected: every assertion PASSes (a11 restarts the stack; allow it two minutes).

```bash
git add stacks/workstation README.md docs/notes/2026-10-16-workstation-telemetry.md
git commit -m "Add the foundry dashboard and note the telemetry increment"
```

Push the branch and open the pull request on Gitea. Success 1 of the spec holds when this merges.

---

# Part B: foundry

Work in `~/forge/foundry` on branch `impl/foundry-work` from `main`.

### Task B1: Telemetry setup, root spans and step spans

**Files:**
- Create: `internal/telemetry/telemetry.go`
- Modify: `internal/arch_test.go`
- Modify: `internal/core/app/step.go`
- Create: `internal/core/app/metrics.go`
- Modify: `internal/core/app/ship.go` (step closures take `ctx`)
- Modify: `internal/core/app/friction.go` (step name)
- Modify: `cmd/foundry/main.go` (`dispatch`)
- Create: `cmd/foundry/otlp_test.go`
- Modify: `cmd/foundry/e2e_test.go` (world env)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: `telemetry.Setup(ctx context.Context, getenv func(string) string) (func(context.Context), error)`; `telemetry.Start(ctx context.Context, name string, getenv func(string) string) (context.Context, trace.Span)`; `telemetry.Env(ctx context.Context) []string`; `app.step(ctx context.Context, name string, fn func(context.Context) error) error`; in e2e tests: `startSink(t) *otlpSink`, `(*otlpSink).spans() []sinkSpan`, `(*otlpSink).metricNames() []string`, `sinkSpan{Name, Trace, ID, Parent string; Attrs map[string]string}`, `(*world).exportTo(s *otlpSink)`.

- [ ] **Step 1: Add the dependencies**

```bash
go get go.opentelemetry.io/otel@latest go.opentelemetry.io/otel/sdk@latest go.opentelemetry.io/otel/sdk/metric@latest \
  go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@latest \
  go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp@latest \
  go.opentelemetry.io/proto/otlp@latest google.golang.org/protobuf@latest
```

- [ ] **Step 2: Write the failing end-to-end test**

In `e2e_test.go` `newWorld`, build `w.env` from the environment without trace context, since a Claude Code session running the tests can pass its own `TRACEPARENT`, and append `"OTEL_TRACES_EXPORTER=none", "OTEL_METRICS_EXPORTER=none"`, so no test exports to a real collector:

```go
	base := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "TRACEPARENT=") || strings.HasPrefix(kv, "TRACESTATE=")
	})
	w.env = append(base,
		"PATH="+fakebin+":"+os.Getenv("PATH"), "HOME="+root, "FOUNDRY_HOME="+w.home,
		// ...the existing entries unchanged...
		"OTEL_TRACES_EXPORTER=none", "OTEL_METRICS_EXPORTER=none")
```

Create `cmd/foundry/otlp_test.go`:

```go
package main

import (
	"compress/gzip"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	colmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpSink is an OTLP/HTTP receiver that keeps every span and metric name it is sent.
type otlpSink struct {
	url     string
	mu      sync.Mutex
	got     []sinkSpan
	metrics []string
}

type sinkSpan struct {
	Name, Trace, ID, Parent string
	Attrs                   map[string]string
}

func startSink(t *testing.T) *otlpSink {
	s := &otlpSink{}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *otlpSink) serve(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body = gz
	}
	b, _ := io.ReadAll(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/v1/traces":
		var req coltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					attrs := map[string]string{}
					for _, a := range sp.Attributes {
						attrs[a.Key] = a.Value.String()
					}
					s.got = append(s.got, sinkSpan{Name: sp.Name, Trace: hex.EncodeToString(sp.TraceId),
						ID: hex.EncodeToString(sp.SpanId), Parent: hex.EncodeToString(sp.ParentSpanId), Attrs: attrs})
				}
			}
		}
		out, _ := proto.Marshal(&coltrace.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(out)
	case "/v1/metrics":
		var req colmetric.ExportMetricsServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					s.metrics = append(s.metrics, m.Name)
				}
			}
		}
		out, _ := proto.Marshal(&colmetric.ExportMetricsServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(out)
	}
}

func (s *otlpSink) spans() []sinkSpan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkSpan{}, s.got...)
}

func (s *otlpSink) metricNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.metrics...)
}

// named returns the spans called name.
func named(spans []sinkSpan, name string) []sinkSpan {
	var out []sinkSpan
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func (w *world) exportTo(s *otlpSink) {
	w.env = append(w.env, "OTEL_TRACES_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT="+s.url)
}

func TestShipSpansParentUnderTraceparent(t *testing.T) {
	sink := startSink(t)
	w := newWorld(t, "feat")
	w.exportTo(sink)
	const trace, parent = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	w.env = append(w.env, "TRACEPARENT=00-"+trace+"-"+parent+"-01")
	w.foundry("friction", "none", "--reason", "smooth run")
	if out, _, code := w.foundry("ship", "--recap", w.recap()); code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	spans := sink.spans()
	ship := named(spans, "foundry.ship")
	if len(ship) != 1 || ship[0].Trace != trace || ship[0].Parent != parent {
		t.Fatalf("foundry.ship %+v, want trace %s parent %s; all %+v", ship, trace, parent, spans)
	}
	for _, step := range []string{"preflight", "checks", "review", "push_pr", "ci", "friction"} {
		got := named(spans, "foundry.ship."+step)
		if len(got) != 1 || got[0].Parent != ship[0].ID {
			t.Errorf("foundry.ship.%s %+v, want one child of %s", step, got, ship[0].ID)
		}
	}
}
```

- [ ] **Step 3: Run it to watch it fail**

Run: `go test ./cmd/foundry -run TestShipSpansParentUnderTraceparent -v`
Expected: FAIL `foundry.ship [], want trace 4bf92f35...`.

- [ ] **Step 4: Write the telemetry package**

Create `internal/telemetry/telemetry.go`:

```go
// Package telemetry installs foundry's OpenTelemetry providers and carries trace context across processes.
package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Scope names foundry's tracer and meter.
const Scope = "github.com/tunedev/foundry"

// Setup installs OTLP/HTTP trace and metric providers configured by the standard OTEL_EXPORTER_OTLP_*
// variables, and the W3C trace-context propagator. OTEL_TRACES_EXPORTER=none or OTEL_METRICS_EXPORTER=none
// leaves that signal on the no-op provider. Metrics use delta temporality. Export errors are logged, never
// returned. The returned shutdown flushes both providers within 5s.
func Setup(ctx context.Context, getenv func(string) string) (func(context.Context), error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("telemetry export failed", "error", err.Error())
	}))
	res := resource.NewSchemaless(attribute.String("service.name", "foundry"))
	var stops []func(context.Context) error
	if getenv("OTEL_TRACES_EXPORTER") != "none" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
		otel.SetTracerProvider(tp)
		stops = append(stops, tp.Shutdown)
	}
	if getenv("OTEL_METRICS_EXPORTER") != "none" {
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithTemporalitySelector(delta))
		if err != nil {
			return nil, err
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)), sdkmetric.WithResource(res))
		otel.SetMeterProvider(mp)
		stops = append(stops, mp.Shutdown)
	}
	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for _, stop := range stops {
			if err := stop(ctx); err != nil {
				slog.WarnContext(ctx, "telemetry shutdown", "error", err.Error())
			}
		}
	}, nil
}

func delta(sdkmetric.InstrumentKind) metricdata.Temporality { return metricdata.DeltaTemporality }

// Start begins a command's root span, as a child of the TRACEPARENT the environment carries, if any.
func Start(ctx context.Context, name string, getenv func(string) string) (context.Context, trace.Span) {
	carrier := propagation.MapCarrier{"traceparent": getenv("TRACEPARENT"), "tracestate": getenv("TRACESTATE")}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	return otel.Tracer(Scope).Start(ctx, name)
}

// Env returns TRACEPARENT and TRACESTATE entries for ctx's span, for a child process's environment.
func Env(ctx context.Context) []string {
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	var env []string
	if v := c["traceparent"]; v != "" {
		env = append(env, "TRACEPARENT="+v)
	}
	if v := c["tracestate"]; v != "" {
		env = append(env, "TRACESTATE="+v)
	}
	return env
}
```

- [ ] **Step 5: Spans and the duration metric in step**

Create `internal/core/app/metrics.go`:

```go
package app

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	tracer = otel.Tracer("github.com/tunedev/foundry")
	meter  = otel.Meter("github.com/tunedev/foundry")

	stepDuration, _ = meter.Float64Histogram("foundry_step_duration_seconds", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 15, 30, 60, 120, 300, 600, 1200, 2700))
)
```

Replace `internal/core/app/step.go`'s `step`:

```go
// step runs fn as the named use-case step inside a span of that name. It logs the step's start, outcome
// and duration, and records the duration on foundry_step_duration_seconds.
func step(ctx context.Context, name string, fn func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	slog.InfoContext(ctx, "step start", "step", name)
	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)
	stepDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String("step", name)))
	attrs := []any{"step", name, "duration_ms", elapsed.Milliseconds()}
	if err != nil {
		span.SetStatus(codes.Error, class(err))
		slog.ErrorContext(ctx, "step failed", append(attrs, "error", err.Error(), "class", class(err))...)
		return err
	}
	slog.InfoContext(ctx, "step done", attrs...)
	return nil
}
```

Imports for `step.go`: `context`, `errors`, `log/slog`, `time`, `go.opentelemetry.io/otel/attribute`, `go.opentelemetry.io/otel/codes`, `go.opentelemetry.io/otel/metric`, `ports`.

In `ship.go`, change the steps table to take the step's context and name each span `foundry.ship.<step>`:

```go
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"preflight", func(ctx context.Context) (err error) { r.Branch, err = preflight(ctx, d, in); return err }},
		{"checks", func(ctx context.Context) error { return runChecks(ctx, d, in) }},
		{"review", func(ctx context.Context) error { return review(ctx, d, in) }},
		{"push-pr", func(ctx context.Context) (err error) {
			head, pr, err = pushAndPR(ctx, d, in, r.Branch)
			r.PRURL = pr.URL
			return err
		}},
		{"ci", func(ctx context.Context) error { return waitCI(ctx, d, in, r.Branch, head, pr) }},
		{"friction", func(context.Context) error { return frictionGiven(d, in.Repo, r.Branch) }},
	}
	for _, s := range steps {
		r.Step = s.name
		if err := step(ctx, "foundry.ship."+strings.ReplaceAll(s.name, "-", "_"), s.fn); err != nil {
			return r, err
		}
		r.Passed = append(r.Passed, s.name)
	}
```

In `friction.go`, change `step(ctx, "file friction", func() error {` to `step(ctx, "foundry.friction.file", func(ctx context.Context) error {`.

- [ ] **Step 6: Root span and shutdown in dispatch**

In `cmd/foundry/main.go`, after `defer stop()` in `dispatch`, replace the `switch` with:

```go
	shutdown, err := telemetry.Setup(ctx, os.Getenv)
	if err != nil {
		return usageErr(err)
	}
	defer shutdown(context.WithoutCancel(ctx))
	ctx, span := telemetry.Start(ctx, "foundry."+args[0], os.Getenv)
	defer span.End()
	code := command(ctx, cfg, args)
	if code != exitOK {
		span.SetStatus(codes.Error, "exit "+strconv.Itoa(code))
	}
	return code
}

func command(ctx context.Context, cfg config.Config, args []string) int {
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
```

The deferred calls run in reverse order, so the span ends before shutdown flushes it.

- [ ] **Step 7: Narrow the arch test to foundry's own packages and forbid the SDK in the core**

In `internal/arch_test.go`, in `TestCoreImportsNoDriver`, extend the condition:

```go
		if strings.Contains(dep, "/internal/adapters/") || strings.HasSuffix(dep, "/internal/run") || dep == "os/exec" ||
			strings.HasSuffix(dep, "/internal/telemetry") || strings.HasPrefix(dep, "go.opentelemetry.io/otel/sdk") ||
			strings.HasPrefix(dep, "go.opentelemetry.io/otel/exporters") {
```

In `TestCoreReachesNoOSThroughOurPackages`, skip lines from other modules, since the OpenTelemetry API imports `os`:

```go
		fields := strings.Fields(line)
		if !strings.HasPrefix(fields[0], "github.com/tunedev/foundry/") {
			continue
		}
```

- [ ] **Step 8: Run the tests to watch them pass**

Run: `go vet ./... && go test ./...`
Expected: PASS, including `TestShipSpansParentUnderTraceparent` and both arch tests.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal cmd
git commit -m "Export foundry's spans and step durations over OTLP, parented by TRACEPARENT"
```

### Task B2: Review round limit and the stop detail in the run record

**Files:**
- Modify: `internal/core/ship/recap.go` (add `NotConverged`)
- Modify: `internal/core/app/ship.go`
- Modify: `internal/adapters/outbound/store/store.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `cmd/foundry/main.go` (`cmdShip`), `cmd/foundry/wire.go`
- Modify: `skills/foundry-ship/SKILL.md`
- Test: `cmd/foundry/e2e_test.go`

**Interfaces:**
- Consumes: `app.step` from B1.
- Produces: `ship.NotConverged = "review did not converge"`; `ship.Rounds{Count int; LastSHA string}`; `ShipDeps.Rounds func(repo, branch string) (ship.Rounds, error)`; `ShipDeps.SaveRounds func(repo, branch string, r ship.Rounds) error`; `ShipInput.MaxReviewRounds int`; `store.Run.Detail string` (JSON `detail`); `store.Store.Rounds`, `store.Store.SaveRounds`, `store.Store.LastRun(repo, branch string) (store.Run, bool, error)`; `config.Config.MaxReviewRounds int`; `config.Config.ints() map[string]*int`.

- [ ] **Step 1: Write the failing end-to-end test**

```go
func TestShipStopsWhenReviewDoesNotConverge(t *testing.T) {
	w := newWorld(t, "feat")
	w.env = append(w.env, "FOUNDRY_MAX_REVIEW_ROUNDS=2")
	w.write(w.root, "vouch-gate-fail", "minor calc.go:1 unbounded retry\n")
	out, _, code := w.foundry("ship", "--recap", w.recap())
	if code != 1 || !strings.Contains(out, "stop at review: vouch did not pass") {
		t.Fatalf("round 1: code %d\n%s", code, out)
	}
	if out, _, _ = w.foundry("ship", "--recap", w.recap()); !strings.Contains(out, "vouch did not pass") {
		t.Fatalf("a rerun at the same HEAD counted a round:\n%s", out)
	}
	w.commit("fix attempt")
	out, _, code = w.foundry("ship", "--recap", w.recap())
	if code != 1 || !strings.Contains(out, "review did not converge after 2 rounds") || !strings.Contains(out, "unbounded retry") ||
		!strings.Contains(out, "FOUNDRY_MAX_REVIEW_ROUNDS") {
		t.Fatalf("round 2: code %d\n%s", code, out)
	}
	runs := w.runs()
	if last := runs[len(runs)-1]; last.Step != "review" || !strings.HasPrefix(last.Detail, "review did not converge") {
		t.Fatalf("run record %+v", last)
	}
}
```

Add `Detail string` to the `runLine` struct in `e2e_test.go`.

- [ ] **Step 2: Run it to watch it fail**

Run: `go test ./cmd/foundry -run TestShipStopsWhenReviewDoesNotConverge -v`
Expected: FAIL at startup with `config: ...` or at round 2 without `did not converge` (the env key is unknown to config until Step 4, so the first failure may be exit 2).

- [ ] **Step 3: Store rounds, detail and the last run**

In `store.go`, add `Detail string \`json:"detail,omitempty"\`` to `Run` after `PRURL`, then add:

```go
func (s Store) roundsPath(repo, branch string) string {
	return filepath.Join(s.Home, "repos", filepath.FromSlash(repo), "review", url.PathEscape(branch)+".json")
}

type roundsJSON struct {
	Count   int    `json:"count"`
	LastSHA string `json:"last_sha"`
}

// Rounds returns the review rounds that ended in findings on branch, and the commit the last one reviewed.
func (s Store) Rounds(repo, branch string) (ship.Rounds, error) {
	var r roundsJSON
	b, err := os.ReadFile(s.roundsPath(repo, branch))
	if errors.Is(err, os.ErrNotExist) {
		return ship.Rounds{}, nil
	}
	if err != nil {
		return ship.Rounds{}, err
	}
	err = json.Unmarshal(b, &r)
	return ship.Rounds{Count: r.Count, LastSHA: r.LastSHA}, err
}

func (s Store) SaveRounds(repo, branch string, r ship.Rounds) error {
	return writeJSON(s.roundsPath(repo, branch), roundsJSON{Count: r.Count, LastSHA: r.LastSHA})
}

// LastRun returns the newest ship record for repo and branch.
func (s Store) LastRun(repo, branch string) (Run, bool, error) {
	b, err := os.ReadFile(filepath.Join(s.Home, "runs.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	var last Run
	found := false
	for line := range strings.Lines(string(b)) {
		var r Run
		if json.Unmarshal([]byte(line), &r) == nil && r.Kind == "ship" && r.Repo == repo && r.Branch == branch {
			last, found = r, true
		}
	}
	return last, found, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
```

Make `SaveAnswer` call `writeJSON(s.answerPath(repo, branch), a)`. Add `strings` and `github.com/tunedev/foundry/internal/core/ship` to the imports.

- [ ] **Step 4: Config key max_review_rounds, with env override for integers**

In `config.go`, add to `Config`: `MaxReviewRounds int \`toml:"max_review_rounds"\``; to `Defaults()`: `MaxReviewRounds: 3`. Add:

```go
func (c *Config) ints() map[string]*int {
	return map[string]*int{"max_review_rounds": &c.MaxReviewRounds}
}
```

In `Load`, after the durations loop:

```go
	for key, n := range c.ints() {
		name := "FOUNDRY_" + strings.ToUpper(key)
		if v := getenv(name); v != "" {
			i, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s: %w", name, err)
			}
			*n = i
		}
	}
```

In `validate`, after the durations loop:

```go
	for key, n := range c.ints() {
		if *n < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", key))
		}
	}
```

Add to `config_test.go`:

```go
func TestLoadIntegersFromEnv(t *testing.T) {
	c, err := Load(t.TempDir(), env(map[string]string{"FOUNDRY_MAX_REVIEW_ROUNDS": "5"}))
	if err != nil || c.MaxReviewRounds != 5 {
		t.Fatalf("%d %v", c.MaxReviewRounds, err)
	}
	if _, err := Load(t.TempDir(), env(map[string]string{"FOUNDRY_MAX_REVIEW_ROUNDS": "0"})); err == nil || !strings.Contains(err.Error(), "max_review_rounds must be at least 1") {
		t.Errorf("zero rounds: %v", err)
	}
}
```

- [ ] **Step 5: Count rounds in the review step**

In `internal/core/ship/recap.go`, add:

```go
// NotConverged starts the review stop ship gives once a branch has used its review rounds.
const NotConverged = "review did not converge"

// Rounds is how many reviews of a branch ended in findings, and the commit the last of them reviewed.
type Rounds struct {
	Count   int
	LastSHA string
}
```

In `app/ship.go`, add `Rounds func(repo, branch string) (ship.Rounds, error)` and `SaveRounds func(repo, branch string, r ship.Rounds) error` to `ShipDeps`, `MaxReviewRounds int` to `ShipInput`, pass the branch to the review step (`return review(ctx, d, in, r.Branch)`), and replace `review`:

```go
// review runs vouch on HEAD. A review that ends in findings on a commit not counted before adds one round
// for the branch; the round that reaches in.MaxReviewRounds stops with ship.NotConverged instead of asking
// for another fix.
func review(ctx context.Context, d ShipDeps, in ShipInput, branch string) error {
	err := retryOnce(ctx, in.RetryPause, func() error { return d.Review(ctx, in.Dir) })
	switch {
	case err == nil || isTransient(err):
		return err
	case errors.Is(err, ports.ErrFindings):
		r, rerr := countRound(ctx, d, in, branch)
		if rerr != nil {
			return rerr
		}
		if r.Count >= in.MaxReviewRounds {
			return needs("review", "%s after %d rounds; report the last findings to the user and stop "+
				"(to allow more, rerun with FOUNDRY_MAX_REVIEW_ROUNDS above %d):\n%v", ship.NotConverged, r.Count, r.Count, err)
		}
		return needs("review", "vouch did not pass this code; fix the findings, commit, rerun:\n%v", err)
	}
	return needs("review", "vouch failed to run; report this error to the user:\n%v", err)
}
```

Add below it:

```go
// countRound adds one review round for branch unless HEAD is the commit the last round already counted.
func countRound(ctx context.Context, d ShipDeps, in ShipInput, branch string) (ship.Rounds, error) {
	r, err := d.Rounds(in.Repo, branch)
	if err != nil {
		return r, err
	}
	head, err := d.HeadSHA(ctx, in.Dir)
	if err != nil || head == r.LastSHA {
		return r, err
	}
	r = ship.Rounds{Count: r.Count + 1, LastSHA: head}
	return r, d.SaveRounds(in.Repo, branch, r)
}
```

- [ ] **Step 6: Wire it and record the stop detail**

In `wire.go` `shipDeps`, add `Rounds: w.store.Rounds, SaveRounds: w.store.SaveRounds`. In `cmdShip`, pass `MaxReviewRounds: cfg.MaxReviewRounds` in `ShipInput`, and set `Detail: detail(err)` on the `store.Run`. Add to `main.go`:

```go
// detail is the stop's text for the run record, at most 2000 bytes.
func detail(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if na, ok := errors.AsType[*app.NeedsAction](err); ok {
		s = na.Detail
	}
	return s[:min(len(s), 2000)]
}
```

- [ ] **Step 7: Tell the skill about the new stop**

In `skills/foundry-ship/SKILL.md`, after the `vouch failed to run` bullet, add:

```markdown
- `stop at review: review did not converge`: report the last findings to the user and stop.
```

- [ ] **Step 8: Run the tests**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal cmd skills
git commit -m "Stop ship when review does not converge, and record each stop's detail"
```

### Task B3: Work rules

**Files:**
- Create: `internal/core/work/window.go`, `claim.go`, `pick.go`, `outcome.go`, `prompt.go`
- Test: `internal/core/work/work_test.go`
- Modify: `internal/core/ports/ports.go` (Issue, Comment, PR fields; agent types)
- Modify: `internal/core/friction/friction.go` (label and hit constants)
- Modify: `internal/core/app/friction.go` (use `friction.HitAgain`)

**Interfaces:**
- Produces:
  - `ports.Comment{Author, Body string; Trusted bool; CreatedAt time.Time}`; `Forge.IssueComments(ctx, dir string, number int) ([]Comment, error)`; `ports.Issue` gains `Body string; Labels []string; CreatedAt time.Time; Comments []Comment`; `ports.PR` gains `Head, State string`.
  - `ports.AgentJob{RepoDir, Base, Branch, Prompt string; Verify []string; MaxTurns int}`; `ports.AgentResult{Text, Subtype, SessionID, Model string; Turns int; CostUSD float64}`; `ports.AgentRunner` with `Run(ctx context.Context, job AgentJob) (AgentResult, error)`.
  - `ports.IssueReport{Number int; Title, URL, Outcome, Reason, PRURL, Detail string}`; `ports.Summary{RunID, Repo string; Issues []IssueReport; CostUSD float64; Wall time.Duration; Aborted string}`; `ports.Notifier` with `Notify(ctx context.Context, s Summary) error`.
  - `friction.InProgress = "friction:in-progress"`, `friction.Stuck = "friction:stuck"`, `friction.HitAgain = "Hit again from branch"`.
  - `work.Window`, `work.ParseWindow(s string) (Window, error)`, `(Window) Contains(t time.Time) bool`.
  - `work.ClaimComment(runID string) string`, `work.Claim{RunID string; At time.Time}`, `work.CurrentClaim(comments []ports.Comment, bot string) (Claim, bool)`, `work.Branch(issue int, runID string) string`.
  - `work.Release` with `Keep`, `Expired`, `NoClaim`, `Rejected`; `work.Decide(c Claim, claimed bool, pr ports.PR, found bool, now time.Time, lease time.Duration) Release`.
  - `work.Hits(is ports.Issue) int`, `work.Pick(ready []ports.Issue, open []ports.PR, max int) []ports.Issue`.
  - `work.ShipRecord{Step, Outcome, PRURL, Detail string}`, `work.Evidence{Result ports.AgentResult; RunErr error; TimedOut bool; Ship ShipRecord; Shipped bool}`, `work.Kind` (`ReadyForHuman`, `NeedsDecision`, `Stuck`), `work.Reason` (constants below), `work.Outcome{Kind Kind; Reason Reason; PRURL, Detail string}`, `work.Classify(e Evidence) Outcome`.
  - `work.Prompt(is ports.Issue, verify []string) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/core/work/work_test.go`:

```go
package work

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

var t0 = time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)

func TestWindow(t *testing.T) {
	w, err := ParseWindow("22:00-07:00")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 16, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		t    time.Time
		open bool
	}{{at(23, 30), true}, {at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false}, {at(22, 0), true}} {
		if w.Contains(c.t) != c.open {
			t.Errorf("%s: open=%v", c.t.Format("15:04"), !c.open)
		}
	}
	if always, _ := ParseWindow(""); !always.Contains(at(12, 0)) {
		t.Error("empty window closed")
	}
	for _, bad := range []string{"22:00", "25:00-07:00", "07:00-07:00"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCurrentClaimIgnoresOtherAuthors(t *testing.T) {
	cs := []ports.Comment{
		{Author: "bot", Body: ClaimComment("r1"), CreatedAt: t0},
		{Author: "bot", Body: ClaimComment("r2") + "\nmore", CreatedAt: t0.Add(time.Hour)},
		{Author: "someone", Body: ClaimComment("r9"), CreatedAt: t0.Add(2 * time.Hour)},
	}
	c, ok := CurrentClaim(cs, "bot")
	if !ok || c.RunID != "r2" || !c.At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := CurrentClaim(cs[2:], "bot"); ok {
		t.Error("a claim by another author counted")
	}
}

func TestDecide(t *testing.T) {
	c := Claim{RunID: "r", At: t0}
	lease := 2 * time.Hour
	cases := []struct {
		name    string
		claimed bool
		pr      ports.PR
		found   bool
		now     time.Time
		want    Release
	}{
		{"no claim", false, ports.PR{}, false, t0, NoClaim},
		{"closed unmerged", true, ports.PR{State: "CLOSED"}, true, t0, Rejected},
		{"open PR", true, ports.PR{State: "OPEN"}, true, t0.Add(5 * time.Hour), Keep},
		{"merged PR", true, ports.PR{State: "MERGED"}, true, t0.Add(5 * time.Hour), Keep},
		{"young, no PR", true, ports.PR{}, false, t0.Add(time.Hour), Keep},
		{"expired, no PR", true, ports.PR{}, false, t0.Add(3 * time.Hour), Expired},
	}
	for _, tc := range cases {
		if got := Decide(c, tc.claimed, tc.pr, tc.found, tc.now, lease); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPickOrdersByHitsThenAgeAndSkipsTaken(t *testing.T) {
	hit := ports.Comment{Body: "Hit again from branch `x`.", Trusted: true}
	spam := ports.Comment{Body: "Hit again from branch `y`."}
	ready := []ports.Issue{
		{Number: 1, CreatedAt: t0, Labels: []string{"friction:ready"}},
		{Number: 2, CreatedAt: t0.Add(time.Hour), Labels: []string{"friction:ready"}, Comments: []ports.Comment{hit, hit}},
		{Number: 3, CreatedAt: t0.Add(-time.Hour), Labels: []string{"friction:ready"}},
		{Number: 8, CreatedAt: t0.Add(2 * time.Hour), Labels: []string{"friction:ready"}, Comments: []ports.Comment{spam, spam, spam}},
		{Number: 4, Labels: []string{"friction:ready", "friction:in-progress"}},
		{Number: 5, Labels: []string{"friction:ready", "friction:stuck"}},
		{Number: 6, Labels: []string{"friction:ready"}},
		{Number: 7, Labels: []string{"friction:ready"}},
	}
	open := []ports.PR{{Head: "friction/6-20261015-020000"}, {Head: "fix-docs", Body: "Fixes #7"}}
	got := Pick(ready, open, 3)
	if Hits(ready[3]) != 1 {
		t.Errorf("untrusted hit comments counted: %d", Hits(ready[3]))
	}
	var nums []int
	for _, is := range got {
		nums = append(nums, is.Number)
	}
	if len(nums) != 3 || nums[0] != 2 || nums[1] != 3 || nums[2] != 1 {
		t.Fatalf("picked %v, want [2 3 1]", nums)
	}
}

func TestClassify(t *testing.T) {
	ok := ports.AgentResult{Subtype: "success"}
	cases := []struct {
		name   string
		e      Evidence
		kind   Kind
		reason Reason
	}{
		{"hand-off", Evidence{Result: ok, Shipped: true, Ship: ShipRecord{Step: "hand-off", Outcome: "ready-for-human", PRURL: "u"}}, ReadyForHuman, ""},
		{"hand-off then timeout", Evidence{TimedOut: true, RunErr: errors.New("killed"), Shipped: true, Ship: ShipRecord{Outcome: "ready-for-human"}}, ReadyForHuman, ""},
		{"decision", Evidence{Result: ports.AgentResult{Text: "NEEDS-DECISION: A or B; A."}}, NeedsDecision, ""},
		{"timeout", Evidence{TimedOut: true, RunErr: errors.New("killed")}, Stuck, ReasonTimeout},
		{"turns", Evidence{Result: ports.AgentResult{Subtype: "error_max_turns"}}, Stuck, ReasonMaxTurns},
		{"review", Evidence{Result: ports.AgentResult{Text: "STUCK: no"}, Shipped: true, Ship: ShipRecord{Step: "review", Detail: "review did not converge after 3 rounds"}}, Stuck, ReasonReviewNotConverged},
		{"agent stuck", Evidence{Result: ports.AgentResult{Text: "STUCK: vouch failed"}}, Stuck, ReasonAgentStuck},
		{"crash", Evidence{RunErr: errors.New("exit 3")}, Stuck, ReasonCrash},
		{"ship stop", Evidence{Result: ok, Shipped: true, Ship: ShipRecord{Step: "ci", Outcome: "needs-action"}}, Stuck, ReasonShipStop},
		{"no ship", Evidence{Result: ok}, Stuck, ReasonNoShip},
	}
	for _, c := range cases {
		o := Classify(c.e)
		if o.Kind != c.kind || o.Reason != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, o.Kind, o.Reason, c.kind, c.reason)
		}
	}
	if o := Classify(cases[2].e); o.Detail != "A or B; A." {
		t.Errorf("decision detail %q", o.Detail)
	}
}

func TestPromptCarriesIssueAndOverrides(t *testing.T) {
	p := Prompt(ports.Issue{Number: 7, Title: "Slow tests", Body: "## What slowed me down\n4 minutes"}, []string{"go test ./..."})
	for _, want := range []string{"#7", "Slow tests", "4 minutes", "`go test ./...`", "Closes #7", "NEEDS-DECISION:", "STUCK:", "foundry ship"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
```

- [ ] **Step 2: Run them to watch them fail**

Run: `go test ./internal/core/work -v`
Expected: FAIL to compile (`undefined: ParseWindow`).

- [ ] **Step 3: Extend the ports**

In `ports.go`, add `"time"` to the imports, give `PR` the fields `Head string` and `State string` (`OPEN`, `CLOSED` or `MERGED`), and replace `Issue`:

```go
// Comment is an issue comment. Trusted means its author owns the repo or collaborates on it.
type Comment struct {
	Author, Body string
	Trusted      bool
	CreatedAt    time.Time
}

type Issue struct {
	Number    int
	Title     string
	URL       string
	Body      string
	Labels    []string
	CreatedAt time.Time
	Comments  []Comment
}
```

Add to `Forge`:

```go
	// BranchPR returns the newest pull request whose head is branch, in any state.
	BranchPR(ctx context.Context, dir, branch string) (PR, bool, error)
	OpenPRs(ctx context.Context, dir string) ([]PR, error)
	EditLabels(ctx context.Context, dir string, number int, add, remove []string) error
	IssueComments(ctx context.Context, dir string, number int) ([]Comment, error)
```

Append:

```go
// AgentJob is one issue for an agent: a fresh Branch from origin/Base of the repo checked out at RepoDir.
type AgentJob struct {
	RepoDir, Base, Branch, Prompt string
	Verify                        []string
	MaxTurns                      int
}

// AgentResult is what the agent reported when it finished. Subtype is Claude Code's result subtype,
// such as success or error_max_turns.
type AgentResult struct {
	Text, Subtype, SessionID, Model string
	Turns                           int
	CostUSD                         float64
}

// AgentRunner runs an agent on one job until it finishes or ctx ends.
type AgentRunner interface {
	Run(ctx context.Context, job AgentJob) (AgentResult, error)
}

type IssueReport struct {
	Number                                    int
	Title, URL, Outcome, Reason, PRURL, Detail string
}

// Summary is one work run, for the human. Aborted says why the run ended early, or is empty.
type Summary struct {
	RunID, Repo string
	Issues      []IssueReport
	CostUSD     float64
	Wall        time.Duration
	Aborted     string
}

// Notifier sends a run summary to the human's phone channel.
type Notifier interface {
	Notify(ctx context.Context, s Summary) error
}
```

In `friction.go` (core), add:

```go
const (
	InProgress = "friction:in-progress"
	Stuck      = "friction:stuck"
	// HitAgain starts the comment a later report of the same friction leaves on its issue.
	HitAgain = "Hit again from branch"
)
```

In `app/friction.go`, change the comment text to `friction.HitAgain+" `"+in.Branch+"`.\n\n"+in.Body`.

- [ ] **Step 4: Write the rules**

`internal/core/work/window.go`:

```go
// Package work holds the rules for an unattended run over friction issues.
package work

import (
	"fmt"
	"strings"
	"time"
)

// Window is the daily stretch of local time in which a run may start an issue. The zero Window is always open.
type Window struct {
	start, end int
	set        bool
}

// ParseWindow reads "HH:MM-HH:MM"; an end before the start wraps past midnight. "" is always open.
func ParseWindow(s string) (Window, error) {
	if s == "" {
		return Window{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	start, err1 := clock(a)
	end, err2 := clock(b)
	if !ok || err1 != nil || err2 != nil || start == end {
		return Window{}, fmt.Errorf("work_window %q must be HH:MM-HH:MM with different times", s)
	}
	return Window{start: start, end: end, set: true}, nil
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	return t.Hour()*60 + t.Minute(), err
}

func (w Window) Contains(t time.Time) bool {
	if !w.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return m >= w.start && m < w.end
	}
	return m >= w.start || m < w.end
}
```

`internal/core/work/claim.go`:

```go
package work

import (
	"fmt"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

const claimPrefix = "foundry: claimed by run "

func ClaimComment(runID string) string { return claimPrefix + runID }

type Claim struct {
	RunID string
	At    time.Time
}

// CurrentClaim returns the newest claim comment that bot posted on an issue.
func CurrentClaim(comments []ports.Comment, bot string) (Claim, bool) {
	var c Claim
	found := false
	for _, cm := range comments {
		first, _, _ := strings.Cut(cm.Body, "\n")
		id, ok := strings.CutPrefix(strings.TrimSpace(first), claimPrefix)
		if ok && id != "" && cm.Author == bot && (!found || !cm.CreatedAt.Before(c.At)) {
			c, found = Claim{RunID: id, At: cm.CreatedAt}, true
		}
	}
	return c, found
}

// Branch names the branch one run works an issue on.
func Branch(issue int, runID string) string { return fmt.Sprintf("friction/%d-%s", issue, runID) }

type Release int

const (
	Keep Release = iota
	Expired
	NoClaim
	Rejected
)

// Decide says what happens to an issue labelled in-progress. pr is the newest pull request on the current
// claim's branch, and found says whether there is one. A closed, unmerged pull request was turned down;
// a claim older than lease with no pull request has expired.
func Decide(c Claim, claimed bool, pr ports.PR, found bool, now time.Time, lease time.Duration) Release {
	switch {
	case !claimed:
		return NoClaim
	case found && pr.State == "CLOSED":
		return Rejected
	case found:
		return Keep
	case now.Sub(c.At) > lease:
		return Expired
	}
	return Keep
}
```

`internal/core/work/pick.go`:

```go
package work

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
)

// Hits is how often the friction was reported: the filing plus each later report's comment by a trusted author.
func Hits(is ports.Issue) int {
	n := 1
	for _, c := range is.Comments {
		if c.Trusted && strings.HasPrefix(c.Body, friction.HitAgain) {
			n++
		}
	}
	return n
}

// Pick returns up to max ready issues to work: not claimed, not stuck and without an open pull request
// for them, most hits first, then oldest first.
func Pick(ready []ports.Issue, open []ports.PR, max int) []ports.Issue {
	var out []ports.Issue
	for _, is := range ready {
		if slices.Contains(is.Labels, friction.InProgress) || slices.Contains(is.Labels, friction.Stuck) || hasPR(is.Number, open) {
			continue
		}
		out = append(out, is)
	}
	slices.SortStableFunc(out, func(a, b ports.Issue) int {
		if c := cmp.Compare(Hits(b), Hits(a)); c != 0 {
			return c
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return out[:min(max, len(out))]
}

func hasPR(n int, open []ports.PR) bool {
	closes := regexp.MustCompile(fmt.Sprintf(`(?i)\b(close[sd]?|fix(e[sd])?|resolve[sd]?) #%d\b`, n))
	prefix := fmt.Sprintf("friction/%d-", n)
	return slices.ContainsFunc(open, func(p ports.PR) bool { return strings.HasPrefix(p.Head, prefix) || closes.MatchString(p.Body) })
}
```

`internal/core/work/outcome.go`:

```go
package work

import (
	"strings"

	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/core/ship"
)

type Kind string

const (
	ReadyForHuman Kind = "ready-for-human"
	NeedsDecision Kind = "needs-decision"
	Stuck         Kind = "stuck"
)

type Reason string

const (
	ReasonTimeout            Reason = "timeout"
	ReasonMaxTurns           Reason = "max_turns"
	ReasonReviewNotConverged Reason = "review_not_converged"
	ReasonAgentStuck         Reason = "agent_stuck"
	ReasonShipStop           Reason = "ship_stop"
	ReasonCrash              Reason = "crash"
	ReasonNoShip             Reason = "no_ship"
)

// ShipRecord is the newest line ship wrote for the run's branch.
type ShipRecord struct{ Step, Outcome, PRURL, Detail string }

// Evidence is what a finished agent run left behind.
type Evidence struct {
	Result   ports.AgentResult
	RunErr   error
	TimedOut bool
	Ship     ShipRecord
	Shipped  bool
}

type Outcome struct {
	Kind   Kind
	Reason Reason
	PRURL  string
	Detail string
}

const (
	decisionPrefix = "NEEDS-DECISION:"
	stuckPrefix    = "STUCK:"
)

// Classify reads an issue's outcome from evidence: ship's hand-off record first, then the agent's own
// NEEDS-DECISION reply, and otherwise the first stuck reason that holds.
func Classify(e Evidence) Outcome {
	text := strings.TrimSpace(e.Result.Text)
	stuck := func(r Reason, detail string) Outcome { return Outcome{Kind: Stuck, Reason: r, Detail: detail} }
	switch {
	case e.Shipped && e.Ship.Outcome == string(ReadyForHuman):
		return Outcome{Kind: ReadyForHuman, PRURL: e.Ship.PRURL}
	case !e.TimedOut && e.RunErr == nil && strings.HasPrefix(text, decisionPrefix):
		return Outcome{Kind: NeedsDecision, Detail: strings.TrimSpace(strings.TrimPrefix(text, decisionPrefix))}
	case e.TimedOut:
		return stuck(ReasonTimeout, lastStep(e))
	case e.Result.Subtype == "error_max_turns":
		return stuck(ReasonMaxTurns, lastStep(e))
	case e.Shipped && e.Ship.Step == "review" && strings.HasPrefix(e.Ship.Detail, ship.NotConverged):
		return stuck(ReasonReviewNotConverged, e.Ship.Detail)
	case strings.HasPrefix(text, stuckPrefix):
		return stuck(ReasonAgentStuck, strings.TrimSpace(strings.TrimPrefix(text, stuckPrefix)))
	case e.RunErr != nil:
		return stuck(ReasonCrash, e.RunErr.Error())
	case e.Shipped:
		return stuck(ReasonShipStop, lastStep(e))
	}
	return stuck(ReasonNoShip, text)
}

func lastStep(e Evidence) string {
	if !e.Shipped {
		return "foundry ship never ran"
	}
	return "stop at " + e.Ship.Step + ": " + e.Ship.Detail
}
```

`internal/core/work/prompt.go`:

```go
package work

import (
	"fmt"
	"strings"

	"github.com/tunedev/foundry/internal/core/ports"
)

// Prompt is the task an unattended agent gets for one friction issue.
func Prompt(is ports.Issue, verify []string) string {
	cmds := make([]string, len(verify))
	for i, v := range verify {
		cmds[i] = "`" + v + "`"
	}
	return fmt.Sprintf(`Fix friction issue #%d in this repository. Nobody will answer questions during this run.

Title: %s

%s

Verify your change with: %s.

Ship with the foundry-ship skill: run `+"`foundry ship`"+` until it prints `+"`ready for human`"+`. Under the recap's Links heading write `+"`Closes #%d`"+`.

Two rules override the skill, because nobody is watching:
- If the fix is a product decision rather than an implementation, change nothing and reply with a final message starting `+"`%s`"+` followed by the options and your recommendation.
- Wherever the skill says to report to or ask the user, stop and reply with a final message starting `+"`%s`"+` followed by the reason.

Reach GitHub only through foundry. Never run gh, git push or vouch ack.
`, is.Number, is.Title, strings.TrimSpace(is.Body), strings.Join(cmds, ", "), is.Number, decisionPrefix, stuckPrefix)
}
```

- [ ] **Step 5: Run the tests**

Run: `go vet ./... && go test ./internal/... -v -run 'Window|Claim|Decide|Pick|Classify|Prompt|Core'`
Expected: PASS. `go vet ./...` fails until `github.GitHub` implements the three new `Forge` methods; add stubs now so the tree builds, and B5 replaces them:

```go
func (g GitHub) BranchPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) { return ports.PR{}, false, nil }
func (g GitHub) OpenPRs(ctx context.Context, dir string) ([]ports.PR, error)              { return nil, nil }
func (g GitHub) EditLabels(ctx context.Context, dir string, number int, add, remove []string) error {
	return nil
}
func (g GitHub) IssueComments(ctx context.Context, dir string, number int) ([]ports.Comment, error) { return nil, nil }
```

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal
git commit -m "Add the work rules: window, claims, release, pick, outcome and prompt"
```

### Task B4: Work configuration

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: `config.Config.ints()` from B2; `work.Window`, `work.ParseWindow` from B3.
- Produces: fields `WorkWindow string`, `ClaimLease Duration`, `IssueTimeout Duration`, `MaxIssuesPerRun int`, `MaxTurns int`, `GitHubTokenFile`, `ClaudeTokenFile`, `DiscordWebhookFile`, `BotName`, `BotEmail string`, `AgentPassEnv []string`; `(Config) Window() (work.Window, error)`.

- [ ] **Step 1: Write the failing test**

```go
func TestLoadWorkDefaultsAndSecretPaths(t *testing.T) {
	home := t.TempDir()
	c, err := Load(home, env(map[string]string{"FOUNDRY_MAX_ISSUES_PER_RUN": "1", "FOUNDRY_CLAIM_LEASE": "30m"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxIssuesPerRun != 1 || c.ClaimLease.Duration != 30*time.Minute || c.IssueTimeout.Duration != 45*time.Minute ||
		c.MaxTurns != 150 || c.GitHubTokenFile != filepath.Join(home, "secrets", "github-token") ||
		c.DiscordWebhookFile != filepath.Join(home, "secrets", "discord-webhook") {
		t.Fatalf("%+v", c)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("work_window = \"25:00-07:00\"\n"), 0o644)
	if _, err := Load(home, env(nil)); err == nil || !strings.Contains(err.Error(), "work_window") {
		t.Errorf("bad window: %v", err)
	}
}
```

Run: `go test ./internal/config -run TestLoadWorkDefaultsAndSecretPaths -v`
Expected: FAIL to compile (`c.MaxIssuesPerRun undefined`).

- [ ] **Step 2: Add the keys**

Add to `Config`:

```go
	WorkWindow         string   `toml:"work_window"`
	ClaimLease         Duration `toml:"claim_lease"`
	IssueTimeout       Duration `toml:"issue_timeout"`
	MaxIssuesPerRun    int      `toml:"max_issues_per_run"`
	MaxTurns           int      `toml:"max_turns"`
	GitHubTokenFile    string   `toml:"github_token_file"`
	ClaudeTokenFile    string   `toml:"claude_token_file"`
	DiscordWebhookFile string   `toml:"discord_webhook_file"`
	BotName            string   `toml:"bot_name"`
	BotEmail           string   `toml:"bot_email"`
	AgentPassEnv       []string `toml:"agent_pass_env"`
```

Defaults: `ClaimLease: Duration{2 * time.Hour}`, `IssueTimeout: Duration{45 * time.Minute}`, `MaxIssuesPerRun: 3`, `MaxTurns: 150`. Add `"claim_lease": &c.ClaimLease, "issue_timeout": &c.IssueTimeout` to `durations()` and `"max_issues_per_run": &c.MaxIssuesPerRun, "max_turns": &c.MaxTurns` to `ints()`.

In `Load`, before `return c, c.validate()`:

```go
	for p, name := range map[*string]string{&c.GitHubTokenFile: "github-token", &c.ClaudeTokenFile: "claude-oauth-token", &c.DiscordWebhookFile: "discord-webhook"} {
		if *p == "" {
			*p = filepath.Join(home, "secrets", name)
		}
	}
```

In `validate`:

```go
	if _, err := c.Window(); err != nil {
		errs = append(errs, err)
	}
```

And:

```go
// Window is the daily stretch of local time in which work starts issues.
func (c Config) Window() (work.Window, error) { return work.ParseWindow(c.WorkWindow) }
```

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/config -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/config
git commit -m "Add the work configuration keys and secret file paths"
```

### Task B5: GitHub adapter and fake gh for work

**Files:**
- Modify: `internal/run/run.go` (`CmdEnv`)
- Modify: `internal/adapters/outbound/github/github.go`
- Modify: `cmd/foundry/testdata/fakegh/main.go`
- Test: the existing suite; the B8 work tests are this task's behavioural check

**Interfaces:**
- Consumes: the `ports.Forge` additions from B3.
- Produces: `run.CmdEnv(ctx, timeout, dir string, extra []string, stdin io.Reader, name string, args ...string) ([]byte, error)`; `github.GitHub.Token string`; real `BranchPR`, `OpenPRs`, `EditLabels`, `IssueComments`; `OpenIssues` fills `Body`, `Labels`, `CreatedAt`, `Comments` (with `Trusted` from `authorAssociation`). Fake gh state gains `issues[].created_at`, `comments[].author`, `comments[].association` (default `COLLABORATOR`), `comments[].created_at`, `prs[].state`, `user` (default `foundry-bot`), and handles `issue edit`, `issue view --json comments`, and `pr list` without `--head` or with `--state all`.

- [ ] **Step 1: Pass the bot token explicitly**

B5 has no test of its own: the B8 work tests drive every new command through the built binary and fake gh, and the existing suite pins `FindPR`.

In `internal/run/run.go`, give `Cmd` a sibling that adds variables to the child's environment, and make `Cmd` call it:

```go
// Cmd runs name in dir and returns stdout. (keep the existing doc comment)
func Cmd(ctx context.Context, timeout time.Duration, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return CmdEnv(ctx, timeout, dir, nil, stdin, name, args...)
}

// CmdEnv is Cmd with extra appended to the child's environment.
func CmdEnv(ctx context.Context, timeout time.Duration, dir string, extra []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
```

The body of `CmdEnv` is the old body of `Cmd`, with `c.Env = append(Env(), extra...)`.

In `github.go`, add a `Token string` field to `GitHub`; `gh` sends it when set:

```go
// GitHub runs gh with Token as GH_TOKEN when it is set, and with gh's own login otherwise.
type GitHub struct {
	Timeout time.Duration
	Token   string
}

func (g GitHub) gh(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	var env []string
	if g.Token != "" {
		env = []string{"GH_TOKEN=" + g.Token}
	}
	out, err := run.CmdEnv(ctx, g.Timeout, dir, env, in, "gh", args...)
	return out, run.Classify(err)
}
```

- [ ] **Step 2: Implement the adapter**

In `github.go`, replace `prJSON` and `FindPR`, and add the new methods:

```go
const prFields = "number,url,body,headRefOid,headRefName,state"

type prJSON struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	Body        string `json:"body"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRefName string `json:"headRefName"`
	State       string `json:"state"`
}

func (g GitHub) listPRs(ctx context.Context, dir string, args ...string) ([]ports.PR, error) {
	out, err := g.gh(ctx, dir, "", append([]string{"pr", "list", "--json", prFields}, args...)...)
	if err != nil {
		return nil, err
	}
	var raw []prJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	prs := make([]ports.PR, len(raw))
	for i, p := range raw {
		prs[i] = ports.PR{Number: p.Number, URL: p.URL, Body: p.Body, HeadSHA: p.HeadRefOid, Head: p.HeadRefName, State: p.State}
	}
	return prs, nil
}

func first(prs []ports.PR, err error) (ports.PR, bool, error) {
	if err != nil || len(prs) == 0 {
		return ports.PR{}, false, err
	}
	return prs[0], true, nil
}

func (g GitHub) FindPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) {
	return first(g.listPRs(ctx, dir, "--head", branch, "--state", "open", "--limit", "1"))
}

func (g GitHub) BranchPR(ctx context.Context, dir, branch string) (ports.PR, bool, error) {
	return first(g.listPRs(ctx, dir, "--head", branch, "--state", "all", "--limit", "1"))
}

func (g GitHub) OpenPRs(ctx context.Context, dir string) ([]ports.PR, error) {
	return g.listPRs(ctx, dir, "--state", "open", "--limit", "200")
}

func (g GitHub) EditLabels(ctx context.Context, dir string, number int, add, remove []string) error {
	args := []string{"issue", "edit", strconv.Itoa(number)}
	for _, l := range add {
		args = append(args, "--add-label", l)
	}
	for _, l := range remove {
		args = append(args, "--remove-label", l)
	}
	_, err := g.gh(ctx, dir, "", args...)
	return err
}

type commentJSON struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"`
	Body              string    `json:"body"`
	CreatedAt         time.Time `json:"createdAt"`
}

func (c commentJSON) comment() ports.Comment {
	trusted := c.AuthorAssociation == "OWNER" || c.AuthorAssociation == "MEMBER" || c.AuthorAssociation == "COLLABORATOR"
	return ports.Comment{Author: c.Author.Login, Body: c.Body, Trusted: trusted, CreatedAt: c.CreatedAt}
}

func (g GitHub) IssueComments(ctx context.Context, dir string, number int) ([]ports.Comment, error) {
	out, err := g.gh(ctx, dir, "", "issue", "view", strconv.Itoa(number), "--json", "comments")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Comments []commentJSON `json:"comments"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh issue view: %w", err)
	}
	cs := make([]ports.Comment, len(raw.Comments))
	for i, c := range raw.Comments {
		cs[i] = c.comment()
	}
	return cs, nil
}

type issueJSON struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Comments []commentJSON `json:"comments"`
}

func (g GitHub) OpenIssues(ctx context.Context, dir, label string) ([]ports.Issue, error) {
	out, err := g.gh(ctx, dir, "", "issue", "list", "--label", label, "--state", "open",
		"--json", "number,title,url,body,createdAt,labels,comments", "--limit", "500")
	if err != nil {
		return nil, err
	}
	var raw []issueJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	issues := make([]ports.Issue, len(raw))
	for i, r := range raw {
		is := ports.Issue{Number: r.Number, Title: r.Title, URL: r.URL, Body: r.Body, CreatedAt: r.CreatedAt}
		for _, l := range r.Labels {
			is.Labels = append(is.Labels, l.Name)
		}
		for _, c := range r.Comments {
			is.Comments = append(is.Comments, c.comment())
		}
		issues[i] = is
	}
	return issues, nil
}
```

Delete the B3 stubs.

- [ ] **Step 3: Extend the fake**

In `fakegh/main.go`: add `State string \`json:"state"\`` to `pr`; `CreatedAt string \`json:"created_at"\`` to `issue`; `Author string \`json:"author"\``, `Association string \`json:"association"\`` and `CreatedAt string \`json:"created_at"\`` to `comment`; `User string \`json:"user"\`` to `state`. Add imports `cmp` and `time`. Replace the `pr list`, `issue list` and `issue comment` cases and add `issue edit`:

```go
	case "pr list":
		head, st := flagVal(rest, "--head"), flagVal(rest, "--state")
		out := []map[string]any{}
		for i := len(s.PRs) - 1; i >= 0; i-- {
			p := s.PRs[i]
			state := cmp.Or(p.State, "OPEN")
			if head != "" && p.Head != head || st == "open" && state != "OPEN" {
				continue
			}
			out = append(out, map[string]any{"number": p.Number, "url": p.URL, "body": p.Body,
				"headRefOid": remoteHead(p.Head), "headRefName": p.Head, "state": state})
		}
		printJSON(out)
	case "issue list":
		label := flagVal(rest, "--label")
		out := []map[string]any{}
		for _, is := range s.Issues {
			if !slices.Contains(is.Labels, label) {
				continue
			}
			labels, comments := []map[string]any{}, []map[string]any{}
			for _, l := range is.Labels {
				labels = append(labels, map[string]any{"name": l})
			}
			for _, c := range s.Comments {
				if c.Issue == is.Number {
					comments = append(comments, commentOut(c))
				}
			}
			out = append(out, map[string]any{"number": is.Number, "title": is.Title, "url": is.URL, "body": is.Body,
				"createdAt": cmp.Or(is.CreatedAt, "2026-01-01T00:00:00Z"), "labels": labels, "comments": comments})
		}
		printJSON(out)
	case "issue view":
		n, _ := strconv.Atoi(rest[0])
		comments := []map[string]any{}
		for _, c := range s.Comments {
			if c.Issue == n {
				comments = append(comments, commentOut(c))
			}
		}
		printJSON(map[string]any{"comments": comments})
	case "issue edit":
		n, _ := strconv.Atoi(rest[0])
		for i := range s.Issues {
			if s.Issues[i].Number != n {
				continue
			}
			for _, l := range flagVals(rest, "--add-label") {
				if !slices.Contains(s.Issues[i].Labels, l) {
					s.Issues[i].Labels = append(s.Issues[i].Labels, l)
				}
			}
			s.Issues[i].Labels = slices.DeleteFunc(s.Issues[i].Labels, func(l string) bool { return slices.Contains(flagVals(rest, "--remove-label"), l) })
		}
		s.Writes++
	case "issue comment":
		n, _ := strconv.Atoi(rest[0])
		s.Comments = append(s.Comments, comment{Issue: n, Body: stdin(), Author: cmp.Or(s.User, "foundry-bot"),
			CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		s.Writes++
```

Also set `CreatedAt: time.Now().UTC().Format(time.RFC3339)` in the `issue create` case, and add:

```go
func commentOut(c comment) map[string]any {
	return map[string]any{"author": map[string]any{"login": c.Author}, "authorAssociation": cmp.Or(c.Association, "COLLABORATOR"),
		"body": c.Body, "createdAt": c.CreatedAt}
}
``` Update the package comment to list `issue edit` and the `user` field.

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test ./...`
Expected: PASS, the existing ship and friction tests included. `FindPR` now passes `--json` before `--head`; the fake reads flags in any order.

- [ ] **Step 5: Commit**

```bash
git add internal/run internal/adapters/outbound/github cmd/foundry
git commit -m "Read labels, comments and pull request states from GitHub, and edit labels"
```

### Task B6: The Claude agent runner

**Files:**
- Create: `skills/skills.go`
- Modify: `internal/run/run.go` (`Group`)
- Create: `internal/adapters/outbound/claude/claude.go`, `home.go`
- Test: `internal/adapters/outbound/claude/claude_test.go`

**Interfaces:**
- Consumes: `ports.AgentRunner`, `ports.AgentJob`, `ports.AgentResult` (B3); `telemetry.Env` (B1); `run.Cmd`, `run.Classify`.
- Produces: `run.Group(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error)`; `skills.FoundryShip []byte`; `claude.Runner{Bin, WorktreeRoot string; GitTimeout time.Duration; Env []string}` implementing `ports.AgentRunner`; `claude.Home{BotName, BotEmail, HooksPath string; Verify []string}`; `claude.PrepareHome(dir string, h Home) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/adapters/outbound/claude/claude_test.go`:

```go
package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// repo makes an origin with one commit on main and a clone of it, and returns the clone.
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	seed, origin, clone := filepath.Join(root, "seed"), filepath.Join(root, "origin.git"), filepath.Join(root, "clone")
	run(root, "init", "-q", "-b", "main", seed)
	os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644)
	run(seed, "add", ".")
	run(seed, "commit", "-qm", "seed")
	run(root, "clone", "-q", "--bare", seed, origin)
	run(root, "clone", "-q", origin, clone)
	return clone
}

// fake writes a stand-in claude script and returns its path. The script records its environment and
// working directory under out, then runs body.
func fake(t *testing.T, out, body string) string {
	p := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nenv > " + out + "/env\npwd > " + out + "/pwd\ngit branch --show-current > " + out + "/branch\ncat > " + out + "/prompt\n" + body
	os.WriteFile(p, []byte(script), 0o755)
	return p
}

const success = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1","num_turns":4,"total_cost_usd":0.5,"modelUsage":{"m1":{}}}`

func runner(t *testing.T, bin string) Runner {
	return Runner{Bin: bin, WorktreeRoot: filepath.Join(t.TempDir(), "wt"), GitTimeout: time.Minute,
		Env: []string{"HOME=/agent", "PATH=" + os.Getenv("PATH"), "GH_TOKEN=x"}}
}

func job(dir string) ports.AgentJob {
	return ports.AgentJob{RepoDir: dir, Base: "main", Branch: "friction/1-r1", Prompt: "fix #1", MaxTurns: 9}
}

func TestRunGivesTheAgentAFreshWorktreeAndOnlyItsEnv(t *testing.T) {
	dir, out := repo(t), t.TempDir()
	r := runner(t, fake(t, out, "echo '"+success+"'\n"))
	res, err := r.Run(t.Context(), job(dir))
	if err != nil || res.Text != "done" || res.SessionID != "s1" || res.Turns != 4 || res.CostUSD != 0.5 || res.Model != "m1" {
		t.Fatalf("%+v %v", res, err)
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(out, f)); return strings.TrimSpace(string(b)) }
	if read("branch") != "friction/1-r1" || read("prompt") != "fix #1" || !strings.HasPrefix(read("pwd"), r.WorktreeRoot) {
		t.Fatalf("branch %q prompt %q pwd %q", read("branch"), read("prompt"), read("pwd"))
	}
	var names []string
	for _, kv := range strings.Split(read("env"), "\n") {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains([]string{"PWD", "SHLVL", "_", "OLDPWD"}, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"GH_TOKEN", "HOME", "PATH"}) {
		t.Fatalf("agent env names %v", names)
	}
	if entries, _ := os.ReadDir(r.WorktreeRoot); len(entries) != 0 {
		t.Fatalf("worktree left behind: %v", entries)
	}
}

func TestRunReadsMaxTurnsFromAFailingExit(t *testing.T) {
	dir, out := repo(t), t.TempDir()
	r := runner(t, fake(t, out, `echo '{"type":"result","subtype":"error_max_turns","is_error":true,"result":null,"num_turns":10}'`+"\nexit 1\n"))
	res, err := r.Run(t.Context(), job(dir))
	if err != nil || res.Subtype != "error_max_turns" || res.Turns != 10 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunReportsACrashWithStderr(t *testing.T) {
	dir, out := repo(t), t.TempDir()
	r := runner(t, fake(t, out, "echo 'panic: boom' >&2\nexit 3\n"))
	if _, err := r.Run(t.Context(), job(dir)); err == nil || !strings.Contains(err.Error(), "panic: boom") {
		t.Fatalf("err %v", err)
	}
}

func TestRunKillsTheProcessGroupAtTimeout(t *testing.T) {
	dir, out := repo(t), t.TempDir()
	r := runner(t, fake(t, out, "sleep 300 &\necho $! > "+out+"/child\nsleep 300\n"))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := r.Run(ctx, job(dir)); err == nil {
		t.Fatal("hung agent returned no error")
	}
	b, _ := os.ReadFile(filepath.Join(out, "child"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child %d still running", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if entries, _ := os.ReadDir(r.WorktreeRoot); len(entries) != 0 {
		t.Fatalf("worktree left behind: %v", entries)
	}
}

func TestPrepareHomeWritesSkillSettingsAndGitConfig(t *testing.T) {
	home := t.TempDir()
	if err := PrepareHome(home, Home{BotName: "bot", BotEmail: "b@x", HooksPath: "/v/githooks", Verify: []string{"go test -race ./..."}}); err != nil {
		t.Fatal(err)
	}
	git, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if !strings.Contains(string(git), "hooksPath = /v/githooks") || !strings.Contains(string(git), "name = bot") {
		t.Fatalf("gitconfig:\n%s", git)
	}
	var s struct {
		Permissions struct{ Allow, Deny []string } `json:"permissions"`
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err := json.Unmarshal(b, &s); err != nil || !slices.Contains(s.Permissions.Deny, "Bash(gh:*)") ||
		!slices.Contains(s.Permissions.Allow, "Bash(go test -race ./...)") {
		t.Fatalf("settings %s %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "foundry-ship", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
```

Add `"context"` to the test imports.

Run: `go test ./internal/adapters/outbound/claude -v`
Expected: FAIL to compile (`undefined: Runner`).

- [ ] **Step 2: Embed the skill**

Create `skills/skills.go`:

```go
// Package skills embeds the agent skills foundry installs for its agents.
package skills

import _ "embed"

//go:embed foundry-ship/SKILL.md
var FoundryShip []byte
```

- [ ] **Step 3: Process-group runs in internal/run**

Adapters run commands through `internal/run`, so the group kill lives there. In `run.go`, move the part of `CmdEnv` from `c.WaitDelay = 5 * time.Second` to the end into `output`, and add `Group`:

```go
// CmdEnv is Cmd with extra appended to the child's environment.
func CmdEnv(ctx context.Context, timeout time.Duration, dir string, extra []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env, c.Stdin = dir, append(Env(), extra...), stdin
	return output(ctx, c, " after "+timeout.String())
}

// Group runs name in dir with exactly env and no deadline of its own, in a new process group: ending ctx
// kills every process the command started. Output, errors and logging are as for Cmd.
func Group(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env, c.Stdin = dir, env, stdin
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	return output(ctx, c, "")
}

// output runs c and returns stdout. A deadline that ends ctx is ErrTimeout; a non-zero exit is *Error.
// After ctx ends it waits at most 5s more for children still holding the output pipes.
func output(ctx context.Context, c *exec.Cmd, after string) ([]byte, error) {
	c.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	c.Stderr = &stderr
	start := time.Now()
	out, err := c.Output()
	exit := -1
	if c.ProcessState != nil {
		exit = c.ProcessState.ExitCode()
	}
	line := strings.Join(c.Args, " ")
	attrs := []any{"argv", c.Args, "dir", c.Dir, "duration_ms", time.Since(start).Milliseconds(), "exit", exit}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.WarnContext(ctx, "exec timed out", append(attrs, "stderr", tail(stderr.String()))...)
		return out, fmt.Errorf("%s: %w%s", line, ErrTimeout, after)
	}
	if err != nil {
		slog.WarnContext(ctx, "exec failed", append(attrs, "stderr", tail(stderr.String()), "stdout", tail(string(out)))...)
		e := &Error{Stderr: strings.TrimSpace(stderr.String()), Err: err}
		e.Msg = fmt.Sprintf("%s: %v: %s", line, err, e.Stderr)
		if s := tail(string(out)); s != "" {
			e.Msg += "\nstdout: " + s
		}
		return out, e
	}
	slog.DebugContext(ctx, "exec", attrs...)
	return out, nil
}
```

Add `syscall` to the imports. `TestClassifyIgnoresStdout` and the e2e suite pin that `Cmd`'s messages are unchanged.

- [ ] **Step 4: Write the runner**

Create `internal/adapters/outbound/claude/claude.go`:

```go
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
```

- [ ] **Step 5: Write the agent home**

Create `internal/adapters/outbound/claude/home.go`:

```go
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tunedev/foundry/skills"
)

// Home is what an agent's HOME holds: the bot's git identity, vouch's hooks, Claude Code's permission
// allowlist and the foundry-ship skill.
type Home struct {
	BotName, BotEmail, HooksPath string
	Verify                       []string
}

// PrepareHome writes the agent's .gitconfig, .claude/settings.json and foundry-ship skill under dir,
// replacing what is there.
func PrepareHome(dir string, h Home) error {
	git := fmt.Sprintf(`[user]
	name = %s
	email = %s
[core]
	hooksPath = %s
[credential "https://github.com"]
	helper =
	helper = !gh auth git-credential
[url "https://github.com/"]
	insteadOf = git@github.com:
`, h.BotName, h.BotEmail, h.HooksPath)
	settings, err := json.MarshalIndent(map[string]any{"permissions": map[string]any{
		"allow": append([]string{"Read", "Glob", "Grep", "Edit(./**)", "Write(./**)", "Bash(go:*)", "Bash(git:*)",
			"Bash(foundry:*)", "Bash(vouch:*)"}, bashRules(h.Verify)...),
		"deny": []string{"Bash(gh:*)", "Bash(git push:*)", "Bash(vouch ack:*)", "Bash(vouch rules:*)", "WebFetch", "WebSearch"},
	}}, "", "  ")
	if err != nil {
		return err
	}
	for path, content := range map[string][]byte{
		filepath.Join(dir, ".gitconfig"):                                 []byte(git),
		filepath.Join(dir, ".claude", "settings.json"):                    settings,
		filepath.Join(dir, ".claude", "skills", "foundry-ship", "SKILL.md"): skills.FoundryShip,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func bashRules(cmds []string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = "Bash(" + c + ")"
	}
	return out
}
```

- [ ] **Step 6: Run the tests**

Run: `go vet ./... && go test ./...`
Expected: PASS, the five claude tests included.

- [ ] **Step 7: Commit**

```bash
git add skills internal/run internal/adapters/outbound/claude
git commit -m "Run Claude Code headless in a fresh worktree as the agent runner"
```

### Task B7: Discord notifier

**Files:**
- Create: `internal/adapters/outbound/discord/discord.go`
- Test: `internal/adapters/outbound/discord/discord_test.go`

**Interfaces:**
- Consumes: `ports.Notifier`, `ports.Summary`, `ports.ErrTransient` (B3).
- Produces: `discord.Webhook{URL string; Timeout time.Duration}` implementing `ports.Notifier`; `discord.Render(s ports.Summary) string`.

- [ ] **Step 1: Write the failing tests**

```go
package discord

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

var summary = ports.Summary{RunID: "20261016-020000", Repo: "acme/widget", CostUSD: 1.25, Wall: 42 * time.Minute,
	Issues: []ports.IssueReport{
		{Number: 1, Title: "@everyone slow tests", Outcome: "ready-for-human", PRURL: "https://github.com/acme/widget/pull/9"},
		{Number: 2, Title: "Flaky e2e", Outcome: "stuck", Reason: "timeout"},
		{Number: 3, Title: "Rename flag", Outcome: "needs-decision"},
	}}

func TestNotifyPostsTheSummaryWithoutMentions(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := (Webhook{URL: srv.URL + "/api/webhooks/1/tok", Timeout: time.Second}).Notify(t.Context(), summary); err != nil {
		t.Fatal(err)
	}
	content, _ := got["content"].(string)
	for _, want := range []string{"acme/widget", "pull/9", "#2 Flaky e2e: timeout", "#3 Rename flag", "$1.25"} {
		if !strings.Contains(content, want) {
			t.Errorf("content lacks %q:\n%s", want, content)
		}
	}
	if am, _ := got["allowed_mentions"].(map[string]any); am == nil || len(am["parse"].([]any)) != 0 {
		t.Errorf("allowed_mentions %v", got["allowed_mentions"])
	}
}

func TestNotifyMarksServerErrorsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	err := (Webhook{URL: srv.URL + "/api/webhooks/1/tok", Timeout: time.Second}).Notify(t.Context(), summary)
	if !errors.Is(err, ports.ErrTransient) {
		t.Fatalf("502: %v", err)
	}
}

func TestNotifyNeverQuotesTheWebhookURL(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:1/api/webhooks/1/s3cret", "http://[::1]:1/api/webhooks/1/s3cret"} {
		err := (Webhook{URL: url, Timeout: time.Second}).Notify(t.Context(), summary)
		if err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("error %v", err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer srv.Close()
	if err := (Webhook{URL: srv.URL + "/api/webhooks/1/s3cret", Timeout: time.Second}).Notify(t.Context(), summary); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("404 error %v", err)
	}
}
```

Run: `go test ./internal/adapters/outbound/discord -v`
Expected: FAIL to compile (`undefined: Webhook`).

- [ ] **Step 2: Implement**

```go
// Package discord sends run summaries to a Discord channel webhook.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tunedev/foundry/internal/core/ports"
)

// Webhook posts to URL, which is a secret: no error it returns quotes it.
type Webhook struct {
	URL     string
	Timeout time.Duration
}

// Notify posts the rendered summary with mentions disabled. A network failure, 429 or 5xx is ErrTransient.
func (w Webhook) Notify(ctx context.Context, s ports.Summary) error {
	body, err := json.Marshal(map[string]any{"content": Render(s), "allowed_mentions": map[string]any{"parse": []string{}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("discord webhook: invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: w.Timeout}).Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("%w: discord webhook: %v", ports.ErrTransient, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%w: discord webhook: HTTP %d", ports.ErrTransient, resp.StatusCode)
	}
	return fmt.Errorf("discord webhook: HTTP %d", resp.StatusCode)
}

// Render writes the summary as a Discord message of at most 2000 characters.
func Render(s ports.Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "foundry run %s on %s: %d issues, $%.2f, %s\n", s.RunID, s.Repo, len(s.Issues), s.CostUSD, s.Wall.Round(time.Second))
	section := func(title, outcome string, line func(ports.IssueReport) string) {
		var lines []string
		for _, is := range s.Issues {
			if is.Outcome == outcome {
				lines = append(lines, "- "+line(is))
			}
		}
		if len(lines) > 0 {
			b.WriteString(title + ":\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	section("Ready for review", "ready-for-human", func(is ports.IssueReport) string { return fmt.Sprintf("#%d %s: %s", is.Number, is.Title, is.PRURL) })
	section("Stuck", "stuck", func(is ports.IssueReport) string { return fmt.Sprintf("#%d %s: %s", is.Number, is.Title, is.Reason) })
	section("Needs a decision", "needs-decision", func(is ports.IssueReport) string { return fmt.Sprintf("#%d %s", is.Number, is.Title) })
	if s.Aborted != "" {
		b.WriteString("Ended early: " + s.Aborted + "\n")
	}
	out := []rune(b.String())
	return string(out[:min(len(out), 2000)])
}
```

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/adapters/outbound/discord -v`
Expected: PASS. If `TestNotifyNeverQuotesTheWebhookURL` fails because an inner error still names the path, find which error type carries it and strip it at the same place; do not drop the error.

- [ ] **Step 4: Commit**

```bash
git add internal/adapters/outbound/discord
git commit -m "Send run summaries to a Discord webhook"
```

### Task B8: The work use case and `foundry work`

**Files:**
- Create: `internal/core/app/work.go`
- Create: `cmd/foundry/work.go` (`cmdWork`, lock, secrets, agent env)
- Modify: `cmd/foundry/main.go` (usage, `command` case), `cmd/foundry/wire.go` (`workDeps`)
- Create: `cmd/foundry/testdata/fakeclaude/main.go`
- Test: `cmd/foundry/work_test.go`; `cmd/foundry/e2e_test.go` (`TestMain` builds fakeclaude and puts foundry in fakebin; `newWorld` split)

**Interfaces:**
- Consumes: everything in B2 to B7.
- Produces: `app.WorkDeps{Forge ports.Forge; Agent ports.AgentRunner; Notifier ports.Notifier; LastShip func(repo, branch string) (work.ShipRecord, bool, error); Now func() time.Time}`; `app.WorkInput{Dir, Repo, Base, RunID, Bot string; Verify, UserDataPaths []string; Window work.Window; ClaimLease, IssueTimeout, RetryPause time.Duration; MaxIssues, MaxTurns int}`; `app.Work(ctx, WorkDeps, WorkInput) (ports.Summary, error)`; e2e helpers `newWorkWorld(t) *world`, `(*world).work() (string, string, int)`, `(*world).scenario(issue int, name string)`, `(*world).discordBodies() []string`.

- [ ] **Step 1: Build the fake claude**

Create `cmd/foundry/testdata/fakeclaude/main.go`:

```go
// Command fakeclaude stands in for claude in foundry's end-to-end tests. It reads the prompt on stdin, takes
// the issue number from "Closes #<n>", records its environment and prompt in $FAKE_LOGS/claude-<n>.env and
// .prompt, and acts out the scenario in $FAKE_LOGS/claude-<n>.scenario (default ship): ship, needs-decision,
// stuck, hang, crash or review-loop. It runs foundry with its own environment, so TRACEPARENT passes through.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var logs = os.Getenv("FAKE_LOGS")

func main() {
	prompt, _ := io.ReadAll(os.Stdin)
	m := regexp.MustCompile(`Closes #(\d+)`).FindSubmatch(prompt)
	if m == nil {
		fmt.Fprintln(os.Stderr, "fake claude: no issue in prompt")
		os.Exit(2)
	}
	n := string(m[1])
	write("claude-"+n+".env", strings.Join(os.Environ(), "\n"))
	write("claude-"+n+".prompt", string(prompt))
	scenario, _ := os.ReadFile(filepath.Join(logs, "claude-"+n+".scenario"))
	switch strings.TrimSpace(string(scenario)) {
	case "", "ship":
		commit("fix #" + n)
		foundry("friction", "none", "--reason", "smooth run")
		result(lastLine(foundry("ship", "--recap", recap(n))))
	case "needs-decision":
		result("NEEDS-DECISION: rename the flag or keep it; I recommend keeping it.")
	case "stuck":
		result("STUCK: vouch failed to run")
	case "hang":
		child := exec.Command("sleep", "300")
		child.Start()
		write("claude-"+n+".child", fmt.Sprint(child.Process.Pid))
		time.Sleep(time.Hour)
	case "crash":
		fmt.Fprintln(os.Stderr, "panic: fake crash")
		os.Exit(3)
	case "review-loop":
		write("vouch-gate-fail", "minor calc.go:1 unbounded retry\n")
		for i := range 5 {
			commit(fmt.Sprint("attempt ", i))
			if strings.Contains(foundry("ship", "--recap", recap(n)), "did not converge") {
				break
			}
		}
		result("STUCK: review did not converge")
	}
}

func write(name, content string) { os.WriteFile(filepath.Join(logs, name), []byte(content), 0o644) }

func commit(msg string) {
	f, _ := os.OpenFile("calc.go", os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintf(f, "// %s\n", msg)
	f.Close()
	if out, err := exec.Command("git", "commit", "-qam", msg).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "fake claude: git commit: %v\n%s", err, out)
		os.Exit(4)
	}
}

func foundry(args ...string) string {
	out, _ := exec.Command("foundry", args...).Output()
	return string(out)
}

func recap(n string) string {
	p := filepath.Join(os.TempDir(), "fake-recap-"+n+".md")
	os.WriteFile(p, []byte("## What and why\nfix\n## Touched\ncalc\n## Risk\nlow: one line\n## Verified\ntrue\n## Links\nCloses #"+n+"\n"), 0o644)
	return p
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func result(text string) {
	b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text,
		"session_id": "fake-session", "num_turns": 3, "total_cost_usd": 0.25, "modelUsage": map[string]any{"fake-model": map[string]any{}}})
	fmt.Println(string(b))
}
```

In `e2e_test.go` `TestMain`, build into `fakebin` too and point `bin` at the copy in `fakebin`, so the fake agent finds `foundry` on its PATH:

```go
	bin, fakebin = filepath.Join(dir, "fakebin", "foundry"), filepath.Join(dir, "fakebin")
	for out, pkg := range map[string]string{bin: ".", filepath.Join(fakebin, "gh"): "./testdata/fakegh",
		filepath.Join(fakebin, "claude"): "./testdata/fakeclaude"} {
```

- [ ] **Step 2: Split the world and add work helpers**

In `e2e_test.go`, move everything in `newWorld` up to and including the clone into `seedWorld(t *testing.T) *world`, leaving `newWorld` as:

```go
func newWorld(t *testing.T, branch string) *world {
	t.Helper()
	w := seedWorld(t)
	w.git(w.checkout, "checkout", "-qb", branch)
	w.commit("feature work")
	w.write(w.root, "recap.md", goodRecap)
	return w
}
```

In `seedWorld`, write `checks = ["true"]\nverify = ["true"]\n` as the seed `foundry.toml`.

Create `cmd/foundry/work_test.go` with the helpers:

```go
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
```

Add fields to `world`: `discord []string; discordMu *sync.Mutex`. Extend `ghState` with `Issues[].Labels` (already), `Comments []struct{ Issue int; Body, Author string }`, `PRs[].State string`. Add `"strconv"` to imports.

- [ ] **Step 3: Write the failing work tests**

Append to `work_test.go`:

```go
func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

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
	if d := w.discordBodies(); len(d) != 1 || !strings.Contains(d[0], "pull/1") {
		t.Fatalf("discord %q", d)
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
	if pid == 0 || syscall.Kill(pid, 0) == nil {
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

func TestWorkFailsAtStartupWithoutASecretFile(t *testing.T) {
	w := newWorkWorld(t)
	os.Remove(filepath.Join(w.home, "secrets", "github-token"))
	_, errOut, code := w.work()
	if code != 2 || !strings.Contains(errOut, "github-token") || strings.Contains(errOut, "claude-token") {
		t.Fatalf("code %d\n%s", code, errOut)
	}
}
```

Add `"fmt"` and `"strconv"` to the imports.

Run: `go test ./cmd/foundry -run TestWork -v`
Expected: FAIL; each test exits 2 with `usage: foundry <command>` because `work` is not a command.

- [ ] **Step 4: Write the use case**

Create `internal/core/app/work.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/tunedev/foundry/internal/core/friction"
	"github.com/tunedev/foundry/internal/core/ports"
	"github.com/tunedev/foundry/internal/core/work"
)

type WorkDeps struct {
	Forge    ports.Forge
	Agent    ports.AgentRunner
	Notifier ports.Notifier
	LastShip func(repo, branch string) (work.ShipRecord, bool, error)
	Now      func() time.Time
}

type WorkInput struct {
	Dir, Repo, Base, RunID, Bot          string
	Verify, UserDataPaths                []string
	Window                               work.Window
	ClaimLease, IssueTimeout, RetryPause time.Duration
	MaxIssues, MaxTurns                  int
}

// Work releases stale claims, picks ready friction issues and runs the agent on each in turn, then sends a
// summary when it picked any or ended early. Outside the window it does nothing. A forge failure while
// releasing, picking or claiming ends the run; one issue's failure never stops the next.
func Work(ctx context.Context, d WorkDeps, in WorkInput) (ports.Summary, error) {
	s := ports.Summary{RunID: in.RunID, Repo: in.Repo}
	if !in.Window.Contains(d.Now()) {
		slog.InfoContext(ctx, "outside the work window")
		return s, nil
	}
	start := d.Now()
	err := runWork(ctx, d, in, &s)
	s.Wall = d.Now().Sub(start)
	if err != nil {
		s.Aborted = err.Error()
	}
	if len(s.Issues) > 0 || err != nil {
		step(ctx, "foundry.notify", func(ctx context.Context) error {
			return retryOnce(ctx, in.RetryPause, func() error { return d.Notifier.Notify(ctx, s) })
		})
	}
	return s, err
}

func runWork(ctx context.Context, d WorkDeps, in WorkInput, s *ports.Summary) error {
	if err := step(ctx, "foundry.claims.release", func(ctx context.Context) error { return releaseClaims(ctx, d, in) }); err != nil {
		return err
	}
	var picked []ports.Issue
	if err := step(ctx, "foundry.pick", func(ctx context.Context) (err error) { picked, err = pick(ctx, d, in); return err }); err != nil {
		return err
	}
	if len(picked) == 0 {
		return nil
	}
	if err := retryOnce(ctx, in.RetryPause, func() error { return d.Forge.EnsureLabels(ctx, in.Dir, friction.AllLabels) }); err != nil {
		return err
	}
	for _, is := range picked {
		if !in.Window.Contains(d.Now()) {
			slog.InfoContext(ctx, "work window closed", "remaining", len(picked)-len(s.Issues))
			break
		}
		if err := relabel(ctx, d, in, is.Number, work.ClaimComment(in.RunID), []string{friction.InProgress}, nil); err != nil {
			return err
		}
		rep, cost := runIssue(ctx, d, in, is)
		s.Issues = append(s.Issues, rep)
		s.CostUSD += cost
	}
	return nil
}

func releaseClaims(ctx context.Context, d WorkDeps, in WorkInput) error {
	var claimed []ports.Issue
	if err := retryOnce(ctx, in.RetryPause, func() (err error) { claimed, err = d.Forge.OpenIssues(ctx, in.Dir, friction.InProgress); return err }); err != nil {
		return err
	}
	for _, is := range claimed {
		c, ok := work.CurrentClaim(is.Comments, in.Bot)
		var pr ports.PR
		var found bool
		if ok {
			branch := work.Branch(is.Number, c.RunID)
			if err := retryOnce(ctx, in.RetryPause, func() (err error) { pr, found, err = d.Forge.BranchPR(ctx, in.Dir, branch); return err }); err != nil {
				return err
			}
		}
		var err error
		switch work.Decide(c, ok, pr, found, d.Now(), in.ClaimLease) {
		case work.Rejected:
			err = relabel(ctx, d, in, is.Number, "foundry: "+pr.URL+" was closed without merging, so this needs a decision.",
				[]string{friction.NeedsDecision.Label()}, []string{friction.InProgress, friction.Ready.Label()})
		case work.Expired:
			err = relabel(ctx, d, in, is.Number, "foundry: the claim by run "+c.RunID+" expired with no pull request; back in the queue.",
				nil, []string{friction.InProgress})
		case work.NoClaim:
			err = relabel(ctx, d, in, is.Number, "foundry: in progress with no claim; back in the queue.", nil, []string{friction.InProgress})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func pick(ctx context.Context, d WorkDeps, in WorkInput) ([]ports.Issue, error) {
	var ready []ports.Issue
	var open []ports.PR
	if err := retryOnce(ctx, in.RetryPause, func() (err error) { ready, err = d.Forge.OpenIssues(ctx, in.Dir, friction.Ready.Label()); return err }); err != nil {
		return nil, err
	}
	if err := retryOnce(ctx, in.RetryPause, func() (err error) { open, err = d.Forge.OpenPRs(ctx, in.Dir); return err }); err != nil {
		return nil, err
	}
	return work.Pick(ready, open, in.MaxIssues), nil
}

// relabel comments, then edits labels, each retried once on a transient failure. A crash between the two
// leaves the labels as they were.
func relabel(ctx context.Context, d WorkDeps, in WorkInput, n int, body string, add, remove []string) error {
	if err := comment(ctx, d, in, n, body); err != nil {
		return err
	}
	return retryOnce(ctx, in.RetryPause, func() error { return d.Forge.EditLabels(ctx, in.Dir, n, add, remove) })
}

// comment posts body on issue n. A retry first reads the issue's comments and skips the write when the bot
// already posted this body, as when the first attempt's response was lost.
func comment(ctx context.Context, d WorkDeps, in WorkInput, n int, body string) error {
	attempted := false
	return retryOnce(ctx, in.RetryPause, func() error {
		if attempted {
			cs, err := d.Forge.IssueComments(ctx, in.Dir, n)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(cs, func(c ports.Comment) bool { return c.Author == in.Bot && strings.TrimSpace(c.Body) == strings.TrimSpace(body) }) {
				return nil
			}
		}
		attempted = true
		return d.Forge.CommentIssue(ctx, in.Dir, n, body)
	})
}

func runIssue(ctx context.Context, d WorkDeps, in WorkInput, is ports.Issue) (ports.IssueReport, float64) {
	branch := work.Branch(is.Number, in.RunID)
	var o work.Outcome
	var res ports.AgentResult
	err := step(ctx, "foundry.issue", func(ctx context.Context) error {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.Int("issue", is.Number), attribute.String("branch", branch))
		o, res = runAgent(ctx, d, in, is, branch)
		span.SetAttributes(attribute.String("outcome", string(o.Kind)), attribute.String("reason", string(o.Reason)))
		return applyOutcome(ctx, d, in, is.Number, o)
	})
	rep := ports.IssueReport{Number: is.Number, Title: is.Title, URL: is.URL, Outcome: string(o.Kind), Reason: string(o.Reason),
		PRURL: o.PRURL, Detail: o.Detail}
	if err != nil {
		rep.Detail += "; issue not updated: " + err.Error()
	}
	return rep, res.CostUSD
}

func runAgent(ctx context.Context, d WorkDeps, in WorkInput, is ports.Issue, branch string) (work.Outcome, ports.AgentResult) {
	actx, cancel := context.WithTimeout(ctx, in.IssueTimeout)
	defer cancel()
	res, err := d.Agent.Run(actx, ports.AgentJob{RepoDir: in.Dir, Base: in.Base, Branch: branch,
		Prompt: work.Prompt(is, in.Verify), Verify: in.Verify, MaxTurns: in.MaxTurns})
	rec, shipped, lerr := d.LastShip(in.Repo, branch)
	if lerr != nil {
		slog.ErrorContext(ctx, "ship record unreadable", "branch", branch, "error", lerr.Error())
	}
	timedOut := errors.Is(actx.Err(), context.DeadlineExceeded)
	return work.Classify(work.Evidence{Result: res, RunErr: err, TimedOut: timedOut, Ship: rec, Shipped: shipped}), res
}

func applyOutcome(ctx context.Context, d WorkDeps, in WorkInput, n int, o work.Outcome) error {
	switch o.Kind {
	case work.ReadyForHuman:
		return comment(ctx, d, in, n, "foundry: "+o.PRURL+" is ready for review.")
	case work.NeedsDecision:
		return relabel(ctx, d, in, n, "foundry: this needs a decision.\n\n"+publicSafe(o.Detail, in.UserDataPaths),
			[]string{friction.NeedsDecision.Label()}, []string{friction.InProgress, friction.Ready.Label()})
	}
	return relabel(ctx, d, in, n, fmt.Sprintf("foundry: run %s stuck (%s).\n\n%s", in.RunID, o.Reason, publicSafe(o.Detail, in.UserDataPaths)),
		[]string{friction.Stuck}, []string{friction.InProgress})
}

// publicSafe returns text for a public comment, at most 4000 bytes, or a note that it was withheld.
func publicSafe(text string, userDataPaths []string) string {
	if why := friction.Unsafe(text, userDataPaths); len(why) > 0 {
		return "Details withheld: not public-safe (" + strings.Join(why, ", ") + "). See the run's trace."
	}
	return text[:min(len(text), 4000)]
}
```

- [ ] **Step 5: Write the command**

Create `cmd/foundry/work.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tunedev/foundry/internal/adapters/outbound/claude"
	"github.com/tunedev/foundry/internal/config"
	"github.com/tunedev/foundry/internal/core/app"
)

type secrets struct{ githubToken, claudeToken, discordWebhook string }

func cmdWork(ctx context.Context, cfg config.Config, args []string) int {
	if len(args) > 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	unlock, held, err := lockWork(filepath.Join(cfg.Home, "work.lock"))
	if err != nil {
		return usageErr(err)
	}
	if held {
		slog.InfoContext(ctx, "work skipped: another run holds the lock")
		fmt.Println("skip: another foundry work run holds the lock")
		return exitOK
	}
	defer unlock()
	if cfg.BotName == "" || cfg.BotEmail == "" {
		return usageErr(errors.New("work: set bot_name and bot_email in config.toml"))
	}
	sec, err := loadSecrets(cfg)
	if err != nil {
		return usageErr(err)
	}
	w, err := wireRepo(ctx, cfg)
	if err != nil {
		return usageErr(err)
	}
	vouchHome := os.Getenv("VOUCH_HOME")
	if vouchHome == "" {
		u, _ := os.UserHomeDir()
		vouchHome = filepath.Join(u, ".vouch")
	}
	agentHome := filepath.Join(cfg.Home, "agent")
	home := claude.Home{BotName: cfg.BotName, BotEmail: cfg.BotEmail, HooksPath: filepath.Join(vouchHome, "githooks"), Verify: w.repo.Verify}
	if err := claude.PrepareHome(agentHome, home); err != nil {
		return usageErr(err)
	}
	w.forge.Token = sec.githubToken
	window, _ := cfg.Window()
	s, err := app.Work(ctx, w.workDeps(sec, agentEnv(cfg, sec, agentHome, vouchHome, os.Getenv)), app.WorkInput{
		Dir: w.dir, Repo: w.id, Base: w.repo.Base, RunID: time.Now().UTC().Format("20060102-150405"), Bot: cfg.BotName,
		Verify: w.repo.Verify, UserDataPaths: cfg.UserDataPaths, Window: window, ClaimLease: cfg.ClaimLease.Duration,
		IssueTimeout: cfg.IssueTimeout.Duration, RetryPause: cfg.RetryPause.Duration, MaxIssues: cfg.MaxIssuesPerRun, MaxTurns: cfg.MaxTurns,
	})
	for _, is := range s.Issues {
		fmt.Printf("#%d %s %s %s\n", is.Number, is.Outcome, is.Reason, is.PRURL)
	}
	if err != nil {
		return stopWith(err)
	}
	return exitOK
}

// lockWork takes an exclusive, non-blocking flock on path; held reports that another process has it. The
// kernel drops the lock when its holder exits, however it exits.
func lockWork(path string) (unlock func(), held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, false, nil
}

// loadSecrets reads the three secret files. Errors name the file, never its content.
func loadSecrets(cfg config.Config) (secrets, error) {
	var s secrets
	for path, dst := range map[string]*string{cfg.GitHubTokenFile: &s.githubToken, cfg.ClaudeTokenFile: &s.claudeToken, cfg.DiscordWebhookFile: &s.discordWebhook} {
		b, err := os.ReadFile(path)
		if err != nil {
			return s, fmt.Errorf("secret file %s: %w", path, errors.Unwrap(err))
		}
		if *dst = strings.TrimSpace(string(b)); *dst == "" {
			return s, fmt.Errorf("secret file %s is empty", path)
		}
	}
	return s, nil
}

// agentEnv is the agent's whole environment, apart from the trace context the runner adds.
func agentEnv(cfg config.Config, sec secrets, home, vouchHome string, getenv func(string) string) []string {
	or := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	env := []string{
		"HOME=" + home, "PATH=" + getenv("PATH"), "GH_TOKEN=" + sec.githubToken, "CLAUDE_CODE_OAUTH_TOKEN=" + sec.claudeToken,
		"VOUCH_HOME=" + vouchHome, "FOUNDRY_HOME=" + cfg.Home,
		"CLAUDE_CODE_ENABLE_TELEMETRY=1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1",
		"OTEL_TRACES_EXPORTER=" + or("OTEL_TRACES_EXPORTER", "otlp"), "OTEL_METRICS_EXPORTER=" + or("OTEL_METRICS_EXPORTER", "otlp"),
		"OTEL_LOGS_EXPORTER=none", "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + or("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
	}
	for _, name := range cfg.AgentPassEnv {
		if v := getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}
```

`errors.Unwrap` on an `*fs.PathError` gives the bare cause (`no such file or directory`), so the message names the path once.

In `wire.go`, add:

```go
func (w repoEnv) workDeps(sec secrets, env []string) app.WorkDeps {
	return app.WorkDeps{
		Forge: w.forge,
		Agent: claude.Runner{Bin: "claude", WorktreeRoot: filepath.Join(w.cfg.Home, "worktrees"), GitTimeout: w.cfg.CallTimeout.Duration, Env: env},
		Notifier: discord.Webhook{URL: sec.discordWebhook, Timeout: w.cfg.CallTimeout.Duration},
		LastShip: func(repo, branch string) (work.ShipRecord, bool, error) {
			r, ok, err := w.store.LastRun(repo, branch)
			return work.ShipRecord{Step: r.Step, Outcome: r.Outcome, PRURL: r.PRURL, Detail: r.Detail}, ok, err
		},
		Now: time.Now,
	}
}
```

In `main.go`, add `case "work": return cmdWork(ctx, cfg, args[1:])` to `command`, and to `usage`:

```
  work
        release stale claims, then run an agent on each ready friction issue and send a summary
```

- [ ] **Step 6: Run the tests**

Run: `go vet ./... && go test ./... 2>&1 | tail -30`
Expected: PASS. A failing work test is debugged from `w.root/claude-<n>.env`, `.prompt`, `w.home/logs/foundry.jsonl` and `w.home/runs.jsonl`; read them before changing code.

- [ ] **Step 7: Commit**

```bash
git add internal/core/app/work.go cmd/foundry
git commit -m "Add foundry work: release claims, pick, run the agent per issue and send a summary"
```

### Task B9: Work metrics and the trace across the agent

**Files:**
- Modify: `internal/core/app/metrics.go`, `work.go`, `ship.go`, `friction.go`
- Test: `cmd/foundry/work_test.go`

**Interfaces:**
- Consumes: the sink from B1; `newWorkWorld` from B8; the `foundry.agent` span from B6.
- Produces: instruments `foundry_issue_outcomes_total`, `foundry_agent_cost_usd_total`, `foundry_review_rounds`, `foundry_friction_filed_total`; `WorkDeps.Rounds func(repo, branch string) (ship.Rounds, error)`.

- [ ] **Step 1: Write the failing test**

```go
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
```

The fake agent runs `friction none`, so `foundry_friction_filed_total` is asserted in the friction test instead. Add to `TestFrictionFileCreatesLabelledIssueThenDedupes` in `e2e_test.go`: create a sink, `w.exportTo(sink)` before the first `friction file`, and after it `if !slices.Contains(sink.metricNames(), "foundry_friction_filed_total") { t.Error(...) }`.

Run: `go test ./cmd/foundry -run 'TestWorkTrace|TestFrictionFileCreates' -v`
Expected: FAIL `metric foundry_issue_outcomes_total not exported` (the spans already pass from B1, B6 and B8; if a span assertion fails, that is a real defect in an earlier task; fix it there).

- [ ] **Step 2: Add the instruments**

In `metrics.go`:

```go
	issueOutcomes, _ = meter.Int64Counter("foundry_issue_outcomes_total")
	agentCost, _     = meter.Float64Counter("foundry_agent_cost_usd_total")
	reviewRounds, _  = meter.Int64Histogram("foundry_review_rounds", metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 4, 5, 8))
	frictionFiled, _ = meter.Int64Counter("foundry_friction_filed_total")
```

In `runIssue` (work.go), after the `step` call:

```go
	issueOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("repo", in.Repo),
		attribute.String("outcome", string(o.Kind)), attribute.String("reason", string(o.Reason))))
	agentCost.Add(ctx, res.CostUSD, metric.WithAttributes(attribute.String("repo", in.Repo), attribute.String("model", res.Model)))
```

Review rounds are recorded once per worked issue, so reruns of ship on one branch never add samples. Add `Rounds func(repo, branch string) (ship.Rounds, error)` to `WorkDeps`, wire it to `w.store.Rounds` in `workDeps`, and in `runIssue`, after the `step` call:

```go
	if r, err := d.Rounds(in.Repo, branch); err == nil {
		reviewRounds.Record(ctx, int64(r.Count), metric.WithAttributes(attribute.String("repo", in.Repo)))
	}
```

In `FileFriction`, after a successful filing, before `Save`:

```go
	frictionFiled.Add(ctx, 1, metric.WithAttributes(attribute.String("repo", in.Repo), attribute.String("kind", string(in.Kind))))
```

- [ ] **Step 3: Run the tests**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal cmd
git commit -m "Record outcome, cost, review-round and friction metrics, and trace one issue end to end"
```

### Task B10: Timer, README and skill

**Files:**
- Create: `deploy/systemd/foundry-work@.service`, `deploy/systemd/foundry-work@.timer`
- Modify: `Makefile`, `README.md`, `skills/foundry-ship/SKILL.md`

**Interfaces:**
- Consumes: `foundry work` from B8.
- Produces: `make install-timer`; units run `foundry work` in `~/.foundry/checkouts/<instance>`.

- [ ] **Step 1: Write the units**

`deploy/systemd/foundry-work@.service`:

```ini
[Unit]
Description=foundry work on %i
After=network-online.target

[Service]
Type=oneshot
WorkingDirectory=%h/.foundry/checkouts/%i
Environment=PATH=%h/.local/bin:%h/forge/vouch/bin:/usr/local/go/bin:/usr/bin:/bin
ExecStart=%h/.local/bin/foundry work
TimeoutStartSec=4h
```

`deploy/systemd/foundry-work@.timer`:

```ini
[Unit]
Description=Start foundry work on %i every 2 hours

[Timer]
OnCalendar=*-*-* 00/2:00:00
RandomizedDelaySec=5m

[Install]
WantedBy=timers.target
```

Makefile:

```make
UNITS := $(HOME)/.config/systemd/user

install-timer:
	mkdir -p $(UNITS)
	cp deploy/systemd/foundry-work@.service deploy/systemd/foundry-work@.timer $(UNITS)/
	systemctl --user daemon-reload
	@echo "enable with: systemctl --user enable --now foundry-work@<checkout>.timer"
```

Add `install-timer` to `.PHONY`.

- [ ] **Step 2: Check the units**

Run: `systemd-analyze --user verify deploy/systemd/foundry-work@.service deploy/systemd/foundry-work@.timer`
Expected: no output, or warnings only about the instance name, which is unset here.

- [ ] **Step 3: README and skill**

In `README.md`, add `foundry work` to Commands, and add a section:

```markdown
## Unattended work

`foundry work` runs in a dedicated clone at `~/.foundry/checkouts/<name>`. It releases expired claims,
picks up to `max_issues_per_run` `friction:ready` issues, and runs `claude -p` on each in a fresh worktree
as the bot, under `~/.foundry/agent`. Each issue ends `ready-for-human`, `needs-decision` or `stuck`, and the
run summary goes to Discord. One run at a time; a second exits at once.

    make install-timer
    systemctl --user enable --now foundry-work@<name>.timer

Needs `bot_name`, `bot_email` and the secret files `github_token_file`, `claude_token_file`,
`discord_webhook_file` (defaults under `~/.foundry/secrets/`), and `loginctl enable-linger`.
```

Add the new keys to the Configuration paragraph: `work_window`, `claim_lease`, `issue_timeout`, `max_issues_per_run`, `max_turns`, `max_review_rounds`, `agent_pass_env`, and the bot and secret keys. Add one line: telemetry goes to `OTEL_EXPORTER_OTLP_ENDPOINT` (default `http://localhost:4318`); `OTEL_TRACES_EXPORTER=none` turns traces off.

The skill already has the `review did not converge` bullet from B2.

- [ ] **Step 4: Run the suite and commit**

Run: `go vet ./... && go test ./...`
Expected: PASS.

```bash
git add deploy Makefile README.md
git commit -m "Add the foundry work timer and document unattended work"
```

### Task B11: Ship it, then prove it on the sandbox

**Files:**
- Create: `docs/notes/2026-10-16-increment-2.md`

**Interfaces:**
- Consumes: everything; Part A merged; Part C steps 1 to 7 checked.

- [ ] **Step 1: Whole-branch review, then ship with foundry**

Run the final review the executing skill requires. Then `make install` and `foundry ship --recap <file>` until `ready for human`. This is the pull request for Part B; you merge it.

- [ ] **Step 2: Sandbox run (spec success 2), after the merge**

```bash
make install install-timer
git clone https://github.com/tunedev/foundry-sandbox ~/.foundry/checkouts/foundry-sandbox
cd ~/.foundry/checkouts/foundry-sandbox && foundry work
```

Before it, file one `friction:ready` issue on the sandbox with a small, real fix. Expected, each checked and pasted:
- `foundry work` prints `#<n> ready-for-human  https://github.com/tunedev/foundry-sandbox/pull/<m>`.
- `gh pr view <m> -R tunedev/foundry-sandbox --json author,statusCheckRollup` shows author `tunedev-foundry` and green checks.
- `GH_TOKEN=$(cat ~/.foundry/secrets/github-token) gh pr merge <m> -R tunedev/foundry-sandbox --squash` is refused by branch protection.
- The Discord message arrived.
- In Grafana, Explore, Tempo, the run's `foundry.work` trace shows `foundry.issue`, `foundry.agent`, `claude_code.interaction` and `foundry.ship` with its steps.
- The agent's edits and `go` commands were allowed: if the agent was denied, read the session with `claude --resume <session id>` under the agent home and fix the permission rules.

- [ ] **Step 3: Increment note**

Write `docs/notes/2026-10-16-increment-2.md` with the four Tenet 8 headings from what Step 2 showed. Commit it on a branch and open a pull request.

- [ ] **Step 4: Atlas (spec success 3)**

`git clone https://github.com/tunedev/atlas ~/.foundry/checkouts/atlas`, then `systemctl --user enable --now foundry-work@atlas.timer`. Success is one real atlas friction issue reaching a green bot PR overnight with no human action between.

---

# Part C: your setup, one step at a time

Each step is checked from this session before the next.

1. Create `tunedev-foundry`; add it to `tunedev/atlas` and `tunedev/foundry` with Write; accept the invites as the bot. Check: `gh api repos/tunedev/atlas/collaborators/tunedev-foundry/permission -q .permission` prints `write`.
2. As the bot, create a classic token with scope `repo`; save it to `~/.foundry/secrets/github-token` (mode 600). Check: `GH_TOKEN=$(cat ~/.foundry/secrets/github-token) gh api user -q .login` prints `tunedev-foundry`.
3. Discord webhook URL to `~/.foundry/secrets/discord-webhook`. Check: one test message, sent only after you agree.
4. `claude setup-token` output to `~/.foundry/secrets/claude-oauth-token`. Check: file exists, mode 600, non-empty.
5. Two rulesets on each opted-in repo, with Repository admin in each bypass list:
   - `main`: one approving review required.
   - All branches: block force pushes and deletions.
   Check: `gh api repos/tunedev/<repo>/rulesets`. After step 6, also check on the sandbox that a force-push with the bot's token to a pushed `friction/*` branch is refused.
6. Create `tunedev/foundry-sandbox` with a Go module, CI, `foundry.toml`, vouch rules, the bot as Write collaborator and the same ruleset.
7. Vouch policy per opted-in repo: `vouch repo set gate_severity minor`, `vouch repo set ack_below false`, `vouch repo set post_reviews true`. Check: `vouch repo` in each.
8. `loginctl enable-linger`. Check: `loginctl show-user $USER -p Linger` prints `Linger=yes`.
9. `bot_name = "tunedev-foundry"` and `bot_email = "<id>+tunedev-foundry@users.noreply.github.com"` (the id from `gh api users/tunedev-foundry -q .id`) in `~/.foundry/config.toml`, with `work_window` if you want one.
