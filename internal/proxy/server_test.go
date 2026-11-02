package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/mocks"
	"github.com/johnswift/jevons/pkg/schema"
)

func validDecision() schema.RoutingDecision {
	return schema.RoutingDecision{
		TaskID: "t1", TaskTier: schema.Tier1, SelectedModel: "m1",
		EffortLevel: schema.EffortLow, Source: schema.SourcePrefilter,
	}
}

// syncBuffer is a goroutine-safe log sink.
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

func newTestServer(t *testing.T, r *mocks.StubRouter) (*Server, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewServer(config.Default(), r, logger), logs
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "127.0.0.1:8942"
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != contentTypeJSON {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestEndpoints(t *testing.T) {
	t.Parallel()
	dec := validDecision()
	esc := dec
	esc.TaskTier, esc.Source = schema.Tier2, schema.SourceEscalation
	routeBody, _ := json.Marshal(schema.RouteRequest{Prompt: "fix typo"})
	escBody, _ := json.Marshal(map[string]any{"decision": dec, "cause": "tests failed"})
	invalidEsc, _ := json.Marshal(map[string]any{"decision": schema.RoutingDecision{}, "cause": "x"})
	noCause, _ := json.Marshal(map[string]any{"decision": dec})

	cases := []struct {
		name, method, path, body string
		want                     int
		wantSub                  string
		allow                    string
	}{
		{"healthz", http.MethodGet, "/healthz", "", 200, `{"ok":true}`, ""},
		{"status", http.MethodGet, "/status", "", 200, `"routed_total":7`, ""},
		{"route", http.MethodPost, "/route", string(routeBody), 200, `"task_id":"t1"`, ""},
		{"escalate", http.MethodPost, "/escalate", string(escBody), 200, `"task_tier":"TIER_2"`, ""},
		{"escalate no cause", http.MethodPost, "/escalate", string(noCause), 200, `"source":"escalation"`, ""},
		{"escalate invalid decision", http.MethodPost, "/escalate", string(invalidEsc), 400, "invalid decision", ""},
		{"escalate malformed", http.MethodPost, "/escalate", "{", 400, "malformed JSON", ""},
		{"route malformed", http.MethodPost, "/route", "{nope", 400, "malformed JSON", ""},
		{"route empty body", http.MethodPost, "/route", "", 400, "empty body", ""},
		{"route empty prompt", http.MethodPost, "/route", `{"prompt":"  "}`, 400, "empty prompt", ""},
		{"route trailing junk", http.MethodPost, "/route", `{"prompt":"a"} x`, 400, "malformed JSON", ""},
		{"unknown path", http.MethodGet, "/nope", "", 404, "not found", ""},
		{"route wrong method", http.MethodGet, "/route", "", 405, "method not allowed", "POST"},
		{"escalate wrong method", http.MethodPut, "/escalate", "", 405, "method not allowed", "POST"},
		{"status wrong method", http.MethodPost, "/status", "", 405, "method not allowed", "GET, HEAD"},
		{"healthz wrong method", http.MethodDelete, "/healthz", "", 405, "method not allowed", "GET, HEAD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &mocks.StubRouter{Decision: dec, Escalated: esc, Snapshot: schema.StatusSnapshot{RoutedTotal: 7}}
			s, _ := newTestServer(t, r)
			rec := do(t, s.Handler(), tc.method, tc.path, tc.body, nil)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			assertJSON(t, rec)
			if !strings.Contains(rec.Body.String(), tc.wantSub) {
				t.Errorf("body %q missing %q", rec.Body, tc.wantSub)
			}
			if tc.allow != "" && rec.Header().Get("Allow") != tc.allow {
				t.Errorf("Allow = %q, want %q", rec.Header().Get("Allow"), tc.allow)
			}
		})
	}
}

func TestRouteAndEscalateRecordCalls(t *testing.T) {
	t.Parallel()
	r := &mocks.StubRouter{Decision: validDecision(), Escalated: validDecision()}
	s, _ := newTestServer(t, r)
	do(t, s.Handler(), http.MethodPost, "/route", `{"prompt":"hello","session_id":"s1"}`, nil)
	if got := r.Routes(); len(got) != 1 || got[0].Prompt != "hello" || got[0].SessionID != "s1" {
		t.Errorf("routes = %+v", got)
	}
	body, _ := json.Marshal(map[string]any{"decision": validDecision(), "cause": "boom"})
	do(t, s.Handler(), http.MethodPost, "/escalate", string(body), nil)
	got := r.Escalations()
	if len(got) != 1 || got[0].Cause == nil || got[0].Cause.Error() != "boom" || got[0].Prev.TaskID != "t1" {
		t.Errorf("escalations = %+v", got)
	}
}

func TestRouterErrors(t *testing.T) {
	t.Parallel()
	const secret = "TOP-SECRET-PROMPT-CONTENT"
	cases := []struct {
		name string
		err  error
		want int
		msg  string
	}{
		{"classifier unavailable", fmt.Errorf("router: %w: %s", schema.ErrClassifierUnavailable, secret), 502, "classifier unavailable"},
		{"generic", fmt.Errorf("router: failed on %q", secret), 500, "internal error"},
		{"timeout", fmt.Errorf("%w %s", context.DeadlineExceeded, secret), 500, "router timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &mocks.StubRouter{Err: tc.err, EscalateErr: tc.err}
			s, logs := newTestServer(t, r)
			rec := do(t, s.Handler(), http.MethodPost, "/route", `{"prompt":"`+secret+`"}`, nil)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Fatalf("route: %d %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), secret) {
				t.Error("route error body leaks prompt")
			}
			body, _ := json.Marshal(map[string]any{"decision": validDecision(), "cause": secret})
			rec = do(t, s.Handler(), http.MethodPost, "/escalate", string(body), nil)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("escalate: %d %s", rec.Code, rec.Body)
			}
			if strings.Contains(logs.String(), secret) {
				t.Error("logs leak prompt")
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	t.Parallel()
	prefix, suffix := `{"prompt":"`, `"}`
	mk := func(total int) string {
		return prefix + strings.Repeat("a", total-len(prefix)-len(suffix)) + suffix
	}
	cases := []struct {
		name string
		size int
		want int
	}{
		{"exactly limit", MaxBodyBytes, 200},
		{"limit plus one", MaxBodyBytes + 1, 413},
		{"well over", 2 * MaxBodyBytes, 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestServer(t, &mocks.StubRouter{Decision: validDecision()})
			body := mk(tc.size)
			if len(body) != tc.size {
				t.Fatalf("body len %d", len(body))
			}
			rec := do(t, s.Handler(), http.MethodPost, "/route", body, nil)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			assertJSON(t, rec)
		})
	}
}

func TestHostHeader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1", 200}, {"127.0.0.1:8942", 200}, {"localhost", 200}, {"LOCALHOST:1", 200},
		{"[::1]", 200}, {"[::1]:8942", 200}, {"::1", 200},
		{"example.com", 421}, {"evil.localhost.attacker.com:8942", 421}, {"10.0.0.1:8942", 421},
		{"", 421}, {"127.0.0.2", 421},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestServer(t, &mocks.StubRouter{})
			rec := do(t, s.Handler(), http.MethodGet, "/healthz", "", map[string]string{"Host": tc.host})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			assertJSON(t, rec)
		})
	}
}

