# Install and run

This page takes you from a clone to an always-on daemon on macOS. Linux works for the
binaries, but the launchd agent and keychain steps are macOS-only.

## 1. Build and install the binaries

```bash
make install            # builds, then copies jev-router, jev-runner, jev-hook to ~/.local/bin
```

Install somewhere else with `make install PREFIX=/usr/local`. Make sure `$PREFIX/bin` is on
your `PATH`:

```bash
command -v jev-router jev-runner jev-hook
```

| Binary | What it is |
|--------|-----------|
| `jev-router` | The daemon. Routes prompts and serves the loopback API on `127.0.0.1:8942`. |
| `jev-runner` | Runs a skill headlessly with `claude -p`, using the daemon to pick the model. |
| `jev-hook` | Claude Code hook and status line. Shows which tier Jev would pick. |

## 2. Put the API key in the keychain

The daemon's classifier calls the Anthropic Messages API. An agent started by launchd does not
read `~/.zshrc`, so it gets the key from the login keychain instead. Store it once, under the
service name `jev-pro`:

```bash
security add-generic-password -a "$USER" -s jev-pro -T /usr/bin/security -U -w
```

`-w` with no value makes `security` prompt for the key, so it stays out of your shell
history. `-U` updates an existing item, so the same command rotates the key.

Check it is there without printing it:

```bash
security find-generic-password -s jev-pro >/dev/null && echo stored
```

`jev-router -keychain jev-pro` reads this item only when `ANTHROPIC_API_KEY` is not already
set. The key is never written to a file, a log, or an error message. If the lookup fails, the
daemon logs a warning and keeps running. Slash commands and mechanical prefixes still route to
Tier 1, and everything else falls back to Tier 2.

## 3. Start the daemon at login

```bash
make install-agent
```

This installs the binaries and writes
`~/Library/LaunchAgents/com.github.swiftj.jev-router.plist` from
[`deploy/launchd/jev-router.plist`](../deploy/launchd/jev-router.plist). Then it loads the agent.
launchd starts the daemon now and at every login, and restarts it if it exits. Its log goes to
`~/.jev/jev-router.log`.

Check it:

```bash
curl -s 127.0.0.1:8942/healthz
```

```bash
launchctl print gui/$(id -u)/com.github.swiftj.jev-router | grep -E 'state|pid'
```

### Using a config file with the agent

The agent runs `jev-router -keychain jev-pro` with default settings. To use a
[config file](configuration.md), add it to the installed plist's `ProgramArguments`. launchd
does not expand `~`, so give the full path:

```xml
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

`make install-agent` rewrites the plist from the template, so you have to add the edit again
after each upgrade.

## 4. Connect Claude Code

See [Daily use](daily-use.md) for the hook and status line.

## Upgrade

```bash
git pull
```

```bash
make check && make install-agent
```

`install-agent` replaces the binaries and restarts the daemon. Check the version afterwards:

```bash
jev-router -version
```

## Run in the foreground instead

For a quick try without launchd, run it in a terminal. It reads `ANTHROPIC_API_KEY` from
your shell:

```bash
jev-router
```

## Uninstall

```bash
make uninstall           # stops and removes the agent, then removes the binaries
```

To remove the key and Jev's state as well:

```bash
security delete-generic-password -s jev-pro
```

```bash
rm -rf ~/.jev
```
