---
paths:
  - "**/*_test.go"
  - "internal/mocks/**"
---
# Testing Rules

- Tests must pass under `go test -race -count=1`. Use `t.Parallel()` where state allows.
- Prefer table-driven tests with named cases; use `t.Helper()` in helpers and `t.TempDir()` for files.
- Test against `internal/mocks` fakes and `httptest` servers — no real network, no real `claude`
  binary, no real API keys, no writes outside `t.TempDir()`.
- Time-dependent logic uses `mocks.FakeClock`; never `time.Sleep` to wait for state (use channels
  or polling with a deadline ≤ 2s).
- Fake subprocesses with the `TestHelperProcess` re-exec pattern.
- Every boundary in a spec (thresholds, size limits, timeouts) gets an exact-boundary test case.
- Benchmarks live beside the code (`BenchmarkXxx`) and cover any spec latency budget.
- Coverage targets: `internal/prefilter` 100%, other `internal/*` ≥ 80%.
