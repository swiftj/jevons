package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swiftj/jevons/internal/proxy"
	"github.com/swiftj/jevons/pkg/schema"
)

// syncBuffer is a goroutine-safe writer for daemon logs.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeAddr(t *testing.T) string {
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

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestDaemonEndToEnd(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_CAPACITY_5H", "1000")
	addr := freeAddr(t)
	base := "http://" + addr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runRouter(ctx, []string{"-addr", addr}, &bytes.Buffer{}, logs) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not start; logs: %s", logs)
		}
		time.Sleep(20 * time.Millisecond)
	}

	var d schema.RoutingDecision
	if err := json.NewDecoder(post(t, base+"/route", `{"prompt":"/cleanup src"}`).Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.Source != schema.SourcePrefilter || d.TaskTier != schema.Tier1 {
		t.Errorf("route = %+v", d)
	}
	if resp := post(t, base+"/usage", `{"tokens":900}`); resp.StatusCode != http.StatusOK {
		t.Errorf("usage status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(post(t, base+"/route", `{"prompt":"write a parser"}`).Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.Source != schema.SourceThrottle {
		t.Errorf("expected throttled decision, got %+v", d)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("daemon did not shut down")
	}
	for _, want := range []string{"API key not set", "jev-router starting"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q: %s", want, logs)
		}
	}
	if strings.Contains(logs.String(), "write a parser") {
		t.Error("prompt leaked into logs")
	}
}

func TestRunFlagsAndErrors(t *testing.T) {
	ctx := context.Background()
	var out, errOut bytes.Buffer
	if err := runRouter(ctx, []string{"-version"}, &out, &errOut); err != nil || out.String() != "dev\n" {
		t.Errorf("-version: %v %q", err, out.String())
	}
	if err := runRouter(ctx, []string{"-nope"}, &out, &errOut); err == nil {
		t.Error("bad flag: expected error")
	}
	if err := runRouter(ctx, []string{"-config", "/does/not/exist.json"}, &out, &errOut); err == nil {
		t.Error("bad config: expected error")
	}
	if err := runRouter(ctx, []string{"-addr", "0.0.0.0:0"}, &out, &errOut); !errors.Is(err, proxy.ErrNonLoopback) {
		t.Errorf("non-loopback: %v", err)
	}
}

func TestDaemonRoutesWithJev(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "ts-test-key-not-real")
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer ts-test-key-not-real" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"model":"jev-1.14.0","answers":{
"task_tier":{"type":"choice","choice":"TIER_3","confidence":0.9},
"effort":{"type":"score","score":2,"confidence":0.9}}}`) // test server write
	}))
	defer jev.Close()
	t.Setenv("JEV_TYPESAFE_BASE_URL", jev.URL)
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runRouter(ctx, []string{"-addr", addr}, &bytes.Buffer{}, logs) }()

	var d schema.RoutingDecision
	deadline := time.Now().Add(2 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/route",
			strings.NewReader(`{"prompt":"design a sharded cache layer for the billing service"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			derr := json.NewDecoder(resp.Body).Decode(&d)
			resp.Body.Close()
			if derr != nil {
				t.Fatalf("decode: %v", derr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never answered: %v\n%s", err, logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.Source != schema.SourceJev || d.TaskTier != schema.Tier3 || d.EffortLevel != schema.EffortHigh {
		t.Errorf("decision = %+v\n%s", d, logs)
	}
	if !strings.Contains(logs.String(), "jev classifier enabled") || strings.Contains(logs.String(), "ts-test-key") {
		t.Errorf("logs:\n%s", logs)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("runRouter: %v", err)
	}
}
