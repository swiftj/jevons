---
paths:
  - "internal/**/*.go"
  - "pkg/**/*.go"
---
# Core Go Library Rules

- Handle every error explicitly. `_ =` discards need a one-line comment justifying them.
- Wrap errors with `fmt.Errorf("pkg: op: %w", err)`; match with `errors.Is/As` against the
  sentinels in `pkg/schema/errors.go`.
- Every function doing I/O, network, or process work takes `ctx context.Context` first.
- Shared mutable state (counters, session maps, LRUs) is guarded by `sync.RWMutex` or
  `sync/atomic`. Never call a dependency (classifier, usage source, notifier) while holding a lock.
- No side effects in libraries: no `os.Exit`, `log.Fatal*`, `panic` for control flow, or
  writes to stdout/stderr. Log via an injected `*slog.Logger` only.
- Depend on interfaces from `pkg/schema`, not concrete types from sibling packages.
  `internal/router` must not import `internal/classifier`, `internal/runner`, or `internal/proxy`.
- Time comes from an injected `schema.Clock`, never `time.Now()` inside decision logic.
- Standard library first. Adding any module to `go.mod` requires lead approval.
- **Zero Python**: never create, generate, or invoke Python scripts or wrappers.
- `pkg/schema`, `internal/config`, and `internal/mocks` are Wave 1 contracts — Wave 2 agents
  must not edit them; report contract gaps to the lead.
