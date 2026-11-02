package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

// Usage window names and spans reported by CountingUsage.
const (
	// Window5h is the name of the 5-hour rolling window.
	Window5h = "5h"
	// Window7d is the name of the 7-day rolling window.
	Window7d = "7d"
	// Span5h is the length of the 5-hour rolling window.
	Span5h = 5 * time.Hour
	// Span7d is the length of the 7-day rolling window.
	Span7d = 7 * 24 * time.Hour
)

// Compile-time interface check.
var _ schema.UsageSource = (*CountingUsage)(nil)

type usageEntry struct {
	at     time.Time
	tokens int64
}

// CountingUsage is an in-memory schema.UsageSource that sums timestamped token
// usage over 5h and 7d rolling windows. It is safe for concurrent use.
type CountingUsage struct {
	clock schema.Clock
	cap5h int64
	cap7d int64

	mu      sync.Mutex
	entries []usageEntry // ordered by insertion time
}

// NewCountingUsage returns an empty source with capacities cfg.Capacity5h and
// cfg.Capacity7d. A nil clock defaults to schema.SystemClock.
func NewCountingUsage(cfg config.Config, clock schema.Clock) *CountingUsage {
	if clock == nil {
		clock = schema.SystemClock{}
	}
	return &CountingUsage{clock: clock, cap5h: cfg.Capacity5h, cap7d: cfg.Capacity7d}
}

// Add records tokens used at the clock's current time. Non-positive values
// are ignored.
func (u *CountingUsage) Add(tokens int64) {
	if tokens <= 0 {
		return
	}
	now := u.clock.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.entries = append(u.entries, usageEntry{at: now, tokens: tokens})
}

// Windows returns the 5h and 7d windows, pruning entries older than 7 days.
// An entry is inside a window when now - at < span.
func (u *CountingUsage) Windows(ctx context.Context) ([]schema.UsageWindow, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("router: usage windows: %w", err)
	}
	now := u.clock.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	keep := u.entries[:0]
	var used5h, used7d int64
	for _, e := range u.entries {
		age := now.Sub(e.at)
		if age >= Span7d {
			continue
		}
		keep = append(keep, e)
		used7d += e.tokens
		if age < Span5h {
			used5h += e.tokens
		}
	}
	for i := len(keep); i < len(u.entries); i++ {
		u.entries[i] = usageEntry{}
	}
	u.entries = keep
	return []schema.UsageWindow{
		{Name: Window5h, Used: used5h, Capacity: u.cap5h},
		{Name: Window7d, Used: used7d, Capacity: u.cap7d},
	}, nil
}
