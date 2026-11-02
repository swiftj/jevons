package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/swiftj/jevons/internal/mocks"
)

var feedNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func newFeedServer(t *testing.T) (http.Handler, *mocks.MemSessions, *mocks.RecordingUsage) {
	t.Helper()
	sessions := mocks.NewMemSessions(50*time.Minute, 100_000)
	usage := &mocks.RecordingUsage{}
	s, _ := newTestServer(t, &mocks.StubRouter{})
	s.WithFeeds(Feeds{Sessions: sessions, Usage: usage, Clock: mocks.NewFakeClock(feedNow)})
	return s.Handler(), sessions, usage
}

func TestSessionFeed(t *testing.T) {
	t.Parallel()
	h, sessions, _ := newFeedServer(t)

	past := feedNow.Add(-time.Minute).Format(time.RFC3339)
	future := feedNow.Add(time.Hour).Format(time.RFC3339)
	cases := []struct {
		name, body string
		status     int
		wantAt     time.Time
	}{
		{"explicit time", `{"session_id":"a","model":"m","cached_tokens":200000,"last_activity":"` + past + `"}`, 200, feedNow.Add(-time.Minute)},
		{"zero time → now", `{"session_id":"b","model":"m","cached_tokens":1}`, 200, feedNow},
		{"future clamped", `{"session_id":"c","model":"m","cached_tokens":1,"last_activity":"` + future + `"}`, 200, feedNow},
		{"empty id", `{"session_id":" ","model":"m"}`, 400, time.Time{}},
		{"long id", `{"session_id":"` + strings.Repeat("x", MaxSessionIDLen+1) + `","model":"m"}`, 400, time.Time{}},
		{"max id", `{"session_id":"` + strings.Repeat("y", MaxSessionIDLen) + `","model":"m"}`, 200, feedNow},
		{"empty model", `{"session_id":"d","model":""}`, 400, time.Time{}},
		{"negative tokens", `{"session_id":"e","model":"m","cached_tokens":-1}`, 400, time.Time{}},
		{"malformed", `{`, 400, time.Time{}},
		{"empty body", ``, 400, time.Time{}},
	}
	for _, tc := range cases {
		rec := do(t, h, http.MethodPost, "/session", tc.body, nil)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, body %s", tc.name, rec.Code, rec.Body)
			continue
		}
		assertJSON(t, rec)
		if tc.status != 200 {
			continue
		}
		id := strings.SplitN(strings.SplitN(tc.body, `"session_id":"`, 2)[1], `"`, 2)[0]
		got, ok := sessions.Get(id)
		if !ok || !got.LastActivity.Equal(tc.wantAt) {
			t.Errorf("%s: stored %+v, ok=%v, want LastActivity %v", tc.name, got, ok, tc.wantAt)
		}
	}
	if warm := sessions.Warm(feedNow); len(warm) != 1 || warm[0].SessionID != "a" {
		t.Errorf("Warm = %+v", warm)
	}
}

func TestUsageFeed(t *testing.T) {
	t.Parallel()
	h, _, usage := newFeedServer(t)
	cases := []struct {
		tokens int64
		status int
	}{
		{1, 200}, {MaxUsageTokens, 200}, {0, 400}, {-5, 400}, {MaxUsageTokens + 1, 400},
	}
	for _, tc := range cases {
		rec := do(t, h, http.MethodPost, "/usage", fmt.Sprintf(`{"tokens":%d}`, tc.tokens), nil)
		if rec.Code != tc.status {
			t.Errorf("tokens=%d: status = %d", tc.tokens, rec.Code)
		}
		assertJSON(t, rec)
	}
	if rec := do(t, h, http.MethodPost, "/usage", `{"tokens":"many"}`, nil); rec.Code != 400 {
		t.Errorf("malformed: status = %d", rec.Code)
	}
	if got := usage.Added(); len(got) != 2 || got[0] != 1 || got[1] != MaxUsageTokens {
		t.Errorf("Added = %v", got)
	}
}

func TestFeedsNotConfigured(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t, &mocks.StubRouter{})
	h := s.WithFeeds(Feeds{}).Handler()
	for path, body := range map[string]string{
		"/session": `{"session_id":"a","model":"m"}`,
		"/usage":   `{"tokens":1}`,
	} {
		if rec := do(t, h, http.MethodPost, path, body, nil); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status = %d", path, rec.Code)
		}
	}
}

func TestFeedMethodNotAllowed(t *testing.T) {
	t.Parallel()
	h, _, _ := newFeedServer(t)
	for _, path := range []string{"/session", "/usage"} {
		rec := do(t, h, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("GET %s: status = %d, Allow = %q", path, rec.Code, rec.Header().Get("Allow"))
		}
	}
}
