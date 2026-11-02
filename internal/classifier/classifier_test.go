package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/pkg/schema"
)

const (
	testKeyEnv = "JEV_TEST_CLASSIFIER_KEY"
	testKey    = "test-key-not-real"
)

func testConfig(baseURL string) config.Config {
	cfg := config.Default()
	cfg.AnthropicBaseURL = baseURL
	cfg.APIKeyEnv = testKeyEnv
	cfg.ClassifierTimeout = 2 * time.Second
	return cfg
}

// textResponse renders a Messages API response with one text block.
func textResponse(text string) string {
	b, err := json.Marshal(map[string]any{
		"id":      "msg_test",
		"type":    "message",
		"role":    "assistant",
		"content": []map[string]string{{"type": "text", "text": text}},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

type fake struct {
	srv   *httptest.Server
	calls atomic.Int32
}

// newFake serves responses in order; the last one repeats.
func newFake(t *testing.T, steps ...func(w http.ResponseWriter, r *http.Request)) *fake {
	t.Helper()
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1)) - 1
		if n >= len(steps) {
			n = len(steps) - 1
		}
		steps[n](w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func status(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func ok(text string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, textResponse(text)) // test server write
	}
}

func raw(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body) // test server write
	}
}

func newClient(cfg config.Config) *Client {
	c := New(cfg, nil)
	c.backoff = 5 * time.Millisecond
	return c
}

func assertNoKey(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), testKey) {
		t.Fatalf("API key leaked in error: %v", err)
	}
}

