# Install, update and uninstall

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
2. **API key.** If the keychain already has the key (service `jev-pro`), it keeps it.
   Otherwise it offers to store `$ANTHROPIC_API_KEY`, or prompts for the key with input
   hidden. Press Enter to skip. The key goes to `security` on stdin, so it never shows up in a
   process list, a file or a log.
3. **Daemon.** Writes the launchd agent
   `~/Library/LaunchAgents/com.github.swiftj.jev-router.plist`, which runs
   `jev router -keychain jev-pro`. It loads the agent and waits for `/healthz` to answer. The
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
| `-yes` | Accept every prompt. Doesn't prompt for a key, but still stores `$ANTHROPIC_API_KEY` if set. |
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

`jev install` normally does this. To store or rotate the key yourself:

```bash
security add-generic-password -a "$USER" -s jev-pro -T /usr/bin/security -U -w
```

`-w` with no value makes `security` prompt for the key, so it stays out of your shell
history. `-U` replaces an existing item. Check that the item exists without printing it:

```bash
security find-generic-password -s jev-pro >/dev/null && echo stored
```

`jev router -keychain jev-pro` reads the item only when `ANTHROPIC_API_KEY` is not already
set. If the lookup fails, the daemon logs a warning and keeps running. Slash commands and
mechanical prefixes still route to Tier 1, and everything else falls back to Tier 2. After
changing the key, restart the daemon:

```bash
launchctl kickstart -k gui/$(id -u)/com.github.swiftj.jev-router
```

## Using a config file with the daemon

The agent runs `jev router -keychain jev-pro` with default settings. To use a
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

`jev install` rewrites the plist, so add the edit again after re-running it.

## Run in the foreground instead

For a quick try without launchd, run the daemon in a terminal. It reads `ANTHROPIC_API_KEY`
from your shell:

```bash
jev router
```

## Uninstall

```bash
jev uninstall
```

This stops the daemon and removes the launchd agent. It removes Jev's hook and status line
from Claude Code settings after asking, then deletes `~/.local/bin/jev` and `jev.prev`. Add
`-purge` to also delete the keychain item and `~/.jev`. Add `-yes` to skip the question.
