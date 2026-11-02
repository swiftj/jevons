# 01 — Architecture & Contracts (Wave 1, Lead Only)

Everything in this file is owned and written by the lead agent in Wave 1. Wave 2 agents
**consume** these contracts and must not edit them. If a contract is wrong, stop and report
it to the lead rather than patching it locally.

## 1. Background

Jev-Pro upgrades the original Jev decision model (Jay's ARMS framework: 4,451 routing calls,
~6ms average per decision, $0.18 total cost, ~40% of queries sent to lightweight models).
The upgrade adds six capabilities, each mapped to a package:

| # | Capability | Package | Spec |
|---|------------|---------|------|
| 1 | Zero-cost local pre-filtering (<1ms regex, $0) | `internal/prefilter` | 02 |
| 2 | Micro-JSON LLM classification fallback | `internal/classifier` | 02 |
| 3 | Cache-warmth intelligence (reuse warm sessions) | `internal/router` | 03 |
| 4 | Token-budget throttling (5h / 7d windows at 80%) | `internal/router` | 03 |
| 5 | Dynamic escalation gate (Tier 1 failure → Tier 2) | `internal/router` | 03 |
| 6 | Headless `claude -p` execution bridge | `internal/runner` | 04 |
| 7 | Claude Mod telemetry bridge on `127.0.0.1:8942` | `internal/proxy` | 05 |

## 2. Directory Layout

```
.
├── CLAUDE.md  Makefile  go.mod  go.sum  .gitignore  .golangci.yml
├── cmd/
│   ├── jev-router/main.go      # Daemon: wires router + proxy server         (Wave 3)
│   └── jev-runner/main.go      # CLI: headless `claude -p` skill runner      (Wave 3)
├── internal/
│   ├── config/                 # Config struct, defaults, env/file loading   (Wave 1)
│   ├── mocks/                  # Deterministic fakes for every interface     (Wave 1)
│   ├── prefilter/              # Regex + slash-command engine                (Wave 2-A)
│   ├── classifier/             # Anthropic Messages API micro-JSON client    (Wave 2-A)
│   ├── router/                 # Decision engine                             (Wave 2-B)
│   ├── runner/                 # `claude -p` process manager                 (Wave 2-C)
│   └── proxy/                  # Local HTTP telemetry server                 (Wave 2-D)
└── pkg/schema/                 # Shared types + interfaces                   (Wave 1)
```

Module path: `github.com/johnswift/jevons` (confirm before Wave 1). `go 1.22` directive.

## 3. Shared Types — `pkg/schema/types.go`

```go
type Tier string
const (
    Tier1 Tier = "TIER_1" // Haiku-class: mechanical / short tasks
    Tier2 Tier = "TIER_2" // Sonnet-class: general coding & reasoning
    Tier3 Tier = "TIER_3" // Opus-class: architecture, deep multi-step work
)

type Effort string
const ( EffortLow Effort = "low"; EffortMedium Effort = "medium"; EffortHigh Effort = "high" )

type DecisionSource string
const (
    SourcePrefilter  DecisionSource = "prefilter"   // matched a local rule, $0
    SourceClassifier DecisionSource = "classifier"  // LLM micro-JSON call
    SourceCache      DecisionSource = "cache_warm"  // reused warm session
    SourceThrottle   DecisionSource = "throttle"    // downgraded by quota
    SourceEscalation DecisionSource = "escalation"  // promoted after Tier 1 failure
)

// RoutingDecision is the wire contract returned by the classifier and POST /route.
type RoutingDecision struct {
    TaskID        string         `json:"task_id"`
    TaskTier      Tier           `json:"task_tier"`
    SelectedModel string         `json:"selected_model"` // resolved from config.Models[TaskTier]
    EffortLevel   Effort         `json:"effort_level"`
    Rationale     string         `json:"rationale"`
    Source        DecisionSource `json:"source"`
    SessionID     string         `json:"session_id,omitempty"` // set when SourceCache
    LatencyMicros int64          `json:"latency_us"`
}

func (d RoutingDecision) Validate() error // tier/effort enum check, non-empty TaskID & model

type RouteRequest struct {
    TaskID    string `json:"task_id,omitempty"` // generated if empty
    Prompt    string `json:"prompt"`
    SessionID string `json:"session_id,omitempty"` // caller's current session, if any
    Essential bool   `json:"essential,omitempty"`  // exempt from quota downgrade
}

type SessionState struct {
    SessionID    string    `json:"session_id"`
    Model        string    `json:"model"`
    CachedTokens int64     `json:"cached_tokens"`
    LastActivity time.Time `json:"last_activity"`
}

type UsageWindow struct {
    Name     string  `json:"name"`      // "5h" or "7d"
    Used     int64   `json:"used"`
    Capacity int64   `json:"capacity"`
}
func (w UsageWindow) Ratio() float64 // Used/Capacity, 0 when Capacity==0

type StatusSnapshot struct {
    Uptime         string            `json:"uptime"`
    RoutedTotal    int64             `json:"routed_total"`
    BySource       map[DecisionSource]int64 `json:"by_source"`
    ByTier         map[Tier]int64    `json:"by_tier"`
    Usage          []UsageWindow     `json:"usage"`
    WarmSessions   []SessionState    `json:"warm_sessions"`
    Throttled      bool              `json:"throttled"`
}

type RunRequest struct {
    Skill   string        `json:"skill"`             // e.g. "/cleanup"
    Prompt  string        `json:"prompt,omitempty"`
    Model   string        `json:"model,omitempty"`
    WorkDir string        `json:"work_dir,omitempty"`
    Timeout time.Duration `json:"timeout,omitempty"`
}

type RunResult struct {
    RunID    string        `json:"run_id"`
    ExitCode int           `json:"exit_code"`
    Stdout   string        `json:"stdout"`
    Stderr   string        `json:"stderr"`
    Duration time.Duration `json:"duration"`
    LogPath  string        `json:"log_path"`
}
```

