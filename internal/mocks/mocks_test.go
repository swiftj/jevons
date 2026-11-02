package mocks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/swiftj/jevons/pkg/schema"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestFakeClock(t *testing.T) {
	t.Parallel()
	c := NewFakeClock(t0)
	c.Advance(time.Minute)
	if got := c.Now(); !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("Now = %v", got)
	}
	c.Set(t0)
	if !c.Now().Equal(t0) {
		t.Error("Set failed")
	}
}

func TestMemSessionsWarmBoundaries(t *testing.T) {
	t.Parallel()
	m := NewMemSessions(50*time.Minute, 100_000)
	m.Touch(schema.SessionState{SessionID: "exact", CachedTokens: 100_000, LastActivity: t0})
	m.Touch(schema.SessionState{SessionID: "warm", CachedTokens: 100_001, LastActivity: t0})
	if _, ok := m.Get("warm"); !ok {
		t.Fatal("Get(warm) missing")
	}
	if got := m.Warm(t0.Add(50*time.Minute - time.Second)); len(got) != 1 || got[0].SessionID != "warm" {
		t.Errorf("Warm(49m59s) = %+v", got)
	}
	if got := m.Warm(t0.Add(50 * time.Minute)); len(got) != 0 {
		t.Errorf("Warm(50m) = %+v", got)
	}
}

func TestStubsRecordConcurrently(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cl := &StubClassifier{Decision: schema.RoutingDecision{TaskTier: schema.Tier2}}
	rt := &StubRouter{}
	rn := &StubRunner{}
	nt := &RecordingNotifier{}
	pf := &StubPrefilter{Rules: map[string]schema.RoutingDecision{"hit": {TaskID: "x"}}}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, _ := cl.Classify(ctx, schema.RouteRequest{TaskID: "id"})
			if d.TaskID != "id" {
				t.Errorf("TaskID not copied: %q", d.TaskID)
			}
			_, _ = rt.Route(ctx, schema.RouteRequest{})
			_, _ = rt.Escalate(ctx, schema.RoutingDecision{}, errors.New("x"))
			_ = rt.Status(ctx)
			_, _ = rn.Run(ctx, schema.RunRequest{})
			_ = nt.Notify(ctx, "t", "b")
			pf.Match("hit")
		}()
	}
	wg.Wait()
	if cl.Calls() != 50 || len(cl.Requests()) != 50 || len(rt.Routes()) != 50 ||
		len(rt.Escalations()) != 50 || len(rn.Requests()) != 50 ||
		len(nt.Notifications()) != 50 || pf.Calls() != 50 {
		t.Error("call counts mismatch")
	}
}

func TestStubErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	cl := &StubClassifier{Err: boom}
	if _, err := cl.Classify(context.Background(), schema.RouteRequest{}); !errors.Is(err, boom) {
		t.Errorf("Classify err = %v", err)
	}
	u := NewStaticUsage(schema.UsageWindow{Name: "5h", Used: 8, Capacity: 10})
	ws, err := u.Windows(context.Background())
	if err != nil || len(ws) != 1 || ws[0].Ratio() != 0.8 {
		t.Errorf("Windows = %+v, %v", ws, err)
	}
	ru := &RecordingUsage{}
	ru.Add(5)
	if got := ru.Added(); len(got) != 1 || got[0] != 5 {
		t.Errorf("Added = %v", got)
	}
	u.Set(nil, boom)
	if _, err := u.Windows(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Windows err = %v", err)
	}
}
