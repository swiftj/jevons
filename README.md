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

It runs `claude -p --output-format json`, so it also:

- **Reports usage** — the run's total tokens (input + output + cache) go to the daemon's
  `/usage` endpoint, feeding the quota throttle. Skipped silently when the daemon isn't running;
  disable with `-report=false`.
- **Escalates** — when `-route` picked a Tier 1 model and the run fails (non-zero exit or
  `is_error: true`), it asks the daemon to escalate and retries once on the Tier 2 model (the
  local Tier 2 model if the daemon can't). Timeouts and explicit `-model` / `-tier` runs are not
  retried. Disable with `-escalate=false`.

## Endpoints (loopback only)

| Method & path | Body | Returns |
|---------------|------|---------|
| `GET /healthz` | — | `{"ok":true}` |
| `GET /status` | — | routing counts, usage windows, warm sessions, throttle flag |
| `POST /route` | `{"prompt", "task_id"?, "session_id"?, "essential"?}` | routing decision |
| `POST /escalate` | `{"decision": <decision>, "cause": "..."}` | Tier 2 decision |
| `POST /session` | `{"session_id", "model", "cached_tokens", "last_activity"?}` | `{"ok":true}` |
| `POST /usage` | `{"tokens": n}` | `{"ok":true}` |

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

## Feeding cache-warmth and quota data

The router only knows what clients tell it. Report session activity and token usage so the
cache-warmth and quota-throttle rules can act:

```bash
curl -s -X POST 127.0.0.1:8942/session \
  -d '{"session_id":"chat-1","model":"claude-sonnet-5-5","cached_tokens":150000}'
curl -s -X POST 127.0.0.1:8942/usage -d '{"tokens":42000}'
```

A session counts as warm when it holds more than 100k cached tokens and was active in the last
50 minutes; a `/route` call carrying that `session_id` then reuses it. Throttling also needs
`capacity_5h` and/or `capacity_7d` set; at 0 that window is ignored.

## Development

`make check` (vet + golangci-lint + race tests), `make cover`, `make bench`. See `CLAUDE.md`
and `specs/` for the architecture.
