// Package router implements the cache-aware routing engine (schema.Router):
// pre-filter, cache-warmth reuse, classifier, quota throttle, and the
// one-shot Tier 1 → Tier 2 escalation gate. It composes injected
// dependencies only and never constructs real clients itself.
package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/pkg/schema"
)

// Sentinel errors returned by the Engine.
var (
	// ErrEmptyPrompt means Route was called without a prompt.
	ErrEmptyPrompt = errors.New("router: empty prompt")
	// ErrNotEscalatable means Escalate was called on a decision that is not Tier 1.
	ErrNotEscalatable = errors.New("router: only tier 1 decisions can escalate")
	// ErrAlreadyEscalated means the task has already used its single escalation.
	ErrAlreadyEscalated = errors.New("router: task already escalated")
)

// Rationale strings and limits used by the pipeline.
const (
	// RationaleClassifierUnavailable is the rationale of the classifier-failure fallback.
	RationaleClassifierUnavailable = "classifier_unavailable"
	// RationaleCacheWarm is the rationale of a warm-session reuse decision.
	RationaleCacheWarm = "cache_warm: reusing warm session"
	// EscalationLRUSize bounds how many escalated TaskIDs are remembered.
	EscalationLRUSize = 10_000
	// maxCauseLen bounds the escalation cause summary, in runes.
	maxCauseLen = 120
)

// Compile-time interface check.
var _ schema.Router = (*Engine)(nil)

// Deps are the injected collaborators of the Engine. Any of Prefilter,
// Classifier, Sessions, or Usage may be nil, in which case that pipeline step
// is skipped (a nil Classifier triggers the Tier 2 fallback). A nil Clock
// defaults to schema.SystemClock.
type Deps struct {
	Prefilter  schema.Prefilter
	Classifier schema.Classifier
	Sessions   schema.SessionTracker
	Usage      schema.UsageSource
	Clock      schema.Clock
}

// Engine is the production schema.Router. It is safe for concurrent use.
type Engine struct {
	cfg   config.Config
	deps  Deps
	start time.Time

	mu          sync.RWMutex
	routed      int64
	bySource    map[schema.DecisionSource]int64
	byTier      map[schema.Tier]int64
	usage       []schema.UsageWindow
	usageErrors int64
	escalated   *lru
}

// New returns an Engine for cfg composed from d.
func New(cfg config.Config, d Deps) *Engine {
	if d.Clock == nil {
		d.Clock = schema.SystemClock{}
	}
	return &Engine{
		cfg:       cfg,
		deps:      d,
		start:     d.Clock.Now(),
		bySource:  map[schema.DecisionSource]int64{},
		byTier:    map[schema.Tier]int64{},
		escalated: newLRU(EscalationLRUSize),
	}
}

// Route picks a model for req following the pipeline in specs/03-router.md:
// validate, pre-filter, cache warmth, classify, quota throttle, record.
func (e *Engine) Route(ctx context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	began := e.deps.Clock.Now()
	if req.Prompt == "" {
		return schema.RoutingDecision{}, ErrEmptyPrompt
	}
	if req.TaskID == "" {
		req.TaskID = schema.NewID()
	}

	var d schema.RoutingDecision
	hit := false
	if e.deps.Prefilter != nil {
		d, hit = e.deps.Prefilter.Match(req.Prompt)
		if hit {
			d = e.normalize(d, req, schema.SourcePrefilter)
		}
	}

	if !hit {
		if warm, ok := e.warmDecision(req, began); ok {
			return e.finish(warm, began)
		}
		d = e.classify(ctx, req)
	}

	d = e.throttle(ctx, d, req)
	return e.finish(d, began)
}

