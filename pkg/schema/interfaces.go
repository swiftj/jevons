package schema

import (
	"context"
	"time"
)

// Prefilter matches prompts against local rules without any I/O.
type Prefilter interface {
	// Match returns (decision, true) on a local rule hit.
	Match(prompt string) (RoutingDecision, bool)
}

// Classifier asks an LLM to classify a prompt into a tier.
type Classifier interface {
	Classify(ctx context.Context, req RouteRequest) (RoutingDecision, error)
}

// SessionTracker records session activity and reports warm sessions.
type SessionTracker interface {
	// Touch upserts a session on activity.
	Touch(s SessionState)
	// Warm returns sessions meeting the cache-warmth rule at now.
	Warm(now time.Time) []SessionState
	// Get returns a session by ID.
	Get(id string) (SessionState, bool)
}

// UsageSource reports quota window usage.
type UsageSource interface {
	Windows(ctx context.Context) ([]UsageWindow, error)
}

// UsageRecorder accepts client-reported token usage.
type UsageRecorder interface {
	Add(tokens int64)
}

// Router picks a model for each request.
type Router interface {
	Route(ctx context.Context, req RouteRequest) (RoutingDecision, error)
	// Escalate is called after a failed or invalid Tier 1 execution.
	Escalate(ctx context.Context, prev RoutingDecision, cause error) (RoutingDecision, error)
	Status(ctx context.Context) StatusSnapshot
}

// Runner executes headless `claude -p` runs.
type Runner interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// Notifier sends a desktop notification.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// SystemClock is the real wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }
