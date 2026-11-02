# Jev-Pro

Jev picks the cheapest capable Claude model tier for each prompt. It is one Go binary, `jev`,
which contains:
- the routing daemon
- a headless `claude -p` runner
- a Claude Code hook and status line
- a loopback telemetry server for Claude Mods
- its own installer and updater

How it decides:
- **Zero-cost pre-filter.** Slash commands (`/cleanup`, `/grammar`, `/format`, `/calibrate`)
  and mechanical prefixes (`fix typo:`, `format json:`, …) route to Tier 1 locally in under a
  microsecond.
- **Jev classifier.** Everything else goes to [Jev](https://docs.typesafe.ai/), TypeSafe AI's
  System One model, which picks a tier with a calibrated confidence in about 100 ms. When Jev
  is unsure or unavailable, one small Haiku call decides instead; if that fails too, the
  router falls back to Tier 2.
- **Cache-warmth reuse**, **quota throttling** (5h / 7d windows at 80%), and **Tier 1 → Tier 2
  escalation** for failed headless runs.

It is pure Go, uses only the standard library, and has no Python.

## Quick start (macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/swiftj/jevons/main/install.sh | sh
```

This downloads the latest release, checks its SHA-256 and runs `jev install`, which does the
following:
- installs `jev` to `~/.local/bin`
- stores your Anthropic and TypeSafe API keys in the keychain
- starts the daemon at login
- adds the hook and status line to Claude Code after asking you

Then check the setup:

```bash
jev doctor
```

Update later with `jev update`. Run skills headlessly with:

```bash
jev run -skill=/cleanup -prompt="tidy the README" -route
```

## Documentation

| Page | Covers |
|------|--------|
| [Install, update and uninstall](docs/install.md) | One-line install, `jev install` / `update` / `uninstall`, keychain, launchd, verifying releases |
| [Daily use](docs/daily-use.md) | How tiers are chosen, Claude Code hook and status line, `jev run`, feeding usage |
| [Configuration](docs/configuration.md) | Config file, `JEV_*` variables, flags, choosing capacities |
| [Daemon API](docs/endpoints.md) | Endpoints, decision and status formats, error codes |
| [Troubleshooting](docs/troubleshooting.md) | `jev doctor`, diagnosing problems, maintenance, cutting a release |

## Development

`make check` runs vet, golangci-lint and race tests. `make cover`, `make bench` and
`make release` are also available. `make install` builds from source and runs `jev install`.
See [`CLAUDE.md`](CLAUDE.md) and [`specs/`](specs/) for the architecture.
