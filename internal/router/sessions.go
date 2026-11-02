package router

import (
	"sort"
	"sync"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/pkg/schema"
)

// Compile-time interface check.
var _ schema.SessionTracker = (*SessionTracker)(nil)

// SessionTracker is the production in-memory schema.SessionTracker. Sessions
// idle longer than 2x WarmIdleMax are evicted lazily on Touch. It is safe for
// concurrent use.
type SessionTracker struct {
	clock     schema.Clock
	idleMax   time.Duration
	minTokens int64

	mu        sync.RWMutex
	sessions  map[string]schema.SessionState
	lastSweep time.Time
}

// NewSessionTracker returns an empty tracker using cfg's warmth thresholds.
// A nil clock defaults to schema.SystemClock.
func NewSessionTracker(cfg config.Config, clock schema.Clock) *SessionTracker {
	if clock == nil {
		clock = schema.SystemClock{}
	}
	return &SessionTracker{
		clock:     clock,
		idleMax:   cfg.WarmIdleMax,
		minTokens: cfg.WarmMinTokens,
		sessions:  map[string]schema.SessionState{},
		lastSweep: clock.Now(),
	}
}

// Touch upserts a session. Empty SessionIDs are ignored; a zero LastActivity
// is stamped with the clock's current time. Stale sessions are swept at most
// once per WarmIdleMax.
func (t *SessionTracker) Touch(s schema.SessionState) {
	if s.SessionID == "" {
		return
	}
	now := t.clock.Now()
	if s.LastActivity.IsZero() {
		s.LastActivity = now
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[s.SessionID] = s
	if now.Sub(t.lastSweep) >= t.idleMax {
		t.evictLocked(now)
		t.lastSweep = now
	}
}

// evictLocked removes sessions idle longer than 2x idleMax. Caller holds mu.
func (t *SessionTracker) evictLocked(now time.Time) {
	limit := 2 * t.idleMax
	for id, s := range t.sessions {
		if now.Sub(s.LastActivity) > limit {
			delete(t.sessions, id)
		}
	}
}

// Get returns a session by ID.
func (t *SessionTracker) Get(id string) (schema.SessionState, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.sessions[id]
	return s, ok
}

// Warm returns sessions with CachedTokens > WarmMinTokens and idle < WarmIdleMax
// at now, sorted by SessionID. The result is never nil.
func (t *SessionTracker) Warm(now time.Time) []schema.SessionState {
	t.mu.RLock()
	out := make([]schema.SessionState, 0, len(t.sessions))
	for _, s := range t.sessions {
		if isWarm(s, now, t.minTokens, t.idleMax) {
			out = append(out, s)
		}
	}
	t.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// Len returns the number of tracked sessions.
func (t *SessionTracker) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.sessions)
}
