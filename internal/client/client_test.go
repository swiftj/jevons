package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johnswift/jevons/pkg/schema"
)

func decision(tier schema.Tier) schema.RoutingDecision {
	return schema.RoutingDecision{TaskID: "t", TaskTier: tier, SelectedModel: "m", EffortLevel: schema.EffortLow}
}

// newDaemon starts a fake daemon; handler gets the decoded JSON body.
func newDaemon(t *testing.T, h func(path string, body map[string]any) (int, any)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("bad request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		status, out := h(r.URL.Path, body)
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return New(strings.TrimPrefix(srv.URL, "http://"), nil)
}

func TestRoute(t *testing.T) {
	t.Parallel()
	c := newDaemon(t, func(path string, body map[string]any) (int, any) {
		if path != "/route" || body["prompt"] != "hi" {
			t.Errorf("path %s body %v", path, body)
		}
		return 200, decision(schema.Tier1)
	})
	d, err := c.Route(context.Background(), schema.RouteRequest{Prompt: "hi"})
	if err != nil || d.TaskTier != schema.Tier1 {
		t.Fatalf("Route = %+v, %v", d, err)
	}
}

func TestRouteInvalidDecision(t *testing.T) {
	t.Parallel()
	c := newDaemon(t, func(string, map[string]any) (int, any) { return 200, map[string]string{"task_id": "x"} })
	if _, err := c.Route(context.Background(), schema.RouteRequest{Prompt: "hi"}); !errors.Is(err, schema.ErrInvalidDecision) {
		t.Fatalf("err = %v", err)
	}
}

func TestEscalate(t *testing.T) {
	t.Parallel()
	c := newDaemon(t, func(path string, body map[string]any) (int, any) {
		dec, _ := body["decision"].(map[string]any)
		if path != "/escalate" || body["cause"] != "boom" || dec["task_tier"] != "TIER_1" {
			t.Errorf("path %s body %v", path, body)
		}
		return 200, decision(schema.Tier2)
	})
	d, err := c.Escalate(context.Background(), decision(schema.Tier1), "boom")
	if err != nil || d.TaskTier != schema.Tier2 {
		t.Fatalf("Escalate = %+v, %v", d, err)
	}
}

func TestReportUsage(t *testing.T) {
	t.Parallel()
	c := newDaemon(t, func(path string, body map[string]any) (int, any) {
		if path != "/usage" || body["tokens"] != float64(42) {
			t.Errorf("path %s body %v", path, body)
		}
		return 200, map[string]bool{"ok": true}
	})
	if err := c.ReportUsage(context.Background(), 42); err != nil {
		t.Fatalf("ReportUsage: %v", err)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	c := newDaemon(t, func(string, map[string]any) (int, any) {
		return 400, map[string]string{"error": "tokens out of range"}
	})
	err := c.ReportUsage(context.Background(), 0)
	if err == nil || errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "400: tokens out of range") {
		t.Errorf("status err = %v", err)
	}

	bad := newDaemon(t, func(string, map[string]any) (int, any) { return 200, "not an object" })
	if _, err := bad.Route(context.Background(), schema.RouteRequest{Prompt: "x"}); err == nil || errors.Is(err, ErrUnreachable) {
		t.Errorf("decode err = %v", err)
	}

	// A closed port is unreachable.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := New(addr, nil).ReportUsage(context.Background(), 1); !errors.Is(err, ErrUnreachable) {
		t.Errorf("unreachable err = %v", err)
	}
	if err := New(addr, nil).post(context.Background(), "/x", func() {}, nil); err == nil {
		t.Error("expected encode error")
	}
}
