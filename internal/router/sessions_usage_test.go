package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/mocks"
	"github.com/johnswift/jevons/pkg/schema"
)

func TestSessionTrackerWarmBoundaries(t *testing.T) {
	t.Parallel()
	clock := mocks.NewFakeClock(epoch)
	tr := NewSessionTracker(config.Default(), clock)
	tr.Touch(schema.SessionState{SessionID: "exact", CachedTokens: 100_000, LastActivity: epoch})
	tr.Touch(schema.SessionState{SessionID: "b-warm", CachedTokens: 100_001, LastActivity: epoch.Add(-(49*time.Minute + 59*time.Second))})
	tr.Touch(schema.SessionState{SessionID: "a-warm", CachedTokens: 100_001, LastActivity: epoch})
	tr.Touch(schema.SessionState{SessionID: "idle50", CachedTokens: 200_000, LastActivity: epoch.Add(-50 * time.Minute)})
	tr.Touch(schema.SessionState{}) // ignored

	warm := tr.Warm(epoch)
	if len(warm) != 2 || warm[0].SessionID != "a-warm" || warm[1].SessionID != "b-warm" {
		t.Fatalf("warm = %+v", warm)
	}
	if got := tr.Warm(epoch.Add(24 * time.Hour)); got == nil || len(got) != 0 {
		t.Errorf("warm later = %+v", got)
	}
	if s, ok := tr.Get("exact"); !ok || s.CachedTokens != 100_000 {
		t.Errorf("get = %+v %v", s, ok)
	}
	if _, ok := tr.Get("nope"); ok {
		t.Error("get missing")
	}
	if tr.Len() != 4 {
		t.Errorf("len = %d", tr.Len())
	}
}

func TestSessionTrackerStampsZeroActivity(t *testing.T) {
	t.Parallel()
	clock := mocks.NewFakeClock(epoch)
	tr := NewSessionTracker(config.Default(), clock)
	tr.Touch(schema.SessionState{SessionID: "s"})
	if s, _ := tr.Get("s"); !s.LastActivity.Equal(epoch) {
		t.Errorf("last activity = %v", s.LastActivity)
	}
}

func TestSessionTrackerEviction(t *testing.T) {
	t.Parallel()
	cfg := config.Default() // WarmIdleMax 50m → evict idle > 100m
	clock := mocks.NewFakeClock(epoch)
	tr := NewSessionTracker(cfg, clock)
	tr.Touch(schema.SessionState{SessionID: "old", LastActivity: epoch})
	tr.Touch(schema.SessionState{SessionID: "edge", LastActivity: epoch.Add(10 * time.Minute)})

	clock.Advance(110 * time.Minute) // old idle 110m (> 100m), edge idle exactly 100m
	tr.Touch(schema.SessionState{SessionID: "new"})
	if _, ok := tr.Get("old"); ok {
		t.Error("old not evicted")
	}
	if _, ok := tr.Get("edge"); !ok {
		t.Error("edge (idle == 2x) evicted")
	}

	// No sweep again until WarmIdleMax has passed since the last sweep.
	clock.Advance(49 * time.Minute)
	tr.Touch(schema.SessionState{SessionID: "new"})
	if _, ok := tr.Get("edge"); !ok {
		t.Error("swept too early")
	}
	clock.Advance(time.Minute)
	tr.Touch(schema.SessionState{SessionID: "new"})
	if _, ok := tr.Get("edge"); ok {
		t.Error("edge not evicted after sweep interval")
	}
	if tr.Len() != 1 {
		t.Errorf("len = %d", tr.Len())
	}
}

func TestSessionTrackerWithEngine(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	clock := mocks.NewFakeClock(epoch)
	tr := NewSessionTracker(cfg, clock)
	tr.Touch(schema.SessionState{SessionID: "s", Model: cfg.ModelFor(schema.Tier3), CachedTokens: 150_000})
	e := New(cfg, Deps{Sessions: tr, Clock: clock})
	d, err := e.Route(context.Background(), schema.RouteRequest{Prompt: "x", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != schema.SourceCache || d.TaskTier != schema.Tier3 {
		t.Errorf("decision = %+v", d)
	}
	if ws := e.Status(context.Background()).WarmSessions; len(ws) != 1 {
		t.Errorf("warm = %+v", ws)
	}
}

func TestSessionTrackerConcurrent(t *testing.T) {
	t.Parallel()
	tr := NewSessionTracker(config.Default(), nil)
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("s%d", i%10)
				tr.Touch(schema.SessionState{SessionID: id, CachedTokens: int64(g * i)})
				tr.Get(id)
				tr.Warm(time.Now())
			}
		}(g)
	}
	wg.Wait()
	if tr.Len() != 10 {
		t.Errorf("len = %d", tr.Len())
	}
}

func TestCountingUsageWindows(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Capacity5h, cfg.Capacity7d = 1000, 10_000
	clock := mocks.NewFakeClock(epoch)
	u := NewCountingUsage(cfg, clock)
	ctx := context.Background()

	u.Add(100) // t0
	u.Add(0)   // ignored
	u.Add(-5)  // ignored
	clock.Advance(5*time.Hour - time.Second)
	u.Add(50) // t0+5h-1s

	ws, err := u.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []schema.UsageWindow{{Name: Window5h, Used: 150, Capacity: 1000}, {Name: Window7d, Used: 150, Capacity: 10_000}}
	if fmt.Sprint(ws) != fmt.Sprint(want) {
		t.Fatalf("windows = %+v", ws)
	}

	clock.Advance(time.Second) // t0 entry exactly 5h old → out of 5h window
	ws, _ = u.Windows(ctx)
	if ws[0].Used != 50 || ws[1].Used != 150 {
		t.Errorf("at 5h boundary = %+v", ws)
	}

	clock.Set(epoch.Add(Span7d - time.Nanosecond))
	ws, _ = u.Windows(ctx)
	if ws[0].Used != 0 || ws[1].Used != 150 {
		t.Errorf("just inside 7d = %+v", ws)
	}
	clock.Set(epoch.Add(Span7d)) // t0 entry pruned
	ws, _ = u.Windows(ctx)
	if ws[1].Used != 50 || len(u.entries) != 1 {
		t.Errorf("7d boundary = %+v (entries %d)", ws, len(u.entries))
	}
	clock.Advance(Span7d)
	ws, _ = u.Windows(ctx)
	if ws[1].Used != 0 || len(u.entries) != 0 {
		t.Errorf("all pruned = %+v", ws)
	}
}

func TestCountingUsageCanceledContext(t *testing.T) {
	t.Parallel()
	u := NewCountingUsage(config.Default(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := u.Windows(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestCountingUsageThrottlesEngine(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Capacity5h = 100
	clock := mocks.NewFakeClock(epoch)
	u := NewCountingUsage(cfg, clock)
	e := New(cfg, Deps{Classifier: &mocks.StubClassifier{Decision: schema.RoutingDecision{TaskTier: schema.Tier2}}, Usage: u, Clock: clock})
	u.Add(80)
	d, err := e.Route(context.Background(), schema.RouteRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != schema.SourceThrottle || d.Rationale != "; throttled:5h" {
		t.Errorf("decision = %+v", d)
	}
}

func TestCountingUsageConcurrent(t *testing.T) {
	t.Parallel()
	u := NewCountingUsage(config.Default(), mocks.NewFakeClock(epoch))
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				u.Add(1)
				if _, err := u.Windows(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	ws, err := u.Windows(context.Background())
	if err != nil || ws[0].Used != 5000 || ws[1].Used != 5000 {
		t.Errorf("windows = %+v, %v", ws, err)
	}
}
