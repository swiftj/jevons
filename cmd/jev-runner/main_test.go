package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/johnswift/jevons/internal/client"
	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/mocks"
	"github.com/johnswift/jevons/internal/proxy"
	"github.com/johnswift/jevons/pkg/schema"
)

// seqRunner returns scripted results in order and records requests.
type seqRunner struct {
	mu      sync.Mutex
	results []schema.RunResult
	errs    []error
	reqs    []schema.RunRequest
}

func (r *seqRunner) Run(_ context.Context, req schema.RunRequest) (schema.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := len(r.reqs)
	r.reqs = append(r.reqs, req)
	return r.results[i], r.errs[i]
}

var (
	errFailed = fmt.Errorf("run exited 1: %w", schema.ErrRunFailed)
	tier1     = schema.RoutingDecision{TaskID: "t1", TaskTier: schema.Tier1, SelectedModel: "claude-haiku-5-5",
		EffortLevel: schema.EffortLow, Source: schema.SourcePrefilter}
	tier2 = schema.RoutingDecision{TaskID: "t1", TaskTier: schema.Tier2, SelectedModel: "claude-sonnet-5-5",
		EffortLevel: schema.EffortMedium, Source: schema.SourceEscalation}
)

// daemon starts a real proxy server backed by a stub router and a usage recorder.
func daemon(t *testing.T, r *mocks.StubRouter) (string, *mocks.RecordingUsage) {
	t.Helper()
	usage := &mocks.RecordingUsage{}
	srv := proxy.NewServer(config.Default(), r, nil).WithFeeds(proxy.Feeds{Usage: usage})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://"), usage
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func newFlow(addr string, rn schema.Runner) (*flow, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &flow{cfg: config.Default(), runner: rn, daemon: client.New(addr, nil), out: &out, errOut: &errOut}, &out, &errOut
}

func routed() options { return options{skill: "/cleanup", route: true, escalate: true, report: true} }

func TestEscalatesFailedTier1(t *testing.T) {
	t.Parallel()
	sr := &mocks.StubRouter{Decision: tier1, Escalated: tier2}
	addr, usage := daemon(t, sr)
	rn := &seqRunner{
		results: []schema.RunResult{{ExitCode: 1, Tokens: 100}, {Tokens: 250}},
		errs:    []error{errFailed, nil},
	}
	f, out, errOut := newFlow(addr, rn)
	code, err := f.execute(context.Background(), routed())
	if code != 0 || err != nil {
		t.Fatalf("execute = %d, %v (stderr %s)", code, err, errOut)
	}
	if len(rn.reqs) != 2 || rn.reqs[0].Model != "claude-haiku-5-5" || rn.reqs[1].Model != "claude-sonnet-5-5" {
		t.Errorf("runs %+v", rn.reqs)
	}
	if esc := sr.Escalations(); len(esc) != 1 || esc[0].Prev.TaskID != "t1" || !strings.Contains(esc[0].Cause.Error(), "run exited 1") {
		t.Errorf("escalations %+v", esc)
	}
	if got := usage.Added(); len(got) != 2 || got[0] != 100 || got[1] != 250 {
		t.Errorf("usage %v", got)
	}
	if strings.Count(out.String(), "run_id=") != 2 || !strings.Contains(errOut.String(), "retrying on claude-sonnet-5-5") {
		t.Errorf("stdout %q stderr %q", out, errOut)
	}
}

func TestEscalationFailsTwice(t *testing.T) {
	t.Parallel()
	addr, _ := daemon(t, &mocks.StubRouter{Decision: tier1, Escalated: tier2})
	rn := &seqRunner{
		results: []schema.RunResult{{ExitCode: 1}, {ExitCode: 4}},
		errs:    []error{errFailed, errFailed},
	}
	f, _, _ := newFlow(addr, rn)
	code, err := f.execute(context.Background(), routed())
	if code != 4 || !errors.Is(err, schema.ErrRunFailed) || len(rn.reqs) != 2 {
		t.Fatalf("execute = %d, %v, runs %d", code, err, len(rn.reqs))
	}
}

func TestNoEscalation(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		decision schema.RoutingDecision
		opts     func(*options)
		err      error
	}{
		"tier 2 decision":   {tier2, func(*options) {}, errFailed},
		"escalate disabled": {tier1, func(o *options) { o.escalate = false }, errFailed},
		"explicit tier":     {tier1, func(o *options) { o.route = false; o.tier = "TIER_1" }, errFailed},
		"timeout":           {tier1, func(*options) {}, schema.ErrTimeout},
	}
	for name, tc := range cases {
		addr, _ := daemon(t, &mocks.StubRouter{Decision: tc.decision, Escalated: tier2})
		rn := &seqRunner{results: []schema.RunResult{{ExitCode: 1}}, errs: []error{tc.err}}
		f, _, _ := newFlow(addr, rn)
		o := routed()
		tc.opts(&o)
		if code, _ := f.execute(context.Background(), o); code != 1 || len(rn.reqs) != 1 {
			t.Errorf("%s: code %d, runs %d", name, code, len(rn.reqs))
		}
	}
}

