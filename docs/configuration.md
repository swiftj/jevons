# Configuration

Every `jev` command loads settings the same way. They start from the defaults, then apply an
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
| `classifier_model` | `JEV_CLASSIFIER_MODEL` | the Tier 1 model | `jev router` |
| `anthropic_base_url` | `JEV_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | `jev router` |
| `api_key_env` | | `ANTHROPIC_API_KEY` | `jev router` |
| `classifier_timeout` | | `3s` (applies to Jev and Haiku separately) | `jev router` |
| `typesafe_base_url` | `JEV_TYPESAFE_BASE_URL` | `https://api.typesafe.ai` | `jev router` |
| `typesafe_model` | `JEV_TYPESAFE_MODEL` | `jev-latest` | `jev router` |
| `typesafe_api_key_env` | | `TYPESAFE_API_KEY` | `jev router` |
| `jev_min_confidence` | `JEV_MIN_CONFIDENCE` | `0.5` (0 to 1; below it, Haiku decides) | `jev router` |
| `warm_idle_max` | | `50m` | `jev router` |
| `warm_min_tokens` | | `100000` | `jev router` |
| `quota_threshold` | `JEV_QUOTA_THRESHOLD` | `0.80` | `jev router` |
| `capacity_5h` / `capacity_7d` | `JEV_CAPACITY_5H` / `JEV_CAPACITY_7D` | `0` (that window is off) | `jev router` |
| `claude_bin` | `JEV_CLAUDE_BIN` | `claude` | `jev run` |
| `run_timeout` | | `30m` | `jev run` |
| `log_dir` | `JEV_LOG_DIR` | `~/.jev/logs` | `jev run`; `jev hook` keeps state in its sibling `hook/` |
| `log_max_files` | | `200` | `jev run` |

## Keep the commands in step

`jev hook`, `jev statusline`, `jev run` and the setup commands find the daemon through
`listen_addr`, and they use the same `models` table. If you change either of those for the
daemon, give those commands the same `-config` (or the same `JEV_*` variables). Otherwise they
will talk to the wrong port or mislabel tiers.

## Flags that are not config keys

| Command | Flag | Meaning |
|---------|------|---------|
| `jev router` | `-addr` | Listen address. Overrides `listen_addr`. |
| `jev router` | `-keychain SERVICE` | Read the Anthropic API key from this macOS keychain item when its env var is unset. |
| `jev router` | `-typesafe-keychain SERVICE` | Read the TypeSafe API key (for Jev) from this keychain item when its env var is unset. Without a TypeSafe key, Haiku is the only classifier. |
| `jev hook` / `jev statusline` | `-addr` | Daemon address. Overrides `listen_addr`. |
| `jev hook` / `jev statusline` | `-state-dir` | Where the hook keeps per-session suggestions (default `~/.jev/hook`). |
| `jev hook` | `-timeout` | How long to wait for the daemon (default `5s`). |
| `jev install` | `-yes`, `-no-agent`, `-no-settings` | See [Install](install.md#install-one-line). |
| `jev update` | `-check`, `-force` | See [Install](install.md#update). |
| `jev doctor` | `-offline` | Skip the GitHub release check. |
| `jev uninstall` | `-yes`, `-purge` | See [Install](install.md#uninstall). |
| `jev on` / `jev off` / `jev status` | none of their own | See [Install](install.md#turn-jev-off-and-on). |
| setup commands | `-bin-dir` | Where `jev` is installed (default `$JEV_BIN_DIR`, else `~/.local/bin`). |

`jev statusline` is shorthand for `jev hook -statusline`. `jev version` prints the version.
The flags for `jev run` are listed in [Daily use](daily-use.md#headless-runs-with-jev-run).

| Env | Meaning |
|-----|---------|
| `JEV_BIN_DIR` | Default install directory for the setup commands. |
| `JEV_GITHUB_API` | GitHub API base URL used by `jev update` and `jev doctor` (default `https://api.github.com`; meant for testing). |

## Choosing capacities

Throttling compares the tokens reported to `/usage` within a rolling 5-hour or 7-day window
against `capacity_5h` / `capacity_7d`. The tokens include cache reads. Anthropic doesn't publish
these capacities as fixed token counts, so start from your own usage. Watch `usage` in
`/status` for a week, then set each capacity to the level where you want Jev to start moving
Tier 2 work to Tier 1. Only reported usage counts, which means `jev run` runs plus anything
you post to `/usage` yourself.

## Pinning the Jev model

`jev-latest` moves when TypeSafe ships a new Jev release, and its confidence scores can shift
with it. If you have tuned `jev_min_confidence`, pin a versioned ID such as `jev-1.14.0` in
`typesafe_model`, and move to a newer one when you are ready. Decisions name the version that
answered in their `rationale` (for example `jev-1.14.0 tier confidence 0.92`).
