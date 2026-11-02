# Daily use

Jev helps in two ways:

- **Interactive Claude Code sessions: it advises.** `jev-hook` routes each prompt you type and
  shows the tier Jev would pick. Hooks cannot change the model, so you switch with `/model`
  when the suggestion is worth it.
- **Headless runs: it decides.** `jev-runner -route` asks the daemon for a model, runs
  `claude -p` with it, reports token usage, and retries a failed Tier 1 run on Tier 2.

Both need the daemon running (see [Install](install.md)).

## How Jev picks a tier

| Tier | Default model | Used for |
|------|---------------|----------|
| Tier 1 | `claude-haiku-5-5` | Mechanical, short tasks |
| Tier 2 | `claude-sonnet-5-5` | General coding and reasoning (the safe default) |
| Tier 3 | `claude-opus-5-5` | Architecture and deep multi-step work |

Each prompt goes through these stages, and the first one that decides wins:

1. **Pre-filter (free, local).** Slash commands `/cleanup`, `/grammar`, `/format`,
   `/calibrate` and prefixes like `fix typo:`, `format json:`, `list files:`,
   `check spelling:` go to Tier 1 straight away.
2. **Cache warmth.** If the request names a session that is warm (more than 100k cached
   tokens, active in the last 50 minutes), Jev keeps that session's model. Re-reading a warm
   cache is cheaper than switching.
3. **Classifier.** Everything else gets one small Messages API call to the Tier 1 model, which
   returns a tier. If that call fails or times out (3 s), Jev falls back to Tier 2.
4. **Quota throttle.** If you have set capacities and a 5h or 7d window is past 80%, Tier 2
   decisions drop to Tier 1, unless the request is marked `essential`. Tier 3 decisions are
   never throttled.

The `source` in each decision (`prefilter`, `cache_warm`, `classifier`, `throttle`,
`escalation`) says which stage decided.

## Claude Code: hook and status line

Add this to `~/.claude/settings.json` (or a project's `.claude/settings.json`). Use the full
path to `jev-hook`, which is `~/.local/bin/jev-hook` after `make install`:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "/Users/you/.local/bin/jev-hook", "timeout": 10 } ] }
    ]
  },
  "statusLine": { "type": "command", "command": "/Users/you/.local/bin/jev-hook -statusline" }
}
```

**On each prompt**, the hook sends it to the daemon and shows a one-line message such as:

```
Jev: Tier 1 → claude-haiku-5-5 (prefilter)
```

**In the status line** you see the last suggestion for this session next to the model you
are actually using:

| Status line | Meaning |
|-------------|---------|
| `Jev ✓ Tier 1` | You're already on the suggested model. |
| `Jev → Tier 1 (claude-haiku-5-5) · now Tier 3` | Jev suggests Tier 1, and you're on Tier 3. Switch with `/model` if you agree. |
| `Jev: –` | No suggestion yet for this session, or the daemon is not running. |

The status line command can only be one command. If you already have a status line script,
call `jev-hook -statusline` from it and pass its stdin along.

**What it costs.** A prompt that the pre-filter doesn't catch costs one classifier call to
the Tier 1 model. That call is a few hundred tokens and can delay your prompt by up to 3 s.
Without a key, the classifier is skipped and those prompts show Tier 2.

**It never gets in your way.** If the daemon is down, the hook prints nothing and your prompt
goes through as normal. Any other problem is written to stderr (you see it in verbose mode)
and also lets the prompt through. The hook stores only the tier, model and source for each
session, never the prompt. That data sits in `~/.jev/hook/` (mode 0600), and files older than
7 days are pruned.

## Headless runs with jev-runner

```bash
jev-runner -skill=/cleanup -prompt="tidy the README" -route
```

The model comes from the first of these that applies: `-model` (an explicit ID), then
`-tier` (`TIER_1`, `TIER_2` or `TIER_3`), then `-route` (ask the daemon, or use Tier 2 if it
is down), and finally the `claude` CLI's own default.

| Flag | Default | Meaning |
|------|---------|---------|
| `-skill` | (required) | Skill or slash command to run, e.g. `/cleanup` |
| `-prompt` | | Text appended to the skill |
| `-route` | off | Let the daemon pick the model |
| `-model` / `-tier` | | Pick the model yourself |
| `-dir` | current dir | Working directory for the run |
| `-timeout` | `30m` | Kill the run after this long |
| `-escalate` | on | With `-route`: retry a failed Tier 1 run once on Tier 2 |
| `-report` | on | Send the run's token usage to the daemon |
| `-notify` | on | Desktop notification when the run finishes (✓ or ✗) |
| `-config` | | JSON config file |

`jev-runner` writes one log per run to `~/.jev/logs/` (mode 0600, keeps the last 200) and
exits with `claude`'s exit code.

**Escalation** happens only when `-route` picked a Tier 1 model and the run failed (a non-zero
exit or `is_error: true`). Timeouts are not retried, and neither are runs where you chose
`-model` or `-tier` yourself.

**Usage reporting** counts all four token kinds (input, output, cache writes and cache reads).
The total feeds the quota throttle. It is skipped quietly when the daemon is not running.

## Feeding the throttle and cache-warmth rules

The daemon only knows what clients tell it. `jev-runner` reports usage automatically.
Interactive sessions report nothing, so nothing counts toward the throttle unless you send it
yourself:

```bash
curl -s -X POST 127.0.0.1:8942/usage -d '{"tokens":42000}'
```

```bash
curl -s -X POST 127.0.0.1:8942/session \
  -d '{"session_id":"chat-1","model":"claude-sonnet-5-5","cached_tokens":150000}'
```

Throttling stays off until you set `capacity_5h` and/or `capacity_7d` (see
[Configuration](configuration.md)). Usage and session data live in memory and reset when the
daemon restarts.

## Checking what Jev is doing

```bash
curl -s 127.0.0.1:8942/status
```

This shows routing counts by tier and source, usage windows, warm sessions, whether throttling
is on, and the classifier error count with its last error. See [Troubleshooting](troubleshooting.md).
