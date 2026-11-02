package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/internal/setup"
	"github.com/swiftj/jevons/pkg/schema"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// fakeDaemon answers /route with a Tier 1 prefilter decision and counts calls.
func fakeDaemon(t *testing.T, model string) (addr string, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req schema.RouteRequest
		if r.URL.Path != "/route" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Prompt == "reject" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		d := schema.RoutingDecision{TaskID: "t1", TaskTier: schema.Tier1, SelectedModel: model,
			EffortLevel: schema.EffortLow, Source: schema.SourcePrefilter}
		_ = json.NewEncoder(w).Encode(d) // test server; write errors surface as client failures
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), calls
}

func hookCmd(t *testing.T, args []string, stdin string) (int, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, err := runHook(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), err
}

func TestPromptHookRoutesAndStores(t *testing.T) {
	t.Parallel()
	haiku := testConfig(t).Models[schema.Tier1]
	addr, calls := fakeDaemon(t, haiku)
	dir := t.TempDir()
	code, out, err := hookCmd(t, []string{"-addr", addr, "-state-dir", dir},
		`{"session_id":"sess-1","prompt":"/cleanup secret-prompt-text"}`)
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var msg map[string]string
	if err := json.Unmarshal([]byte(out), &msg); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if want := "Jev: Tier 1 → " + haiku + " (prefilter)"; msg["systemMessage"] != want {
		t.Errorf("systemMessage = %q, want %q", msg["systemMessage"], want)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d", calls.Load())
	}
	path := filepath.Join(dir, "sess-1.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("suggestion file: %v", err)
	}
	if strings.Contains(string(b), "secret-prompt-text") {
		t.Error("prompt stored in suggestion file")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", info.Mode(), err)
	}
}

func TestPromptHookSilentCases(t *testing.T) {
	t.Parallel()
	haiku := testConfig(t).Models[schema.Tier1]
	addr, calls := fakeDaemon(t, haiku)

	// A port nothing listens on: the daemon is down.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	down := ln.Addr().String()
	_ = ln.Close() // free the port so connections are refused

	cases := []struct {
		name, addr, stdin string
		wantErr, wantOut  bool
	}{
		{"empty prompt", addr, `{"session_id":"sess-1","prompt":"  "}`, false, false},
		{"daemon down", down, `{"session_id":"sess-1","prompt":"hi"}`, false, false},
		{"bad stdin", addr, `not json`, true, false},
		{"daemon rejects", addr, `{"session_id":"sess-1","prompt":"reject"}`, true, false},
		// Unsafe IDs still get the message but never become a file path.
		{"unsafe session id", addr, `{"session_id":"../x","prompt":"hi"}`, false, true},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		code, out, err := hookCmd(t, []string{"-addr", tc.addr, "-state-dir", dir, "-timeout", "2s"}, tc.stdin)
		if code != 0 || (out != "") != tc.wantOut || (err != nil) != tc.wantErr {
			t.Errorf("%s: code=%d out=%q err=%v", tc.name, code, out, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %d files", tc.name, len(entries))
		}
	}
	if calls.Load() != 2 {
		t.Errorf("daemon calls = %d, want 2", calls.Load())
	}
}

func TestStatusline(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	haiku, opus := cfg.Models[schema.Tier1], cfg.Models[schema.Tier3]
	dir := t.TempDir()
	if err := writeSuggestion(dir, "sess-1", suggestion{Tier: schema.Tier1, Model: haiku, Source: schema.SourceClassifier}); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, stdin, want string }{
		{"no suggestion", `{"session_id":"sess-2","model":{"id":"` + opus + `"}}`, "Jev: –"},
		{"matches", `{"session_id":"sess-1","model":{"id":"` + haiku + `"}}`, "Jev ✓ Tier 1"},
		{"matches with suffix", `{"session_id":"sess-1","model":{"id":"` + haiku + `[1m]"}}`, "Jev ✓ Tier 1"},
		{"differs", `{"session_id":"sess-1","model":{"id":"` + opus + `"}}`, "Jev → Tier 1 (" + haiku + ") · now Tier 3"},
		{"unknown model", `{"session_id":"sess-1","model":{"id":"other"}}`, "Jev → Tier 1 (" + haiku + ")"},
		{"no model", `{"session_id":"sess-1"}`, "Jev → Tier 1 (" + haiku + ")"},
		{"unsafe id", `{"session_id":"../sess-1"}`, "Jev: –"},
	}
	for _, tc := range cases {
		code, out, err := hookCmd(t, []string{"-statusline", "-state-dir", dir}, tc.stdin)
		if code != 0 || err != nil || out != tc.want+"\n" {
			t.Errorf("%s: code=%d err=%v out=%q want %q", tc.name, code, err, out, tc.want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readSuggestion(dir, "bad") != nil {
		t.Error("corrupt file should read as no suggestion")
	}
}

func TestPruneStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	files := map[string]time.Duration{"old.json": staleAfter + time.Hour, "fresh.json": time.Hour, "old.txt": staleAfter + time.Hour}
	for name, age := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	pruneStale(dir, now)
	for name, keep := range map[string]bool{"old.json": false, "fresh.json": true, "old.txt": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != keep {
			t.Errorf("%s: exists=%v want %v", name, err == nil, keep)
		}
	}
	pruneStale(filepath.Join(dir, "missing"), now) // no panic on a missing dir
}

func TestFlagsAndConfig(t *testing.T) {
	t.Parallel()
	if code, out, err := hookCmd(t, []string{"-version"}, ""); code != 0 || err != nil || out != "dev\n" {
		t.Errorf("-version: %d %q %v", code, out, err)
	}
	if code, _, err := hookCmd(t, []string{"-nope"}, ""); code != 1 || err == nil {
		t.Errorf("bad flag: %d %v", code, err)
	}
	if code, _, err := hookCmd(t, []string{"-config", "/does/not/exist.json"}, "{}"); code != 0 || err == nil {
		t.Errorf("bad config: %d %v", code, err)
	}
	if got := tierLabel("weird"); got != "weird" {
		t.Errorf("tierLabel = %q", got)
	}
}

func TestHookOff(t *testing.T) {
	jevDir := t.TempDir()
	t.Setenv("JEV_LOG_DIR", filepath.Join(jevDir, "logs"))
	if err := os.WriteFile(filepath.Join(jevDir, setup.OffFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	addr, calls := fakeDaemon(t, testConfig(t).Models[schema.Tier1])
	stateDir := t.TempDir()

	code, out, err := hookCmd(t, []string{"-addr", addr, "-state-dir", stateDir}, `{"session_id":"sess-1","prompt":"hi"}`)
	if code != 0 || err != nil || out != "" {
		t.Errorf("hook: code=%d err=%v out=%q", code, err, out)
	}
	if calls.Load() != 0 {
		t.Errorf("routed %d prompts while off", calls.Load())
	}
	code, out, err = hookCmd(t, []string{"-statusline", "-state-dir", stateDir}, `{"session_id":"sess-1"}`)
	if code != 0 || err != nil || out != "Jev: off\n" {
		t.Errorf("statusline: code=%d err=%v out=%q", code, err, out)
	}
}