// warmDecision applies the cache-warmth rule (strict comparisons).
func (e *Engine) warmDecision(req schema.RouteRequest, now time.Time) (schema.RoutingDecision, bool) {
	if req.SessionID == "" || e.deps.Sessions == nil {
		return schema.RoutingDecision{}, false
	}
	s, ok := e.deps.Sessions.Get(req.SessionID)
	if !ok || s.Model == "" || !isWarm(s, now, e.cfg.WarmMinTokens, e.cfg.WarmIdleMax) {
		return schema.RoutingDecision{}, false
	}
	tier, known := e.cfg.TierForModel(s.Model)
	if !known {
		tier = schema.Tier2
	}
	return schema.RoutingDecision{
		TaskID:        req.TaskID,
		TaskTier:      tier,
		SelectedModel: s.Model,
		EffortLevel:   defaultEffort(tier),
		Rationale:     RationaleCacheWarm,
		Source:        schema.SourceCache,
		SessionID:     s.SessionID,
	}, true
}

// isWarm reports whether s satisfies CachedTokens > minTokens and idle < idleMax.
func isWarm(s schema.SessionState, now time.Time, minTokens int64, idleMax time.Duration) bool {
	return s.CachedTokens > minTokens && now.Sub(s.LastActivity) < idleMax
}

// classify calls the classifier, falling back to Tier 2 on any failure.
func (e *Engine) classify(ctx context.Context, req schema.RouteRequest) schema.RoutingDecision {
	if e.deps.Classifier != nil {
		d, err := e.deps.Classifier.Classify(ctx, req)
		if err == nil {
			d = e.normalize(d, req, schema.SourceClassifier)
			if d.Validate() == nil {
				return d
			}
		}
	}
	return schema.RoutingDecision{
		TaskID:        req.TaskID,
		TaskTier:      schema.Tier2,
		SelectedModel: e.cfg.ModelFor(schema.Tier2),
		EffortLevel:   defaultEffort(schema.Tier2),
		Rationale:     RationaleClassifierUnavailable,
		Source:        schema.SourceClassifier,
		SessionID:     req.SessionID,
	}
}

// normalize fills TaskID, model, effort, source, and session from defaults.
func (e *Engine) normalize(d schema.RoutingDecision, req schema.RouteRequest, src schema.DecisionSource) schema.RoutingDecision {
	d.TaskID = req.TaskID
	if d.SelectedModel == "" && d.TaskTier.Valid() {
		d.SelectedModel = e.cfg.ModelFor(d.TaskTier)
	}
	if !d.EffortLevel.Valid() {
		d.EffortLevel = defaultEffort(d.TaskTier)
	}
	if d.Source == "" {
		d.Source = src
	}
	if d.SessionID == "" {
		d.SessionID = req.SessionID
	}
	return d
}

// throttle downgrades a non-essential Tier 2 decision to Tier 1 when any quota
// window is at or above the threshold. Tier 3 is never downgraded. Usage is
// only fetched when a downgrade is possible.
func (e *Engine) throttle(ctx context.Context, d schema.RoutingDecision, req schema.RouteRequest) schema.RoutingDecision {
	if d.TaskTier != schema.Tier2 || req.Essential || e.deps.Usage == nil {
		return d
	}
	ws, ok := e.fetchUsage(ctx)
	if !ok {
		return d
	}
	if w, over := overThreshold(ws, e.cfg.QuotaThreshold); over {
		d.TaskTier = schema.Tier1
		d.SelectedModel = e.cfg.ModelFor(schema.Tier1)
		d.Source = schema.SourceThrottle
		d.Rationale += "; throttled:" + w
	}
	return d
}

// fetchUsage calls the usage source outside the lock and records the result.
func (e *Engine) fetchUsage(ctx context.Context) ([]schema.UsageWindow, bool) {
	ws, err := e.deps.Usage.Windows(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.usageErrors++
		return nil, false
	}
	e.usage = append([]schema.UsageWindow(nil), ws...)
	return ws, true
}

// overThreshold returns the first window whose ratio is >= threshold.
func overThreshold(ws []schema.UsageWindow, threshold float64) (string, bool) {
	for _, w := range ws {
		if w.Ratio() >= threshold {
			return w.Name, true
		}
	}
	return "", false
}

