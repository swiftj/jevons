package router

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/internal/mocks"
	"github.com/swiftj/jevons/pkg/schema"
)

var epoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type fixture struct {
	cfg      config.Config
	clock    *mocks.FakeClock
	pre      *mocks.StubPrefilter
	cls      *mocks.StubClassifier
	sessions *mocks.MemSessions
	usage    *mocks.StaticUsage
	engine   *Engine
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := config.Default()
	f := &fixture{
		cfg:   cfg,
		clock: mocks.NewFakeClock(epoch),
		pre: &mocks.StubPrefilter{Rules: map[string]schema.RoutingDecision{
			"git status": {TaskTier: schema.Tier1, EffortLevel: schema.EffortLow, Rationale: "rule:git"},
			"t2 rule":    {TaskTier: schema.Tier2, Rationale: "rule:t2"},
		}},
		cls: &mocks.StubClassifier{Decision: schema.RoutingDecision{
			TaskTier: schema.Tier2, SelectedModel: cfg.ModelFor(schema.Tier2),
			EffortLevel: schema.EffortMedium, Rationale: "general coding",
		}},
		sessions: mocks.NewMemSessions(cfg.WarmIdleMax, cfg.WarmMinTokens),
		usage:    mocks.NewStaticUsage(),
	}
	f.engine = New(cfg, Deps{Prefilter: f.pre, Classifier: f.cls, Sessions: f.sessions, Usage: f.usage, Clock: f.clock})
	return f
}

func TestRouteEmptyPrompt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.engine.Route(context.Background(), schema.RouteRequest{}); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("err = %v, want ErrEmptyPrompt", err)
	}
}

func TestRouteGeneratesAndKeepsTaskID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "refactor this"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.TaskID) != 16 {
		t.Errorf("generated TaskID = %q", d.TaskID)
	}
	d, err = f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "refactor this", TaskID: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskID != "mine" {
		t.Errorf("TaskID = %q, want mine", d.TaskID)
	}
}

func TestPrefilterShortCircuits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.sessions.Touch(schema.SessionState{SessionID: "s", Model: f.cfg.ModelFor(schema.Tier3), CachedTokens: 500_000, LastActivity: epoch})
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "git status", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if f.cls.Calls() != 0 {
		t.Errorf("classifier calls = %d, want 0", f.cls.Calls())
	}
	if d.Source != schema.SourcePrefilter || d.TaskTier != schema.Tier1 {
		t.Errorf("decision = %+v", d)
	}
	if d.SelectedModel != f.cfg.ModelFor(schema.Tier1) {
		t.Errorf("model = %q", d.SelectedModel)
	}
	if d.SessionID != "s" {
		t.Errorf("session = %q", d.SessionID)
	}
}

func TestPrefilterTier2HitIsThrottled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.usage.Set([]schema.UsageWindow{{Name: "5h", Used: 90, Capacity: 100}}, nil)
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "t2 rule"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskTier != schema.Tier1 || d.Source != schema.SourceThrottle || d.EffortLevel != schema.EffortMedium {
		t.Errorf("decision = %+v", d)
	}
}

func TestWarmthBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tokens int64
		idle   time.Duration
		warm   bool
	}{
		{"tokens exactly 100k", 100_000, time.Minute, false},
		{"tokens 100001", 100_001, time.Minute, true},
		{"idle 49m59s", 200_000, 49*time.Minute + 59*time.Second, true},
		{"idle 50m", 200_000, 50 * time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			model := f.cfg.ModelFor(schema.Tier3)
			f.sessions.Touch(schema.SessionState{SessionID: "s", Model: model, CachedTokens: tc.tokens, LastActivity: epoch.Add(-tc.idle)})
			d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "explain", SessionID: "s"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.warm {
				if d.Source != schema.SourceCache || d.SelectedModel != model || d.TaskTier != schema.Tier3 || d.SessionID != "s" {
					t.Errorf("want warm reuse, got %+v", d)
				}
				if f.cls.Calls() != 0 {
					t.Errorf("classifier called on warm path")
				}
			} else if d.Source != schema.SourceClassifier {
				t.Errorf("want classifier, got %+v", d)
			}
		})
	}
}