func TestEscalateFallsBackLocally(t *testing.T) {
	t.Parallel()
	addr, _ := daemon(t, &mocks.StubRouter{Decision: tier1, EscalateErr: errors.New("already escalated")})
	rn := &seqRunner{results: []schema.RunResult{{ExitCode: 1}, {}}, errs: []error{errFailed, nil}}
	f, _, errOut := newFlow(addr, rn)
	if code, err := f.execute(context.Background(), routed()); code != 0 || err != nil {
		t.Fatalf("execute = %d, %v", code, err)
	}
	if rn.reqs[1].Model != config.Default().ModelFor(schema.Tier2) || !strings.Contains(errOut.String(), "using local TIER_2") {
		t.Errorf("model %q stderr %q", rn.reqs[1].Model, errOut)
	}
}

func TestDaemonUnreachable(t *testing.T) {
	t.Parallel()
	rn := &seqRunner{results: []schema.RunResult{{Tokens: 9}}, errs: []error{nil}}
	f, _, errOut := newFlow(closedAddr(t), rn)
	if code, err := f.execute(context.Background(), routed()); code != 0 || err != nil {
		t.Fatalf("execute = %d, %v", code, err)
	}
	if rn.reqs[0].Model != "claude-sonnet-5-5" {
		t.Errorf("model %q", rn.reqs[0].Model)
	}
	// Routing fallback warns; the unreachable usage report stays silent.
	if s := errOut.String(); !strings.Contains(s, "router unavailable") || strings.Contains(s, "usage not reported") {
		t.Errorf("stderr %q", s)
	}
}

func TestUsageReporting(t *testing.T) {
	t.Parallel()
	addr, usage := daemon(t, &mocks.StubRouter{})
	for _, report := range []bool{true, false} {
		rn := &seqRunner{results: []schema.RunResult{{Tokens: 7}}, errs: []error{nil}}
		f, _, _ := newFlow(addr, rn)
		if _, err := f.execute(context.Background(), options{skill: "/s", report: report}); err != nil {
			t.Fatal(err)
		}
	}
	if got := usage.Added(); len(got) != 1 || got[0] != 7 {
		t.Errorf("usage %v", got)
	}

	// A daemon without a usage feed answers 501, which is worth a warning.
	ts := httptest.NewServer(proxy.NewServer(config.Default(), &mocks.StubRouter{}, nil).Handler())
	t.Cleanup(ts.Close)
	rn := &seqRunner{results: []schema.RunResult{{Tokens: 7}}, errs: []error{nil}}
	f, _, errOut := newFlow(strings.TrimPrefix(ts.URL, "http://"), rn)
	if _, err := f.execute(context.Background(), options{skill: "/s", report: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "usage not reported") {
		t.Errorf("stderr %q", errOut)
	}
}

func TestResolveModel(t *testing.T) {
	t.Parallel()
	f, _, _ := newFlow(closedAddr(t), nil)
	ctx := context.Background()
	if m, d, err := f.resolveModel(ctx, options{model: "x", tier: "TIER_3"}, ""); m != "x" || d != nil || err != nil {
		t.Errorf("model flag: %q %v %v", m, d, err)
	}
	if m, _, _ := f.resolveModel(ctx, options{tier: "TIER_3"}, ""); m != "claude-opus-5-5" {
		t.Errorf("tier flag: %q", m)
	}
	if _, _, err := f.resolveModel(ctx, options{tier: "TIER_9"}, ""); err == nil {
		t.Error("bad tier: expected error")
	}
	if m, _, _ := f.resolveModel(ctx, options{}, ""); m != "" || displayModel(m) != "(claude default)" {
		t.Errorf("default: %q", m)
	}
}

func TestRunFlags(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if code, err := run([]string{"-version"}, &out, &errOut); code != 0 || err != nil || out.String() != "dev\n" {
		t.Errorf("-version: %d %v %q", code, err, out.String())
	}
	if code, err := run(nil, &out, &errOut); code != 2 || err == nil {
		t.Errorf("missing skill: %d %v", code, err)
	}
	if code, _ := run([]string{"-nope"}, &out, &errOut); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
	if code, _ := run([]string{"-skill=/s", "-config=/does/not/exist.json"}, &out, &errOut); code != 1 {
		t.Errorf("bad config: %d", code)
	}
}
