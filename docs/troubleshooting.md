# Troubleshooting and maintenance

## Start with jev doctor

```bash
jev doctor
```

It checks the installed binary, `PATH`, whether a newer release exists, the keychain key, the
launchd agent, the daemon, recent classifier failures and the Claude Code wiring. Each line is
`✓` or `✗` with what to do next. It exits 1 if anything fails. `-offline` skips the GitHub check.

## Where to look

| What | Where |
|------|-------|
| Is the daemon up? | `curl -s 127.0.0.1:8942/healthz` → `{"ok":true}` |
| What has it been doing? | `curl -s 127.0.0.1:8942/status` |
| Daemon log (launchd) | `~/.jev/jev-router.log` (JSON lines) |
| Per-run logs (`jev run`) | `~/.jev/logs/*.log`, newest last |
| Hook state | `~/.jev/hook/<session_id>.json` |
| launchd state | `launchctl print gui/$(id -u)/com.github.swiftj.jev-router` |

None of these logs contain prompt text or model output. They record lengths, IDs, latency and
status only.

## Common problems

**The status line shows `Jev: –` and no message appears when I submit a prompt.**
The daemon isn't reachable. The hook stays silent rather than block you. Check `healthz`, then
check the launchd state. A `last exit code` other than 0 together with a quick restart loop
usually means a config error, which will be logged in `~/.jev/jev-router.log`. Also make sure
the `settings.json` commands use the absolute path of the installed binary
(`/Users/you/.local/bin/jev hook`). Re-running `jev install` fixes them.

**Every free-text prompt shows Tier 2.**
The classifier is failing and the router is falling back to Tier 2. In this case the decision
still shows `source` = `classifier`. To confirm, look at `classifier_errors` and
`last_classifier_error` in `/status`. Common causes:
- `api key env "ANTHROPIC_API_KEY" is not set`: the keychain lookup failed. Look for
  `could not read API key from keychain` in the daemon log, then re-run
  `jev install`, or store the key by hand as in
  [Install](install.md#put-the-api-key-in-the-keychain-by-hand).
  If macOS asks whether `security` may access the item, choose **Always Allow**.
- HTTP 401: the key in the keychain is wrong or has been revoked. Store the new one (see
  key rotation below).
- Timeouts: the API was slower than `classifier_timeout` (3 s). Raise the timeout in a
  config file if this keeps happening.

**`jev run` reports 0 tokens.**
`jev run` parses `claude -p --output-format json`. It handles warning lines that `claude`
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
`jev run` uses `osascript` on macOS (`notify-send` on Linux). Check that notifications for
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
- **Key rotation:** store the new key, then restart the daemon. `jev install` only prompts
  when no key is stored, so replace an existing key by hand with the `security` command in
  [Install](install.md#put-the-api-key-in-the-keychain-by-hand) (`-U` replaces it). Then:

  ```bash
  launchctl kickstart -k gui/$(id -u)/com.github.swiftj.jev-router
  ```

- **New model IDs:** change `models` in a config file (or the `JEV_MODEL_TIER_*` variables)
  everywhere: `jev router`, `jev hook` and `jev run`. You don't need to rebuild.
- **Upgrades:** `jev update` (see [Install](install.md#update)). It keeps the previous binary
  as `~/.local/bin/jev.prev` and rolls back by itself if the new daemon does not come up. To
  go back by hand later:

  ```bash
  mv ~/.local/bin/jev.prev ~/.local/bin/jev
  ```

  ```bash
  launchctl kickstart -k gui/$(id -u)/com.github.swiftj.jev-router
  ```

## Development

```bash
make check      # go vet + golangci-lint v2 + race tests
make cover      # coverage per package (prefilter 100%, others ≥ 80%)
make bench      # pre-filter and router benchmarks
make release    # dist/jev_<os>_<arch> (one universal macOS file where lipo exists) + SHA256SUMS
```

### Cutting a release

Releases are built by `.github/workflows/release.yml` when a `v*` tag is pushed. It builds the
binaries, writes `SHA256SUMS`, adds build provenance attestations, and publishes the GitHub
release that `install.sh` and `jev update` download from.

```bash
make check
```

```bash
git tag v0.1.0
```

```bash
git push origin v0.1.0
```

```bash
gh run list -L1 --workflow release.yml
```

Then `gh run watch <id> --exit-status`. A pushed tag publishes a public release, so only push
one for a version you want people to install.

The architecture and package contracts are in [`specs/`](../specs/), and repo rules are in
[`CLAUDE.md`](../CLAUDE.md). CI runs `make check` on ubuntu (Go stable and 1.22.x) and on
macOS (Go stable). macOS + Go 1.22 is left out on purpose because of a toolchain problem
(`dyld: missing LC_UUID load command`).