func TestWarmUnknownModelIsTier2(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.sessions.Touch(schema.SessionState{SessionID: "s", Model: "custom-model", CachedTokens: 200_000, LastActivity: epoch})
	// Warm decisions are terminal and never throttled.
	f.usage.Set([]schema.UsageWindow{{Name: "5h", Used: 99, Capacity: 100}}, nil)
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "x", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskTier != schema.Tier2 || d.SelectedModel != "custom-model" || d.Source != schema.SourceCache {
		t.Errorf("decision = %+v", d)
	}
}

func TestUnknownSessionFallsThrough(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "x", SessionID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != schema.SourceClassifier {
		t.Errorf("source = %s", d.Source)
	}
}

func TestClassifierErrorFallback(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.cls.Err = schema.ErrClassifierUnavailable
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskTier != schema.Tier2 || d.Rationale != RationaleClassifierUnavailable || d.SelectedModel != f.cfg.ModelFor(schema.Tier2) {
		t.Errorf("decision = %+v", d)
	}
}

func TestClassifierInvalidDecisionFallback(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.cls.Decision = schema.RoutingDecision{TaskTier: "TIER_9"}
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Rationale != RationaleClassifierUnavailable {
		t.Errorf("decision = %+v", d)
	}
}

func TestClassifierDecisionNormalized(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.cls.Decision = schema.RoutingDecision{TaskID: "other", TaskTier: schema.Tier3}
	d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "x", TaskID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskID != "t" || d.SelectedModel != f.cfg.ModelFor(schema.Tier3) || d.EffortLevel != schema.EffortHigh || d.Source != schema.SourceClassifier {
		t.Errorf("decision = %+v", d)
	}
}

func TestNilDeps(t *testing.T) {
	t.Parallel()
	e := New(config.Default(), Deps{})
	d, err := e.Route(context.Background(), schema.RouteRequest{Prompt: "x", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskTier != schema.Tier2 || d.Rationale != RationaleClassifierUnavailable {
		t.Errorf("decision = %+v", d)
	}
	s := e.Status(context.Background())
	if s.RoutedTotal != 1 || s.WarmSessions == nil || s.Usage == nil {
		t.Errorf("status = %+v", s)
	}
}

func TestRouteInvalidConfigFails(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Models = map[schema.Tier]string{}
	e := New(cfg, Deps{Clock: mocks.NewFakeClock(epoch)})
	if _, err := e.Route(context.Background(), schema.RouteRequest{Prompt: "x"}); !errors.Is(err, schema.ErrInvalidDecision) {
		t.Fatalf("err = %v", err)
	}
	if got := e.Status(context.Background()).RoutedTotal; got != 0 {
		t.Errorf("routed = %d, want 0", got)
	}
}

func TestThrottle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		used      int64
		tier      schema.Tier
		essential bool
		usageErr  error
		wantTier  schema.Tier
		throttled bool
	}{
		{"79% not throttled", 79, schema.Tier2, false, nil, schema.Tier2, false},
		{"80% throttled", 80, schema.Tier2, false, nil, schema.Tier1, true},
		{"essential exempt", 95, schema.Tier2, true, nil, schema.Tier2, false},
		{"tier3 untouched", 95, schema.Tier3, false, nil, schema.Tier3, false},
		{"tier1 untouched", 95, schema.Tier1, false, nil, schema.Tier1, false},
		{"usage error no throttle", 95, schema.Tier2, false, errors.New("boom"), schema.Tier2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.cls.Decision = schema.RoutingDecision{TaskTier: tc.tier, Rationale: "r"}
			f.usage.Set([]schema.UsageWindow{
				{Name: "7d", Used: 0, Capacity: 0},
				{Name: "5h", Used: tc.used, Capacity: 100},
			}, tc.usageErr)
			d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: "x", Essential: tc.essential})
			if err != nil {
				t.Fatal(err)
			}
			if d.TaskTier != tc.wantTier {
				t.Errorf("tier = %s, want %s", d.TaskTier, tc.wantTier)
			}
			if tc.throttled {
				if d.Source != schema.SourceThrottle || d.Rationale != "r; throttled:5h" || d.SelectedModel != f.cfg.ModelFor(schema.Tier1) {
					t.Errorf("decision = %+v", d)
				}
			} else if d.Source == schema.SourceThrottle {
				t.Errorf("unexpected throttle: %+v", d)
			}
			if tc.usageErr != nil {
				if got := f.engine.Status(context.Background()).UsageErrors; got != 2 {
					t.Errorf("usage errors = %d, want 2 (route + status)", got)
				}
			}
		})
	}
}