Sentinel errors (`pkg/schema/errors.go`): `ErrNoMatch`, `ErrInvalidDecision`,
`ErrClassifierUnavailable`, `ErrRunFailed`, `ErrTimeout`.

## 4. Interfaces — `pkg/schema/interfaces.go`

```go
type Prefilter interface {
    // Match returns (decision, true) on a local rule hit; never does I/O.
    Match(prompt string) (RoutingDecision, bool)
}

type Classifier interface {
    Classify(ctx context.Context, req RouteRequest) (RoutingDecision, error)
}

type SessionTracker interface {
    Touch(s SessionState)                       // upsert on activity
    Warm(now time.Time) []SessionState          // sessions meeting the warmth rule
    Get(id string) (SessionState, bool)
}

type UsageSource interface {
    Windows(ctx context.Context) ([]UsageWindow, error)
}

type Router interface {
    Route(ctx context.Context, req RouteRequest) (RoutingDecision, error)
    // Escalate is called after a failed / invalid Tier 1 execution.
    Escalate(ctx context.Context, prev RoutingDecision, cause error) (RoutingDecision, error)
    Status(ctx context.Context) StatusSnapshot
}

type Runner interface {
    Run(ctx context.Context, req RunRequest) (RunResult, error)
}

type Notifier interface {
    Notify(ctx context.Context, title, body string) error
}

type Clock interface { Now() time.Time }
```

## 5. Config — `internal/config`

```go
type Config struct {
    ListenAddr        string                 // default "127.0.0.1:8942"
    Models            map[schema.Tier]string // defaults below
    ClassifierModel   string                 // default Models[Tier1]
    AnthropicBaseURL  string                 // default "https://api.anthropic.com"
    APIKeyEnv         string                 // default "ANTHROPIC_API_KEY" (name, never the value)
    ClassifierTimeout time.Duration          // default 3s
    WarmIdleMax       time.Duration          // default 50m
    WarmMinTokens     int64                  // default 100_000
    QuotaThreshold    float64                // default 0.80
    Capacity5h, Capacity7d int64             // default 0 = unknown → throttle disabled
    ClaudeBin         string                 // default "claude"
    RunTimeout        time.Duration          // default 30m
    LogDir            string                 // default "~/.jev/logs" (expanded)
    LogMaxFiles       int                    // default 200 (rotation)
}
func Default() Config
func Load(path string) (Config, error) // JSON file (optional) then JEV_* env overrides
```

Default model map (the source PRD listed retired `claude-3-*` IDs; use current IDs, all
overridable): `TIER_1 → claude-haiku-5-5`, `TIER_2 → claude-sonnet-5-5`, `TIER_3 → claude-opus-5-5`.

## 6. Mocks — `internal/mocks` (Wave 1)

Deterministic, dependency-free fakes. Every Wave 2 package tests against these.

| Mock | Behavior |
|------|----------|
| `FakeClock` | `Now()` returns a settable time; `Advance(d)` |
| `StubPrefilter` | Map of exact prompt → decision |
| `StubClassifier` | Returns a fixed decision or a configured error; records calls |
| `MemSessions` | In-memory `SessionTracker` honoring `WarmIdleMax`/`WarmMinTokens` via a `Clock` |
| `StaticUsage` | Returns configured `[]UsageWindow` or error |
| `StubRouter` | Returns fixed decision / status; records calls |
| `StubRunner` | Returns fixed `RunResult`; records requests |
| `RecordingNotifier` | Appends `(title, body)` to a slice; mutex-guarded |

All mocks must be safe for concurrent use (`sync.Mutex`).

## 7. Wave 1 Additions (as built — authoritative over the sketches above)

- `schema.UsageRecorder { Add(tokens int64) }` — implemented by `router.CountingUsage`,
  faked by `mocks.RecordingUsage`; fed by `POST /usage` (spec 05).
- `schema.NewID()` — 16-char crypto-random hex; use for TaskID / RunID.
- `schema.SystemClock{}` — real `Clock`. `Tier.Valid()`, `Effort.Valid()` helpers.
- `StatusSnapshot.UsageErrors int64` — count of failed `UsageSource.Windows` calls.
- `config.Config.ModelFor(tier)`, `TierForModel(model) (Tier, bool)`, `Validate()`.
- `mocks.NewMemSessions(idleMax, minTokens)`, `mocks.NewStaticUsage(ws...)` + `Set(ws, err)`,
  `mocks.NewFakeClock(t)`; stubs expose `Calls()` / `Requests()` / `Routes()` / `Escalations()` /
  `Notifications()` accessors. `StubClassifier` copies the request TaskID when its decision has none.

## 8. Wave 1 Gate

`go build ./... && go vet ./... && go test ./pkg/... ./internal/config/... ./internal/mocks/...`
must pass. `cmd/*/main.go` exist as compiling stubs (`func main() {}` with TODO).
