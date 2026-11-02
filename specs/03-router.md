# 03 — Cache-Aware Router (Wave 2-B)

**Owns:** `internal/router/**`
**Consumes:** `pkg/schema`, `internal/config`, `internal/mocks` (read-only)

Implements `schema.Router`. Composes injected dependencies only — never constructs a real
classifier, HTTP client, or runner itself.

```go
type Deps struct {
    Prefilter  schema.Prefilter
    Classifier schema.Classifier
    Sessions   schema.SessionTracker
    Usage      schema.UsageSource
    Clock      schema.Clock
}
func New(cfg config.Config, d Deps) *Engine
```

Also provide the production `SessionTracker` implementation here: `NewSessionTracker(cfg, clock)`
(in-memory, `sync.RWMutex`, evicts entries idle > 2× `WarmIdleMax`).

## Decision Pipeline (`Route`)

Evaluate in this exact order; first terminal step wins.

1. **Validate input** — empty prompt → error. Generate `TaskID` if missing.
2. **Pre-filter** — `Prefilter.Match`; on hit, go to step 5 (throttle never upgrades, so
   Tier 1 hits are terminal after recording).
3. **Cache-warmth rule** — if `req.SessionID` is set and `Sessions.Get` returns a session with
   `CachedTokens > cfg.WarmMinTokens` and `now - LastActivity < cfg.WarmIdleMax`, return a
   decision that reuses that session: `SelectedModel = session.Model`, tier inferred from the
   model via reverse lookup of `cfg.Models` (unknown → Tier2), `Source=SourceCache`,
   `SessionID` set. Rationale: reusing warm cache beats a cold cheaper model.
4. **Classify** — `Classifier.Classify`. On error: fall back to `Tier2` with
   `Rationale="classifier_unavailable"` (fail safe toward capability, not toward cost).
   Each failure (error or invalid decision) increments `StatusSnapshot.ClassifierErrors`, sets
   `LastClassifierError` (truncated to 120 runes), and logs a warning via `Deps.Logger` with the
   task ID — never the prompt.
5. **Quota throttle** — fetch `Usage.Windows`. If any window `Ratio() >= cfg.QuotaThreshold`
   and the decision is `Tier2` and `!req.Essential`, downgrade to `Tier1`, set
   `Source=SourceThrottle`, append `"; throttled:<window>"` to rationale. Tier 3 is **never**
   auto-downgraded (only reported via `Throttled=true` in status). Usage errors → no throttle,
   but counted in status.
6. **Record** — increment counters by source and tier, set `LatencyMicros`, `Validate()`, return.

## Escalation Gate (`Escalate`)

- Only valid when `prev.TaskTier == Tier1`; otherwise return `prev` with an error.
- Returns a `Tier2` decision with the same `TaskID`, `Source=SourceEscalation`,
  `Rationale="escalated: <cause summary, max 120 chars>"`. Not subject to throttling.
- A task may escalate at most once (track `TaskID` in a bounded LRU of 10k entries).
- Callers decide "invalid format"; the router does not inspect model output.

## Status

`Status()` returns a consistent `StatusSnapshot` copy (no shared maps leaked) including
counts, latest usage windows, warm sessions from `Sessions.Warm(now)`, and `Throttled`.

## Concurrency

All counters and the escalation LRU are protected by a single `sync.RWMutex` (or atomics for
counters). Dependency calls must happen **outside** the lock.

## Tests (mock-backed only)

- Pipeline order: prefilter hit short-circuits classifier (assert `StubClassifier` call count 0).
- Warmth boundaries: tokens exactly 100k (not warm), 100,001 (warm); idle 49m59s (warm), 50m (not).
- Throttle: 79% vs 80%, Essential exemption, Tier 3 untouched, usage error → no throttle.
- Classifier error → Tier 2 fallback.
- Escalation: Tier1→Tier2, double escalation rejected, Tier2 input rejected.
- 100 goroutines × 1,000 `Route` calls under `-race`; final counts exact.
- `BenchmarkRoutePrefilterHit` must stay under 50µs/op.