func TestEscalate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	prev, err := f.engine.Route(ctx, schema.RouteRequest{Prompt: "git status", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New(strings.Repeat("x", 300))
	d, err := f.engine.Escalate(ctx, prev, cause)
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskID != prev.TaskID || d.TaskTier != schema.Tier2 || d.Source != schema.SourceEscalation || d.SessionID != "s" {
		t.Errorf("decision = %+v", d)
	}
	if want := "escalated: " + strings.Repeat("x", 120); d.Rationale != want {
		t.Errorf("rationale len = %d", len(d.Rationale))
	}

	// Double escalation rejected.
	got, err := f.engine.Escalate(ctx, prev, cause)
	if !errors.Is(err, ErrAlreadyEscalated) || got != prev {
		t.Errorf("double escalate: %v, %+v", err, got)
	}
	// Tier 2 input rejected.
	got, err = f.engine.Escalate(ctx, d, cause)
	if !errors.Is(err, ErrNotEscalatable) || got != d {
		t.Errorf("tier2 escalate: %v", err)
	}
	// Empty TaskID rejected.
	if _, err := f.engine.Escalate(ctx, schema.RoutingDecision{TaskTier: schema.Tier1}, nil); !errors.Is(err, schema.ErrInvalidDecision) {
		t.Errorf("empty id: %v", err)
	}
	// Nil cause, not throttled even when over quota.
	f.usage.Set([]schema.UsageWindow{{Name: "5h", Used: 100, Capacity: 100}}, nil)
	d, err = f.engine.Escalate(ctx, schema.RoutingDecision{TaskID: "n", TaskTier: schema.Tier1}, nil)
	if err != nil || d.Rationale != "escalated: unknown" || d.TaskTier != schema.Tier2 {
		t.Errorf("nil cause: %v %+v", err, d)
	}

	s := f.engine.Status(ctx)
	if s.BySource[schema.SourceEscalation] != 2 || s.RoutedTotal != 3 {
		t.Errorf("status = %+v", s)
	}
}

func TestEscalateInvalidConfig(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Models = map[schema.Tier]string{}
	e := New(cfg, Deps{})
	prev := schema.RoutingDecision{TaskID: "a", TaskTier: schema.Tier1}
	if got, err := e.Escalate(context.Background(), prev, nil); !errors.Is(err, schema.ErrInvalidDecision) || got != prev {
		t.Errorf("err = %v", err)
	}
}

func TestEscalationRuneTruncation(t *testing.T) {
	t.Parallel()
	s := summarize(errors.New(strings.Repeat("é", 130)))
	if n := len([]rune(s)); n != maxCauseLen {
		t.Errorf("runes = %d", n)
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.sessions.Touch(schema.SessionState{SessionID: "w", Model: "m", CachedTokens: 200_000, LastActivity: epoch})
	f.usage.Set([]schema.UsageWindow{{Name: "5h", Used: 85, Capacity: 100}}, nil)
	for _, p := range []string{"git status", "x", "y"} {
		if _, err := f.engine.Route(ctx, schema.RouteRequest{Prompt: p}); err != nil {
			t.Fatal(err)
		}
	}
	f.clock.Advance(time.Hour)
	s := f.engine.Status(ctx)
	if s.RoutedTotal != 3 || s.BySource[schema.SourcePrefilter] != 1 || s.BySource[schema.SourceThrottle] != 2 || s.ByTier[schema.Tier1] != 3 {
		t.Errorf("counts = %+v", s)
	}
	if !s.Throttled || len(s.Usage) != 1 || s.Uptime != "1h0m0s" {
		t.Errorf("status = %+v", s)
	}
	if len(s.WarmSessions) != 0 {
		t.Errorf("warm after 1h = %+v", s.WarmSessions)
	}
	// Mutating the snapshot must not leak into the engine.
	s.BySource[schema.SourcePrefilter] = 999
	s.Usage[0].Used = 0
	s2 := f.engine.Status(ctx)
	if s2.BySource[schema.SourcePrefilter] != 1 || s2.Usage[0].Used != 85 {
		t.Errorf("snapshot leaked: %+v", s2)
	}
	// Usage failure keeps last-known windows.
	f.usage.Set(nil, errors.New("down"))
	s3 := f.engine.Status(ctx)
	if s3.UsageErrors != 1 || len(s3.Usage) != 1 || !s3.Throttled {
		t.Errorf("status after usage error = %+v", s3)
	}
}

func TestConcurrentRoute(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const goroutines, perG = 100, 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				p := "x"
				if i%2 == 0 {
					p = "git status"
				}
				d, err := f.engine.Route(context.Background(), schema.RouteRequest{Prompt: p})
				if err != nil {
					t.Error(err)
					return
				}
				if g%10 == 0 && i%100 == 1 {
					if _, err := f.engine.Escalate(context.Background(), schema.RoutingDecision{TaskID: d.TaskID, TaskTier: schema.Tier1}, nil); err != nil {
						t.Error(err)
						return
					}
					_ = f.engine.Status(context.Background()) // exercised for races only
				}
			}
		}(g)
	}
	wg.Wait()
	s := f.engine.Status(context.Background())
	const routes = goroutines * perG
	const escalations = 10 * 10
	if s.RoutedTotal != routes+escalations {
		t.Errorf("total = %d", s.RoutedTotal)
	}
	if s.BySource[schema.SourcePrefilter] != routes/2 || s.BySource[schema.SourceClassifier] != routes/2 || s.BySource[schema.SourceEscalation] != escalations {
		t.Errorf("by source = %v", s.BySource)
	}
	if s.ByTier[schema.Tier1] != routes/2 || s.ByTier[schema.Tier2] != routes/2+escalations {
		t.Errorf("by tier = %v", s.ByTier)
	}
}

