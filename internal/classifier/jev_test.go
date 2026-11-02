package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

const (
	testJevKeyEnv = "JEV_TEST_TYPESAFE_KEY"
	testJevKey    = "ts-test-key-not-real"
)

func jevConfig(baseURL string) config.Config {
	cfg := config.Default()
	cfg.TypeSafeBaseURL = baseURL
	cfg.TypeSafeAPIKeyEnv = testJevKeyEnv
	cfg.ClassifierTimeout = 2 * time.Second
	return cfg
}

// jevBody renders a System One response with a tier Choice and an effort Score.
func jevBody(tier string, conf, score float64) string {
	return fmt.Sprintf(`{"model":"jev-1.14.0","answers":{
"task_tier":{"type":"choice","choice":%q,"confidence":%v,"probabilities":{}},
"effort":{"type":"score","score":%v,"confidence":0.9,"legend":{},"probabilities":{}}},
"usage":{"input_tokens":120,"output_tokens":12}}`, tier, conf, score)
}

func jevOK(tier string, conf, score float64) func(http.ResponseWriter, *http.Request) {
	return raw(jevBody(tier, conf, score))
}

func newJev(t *testing.T, f *fake) *Jev {
	t.Helper()
	j := NewJev(jevConfig(f.srv.URL), nil)
	j.backoff = time.Millisecond
	return j
}

func TestJevClassifySuccess(t *testing.T) {
	t.Setenv(testJevKeyEnv, testJevKey)
	var got jevRequest
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testJevKey {
			t.Error("missing bearer key")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, jevBody("TIER_3", 0.92, 1.7)) // test server write
	})
	d, err := newJev(t, f).Classify(context.Background(), schema.RouteRequest{TaskID: "t1", Prompt: "design a cache", SessionID: "s1"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if d.TaskTier != schema.Tier3 || d.SelectedModel != "claude-opus-5-5" || d.EffortLevel != schema.EffortHigh ||
		d.Source != schema.SourceJev || d.TaskID != "t1" || d.SessionID != "s1" {
		t.Errorf("decision = %+v", d)
	}
	if d.Rationale != "jev-1.14.0 tier confidence 0.92" {
		t.Errorf("rationale = %q", d.Rationale)
	}
	if got.Model != "jev-latest" || got.State["user_prompt"] != "design a cache" {
		t.Errorf("request = %+v", got)
	}
	tq, eq := got.Questions[jevTierQuestion], got.Questions[jevEffortQuestion]
	if tq.Type != "choice" || eq.Type != "score" {
		t.Errorf("question types = %q, %q", tq.Type, eq.Type)
	}
	crit, _ := tq.Criteria.(map[string]any)
	for _, tier := range []string{"TIER_1", "TIER_2", "TIER_3"} {
		if _, ok := crit[tier]; !ok {
			t.Errorf("tier criteria missing %s", tier)
		}
	}
	if levels, _ := eq.Criteria.([]any); len(levels) != 3 {
		t.Errorf("effort levels = %v", eq.Criteria)
	}
}

func TestJevConfidenceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		conf    float64
		wantLow bool
	}{
		{"at threshold", 0.5, false},
		{"just below", 0.49, true},
		{"above", 0.8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(testJevKeyEnv, testJevKey)
			f := newFake(t, jevOK("TIER_1", tc.conf, 0))
			d, err := newJev(t, f).Classify(context.Background(), schema.RouteRequest{TaskID: "t"})
			if got := errors.Is(err, ErrLowConfidence); got != tc.wantLow {
				t.Fatalf("low confidence = %v (err %v), want %v", got, err, tc.wantLow)
			}
			if d.TaskTier != schema.Tier1 {
				t.Errorf("decision should be returned either way: %+v", d)
			}
		})
	}
}

func TestEffortFromScore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		score float64
		want  schema.Effort
	}{
		{-1, schema.EffortLow}, {0, schema.EffortLow}, {0.49, schema.EffortLow},
		{0.5, schema.EffortMedium}, {1.49, schema.EffortMedium},
		{1.5, schema.EffortHigh}, {2, schema.EffortHigh}, {9, schema.EffortHigh},
	} {
		if got := effortFromScore(tc.score); got != tc.want {
			t.Errorf("effortFromScore(%v) = %s, want %s", tc.score, got, tc.want)
		}
	}
}

func TestJevErrors(t *testing.T) {
	cases := []struct {
		name  string
		steps []func(http.ResponseWriter, *http.Request)
		want  error
		calls int32
	}{
		{"unauthorized", steps(status(http.StatusUnauthorized)), schema.ErrClassifierUnavailable, 1},
		{"validation", steps(status(http.StatusUnprocessableEntity)), schema.ErrClassifierUnavailable, 1},
		{"overloaded twice", steps(status(529)), schema.ErrClassifierUnavailable, 2},
		{"rate limited then ok", steps(status(http.StatusTooManyRequests), jevOK("TIER_2", 0.9, 1)), nil, 2},
		{"not json", steps(raw("nope")), schema.ErrInvalidDecision, 1},
		{"unknown tier", steps(jevOK("TIER_9", 0.9, 1)), schema.ErrInvalidDecision, 1},
		{"missing effort", steps(raw(`{"answers":{"task_tier":{"type":"choice","choice":"TIER_1","confidence":1}}}`)), schema.ErrInvalidDecision, 1},
		{"missing tier", steps(raw(`{"answers":{}}`)), schema.ErrInvalidDecision, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(testJevKeyEnv, testJevKey)
			f := newFake(t, tc.steps...)
			_, err := newJev(t, f).Classify(context.Background(), schema.RouteRequest{TaskID: "t"})
			if tc.want == nil && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), testJevKey) {
				t.Error("error leaks the key")
			}
			if n := f.calls.Load(); n != tc.calls {
				t.Errorf("calls = %d, want %d", n, tc.calls)
			}
		})
	}
}

func TestJevNoKey(t *testing.T) {
	t.Setenv(testJevKeyEnv, "")
	f := newFake(t, jevOK("TIER_1", 1, 0))
	_, err := newJev(t, f).Classify(context.Background(), schema.RouteRequest{})
	if !errors.Is(err, schema.ErrClassifierUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f.calls.Load() != 0 {
		t.Error("request sent without a key")
	}
}

func TestJevTransportError(t *testing.T) {
	t.Setenv(testJevKeyEnv, testJevKey)
	j := NewJev(jevConfig("http://127.0.0.1:1"), nil)
	if _, err := j.Classify(context.Background(), schema.RouteRequest{}); !errors.Is(err, schema.ErrClassifierUnavailable) {
		t.Fatalf("err = %v", err)
	}
	j = NewJev(jevConfig("http://bad host"), nil)
	if _, err := j.Classify(context.Background(), schema.RouteRequest{}); !errors.Is(err, schema.ErrClassifierUnavailable) {
		t.Fatalf("bad url err = %v", err)
	}
}

func TestJevBackoffCancelled(t *testing.T) {
	t.Setenv(testJevKeyEnv, testJevKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		cancel() // the client is now waiting out its backoff
	})
	j := newJev(t, f)
	j.backoff = time.Hour
	if _, err := j.Classify(ctx, schema.RouteRequest{}); !errors.Is(err, schema.ErrClassifierUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func steps(s ...func(http.ResponseWriter, *http.Request)) []func(http.ResponseWriter, *http.Request) {
	return s
}
