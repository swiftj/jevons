---
paths:
  - "cmd/**"
  - "internal/runner/**"
---
# Process Execution & CLI Rules

- Spawn processes only with `exec.CommandContext` and an argv slice. Never `sh -c`, never
  string-concatenate user input into a command line.
- Always bound child processes with a timeout; set `cmd.Cancel` (SIGTERM) and `cmd.WaitDelay`.
- Log files and the log directory are created owner-only (`0o600` / `0o700`).
- Log rotation deletes only `*.log` files inside `LogDir`; resolve and verify paths before removal.
- Truncate prompts in logs and metadata (≤200 chars); never log environment variables.
- `os.Exit`, `flag.Parse`, signal handling, and stdout printing are allowed **only** in `cmd/*/main.go`.
  Keep `main` thin: parse flags → build config → wire deps → run → map error to exit code.
- `cmd/` binaries expose `-version` populated via `-ldflags "-X main.version=..."`.
