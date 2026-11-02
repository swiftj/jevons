# 04 — Headless Runner (Wave 2-C)

**Owns:** `internal/runner/**`
**Consumes:** `pkg/schema`, `internal/config`, `internal/mocks` (read-only)

Implements `schema.Runner`: runs `claude -p` in the background for self-triggered and
scheduled routines without opening a chat window.

```go
type Options struct {
    Notifier schema.Notifier // nil → no notifications
    Clock    schema.Clock
    Exec     CommandFactory  // injectable for tests; default wraps exec.CommandContext
}
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd
func New(cfg config.Config, opt Options) *Runner
```

## Invocation

- Command: `cfg.ClaudeBin -p <prompt> [--model <model>] --output-format json`.
  - Stdout is parsed as a `{"type":"result",...}` object: `RunResult.Stdout` becomes `result`,
    `Tokens` the sum of input, output, cache-creation and cache-read tokens, `SessionID` the
    session ID. `is_error: true` with exit 0 wraps `schema.ErrRunFailed`. Non-JSON output is left
    as-is with `Tokens = 0`.
  - `prompt` = `req.Skill` followed by a space and `req.Prompt` when both are present.
  - Arguments are passed as an argv slice — **never** through a shell.
- `exec.CommandContext` with `ctx` wrapped in `context.WithTimeout(req.Timeout || cfg.RunTimeout)`.
- Set `cmd.Dir = req.WorkDir` when non-empty (must exist and be a directory, else error).
- Set `cmd.Cancel` to send `SIGTERM` and `cmd.WaitDelay = 10s` before SIGKILL.
- Environment: inherit `os.Environ()`; do not add secrets.

## Output & Logging

- Stream stdout/stderr concurrently to both an in-memory capped buffer (1 MiB each, keep tail)
  and the log file — use `io.MultiWriter`.
- Log file: `cfg.LogDir/<UTC yyyymmdd-HHMMSS>-<runID>.log`, dir created `0o700`, file `0o600`.
- First lines of each log are a JSON metadata header: run ID, skill, model, workdir, start time,
  argv (prompt truncated to 200 chars). Final line: JSON footer with exit code, duration, error.
- **Rotation:** after each run, if `LogDir` holds more than `cfg.LogMaxFiles` `*.log` files,
  delete the oldest by name until within the limit. Never delete non-`.log` files.

## Result & Errors

- Exit 0 → `RunResult`, nil error.
- Non-zero exit → `RunResult` populated + error wrapping `schema.ErrRunFailed`.
- Deadline exceeded → wrap `schema.ErrTimeout`.
- `claude` binary missing → descriptive error (use `exec.LookPath` up front).

## Desktop Notifications

Provide `NewDesktopNotifier()` returning a `schema.Notifier`:
- macOS: `osascript -e 'display notification ... with title ...'` — escape quotes/backslashes;
  pass via argv, never via shell.
- Linux: `notify-send <title> <body>` if on `PATH`; otherwise no-op.
- Notification failures are logged into the run log, never fail the run.
- Runner sends one notification on completion: title `Jev: <skill> ✓` or `Jev: <skill> ✗`.

## Tests

- Use the `TestHelperProcess` pattern (re-exec the test binary via `CommandFactory`) to fake
  `claude`: success, non-zero exit, large output (>1 MiB, verify tail kept), hang (timeout),
  stderr-only output.
- Rotation: temp dir with 205 logs + a `.txt` file → 200 logs remain, `.txt` untouched.
- Log permissions are 0600; metadata header parses as JSON.
- `RecordingNotifier` receives exactly one notification with correct ✓/✗.
- osascript escaping is unit-tested as a pure function (no process spawned).