func TestLRU(t *testing.T) {
	t.Parallel()
	l := newLRU(0)
	if !l.add("a") || l.add("a") {
		t.Fatal("add semantics")
	}
	if !l.add("b") || l.len() != 1 {
		t.Fatalf("len = %d", l.len())
	}
	if !l.add("a") {
		t.Error("evicted key should be re-addable")
	}
	big := newLRU(EscalationLRUSize)
	for i := 0; i < EscalationLRUSize+5; i++ {
		big.add(schema.NewID())
	}
	if big.len() != EscalationLRUSize {
		t.Errorf("len = %d", big.len())
	}
}

func BenchmarkRoutePrefilterHit(b *testing.B) {
	cfg := config.Default()
	e := New(cfg, Deps{
		Prefilter:  &mocks.StubPrefilter{Rules: map[string]schema.RoutingDecision{"git status": {TaskTier: schema.Tier1}}},
		Classifier: &mocks.StubClassifier{},
		Sessions:   mocks.NewMemSessions(cfg.WarmIdleMax, cfg.WarmMinTokens),
		Usage:      mocks.NewStaticUsage(),
		Clock:      schema.SystemClock{},
	})
	req := schema.RouteRequest{Prompt: "git status"}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRouteClassifier(b *testing.B) {
	cfg := config.Default()
	e := New(cfg, Deps{
		Classifier: &mocks.StubClassifier{Decision: schema.RoutingDecision{TaskTier: schema.Tier2}},
		Usage:      mocks.NewStaticUsage(schema.UsageWindow{Name: "5h", Used: 1, Capacity: 100}),
	})
	req := schema.RouteRequest{Prompt: "x"}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Route(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}