func TestCORS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, method, origin string
		want                 int
		allowed              bool
	}{
		{"null origin", http.MethodGet, "null", 200, true},
		{"file origin", http.MethodGet, "file://", 200, true},
		{"loopback http", http.MethodGet, "http://127.0.0.1:3000", 200, true},
		{"localhost https", http.MethodGet, "https://localhost", 200, true},
		{"ipv6 loopback", http.MethodGet, "http://[::1]:8080", 200, true},
		{"no origin", http.MethodGet, "", 200, false},
		{"remote origin", http.MethodGet, "https://evil.example", 403, false},
		{"bad scheme", http.MethodGet, "ftp://127.0.0.1", 403, false},
		{"unparseable", http.MethodGet, "http://%zz", 403, false},
		{"preflight allowed", http.MethodOptions, "http://localhost:5173", 204, true},
		{"preflight disallowed", http.MethodOptions, "https://evil.example", 403, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestServer(t, &mocks.StubRouter{})
			hdr := map[string]string{}
			if tc.origin != "" {
				hdr["Origin"] = tc.origin
			}
			if tc.method == http.MethodOptions {
				hdr["Access-Control-Request-Method"] = "POST"
			}
			rec := do(t, s.Handler(), tc.method, "/healthz", "", hdr)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			acao := rec.Header().Get("Access-Control-Allow-Origin")
			if acao == "*" {
				t.Fatal("wildcard CORS")
			}
			if tc.allowed != (acao == tc.origin && acao != "") {
				t.Errorf("ACAO = %q, allowed=%v", acao, tc.allowed)
			}
			if tc.method == http.MethodOptions && tc.allowed &&
				!strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "POST") {
				t.Error("preflight missing Allow-Methods")
			}
		})
	}
}