// finish validates d, stamps latency, and records counters.
func (e *Engine) finish(d schema.RoutingDecision, began time.Time) (schema.RoutingDecision, error) {
	d.LatencyMicros = e.deps.Clock.Now().Sub(began).Microseconds()
	if err := d.Validate(); err != nil {
		return schema.RoutingDecision{}, fmt.Errorf("router: route: %w", err)
	}
	e.record(d)
	return d, nil
}

func (e *Engine) record(d schema.RoutingDecision) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.routed++
	e.bySource[d.Source]++
	e.byTier[d.TaskTier]++
}

// Escalate promotes a failed Tier 1 decision to Tier 2. Each TaskID may
// escalate at most once; on rejection prev is returned with an error.
func (e *Engine) Escalate(_ context.Context, prev schema.RoutingDecision, cause error) (schema.RoutingDecision, error) {
	if prev.TaskTier != schema.Tier1 {
		return prev, fmt.Errorf("%w: got %s", ErrNotEscalatable, prev.TaskTier)
	}
	if prev.TaskID == "" {
		return prev, fmt.Errorf("router: escalate: %w: empty task_id", schema.ErrInvalidDecision)
	}
	e.mu.Lock()
	added := e.escalated.add(prev.TaskID)
	e.mu.Unlock()
	if !added {
		return prev, fmt.Errorf("%w: %s", ErrAlreadyEscalated, prev.TaskID)
	}
	d := schema.RoutingDecision{
		TaskID:        prev.TaskID,
		TaskTier:      schema.Tier2,
		SelectedModel: e.cfg.ModelFor(schema.Tier2),
		EffortLevel:   defaultEffort(schema.Tier2),
		Rationale:     "escalated: " + summarize(cause),
		Source:        schema.SourceEscalation,
		SessionID:     prev.SessionID,
	}
	if err := d.Validate(); err != nil {
		return prev, fmt.Errorf("router: escalate: %w", err)
	}
	e.record(d)
	return d, nil
}

// summarize returns cause's message truncated to maxCauseLen runes.
func summarize(cause error) string {
	if cause == nil {
		return "unknown"
	}
	r := []rune(cause.Error())
	if len(r) > maxCauseLen {
		r = r[:maxCauseLen]
	}
	return string(r)
}

// defaultEffort maps a tier to its default effort level.
func defaultEffort(t schema.Tier) schema.Effort {
	switch t {
	case schema.Tier1:
		return schema.EffortLow
	case schema.Tier3:
		return schema.EffortHigh
	default:
		return schema.EffortMedium
	}
}

// Status returns a point-in-time copy of router telemetry. It refreshes usage
// windows (falling back to the last known windows on error) and warm sessions;
// dependency calls happen outside the lock.
func (e *Engine) Status(ctx context.Context) schema.StatusSnapshot {
	now := e.deps.Clock.Now()
	if e.deps.Usage != nil {
		// A failed refresh is counted in UsageErrors; the last known windows are reported.
		_, _ = e.fetchUsage(ctx)
	}
	var warm []schema.SessionState
	if e.deps.Sessions != nil {
		warm = append([]schema.SessionState{}, e.deps.Sessions.Warm(now)...)
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	snap := schema.StatusSnapshot{
		Uptime:       now.Sub(e.start).String(),
		RoutedTotal:  e.routed,
		BySource:     make(map[schema.DecisionSource]int64, len(e.bySource)),
		ByTier:       make(map[schema.Tier]int64, len(e.byTier)),
		Usage:        append([]schema.UsageWindow{}, e.usage...),
		UsageErrors:  e.usageErrors,
		WarmSessions: warm,
	}
	if snap.WarmSessions == nil {
		snap.WarmSessions = []schema.SessionState{}
	}
	for k, v := range e.bySource {
		snap.BySource[k] = v
	}
	for k, v := range e.byTier {
		snap.ByTier[k] = v
	}
	_, snap.Throttled = overThreshold(snap.Usage, e.cfg.QuotaThreshold)
	return snap
}
