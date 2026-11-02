# CLAUDE.md — Jev-Pro Agentic Router & Extension Engine

**Jev-Pro** is a compiled Go routing engine for Claude Desktop / Claude Code that picks the
cheapest capable model tier per prompt: $0 local regex pre-filtering, TypeSafe's Jev System One classifier with a
micro-JSON LLM (Haiku) fallback, cache-warmth reuse, quota throttling, a Tier 1→2 escalation gate, a
headless `claude -p` runner, and a loopback telemetry server for Claude Mods.

## Locked Stack

- **Go 1.22+** (`go 1.22` directive), standard library first. New modules need lead approval.
- **ZERO Python** — no `.py`, no Python tooling, no Python subprocesses. CI enforces it.
- Targets: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`, static (`CGO_ENABLED=0`).
- State: in-memory only (sessions, counters); run logs in `~/.jev/logs/`.
- Anthropic Messages API via raw `net/http`; model IDs live in `internal/config` only
  (defaults: `claude-haiku-5-5` / `claude-sonnet-5-5` / `claude-opus-5-5`).

## Security & Hygiene

- Telemetry server binds **loopback only** (`127.0.0.1:8942`); no wildcard CORS.
- API key read from `$ANTHROPIC_API_KEY` at call time; never logged, serialized, or committed.
- Never log prompt contents or model output.
- Processes spawned via `exec.CommandContext` with argv slices — never through a shell.
- `internal/` and `pkg/` never call `os.Exit`, `log.Fatal`, or print to stdout.
- Never commit `bin/`, `dist/`, `.env*`, or logs.

## Commands

| Task | Command |
|------|---------|
| Build | `make build` → `bin/jev` (one binary: `router`, `run`, `hook`, `statusline`, `install`, `update`, `doctor`, `uninstall`) |
| Test (race) | `make test` |
| Lint | `make lint` (golangci-lint v2) |
| Full gate | `make check` (vet + lint + race tests) |
| Coverage | `make cover` |
| Benchmarks | `make bench` |
| Release | `make release` → `dist/jev_darwin_universal`, `jev_linux_{amd64,arm64}` + `SHA256SUMS`; publish by pushing a `v*` tag (`.github/workflows/release.yml`) |
| Run daemon | `./bin/jev router` |
| Run headless skill | `./bin/jev run -skill=/cleanup` |

## Context Map (Progressive Disclosure)

Read **only** the spec for the work you own. Path-scoped rules in `.claude/rules/` load
automatically when you touch matching files.

| Spec | Scope | Read when |
|------|-------|-----------|
| `specs/01-architecture-and-contracts.md` | Layout, shared types, interfaces, config, mocks | Wave 1; any contract question |
| `specs/02-prefilter-and-classifier.md` | `internal/prefilter`, `internal/classifier` | Wave 2-A |
| `specs/03-router.md` | `internal/router` pipeline, warmth, throttle, escalation | Wave 2-B |
| `specs/04-runner.md` | `internal/runner`, logs, rotation, notifications | Wave 2-C |
| `specs/05-proxy-telemetry.md` | `internal/proxy` endpoints (incl. `/session`, `/usage` feeds) & hardening | Wave 2-D |
| `specs/06-release-and-ci.md` | Makefile, `cmd/*`, `internal/client`, CI, release, verification | Wave 1 scaffold; Wave 3 |

| Rule file | Applies to |
|-----------|-----------|
| `.claude/rules/go-core.md` | `internal/**/*.go`, `pkg/**/*.go` |
| `.claude/rules/process-and-cli.md` | `cmd/**`, `internal/runner/**` |
| `.claude/rules/network-security.md` | `internal/proxy/**`, `internal/classifier/**`, `internal/client/**`, `internal/config/**` |
| `.claude/rules/testing.md` | `**/*_test.go`, `internal/mocks/**` |

## Status

All three waves are complete. Since then: `POST /session` + `POST /usage` feeds (spec 05),
and `jev run` usage reporting + Tier 1 → Tier 2 auto-escalation via `internal/client` (spec 06),
the Claude Code hook (`jev hook` / `jev statusline`), and a single `jev` binary that installs and
updates itself from GitHub Releases (`internal/setup`: keychain key, launchd agent, Claude settings).
Classification now goes to Jev first (`internal/classifier/jev.go`, `chain.go`; spec 02 section C),
with its TypeSafe key in the keychain item `jev-pro-typesafe`.
For new work, keep the ownership boundaries below: change `pkg/schema` / `internal/config` /
`internal/mocks` first, gate, then edit implementation packages in parallel.

## 3-Wave Execution Plan (reference)

### Wave 1 — Contract Lock (lead only, synchronous)

Owns: `go.mod`, `Makefile`, `.gitignore`, `.golangci.yml`, `pkg/schema/**`,
`internal/config/**`, `internal/mocks/**`, stub `cmd/*/main.go`.
Spec: `specs/01` + sections A–C of `specs/06`.

**Gate:** `git init` done, and `go build ./... && go vet ./... && go test -race ./pkg/... ./internal/config/... ./internal/mocks/...`
passes. No Wave 2 agent starts before this gate is green.

### Wave 2 — Parallel Teammates (4 agents, isolated)

| Agent | Reads ONLY | Exclusive write ownership |
|-------|-----------|---------------------------|
| 2-A Prefilter+Classifier | `specs/02` | `internal/prefilter/**`, `internal/classifier/**` |
| 2-B Router | `specs/03` | `internal/router/**` |
| 2-C Runner | `specs/04` | `internal/runner/**` |
| 2-D Proxy | `specs/05` | `internal/proxy/**` |

Every Wave 2 agent:
- May read `pkg/schema`, `internal/config`, `internal/mocks` but **must not edit** them —
  report contract gaps to the lead instead.
- Must not edit any file outside its ownership glob (including `go.mod`, `Makefile`, `cmd/`).
- Tests its package against `internal/mocks` / `httptest` only, and finishes with
  `go test -race ./internal/<pkg>/...` and `golangci-lint run ./internal/<pkg>/...` green.

### Wave 3 — Integration & Release (lead, synchronous)

Owns: `cmd/**`, `internal/client/**`, `Makefile` completion, `.github/workflows/**`, `README.md`.
Spec: sections D–H of `specs/06`.

1. Wire `cmd/jev-router` and `cmd/jev-runner` from Wave 2 packages.
2. `make check` and `make cover` (prefilter 100%, others ≥80%) and `make bench`.
3. `make release` → 8 binaries + checksums.
4. Smoke tests: daemon `/healthz`, `/status`, `POST /route`; runner against a stub `claude`.
5. Report verification output and resolve the Open Questions in `specs/06`.
