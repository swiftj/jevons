# Configuration

All three binaries load settings the same way. They start from the defaults, then apply an
optional JSON file (`-config path.json`), then any `JEV_*` environment variables. The file
needs only the keys you want to change.

```json
{
  "capacity_5h": 2000000,
  "capacity_7d": 20000000,
  "classifier_timeout": "2s"
}
```

Durations are Go duration strings (`"3s"`, `"50m"`, `"30m"`).

| Key (JSON) | Env | Default | Used by |
|------------|-----|---------|---------|
| `listen_addr` | `JEV_LISTEN_ADDR` | `127.0.0.1:8942` | all (must be loopback) |
| `models.TIER_1` / `TIER_2` / `TIER_3` | `JEV_MODEL_TIER_1` / `2` / `3` | `claude-haiku-5-5` / `claude-sonnet-5-5` / `claude-opus-5-5` | all |
| `classifier_model` | `JEV_CLASSIFIER_MODEL` | the Tier 1 model | router |
| `anthropic_base_url` | `JEV_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | router |
| `api_key_env` | | `ANTHROPIC_API_KEY` | router |
| `classifier_timeout` | | `3s` | router |
| `warm_idle_max` | | `50m` | router |
| `warm_min_tokens` | | `100000` | router |
| `quota_threshold` | `JEV_QUOTA_THRESHOLD` | `0.80` | router |
| `capacity_5h` / `capacity_7d` | `JEV_CAPACITY_5H` / `JEV_CAPACITY_7D` | `0` (that window is off) | router |
| `claude_bin` | `JEV_CLAUDE_BIN` | `claude` | runner |
| `run_timeout` | | `30m` | runner |
| `log_dir` | `JEV_LOG_DIR` | `~/.jev/logs` | runner; hook keeps state in its sibling `hook/` |
| `log_max_files` | | `200` | runner |

## Keep the binaries in step

`jev-hook` and `jev-runner` find the daemon through `listen_addr`, and they use the same
`models` table. If you change either of those for the daemon, give the hook and the runner the
same `-config` (or the same `JEV_*` variables). Otherwise they will talk to the wrong port or
mislabel tiers.

## Flags that are not config keys

| Binary | Flag | Meaning |
|--------|------|---------|
| `jev-router` | `-addr` | Listen address. Overrides `listen_addr`. |
| `jev-router` | `-keychain SERVICE` | Read the API key from this macOS keychain item when the key env var is unset. |
| `jev-hook` | `-addr` | Daemon address. Overrides `listen_addr`. |
| `jev-hook` | `-statusline` | Print the status line instead of routing a prompt. |
| `jev-hook` | `-state-dir` | Where the hook keeps per-session suggestions (default `~/.jev/hook`). |
| `jev-hook` | `-timeout` | How long to wait for the daemon (default `5s`). |
| all | `-version` | Print the version and exit. |

`jev-runner`'s flags are listed in [Daily use](daily-use.md#headless-runs-with-jev-runner).

## Choosing capacities

Throttling compares the tokens reported to `/usage` within a rolling 5-hour or 7-day window
against `capacity_5h` / `capacity_7d`. The tokens include cache reads. Anthropic doesn't publish
these capacities as fixed token counts, so start from your own usage. Watch `usage` in
`/status` for a week, then set each capacity to the level where you want Jev to start moving
Tier 2 work to Tier 1. Only reported usage counts, which means `jev-runner` runs plus anything
you post to `/usage` yourself.