func TestPromptNeverLogged(t *testing.T) {
	t.Parallel()
	const secret = "please-refactor-the-PAYMENTS-module-xyzzy"
	s, logs := newTestServer(t, &mocks.StubRouter{Decision: validDecision()})
	rec := do(t, s.Handler(), http.MethodPost, "/route", `{"prompt":"`+secret+`"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, `"path":"/route"`) || !strings.Contains(out, `"status":200`) ||
		!strings.Contains(out, `"latency"`) || !strings.Contains(out, `"method":"POST"`) {
		t.Errorf("access log missing fields: %s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("prompt leaked into logs: %s", out)
	}
}

func TestNilLogger(t *testing.T) {
	t.Parallel()
	s := NewServer(config.Default(), &mocks.StubRouter{}, nil)
	if rec := do(t, s.Handler(), http.MethodGet, "/healthz", "", nil); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestWriteJSONEncodeFailure(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"bad": make(chan int)})
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "encode failure") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestStatusRecorderWriteHeaderOnce(t *testing.T) {
	t.Parallel()
	sr := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	sr.WriteHeader(418)
	sr.WriteHeader(200)
	if sr.status != 418 {
		t.Errorf("status = %d", sr.status)
	}
}

func TestListenRefusesNonLoopback(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"0.0.0.0:8942", ":8942", "192.168.1.5:8942", "example.com:80", "no-port", "[::]:8942"} {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.ListenAddr = addr
			s := NewServer(cfg, &mocks.StubRouter{}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := s.ListenAndServe(ctx); !errors.Is(err, ErrNonLoopback) {
				t.Fatalf("err = %v, want ErrNonLoopback", err)
			}
		})
	}
}

func TestListenBindFailure(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := config.Default()
	cfg.ListenAddr = ln.Addr().String()
	s := NewServer(cfg, &mocks.StubRouter{}, nil)
	if err := s.ListenAndServe(context.Background()); err == nil || errors.Is(err, ErrNonLoopback) {
		t.Fatalf("err = %v, want bind error", err)
	}
}

func TestListenAndServeGracefulShutdown(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	s := NewServer(cfg, &mocks.StubRouter{}, nil)
	addrCh := make(chan net.Addr, 1)
	s.onListen = func(a net.Addr) { addrCh <- a }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()

	var addr net.Addr
	select {
	case addr = <-addrCh:
	case err := <-done:
		t.Fatalf("server exited early: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not start")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr.String()+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("healthz: %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("ListenAndServe did not return within 6s")
	}
}

func BenchmarkHealthz(b *testing.B) {
	s := NewServer(config.Default(), &mocks.StubRouter{}, nil)
	h := s.Handler()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = "127.0.0.1"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
