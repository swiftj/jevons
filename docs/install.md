# Install, update, turn off and uninstall

Jev ships as one binary, `jev`, published on
[GitHub Releases](https://github.com/swiftj/jevons/releases). macOS gets a single universal
file that runs on both Apple silicon and Intel. Linux gets `amd64` and `arm64` builds. The
keychain and launchd steps are macOS-only.

## Install (one line)

```bash
curl -fsSL https://raw.githubusercontent.com/swiftj/jevons/main/install.sh | sh
```

The script downloads the latest release for your platform, checks its SHA-256 against the
release's `SHA256SUMS`, and runs `jev install`. That command goes through these steps:

1. **Binary.** Copies `jev` to `~/.local/bin/jev`. If that directory isn't on your `PATH`,
   it prints the line to add to `~/.zshrc`.
2. **API keys.** Two keychain items: the Anthropic key (service `jev-pro`, used by the Haiku
   fallback) and the TypeSafe key (service `jev-pro-typesafe`, used by Jev). For each, an
   item already in the keychain is kept. Otherwise it offers to store `$ANTHROPIC_API_KEY` or
   `$TYPESAFE_API_KEY`, or prompts for the key with input hidden. Press Enter to skip. Keys go
   to `security` on stdin, so they never show up in a process list, a file or a log.
3. **Daemon.** Writes the launchd agent
   `~/Library/LaunchAgents/com.github.swiftj.jev-router.plist`, which runs
   `jev router -keychain jev-pro -typesafe-keychain jev-pro-typesafe`. It loads the agent and waits for `/healthz` to answer. The
   daemon then starts at every login and restarts if it exits. Its log is
   `~/.jev/jev-router.log`.
4. **Claude Code.** Shows the changes it plans for `~/.claude/settings.json`: a
   `UserPromptSubmit` hook running `jev hook` and a status line running `jev statusline`. It
   asks `[Y/n]` before writing. The old file is kept as `settings.json.jev-backup`, and keys in
   the rewritten file are sorted. If you already have a status line, it is left alone, and
   `jev install` tells you how to add Jev to it.

Re-running `jev install` is safe. Steps that are already done are reported as such.

**Options.** Pin a version with `JEV_VERSION`. Pass flags to `jev install` after `sh -s --`.
For example, an unattended install of a fixed version:

```bash
curl -fsSL https://raw.githubusercontent.com/swiftj/jevons/main/install.sh | JEV_VERSION=v0.1.0 sh -s -- -yes
```

| `jev install` flag | Meaning |
|--------------------|---------|
| `-yes` | Accept every prompt. Doesn't prompt for keys, but still stores `$ANTHROPIC_API_KEY` and `$TYPESAFE_API_KEY` if set. |
| `-no-agent` | Don't install the launchd agent. |
| `-no-settings` | Leave Claude Code settings alone. |
| `-bin-dir DIR` | Install somewhere other than `~/.local/bin` (or set `JEV_BIN_DIR`). |
| `-config FILE` | Read the daemon address and key variable name from a [config file](configuration.md). |

Then check everything:

```bash
jev doctor
```

### Other ways to get the binary

- **Manual download.** Get `jev_darwin_universal` (or `jev_linux_amd64` / `jev_linux_arm64`)
  and `SHA256SUMS` from the release page, check the hash, then run
  `chmod +x jev_darwin_universal && ./jev_darwin_universal install`. Browsers mark downloaded
  files as quarantined, and Gatekeeper then refuses to run the unsigned binary. Clear the flag
  with `xattr -d com.apple.quarantine jev_darwin_universal`, or use `curl`, which doesn't set it.
- **From source** (needs Go 1.22+):

  ```bash
  make install
  ```

  This builds `bin/jev` and runs `bin/jev install`.

### Verifying a release

Every release binary carries a GitHub build provenance attestation. It shows the binary was
built by this repository's release workflow:

```bash
gh attestation verify ~/.local/bin/jev --repo swiftj/jevons
```

## Update

```bash
jev update            # install the latest release if it is newer
```

```bash
jev update -check     # only report whether one is available
```

```bash
jev update -force     # reinstall the latest release even if current
```

`jev update` goes through these steps:
1. Downloads the latest release for your platform and checks its SHA-256.
2. Runs the new binary's `version` to make sure it starts.
3. Swaps it into place, keeping the old binary as `~/.local/bin/jev.prev`.
4. If the launchd agent is loaded, restarts the daemon. If the daemon doesn't answer within
   10 seconds, puts the previous binary back and restarts again.

A checksum mismatch stops the update before anything changes. `jev doctor` also tells you
when a newer release is out.

## Put the API key in the keychain by hand

`jev install` normally does this. To store or rotate a key yourself:

```bash
security add-generic-password -a "$USER" -s jev-pro -T /usr/bin/security -U -w
```

For the TypeSafe key (Jev), use service `jev-pro-typesafe`:

```bash
security add-generic-password -a "$USER" -s jev-pro-typesafe -T /usr/bin/security -U -w
```

`-w` with no value makes `security` prompt for the key, so it stays out of your shell
history. `-U` replaces an existing item. Check that the item exists without printing it:

```bash
security find-generic-password -s jev-pro >/dev/null && echo stored
```

The daemon reads each item only when its env var (`ANTHROPIC_API_KEY`, `TYPESAFE_API_KEY`) is
not already set. If a lookup fails, the daemon logs a warning and keeps running. Without the
TypeSafe key, Haiku classifies every free-text prompt. Without either key, slash commands and
mechanical prefixes still route to Tier 1, and everything else falls back to Tier 2. After
changing the key, restart the daemon:

```bash
launchctl kickstart -k gui/$(id -u)/com.github.swiftj.jev-router
```

## Using a config file with the daemon

The agent runs `jev router -keychain jev-pro -typesafe-keychain jev-pro-typesafe` with default
settings. To use a
[config file](configuration.md), add it to `ProgramArguments` in
`~/Library/LaunchAgents/com.github.swiftj.jev-router.plist`, right after `router`. launchd
doesn't expand `~`, so give the full path:

```xml
<string>router</string>
<string>-config</string>
<string>/Users/you/.jev/config.json</string>
```

Then reload the agent:

```bash
launchctl bootout gui/$(id -u)/com.github.swiftj.jev-router
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.github.swiftj.jev-router.plist
```

`jev off` followed by `jev on` also reloads it. `jev install` rewrites the plist, so add the edit again after re-running it.

## Run in the foreground instead

For a quick try without launchd, run the daemon in a terminal. It reads `ANTHROPIC_API_KEY`
from your shell:

```bash
jev router
```

## Turn Jev off and on

To stop using Jev for a while without uninstalling it:

```bash
jev off
```

This does three things:

1. **Stops the daemon** (`launchctl bootout`).
2. **Keeps it from starting at login** (`launchctl disable`). It stays off across restarts
   until you turn it back on.
3. **Silences the Claude Code hook.** It writes the marker file `~/.jev/off`. While that file
   exists, `jev hook` routes nothing and prints nothing, and the status line reads `Jev: off`.
   Claude Code works exactly as it would without Jev.

Your keys, the launchd agent file, the Claude Code settings and the binary all stay in place.
To turn it back on:

```bash
jev on
```

This removes `~/.jev/off`, re-enables the login agent, starts the daemon and waits for it to
answer. If the agent was never installed, it tells you to run `jev install`.

To see where things stand:

```bash
jev status
```

```
✓ jev          on
✓ login agent  com.github.swiftj.jev-router loaded
✓ daemon       answering at 127.0.0.1:8942
```

After `jev off` the three lines read `off; run jev on to turn it back on`,
`stopped by jev off` and `stopped by jev off`. `jev status` is a quick, offline subset of
`jev doctor`. It always exits 0, so use `jev doctor` in scripts that need a failure code.

Some other commands also take Jev out of the off state:

- `jev install` always turns Jev on. It removes the marker and re-enables the agent before it
  loads it.
- `jev uninstall` removes the marker and launchd's disabled setting, so a later install starts
  clean.
- `jev update` replaces the binary but leaves Jev off. It restarts the daemon only if it was
  running.

On Linux there is no launchd agent. `jev off` and `jev on` only write and remove the marker,
so stop or start `jev router` yourself.

## Uninstall

```bash
jev uninstall
```

This removes Jev but keeps your keys and history:

1. Stops the daemon, removes the launchd agent file and clears any `jev off` setting.
2. Shows the hook and status line entries it will take out of `~/.claude/settings.json` and
   asks before removing them. Your other settings stay.
3. Deletes `~/.local/bin/jev` and the previous version kept by `jev update`, `jev.prev`.

What stays: both keychain items (`jev-pro` and `jev-pro-typesafe`), `~/.jev` (the daemon log,
run logs and hook state) and `settings.json.jev-backup`. Reinstalling picks up the stored keys
without asking.

To remove everything, add `-purge`:

```bash
jev uninstall -purge
```

This also deletes both keychain items and the whole `~/.jev` directory. Your API keys
themselves are untouched. Only Jev's copies in the keychain go.

| Flag | Meaning |
|------|---------|
| `-purge` | Also delete both keychain items and `~/.jev`. |
| `-yes` | Don't ask before editing Claude Code settings. |
| `-bin-dir DIR` | Uninstall from somewhere other than `~/.local/bin`. |

If you'd rather keep Jev installed but not running, use [`jev off`](#turn-jev-off-and-on).

**If `jev` is already gone** (deleted by hand), clean up the rest manually:

```bash
launchctl bootout gui/$(id -u)/com.github.swiftj.jev-router
```

```bash
rm ~/Library/LaunchAgents/com.github.swiftj.jev-router.plist
```

```bash
security delete-generic-password -s jev-pro
```

```bash
security delete-generic-password -s jev-pro-typesafe
```

```bash
rm -rf ~/.jev
```

Then remove the `jev hook` entry under `hooks.UserPromptSubmit` and the `jev statusline`
`statusLine` from `~/.claude/settings.json`.
