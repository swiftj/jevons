// Package mocks provides deterministic, concurrency-safe fakes for every
// interface in pkg/schema. All Wave 2 packages test against these.
package mocks

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/swiftj/jevons/pkg/schema"
)

// Compile-time interface checks.
var (
	_ schema.Clock          = (*FakeClock)(nil)
	_ schema.Prefilter      = (*StubPrefilter)(nil)
	_ schema.Classifier     = (*StubClassifier)(nil)
	_ schema.SessionTracker = (*MemSessions)(nil)
	_ schema.UsageSource    = (*StaticUsage)(nil)
	_ schema.UsageRecorder  = (*RecordingUsage)(nil)
	_ schema.Router         = (*StubRouter)(nil)
	_ schema.Runner         = (*StubRunner)(nil)
	_ schema.Notifier       = (*RecordingNotifier)(nil)
)

// FakeClock is a settable clock.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a clock fixed at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set replaces the current fake time.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// StubPrefilter matches exact prompts from a map.
type StubPrefilter struct {
	mu    sync.Mutex
	Rules map[string]schema.RoutingDecision
	calls int
}

// Match returns the mapped decision for an exact prompt.
func (p *StubPrefilter) Match(prompt string) (schema.RoutingDecision, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	d, ok := p.Rules[prompt]
	return d, ok
}

// Calls returns how many times Match was called.
func (p *StubPrefilter) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// StubClassifier returns a fixed decision or error and records requests.
type StubClassifier struct {
	mu       sync.Mutex
	Decision schema.RoutingDecision
	Err      error
	requests []schema.RouteRequest
}

// Classify records req and returns the configured result. When the configured
// decision has no TaskID, the request's TaskID is copied in.
func (c *StubClassifier) Classify(_ context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	if c.Err != nil {
		return schema.RoutingDecision{}, c.Err
	}
	d := c.Decision
	if d.TaskID == "" {
		d.TaskID = req.TaskID
	}
	return d, nil
}

// Calls returns the number of Classify calls.
func (c *StubClassifier) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// Requests returns a copy of recorded requests.
func (c *StubClassifier) Requests() []schema.RouteRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]schema.RouteRequest(nil), c.requests...)
}

// MemSessions is an in-memory SessionTracker applying the warmth rule:
// CachedTokens > MinTokens and now-LastActivity < IdleMax.
type MemSessions struct {
	mu        sync.RWMutex
	IdleMax   time.Duration
	MinTokens int64
	sessions  map[string]schema.SessionState
}

// NewMemSessions returns an empty tracker with the given warmth thresholds.
func NewMemSessions(idleMax time.Duration, minTokens int64) *MemSessions {
	return &MemSessions{IdleMax: idleMax, MinTokens: minTokens, sessions: map[string]schema.SessionState{}}
}

// Touch upserts a session.
func (m *MemSessions) Touch(s schema.SessionState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.SessionID] = s
}

// Get returns a session by ID.
func (m *MemSessions) Get(id string) (schema.SessionState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Warm returns warm sessions sorted by SessionID.
func (m *MemSessions) Warm(now time.Time) []schema.SessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []schema.SessionState
	for _, s := range m.sessions {
		if s.CachedTokens > m.MinTokens && now.Sub(s.LastActivity) < m.IdleMax {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// StaticUsage returns configured windows or an error.
type StaticUsage struct {
	mu      sync.Mutex
	windows []schema.UsageWindow
	err     error
}

// NewStaticUsage returns a source reporting ws.
func NewStaticUsage(ws ...schema.UsageWindow) *StaticUsage { return &StaticUsage{windows: ws} }

// Set replaces the reported windows and error.
func (u *StaticUsage) Set(ws []schema.UsageWindow, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.windows, u.err = ws, err
}

// Windows returns a copy of the configured windows.
func (u *StaticUsage) Windows(context.Context) ([]schema.UsageWindow, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return nil, u.err
	}
	return append([]schema.UsageWindow(nil), u.windows...), nil
}

// RecordingUsage records every Add call.
type RecordingUsage struct {
	mu    sync.Mutex
	added []int64
}

// Add records tokens.
func (u *RecordingUsage) Add(tokens int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.added = append(u.added, tokens)
}

// Added returns a copy of recorded token counts.
func (u *RecordingUsage) Added() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.added...)
}

// EscalateCall records one Escalate invocation.
type EscalateCall struct {
	Prev  schema.RoutingDecision
	Cause error
}

// StubRouter returns fixed results and records calls.
type StubRouter struct {
	mu          sync.Mutex
	Decision    schema.RoutingDecision
	Err         error
	Escalated   schema.RoutingDecision
	EscalateErr error
	Snapshot    schema.StatusSnapshot
	routes      []schema.RouteRequest
	escalations []EscalateCall
}

// Route records req and returns the configured decision.
func (r *StubRouter) Route(_ context.Context, req schema.RouteRequest) (schema.RoutingDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, req)
	return r.Decision, r.Err
}

// Escalate records the call and returns the configured escalation result.
func (r *StubRouter) Escalate(_ context.Context, prev schema.RoutingDecision, cause error) (schema.RoutingDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.escalations = append(r.escalations, EscalateCall{Prev: prev, Cause: cause})
	return r.Escalated, r.EscalateErr
}

// Status returns the configured snapshot.
func (r *StubRouter) Status(context.Context) schema.StatusSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Snapshot
}

// Routes returns a copy of recorded route requests.
func (r *StubRouter) Routes() []schema.RouteRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]schema.RouteRequest(nil), r.routes...)
}

// Escalations returns a copy of recorded escalations.
func (r *StubRouter) Escalations() []EscalateCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]EscalateCall(nil), r.escalations...)
}

// StubRunner returns a fixed result and records requests.
type StubRunner struct {
	mu       sync.Mutex
	Result   schema.RunResult
	Err      error
	requests []schema.RunRequest
}

// Run records req and returns the configured result.
func (r *StubRunner) Run(_ context.Context, req schema.RunRequest) (schema.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	return r.Result, r.Err
}

// Requests returns a copy of recorded requests.
func (r *StubRunner) Requests() []schema.RunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]schema.RunRequest(nil), r.requests...)
}

// Notification is one recorded notification.
type Notification struct{ Title, Body string }

// RecordingNotifier appends notifications to a slice.
type RecordingNotifier struct {
	mu    sync.Mutex
	Err   error
	items []Notification
}

// Notify records the notification and returns the configured error.
func (n *RecordingNotifier) Notify(_ context.Context, title, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, Notification{Title: title, Body: body})
	return n.Err
}

// Notifications returns a copy of recorded notifications.
func (n *RecordingNotifier) Notifications() []Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Notification(nil), n.items...)
}
