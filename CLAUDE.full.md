# CLAUDE.md — Jev-Pro Agentic OS Router & Extension Engine

## 1. Project Overview & Vision

**Jev-Pro** is a high-performance, compiled Golang routing engine and desktop extension framework designed for Claude Desktop and Claude Code environments. 

It upgrades the original Jev decision model architecture (demonstrated in Jay's ARMS framework) which processed 4,451 routing calls at an average speed of ~6ms per decision and $0.18 total cost, directing roughly 40% of queries to lightweight models.

**Jev-Pro** improves upon this baseline by introducing:
1. **Zero-Cost Local Pre-Filtering**: Sub-millisecond Go regex evaluation for mechanical tasks and slash-commands (`/cleanup`, `/grammar`, `/format`) at $0 API cost.
2. **Cache-Warmth Intelligence**: Prompt cache monitoring to reuse warm sessions (under 50-minute idle threshold with >100k cached tokens).
3. **Token-Budget Throttling**: Dynamic model tier shifting when 5-hour or 7-day usage limits approach 80% capacity.
4. **Dynamic Escalation Gate**: Automated promotion to Tier 2 (Sonnet) if a Tier 1 (Haiku) execution returns formatted error or fails validation.
5. **Headless Execution Bridge**: Direct invocation of `claude -p` for self-triggered and scheduled background routines without opening chat windows.
6. **Claude Mod Telemetry Interface**: Local HTTP/IPC server (`127.0.0.1:8942`) exposing model tier status and cache warmth indicators to desktop extension mods.

---

## 2. Tech Stack & Constraints

- **Language**: Go 1.22+ (Strict Requirement: **ZERO Python dependencies**).
- **Architecture**: Concurrency-first Go packages using standard library modules (`net/http`, `os/exec`, `regexp`, `context`, `sync`, `encoding/json`).
- **Dependencies**: Minimal external dependencies; prefer standard library where possible.
- **Target Environment**: macOS / Linux desktop environment running Claude Desktop and Claude Code CLI (`claude`).

---

## 3. Directory Layout

```
.
├── CLAUDE.md                   # Agent system instructions (this file)
├── Makefile                    # Build, test, and execution tasks
├── go.mod                      # Go module definition
├── go.sum                      # Go checksum file
├── cmd/
│   ├── jev-router/             # Main API routing daemon / proxy binary
│   └── jev-runner/             # CLI binary for headless 'claude -p' execution
├── internal/
│   ├── prefilter/              # Local regex and slash-command pre-filter engine
│   ├── classifier/             # Jev micro-JSON classification LLM client
│   ├── router/                 # Decision engine (pre-filter + cache + quota budget)
│   ├── runner/                 # 'claude -p' background process manager
│   ├── config/                 # Application config and threshold specs
│   └── proxy/                  # HTTP / IPC proxy middleware for Claude Mods
└── pkg/
    └── schema/                 # Shared JSON schemas and Go structs for routing
```

---

## 4. Architectural Specs & Core Components

### Component A: Local Pre-Filter Engine (`internal/prefilter`)
- Must evaluate incoming prompts in **< 1ms**.
- Uses Go `regexp` to detect:
  - Mechanical slash-commands (`/cleanup`, `/grammar`, `/format`, `/calibrate`).
  - Mechanical task prefixes (`fix typo:`, `format json:`, `list files:`, `check spelling:`).
- Returns `Tier1Haiku` immediately without making external API calls ($0 token usage).

### Component B: Jev Micro-JSON Classifier (`internal/classifier`)
- Sends a minimalist system prompt to a fast classifier endpoint when pre-filtering does not match.
- Parses output into the strictly validated Go `struct`:

```go
type RoutingDecision struct {
    TaskID        string    `json:"task_id"`
    TaskTier      string    `json:"task_tier"`      // TIER_1, TIER_2, TIER_3
    SelectedModel string    `json:"selected_model"` // claude-3-5-haiku, claude-3-5-sonnet, claude-3-opus
    EffortLevel   string    `json:"effort_level"`   // low, medium, high
    Rationale     string    `json:"rationale"`
}
```

### Component C: Cache-Aware Router (`internal/router`)
- Tracks active session timestamps and token counts.
- **Cache-Warmth Rule**: If an active chat context holds >100k tokens and the prompt cache is warm (<50 mins idle), route follow-up queries to the existing warm session rather than forcing an uncached Haiku request.
- **Quota Throttle**: If session token usage reaches >80% of capacity, downgrade non-essential Tier 2 requests to Tier 1.
- **Escalation Gate**: If a Tier 1 execution returns an error or invalid format, automatically escalate the prompt to Tier 2 (Sonnet).

### Component D: Headless Runner (`internal/runner`)
- Invokes `claude -p` using Go's `os/exec.CommandContext`.
- Streams stdout/stderr, logs execution metadata to `~/.jev/logs/`, and sends system desktop notifications upon task completion.

### Component E: Claude Mods Telemetry Bridge (`internal/proxy`)
- Hosts a lightweight local HTTP server (`127.0.0.1:8942`).
- Provides endpoints:
  - `GET /status`: Returns current usage stats, routing count, and cache warmth.
  - `POST /route`: Endpoint for Claude Mods UI hooks to request instant classification.

---

## 5. Development Guidelines & Code Style Rules

1. **Idiomatic Go**: Write clean, readable, robust Go code. Handle every error explicitly; do not ignore errors with `_` unless explicitly justified.
2. **Context Propagation**: Always pass `context.Context` to network calls and CLI execution timeouts (`exec.CommandContext`).
3. **Concurrency Safety**: Protect shared router state (cache metrics, token usage) with `sync.RWMutex`.
4. **No Side Effects in Libraries**: Packages under `internal/` must not call `os.Exit()` or write directly to stdout unless designed as a CLI binary in `cmd/`.
5. **Zero Python**: Do not write, generate, or invoke Python scripts or wrappers anywhere in this project.

---

## 6. Build, Test & Commands

- **Build All Binaries**: `make build` or `go build -o bin/ ./cmd/...`
- **Run Router Daemon**: `./bin/jev-router`
- **Run Headless Skill**: `./bin/jev-runner -skill="/cleanup"`
- **Run Tests**: `go test -v -race ./...`
- **Lint Code**: `golangci-lint run`

---

## 7. Phased Implementation Plan for Agent Execution

When building out this codebase with Claude Code, execute the phases sequentially:

- [ ] **Phase 1: Core Schemas & Pre-Filter** — Implement `pkg/schema` structs and `internal/prefilter` regex rule engine with 100% unit test coverage.
- [ ] **Phase 2: Jev Classifier & Router Logic** — Implement `internal/classifier` and `internal/router` with cache-warmth calculation and quota throttling.
- [ ] **Phase 3: Headless Runner Engine** — Implement `internal/runner` wrapping `claude -p` commands with error handling and log rotation.
- [ ] **Phase 4: Telemetry Server & Mod Bridge** — Implement `internal/proxy` HTTP endpoints for local Claude Mod integration.
- [ ] **Phase 5: CLI Entry Points & Makefile** — Wire up `cmd/jev-router`, `cmd/jev-runner`, create root `Makefile`, and run integration tests.
