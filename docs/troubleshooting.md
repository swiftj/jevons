# Troubleshooting and maintenance

## Where to look

| What | Where |
|------|-------|
| Is the daemon up? | `curl -s 127.0.0.1:8942/healthz` → `{"ok":true}` |
| What has it been doing? | `curl -s 127.0.0.1:8942/status` |
| Daemon log (launchd) | `~/.jev/jev-router.log` (JSON lines) |
| Per-run logs (`jev-runner`) | `~/.jev/logs/*.log`, newest last |
| Hook state | `~/.jev/hook/<session_id>.json` |
| launchd state | `launchctl print gui/$(id -u)/com.github.swiftj.jev-router` |

None of these logs contain prompt text or model output. They record lengths, IDs, latency and
status only.

## Common problems

**The status line shows `Jev: –` and no message appears when I submit a prompt.**
The daemon isn't reachable. The hook stays silent rather than block you. Check `healthz`, then
check the launchd state. A `last exit code` other than 0 together with a quick restart loop
usually means a config error, which will be logged in `~/.jev/jev-router.log`. Also make sure
the `settings.json` command paths are absolute and point at the installed binary.

**Every free-text prompt shows Tier 2.**
The classifier is failing and the router is falling back to Tier 2. In this case the decision
still shows `source` = `classifier`. To confirm, look at `classifier_errors` and
`last_classifier_error` in `/status`. Common causes:
- `api key env "ANTHROPIC_API_KEY" is not set`: the keychain lookup failed. Look for
  `could not read API key from keychain` in the daemon log, then re-run the
  `security add-generic-password` step from [Install](install.md#2-put-the-api-key-in-the-keychain).
  If macOS asks whether `security` may access the item, choose **Always Allow**.
- HTTP 401: the key in the keychain is wrong or has been revoked. Update it with the same
  command (`-U` replaces the item), then restart the daemon.
- Timeouts: the API was slower than `classifier_timeout` (3 s). Raise the timeout in a
  config file if this keeps happening.

**`jev-runner` reports 0 tokens.**
The runner parses `claude -p --output-format json`. It handles warning lines that `claude`
prints before the JSON, such as `claude.ai connectors are disabled…`. If `claude` ever changes
its output format, the runner stops parsing it and does so quietly: `stdout` holds the raw
text and tokens show as 0. Compare a fresh
`claude -p "say ok" --output-format json` against `claudeResult` in
`internal/runner/result.go`.

**A browser-based Claude mod gets `403 origin not allowed`.**
Only `null`, `file://` and loopback origins (`http(s)://localhost`, `127.0.0.1`, `[::1]`) are
allowed. Note the exact `Origin` header from the failed request. If it is a real desktop mod,
add it to `isAllowedOrigin` in `internal/proxy/server.go`, with a test.

**`421 host not allowed`.**
The request reached the daemon under a non-loopback `Host` header. Call the daemon as
`127.0.0.1:8942` or `localhost:8942`.

**Throttling never kicks in.**
Throttling is off unless `capacity_5h` or `capacity_7d` is above 0, and it only counts usage
that something reports. Interactive sessions report nothing by themselves. See
[Daily use](daily-use.md#feeding-the-throttle-and-cache-warmth-rules).

**No desktop notification after a run.**
`jev-runner` uses `osascript` on macOS (`notify-send` on Linux). Check that notifications for
Script Editor / your terminal are allowed in System Settings → Notifications. Use `-notify=false`
to turn them off.

## Routine maintenance

- **Daemon log size.** launchd appends to `~/.jev/jev-router.log` and never rotates it. Every
  request adds a line. Truncate it now and then; the daemon keeps writing to the same file:

  ```bash
  : > ~/.jev/jev-router.log
  ```

- **Run logs** rotate on their own (the last 200 are kept). **Hook state** files older than
  7 days are pruned on their own.
- **Key rotation:** update the keychain item, then restart the daemon:

  ```bash
  launchctl kickstart -k gui/$(id -u)/com.github.swiftj.jev-router
  ```

- **New model IDs:** change `models` in a config file (or the `JEV_MODEL_TIER_*` variables)
  everywhere: the daemon, the hook and the runner. You don't need to rebuild.
- **Upgrades:** `git pull && make check && make install-agent` (see [Install](install.md#upgrade)).

## Development

```bash
make check      # go vet + golangci-lint v2 + race tests
make cover      # coverage per package (prefilter 100%, others ≥ 80%)
make bench      # pre-filter and router benchmarks
make release    # cross-compiled binaries + SHA256SUMS in dist/
```

The architecture and package contracts are in [`specs/`](../specs/), and repo rules are in
[`CLAUDE.md`](../CLAUDE.md). CI runs `make check` on ubuntu (Go stable and 1.22.x) and on
macOS (Go stable). macOS + Go 1.22 is left out on purpose because of a toolchain problem
(`dyld: missing LC_UUID load command`).