// All tests share one parent because t.Setenv forbids t.Parallel.
func TestClassify(t *testing.T) {
	t.Setenv(testKeyEnv, testKey)

	const validJSON = `{"task_tier":"TIER_2","effort_level":"medium","rationale":"multi-file debugging"}`

	cases := []struct {
		name      string
		steps     []func(http.ResponseWriter, *http.Request)
		wantErr   error
		wantTier  schema.Tier
		wantCalls int32
	}{
		{"valid json", []func(http.ResponseWriter, *http.Request){ok(validJSON)}, nil, schema.Tier2, 1},
		{"surrounding whitespace", []func(http.ResponseWriter, *http.Request){ok("\n  " + validJSON + "  \n")}, nil, schema.Tier2, 1},
		{"fenced json", []func(http.ResponseWriter, *http.Request){ok("```json\n" + validJSON + "\n```")}, nil, schema.Tier2, 1},
		{"bare fence", []func(http.ResponseWriter, *http.Request){ok("```\n" + validJSON + "\n```")}, nil, schema.Tier2, 1},
		{"tier3", []func(http.ResponseWriter, *http.Request){ok(`{"task_tier":"TIER_3","effort_level":"high","rationale":"architecture"}`)}, nil, schema.Tier3, 1},
		{"prose response", []func(http.ResponseWriter, *http.Request){ok("I think this is TIER_2 because " + validJSON)}, schema.ErrInvalidDecision, "", 1},
		{"unterminated fence", []func(http.ResponseWriter, *http.Request){ok("```json\n" + validJSON)}, schema.ErrInvalidDecision, "", 1},
		{"trailing prose", []func(http.ResponseWriter, *http.Request){ok(validJSON + " hope that helps")}, schema.ErrInvalidDecision, "", 1},
		{"two fences", []func(http.ResponseWriter, *http.Request){ok("```json\n" + validJSON + "\n```\n```json\n" + validJSON + "\n```")}, schema.ErrInvalidDecision, "", 1},
		{"invalid tier", []func(http.ResponseWriter, *http.Request){ok(`{"task_tier":"TIER_9","effort_level":"low","rationale":"x"}`)}, schema.ErrInvalidDecision, "", 1},
		{"invalid effort", []func(http.ResponseWriter, *http.Request){ok(`{"task_tier":"TIER_1","effort_level":"max","rationale":"x"}`)}, schema.ErrInvalidDecision, "", 1},
		{"unknown field", []func(http.ResponseWriter, *http.Request){ok(`{"task_tier":"TIER_1","effort_level":"low","rationale":"x","extra":1}`)}, schema.ErrInvalidDecision, "", 1},
		{"malformed json", []func(http.ResponseWriter, *http.Request){ok(`{"task_tier":`)}, schema.ErrInvalidDecision, "", 1},
		{"non-json envelope", []func(http.ResponseWriter, *http.Request){raw("not json")}, schema.ErrInvalidDecision, "", 1},
		{"no text block", []func(http.ResponseWriter, *http.Request){raw(`{"content":[{"type":"tool_use"}]}`)}, schema.ErrInvalidDecision, "", 1},
		{"429 then 200", []func(http.ResponseWriter, *http.Request){status(429), ok(validJSON)}, nil, schema.Tier2, 2},
		{"529 then 200", []func(http.ResponseWriter, *http.Request){status(529), ok(validJSON)}, nil, schema.Tier2, 2},
		{"500 twice", []func(http.ResponseWriter, *http.Request){status(500)}, schema.ErrClassifierUnavailable, "", 2},
		{"401 no retry", []func(http.ResponseWriter, *http.Request){status(401)}, schema.ErrClassifierUnavailable, "", 1},
		{"400 no retry", []func(http.ResponseWriter, *http.Request){status(400)}, schema.ErrClassifierUnavailable, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, tc.steps...)
			c := newClient(testConfig(f.srv.URL))
			d, err := c.Classify(context.Background(), schema.RouteRequest{Prompt: "debug the router", SessionID: "s1"})
			assertNoKey(t, err)
			if got := f.calls.Load(); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if d.TaskTier != tc.wantTier || d.SelectedModel != config.Default().ModelFor(tc.wantTier) {
				t.Errorf("tier/model = %s/%s", d.TaskTier, d.SelectedModel)
			}
			if d.Source != schema.SourceClassifier || len(d.TaskID) != 16 || d.SessionID != "s1" {
				t.Errorf("decision = %+v", d)
			}
			if d.LatencyMicros < 0 {
				t.Errorf("LatencyMicros = %d", d.LatencyMicros)
			}
		})
	}

	t.Run("request shape and headers", func(t *testing.T) {
		var got messagesRequest
		var hdr http.Header
		var path, method string
		f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
			hdr, path, method = r.Header.Clone(), r.URL.Path, r.Method
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&got); err != nil {
				t.Errorf("decode request: %v", err)
			}
			ok(validJSON)(w, r)
		})
		cfg := testConfig(f.srv.URL + "/") // trailing slash is tolerated
		d, err := newClient(cfg).Classify(context.Background(), schema.RouteRequest{TaskID: "fixedtaskid00001", Prompt: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		if d.TaskID != "fixedtaskid00001" {
			t.Errorf("TaskID not propagated: %s", d.TaskID)
		}
		if method != http.MethodPost || path != "/v1/messages" {
			t.Errorf("%s %s", method, path)
		}
		if hdr.Get("x-api-key") != testKey || hdr.Get("anthropic-version") != "2023-06-01" || hdr.Get("content-type") != "application/json" {
			t.Errorf("headers = %v", hdr)
		}
		if got.Model != cfg.ClassifierModel || got.MaxTokens != 200 || got.Temperature != 0 {
			t.Errorf("model/max_tokens/temperature = %s/%d/%v", got.Model, got.MaxTokens, got.Temperature)
		}
		if len(got.System) != 1 || got.System[0].Type != "text" || got.System[0].Text != SystemPrompt || got.System[0].CacheControl.Type != "ephemeral" {
			t.Errorf("system = %+v", got.System)
		}
		if len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content != "hello" {
			t.Errorf("messages = %+v", got.Messages)
		}
	})

	t.Run("temperature serialized explicitly", func(t *testing.T) {
		var body string
		f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			body = string(b)
			ok(validJSON)(w, r)
		})
		if _, err := newClient(testConfig(f.srv.URL)).Classify(context.Background(), schema.RouteRequest{Prompt: "x"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, `"temperature":0`) {
			t.Errorf("temperature missing from body: %s", body)
		}
	})

	t.Run("prompt truncated to 8KiB", func(t *testing.T) {
		cases := []struct {
			name    string
			prompt  string
			wantLen int
		}{
			{"exact boundary", strings.Repeat("a", MaxPromptBytes), MaxPromptBytes},
			{"one over", strings.Repeat("a", MaxPromptBytes+1), MaxPromptBytes},
			{"large", strings.Repeat("a", 64*1024), MaxPromptBytes},
			// "é" is 2 bytes: 8191 'a' + "é" would split at 8192; cut back to 8191.
			{"rune boundary", strings.Repeat("a", MaxPromptBytes-1) + "é", MaxPromptBytes - 1},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var got messagesRequest
				f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					ok(validJSON)(w, r)
				})
				if _, err := newClient(testConfig(f.srv.URL)).Classify(context.Background(), schema.RouteRequest{Prompt: tc.prompt}); err != nil {
					t.Fatal(err)
				}
				if n := len(got.Messages[0].Content); n != tc.wantLen {
					t.Errorf("sent %d bytes, want %d", n, tc.wantLen)
				}
			})
		}
	})

	t.Run("slow server exceeds timeout", func(t *testing.T) {
		f := newFake(t, func(_ http.ResponseWriter, r *http.Request) {
			blockUntilGone(r)
		})
		cfg := testConfig(f.srv.URL)
		cfg.ClassifierTimeout = 50 * time.Millisecond
		start := time.Now()
		_, err := newClient(cfg).Classify(context.Background(), schema.RouteRequest{Prompt: "x"})
		assertNoKey(t, err)
		if !errors.Is(err, schema.ErrClassifierUnavailable) {
			t.Fatalf("err = %v", err)
		}
		if el := time.Since(start); el > time.Second {
			t.Errorf("took %v, timeout not enforced", el)
		}
		if f.calls.Load() != 1 {
			t.Errorf("timeouts must not be retried, calls = %d", f.calls.Load())
		}
	})

	t.Run("ctx cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		f := newFake(t, func(_ http.ResponseWriter, r *http.Request) {
			cancel()
			blockUntilGone(r)
		})
		_, err := newClient(testConfig(f.srv.URL)).Classify(ctx, schema.RouteRequest{Prompt: "x"})
		if !errors.Is(err, schema.ErrClassifierUnavailable) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("ctx cancelled during backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
			cancel()
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		c := newClient(testConfig(f.srv.URL))
		c.backoff = time.Hour
		start := time.Now()
		_, err := c.Classify(ctx, schema.RouteRequest{Prompt: "x"})
		if !errors.Is(err, schema.ErrClassifierUnavailable) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > time.Second || f.calls.Load() != 1 {
			t.Errorf("backoff ignored ctx: calls=%d", f.calls.Load())
		}
	})

	t.Run("zero backoff", func(t *testing.T) {
		f := newFake(t, status(502), ok(validJSON))
		c := newClient(testConfig(f.srv.URL))
		c.backoff = 0
		if _, err := c.Classify(context.Background(), schema.RouteRequest{Prompt: "x"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("bad base url", func(t *testing.T) {
		_, err := newClient(testConfig("http://bad host\x7f")).Classify(context.Background(), schema.RouteRequest{Prompt: "x"})
		if !errors.Is(err, schema.ErrClassifierUnavailable) {
			t.Fatalf("err = %v", err)
		}
		assertNoKey(t, err)
	})

	t.Run("missing model mapping", func(t *testing.T) {
		f := newFake(t, ok(validJSON))
		cfg := testConfig(f.srv.URL)
		cfg.Models = map[schema.Tier]string{schema.Tier1: "m1"}
		_, err := newClient(cfg).Classify(context.Background(), schema.RouteRequest{Prompt: "x"})
		if !errors.Is(err, schema.ErrInvalidDecision) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMissingKey(t *testing.T) {
	t.Setenv(testKeyEnv, "")
	f := newFake(t, ok("{}"))
	for _, env := range []string{testKeyEnv, ""} {
		cfg := testConfig(f.srv.URL)
		cfg.APIKeyEnv = env
		_, err := newClient(cfg).Classify(context.Background(), schema.RouteRequest{Prompt: "x"})
		if !errors.Is(err, schema.ErrClassifierUnavailable) {
			t.Fatalf("env %q: err = %v", env, err)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatalf("made %d requests without a key", f.calls.Load())
	}
}

func TestNewDefaultClient(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	c := New(cfg, nil)
	if c.http == nil || c.http.Timeout != cfg.ClassifierTimeout {
		t.Fatalf("default client timeout = %v", c.http.Timeout)
	}
	hc := &http.Client{Timeout: time.Second}
	if New(cfg, hc).http != hc {
		t.Fatal("custom client not used")
	}
}

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
		{"aé", 2, "a"},
		{"日本", 2, ""},
		{"", 0, ""},
	}
	for _, tc := range cases {
		if got := truncateUTF8(tc.in, tc.n); got != tc.want {
			t.Errorf("truncateUTF8(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// blockUntilGone consumes the request body (so the server notices client
// disconnects) and blocks until the client goes away or 2s elapse.
func blockUntilGone(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body) // test server: drain body
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
}
