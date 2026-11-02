# 06 — Entrypoints, Release & CI (Wave 1 scaffolding + Wave 3 wiring)

## A. Repository Hygiene (Wave 1)

`.gitignore`:
```
/bin/
/dist/
*.test
*.out
coverage.*
.DS_Store
.env
.env.*
```
- `git init` in Wave 1; never commit `bin/`, `dist/`, logs, or `.env*`.
- No Python files anywhere (`*.py`, `requirements.txt`, `pyproject.toml`) — CI enforces.

## B. Makefile (Wave 1 skeleton, Wave 3 completes)

| Target | Command |
|--------|---------|
| `build` | `go build -trimpath -o bin/ ./cmd/...` |
| `test` | `go test -race -count=1 ./...` |
| `cover` | `go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out` |
| `bench` | `go test -run=^$$ -bench=. -benchmem ./internal/prefilter/... ./internal/router/...` |
| `lint` | `golangci-lint run` |
| `vet` | `go vet ./...` |
| `check` | `vet lint test` (the full gate) |
| `release` | Cross-compile matrix below into `dist/` with `CGO_ENABLED=0 -ldflags "-s -w -X main.version=$(VERSION)"` |
| `run-router` | `go run ./cmd/jev-router` |
| `clean` | `rm -rf bin dist coverage.out` |

`VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`

Release matrix: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`. Artifacts named
`jev-router_<os>_<arch>` / `jev-runner_<os>_<arch>` plus `SHA256SUMS`.

## C. `.golangci.yml` (Wave 1)

golangci-lint v2 config enabling: `errcheck`, `govet`, `staticcheck`, `ineffassign`, `unused`,
`gosec`, `bodyclose`, `contextcheck`, `noctx`, `errorlint`, `revive`; formatters `gofmt`, `goimports`.
`forbidigo` bans `os.Exit`, `log.Fatal*`, and `fmt.Print*` outside `cmd/`.

## D. `cmd/jev-router` (Wave 3)

- Flags: `-config <path>`, `-addr` (overrides `ListenAddr`), `-version`.
- Wire: `config.Load` → `prefilter.New` → `classifier.New` → `router.NewSessionTracker` →
  usage source (`StaticUsage` from config capacities until a real source exists — see Open
  Questions) → `router.New` → `proxy.NewServer` → `ListenAndServe`.
- `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` for shutdown. Exit code 1 on error.

## E. `cmd/jev-runner` (Wave 3)

- Flags: `-skill` (required, e.g. `/cleanup`), `-prompt`, `-model`, `-tier` (resolves via
  `cfg.Models`), `-dir`, `-timeout`, `-route` (ask the router daemon at `ListenAddr` to pick
  the model via `POST /route`; fall back to Tier 2 if the daemon is unreachable), `-notify`
  (default true), `-config`, `-version`.
- `-report` (default true): POST the run's `Tokens` to the daemon's `/usage`; silent if unreachable.
- `-escalate` (default true): when `-route` chose a Tier 1 model and the run fails with
  `ErrRunFailed`, call `POST /escalate` (falling back to the local Tier 2 model) and retry once.
- Daemon calls go through `internal/client` (Route, Escalate, ReportUsage; `ErrUnreachable`).
- Prints run ID, model, exit code, tokens, duration, and log path per run; exits with the
  final run's exit code.

## F. CI — `.github/workflows/ci.yml` (Wave 3)

- Triggers: push to `main`, pull requests.
- Matrix: `ubuntu-latest`, `macos-latest` × Go `1.22.x`, `stable`.
- Steps: checkout → setup-go (cache) → `make vet` → `golangci-lint-action` (v2) → `make test`
  → `make build` → `make release` (ubuntu+stable only, upload `dist/` artifact).
- Guard step: `test -z "$(git ls-files '*.py' 'requirements*.txt' 'pyproject.toml')"`.

## G. Wave 3 Verification Checklist

1. `make check` green (vet, lint, race tests).
2. `make cover` — `internal/prefilter` 100%, all other `internal/*` ≥ 80%.
3. `make bench` — prefilter < 1ms (expect µs), router prefilter path < 50µs.
4. `make release` produces 8 binaries + `SHA256SUMS`.
5. Smoke: start `bin/jev-router`, `curl -s 127.0.0.1:8942/healthz`, `/status`, and
   `POST /route` with `{"prompt":"/cleanup src"}` → `source:"prefilter"`, `TIER_1`.
6. Smoke: `bin/jev-runner -skill=/cleanup -prompt=hello -timeout=60s` against a stub `claude`
   on `PATH` (temp dir script) — verify log file written and exit code propagated.

## H. Documentation

- `README.md`: purpose, install, quickstart, endpoint table, config reference, model defaults.
- Godoc comment on every exported identifier.

## Open Questions (resolve before or during Wave 3)

1. **Usage source for 5h/7d windows** — resolved: clients report tokens via `POST /usage` into
   `router.CountingUsage`; capacities come from config (`0` disables throttling for a window).
   Sessions are reported via `POST /session`. Reading Anthropic's own usage data is future work.
2. **Module path** — `github.com/johnswift/jevons` (resolved).
