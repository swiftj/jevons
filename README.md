# Jev-Pro

A compiled Go router that picks the cheapest capable Claude model tier for each prompt. It
comes with a headless `claude -p` runner, a Claude Code hook, and a loopback telemetry server
for Claude Mods.

- **Zero-cost pre-filter.** Slash commands (`/cleanup`, `/grammar`, `/format`, `/calibrate`)
  and mechanical prefixes (`fix typo:`, `format json:`, …) route to Tier 1 locally in under a
  microsecond.
- **Micro-JSON classifier.** Everything else is classified with one small Messages API call.
  If that call fails, the router falls back to Tier 2.
- **Cache-warmth reuse**, **quota throttling** (5h / 7d windows at 80%), and **Tier 1 → Tier 2
  escalation** for failed headless runs.
- Pure Go, standard library only, no Python.

## Quick start (macOS)

```bash
make install                                                          # → ~/.local/bin
security add-generic-password -a "$USER" -s jev-pro -T /usr/bin/security -U -w   # paste your API key
make install-agent                                                    # run jev-router at login
curl -s 127.0.0.1:8942/healthz
```

Then add the hook and status line to `~/.claude/settings.json` (see
[Daily use](docs/daily-use.md#claude-code-hook-and-status-line)), and run skills headlessly with:

```bash
jev-runner -skill=/cleanup -prompt="tidy the README" -route
```

## Documentation

| Page | Covers |
|------|--------|
| [Install and run](docs/install.md) | Build, keychain key, launchd agent, upgrade, uninstall |
| [Daily use](docs/daily-use.md) | How tiers are chosen, Claude Code hook and status line, `jev-runner`, feeding usage |
| [Configuration](docs/configuration.md) | Config file, `JEV_*` variables, flags, choosing capacities |
| [Daemon API](docs/endpoints.md) | Endpoints, decision and status formats, error codes |
| [Troubleshooting](docs/troubleshooting.md) | Diagnosing problems, maintenance, development commands |

## Development

`make check` (vet + golangci-lint + race tests), `make cover`, `make bench`, `make release`.
See [`CLAUDE.md`](CLAUDE.md) and [`specs/`](specs/) for the architecture.
