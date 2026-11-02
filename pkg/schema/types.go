// Package schema defines the shared types and interfaces that every Jev-Pro
// package builds against. It is the Wave 1 contract and has no dependencies
// beyond the standard library.
package schema

import (
	"fmt"
	"time"
)

// Tier is a model capability tier.
type Tier string

const (
	// Tier1 is Haiku-class: mechanical and short tasks.
	Tier1 Tier = "TIER_1"
	// Tier2 is Sonnet-class: general coding and reasoning.
	Tier2 Tier = "TIER_2"
	// Tier3 is Opus-class: architecture and deep multi-step work.
	Tier3 Tier = "TIER_3"
)

// Valid reports whether t is a known tier.
func (t Tier) Valid() bool {
	switch t {
	case Tier1, Tier2, Tier3:
		return true
	}
	return false
}

// Effort is the reasoning effort requested from the selected model.
type Effort string

// Effort levels.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// Valid reports whether e is a known effort level.
func (e Effort) Valid() bool {
	switch e {
	case EffortLow, EffortMedium, EffortHigh:
		return true
	}
	return false
}

// DecisionSource records which pipeline stage produced a decision.
type DecisionSource string

const (
	// SourcePrefilter means a local rule matched at zero API cost.
	SourcePrefilter DecisionSource = "prefilter"
	// SourceClassifier means the micro-JSON LLM classifier decided.
	SourceClassifier DecisionSource = "classifier"
	// SourceCache means an existing warm session was reused.
	SourceCache DecisionSource = "cache_warm"
	// SourceThrottle means the decision was downgraded by the quota throttle.
	SourceThrottle DecisionSource = "throttle"
	// SourceEscalation means the decision was promoted after a Tier 1 failure.
	SourceEscalation DecisionSource = "escalation"
)

// RoutingDecision is the wire contract returned by the classifier and POST /route.
type RoutingDecision struct {
	TaskID        string         `json:"task_id"`
	TaskTier      Tier           `json:"task_tier"`
	SelectedModel string         `json:"selected_model"`
	EffortLevel   Effort         `json:"effort_level"`
	Rationale     string         `json:"rationale"`
	Source        DecisionSource `json:"source"`
	SessionID     string         `json:"session_id,omitempty"`
	LatencyMicros int64          `json:"latency_us"`
}

// Validate checks enums and required fields. Errors wrap ErrInvalidDecision.
func (d RoutingDecision) Validate() error {
	switch {
	case d.TaskID == "":
		return fmt.Errorf("%w: empty task_id", ErrInvalidDecision)
	case !d.TaskTier.Valid():
		return fmt.Errorf("%w: unknown task_tier %q", ErrInvalidDecision, d.TaskTier)
	case d.SelectedModel == "":
		return fmt.Errorf("%w: empty selected_model", ErrInvalidDecision)
	case !d.EffortLevel.Valid():
		return fmt.Errorf("%w: unknown effort_level %q", ErrInvalidDecision, d.EffortLevel)
	}
	return nil
}

// RouteRequest asks the router to pick a model for a prompt.
type RouteRequest struct {
	TaskID    string `json:"task_id,omitempty"`
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id,omitempty"`
	Essential bool   `json:"essential,omitempty"`
}

// SessionState describes an active chat session and its prompt cache.
type SessionState struct {
	SessionID    string    `json:"session_id"`
	Model        string    `json:"model"`
	CachedTokens int64     `json:"cached_tokens"`
	LastActivity time.Time `json:"last_activity"`
}

// UsageWindow is token usage within a rolling quota window ("5h" or "7d").
type UsageWindow struct {
	Name     string `json:"name"`
	Used     int64  `json:"used"`
	Capacity int64  `json:"capacity"`
}

// Ratio returns Used/Capacity, or 0 when Capacity is not positive.
func (w UsageWindow) Ratio() float64 {
	if w.Capacity <= 0 {
		return 0
	}
	return float64(w.Used) / float64(w.Capacity)
}

// StatusSnapshot is a point-in-time copy of router telemetry.
type StatusSnapshot struct {
	Uptime      string                   `json:"uptime"`
	RoutedTotal int64                    `json:"routed_total"`
	BySource    map[DecisionSource]int64 `json:"by_source"`
	ByTier      map[Tier]int64           `json:"by_tier"`
	Usage       []UsageWindow            `json:"usage"`
	UsageErrors int64                    `json:"usage_errors"`
	// ClassifierErrors counts classifier failures that fell back to Tier 2.
	ClassifierErrors int64 `json:"classifier_errors"`
	// LastClassifierError is the most recent classifier failure (never contains prompt text).
	LastClassifierError string         `json:"last_classifier_error,omitempty"`
	WarmSessions        []SessionState `json:"warm_sessions"`
	Throttled           bool           `json:"throttled"`
}

// RunRequest describes a headless `claude -p` invocation.
type RunRequest struct {
	Skill   string        `json:"skill"`
	Prompt  string        `json:"prompt,omitempty"`
	Model   string        `json:"model,omitempty"`
	WorkDir string        `json:"work_dir,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// RunResult is the outcome of a headless run.
type RunResult struct {
	RunID    string        `json:"run_id"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Duration time.Duration `json:"duration"`
	LogPath  string        `json:"log_path"`
	// Tokens is the total usage claude reported (0 when output was not JSON).
	Tokens int64 `json:"tokens,omitempty"`
	// SessionID is the claude session ID from JSON output, when present.
	SessionID string `json:"session_id,omitempty"`
}
