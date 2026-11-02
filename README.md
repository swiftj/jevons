# Jev-Pro

A compiled Go router that picks the cheapest capable Claude model tier for each prompt,
plus a headless `claude -p` runner and a loopback telemetry server for Claude Mods.

- **Zero-cost pre-filter** — slash commands (`/cleanup`, `/grammar`, `/format`, `/calibrate`) and
  mechanical prefixes (`fix typo:`, `format json:`, `list files:`, `check spelling:`) route to
  Tier 1 locally in under a microsecond.
- **Micro-JSON classifier** — everything else is classified by a small Messages API call; if it is
  unavailable the router fails safe to Tier 2.
- **Cache-warmth reuse**, **quota throttling** (5h / 7d windows at 80%), and a **Tier 1 → Tier 2
  escalation gate**.
- Pure Go, standard library only, no Python.

## Install

```bash
make build        # bin/jev-router, bin/jev-runner
make release      # dist/ for darwin+linux × arm64+amd64, with SHA256SUMS
```

## Quickstart

```bash
export ANTHROPIC_API_KEY=...        # optional; without it non-prefilter prompts go to Tier 2
./bin/jev-router                    # listens on 127.0.0.1:8942
curl -s -X POST 127.0.0.1:8942/route -d '{"prompt":"/cleanup src"}'
./bin/jev-runner -skill=/cleanup -prompt="tidy the README" -route
```

`jev-runner` picks the model by precedence `-model` → `-tier` → `-route` (asks the daemon,
falling back to Tier 2) → the `claude` CLI default. It writes a log per run to `~/.jev/logs/`
(0600, rotated to 200 files) and exits with the child's exit code.

## Endpoints (loopback only)

| Method & path | Body | Returns |
|---------------|------|---------|
| `GET /healthz` | — | `{"ok":true}` |
| `GET /status` | — | routing counts, usage windows, warm sessions, throttle flag |
| `POST /route` | `{"prompt", "task_id"?, "session_id"?, "essential"?}` | routing decision |
| `POST /escalate` | `{"decision": <decision>, "cause": "..."}` | Tier 2 decision |

Non-loopback `Host` headers get 421; cross-origin requests are allowed only from `null`,
`file://`, and loopback origins.

## Configuration

Defaults, then an optional JSON file (`-config path.json`), then `JEV_*` environment variables.

| Key (JSON) | Env | Default |
|------------|-----|---------|
| `listen_addr` | `JEV_LISTEN_ADDR` | `127.0.0.1:8942` |
| `models.TIER_1/2/3` | `JEV_MODEL_TIER_1/2/3` | `claude-haiku-5-5` / `claude-sonnet-5-5` / `claude-opus-5-5` |
| `classifier_model` | `JEV_CLASSIFIER_MODEL` | Tier 1 model |
| `anthropic_base_url` | `JEV_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` |
| `api_key_env` | — | `ANTHROPIC_API_KEY` |
| `classifier_timeout` | — | `3s` |
| `warm_idle_max` / `warm_min_tokens` | — | `50m` / `100000` |
| `quota_threshold` | `JEV_QUOTA_THRESHOLD` | `0.80` |
| `capacity_5h` / `capacity_7d` | `JEV_CAPACITY_5H` / `JEV_CAPACITY_7D` | `0` (throttle off) |
| `claude_bin` | `JEV_CLAUDE_BIN` | `claude` |
| `run_timeout` | — | `30m` |
| `log_dir` / `log_max_files` | `JEV_LOG_DIR` / — | `~/.jev/logs` / `200` |

## Known limitation

The daemon has no feed for session activity or token usage yet, so the cache-warmth and
quota-throttle rules are implemented and tested but inactive at runtime.

## Development

`make check` (vet + golangci-lint + race tests), `make cover`, `make bench`. See `CLAUDE.md`
and `specs/` for the architecture.
