package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/johnswift/jevons/internal/mocks"
	"github.com/johnswift/jevons/pkg/schema"
)

func TestApplyResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		stdout        string
		parsed, isErr bool
		wantStdout    string
		wantTokens    int64
		wantSessionID string
	}{
		{"full", ` {"type":"result","result":"ok","session_id":"s","usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}` + "\n",
			true, false, "ok", 10, "s"},
		{"error flag", `{"type":"result","is_error":true,"result":"x"}`, true, true, "x", 0, ""},
		{"plain text", "hello", false, false, "hello", 0, ""},
		{"json array", `["a"]`, false, false, `["a"]`, 0, ""},
		{"other type", `{"type":"system","result":"x"}`, false, false, `{"type":"system","result":"x"}`, 0, ""},
		{"empty", "", false, false, "", 0, ""},
	}
	for _, tc := range cases {
		res := schema.RunResult{Stdout: tc.stdout}
		parsed, isErr := applyResult(&res)
		if parsed != tc.parsed || isErr != tc.isErr {
			t.Errorf("%s: parsed=%v isErr=%v", tc.name, parsed, isErr)
		}
		if tc.parsed && (res.Stdout != tc.wantStdout || res.Tokens != tc.wantTokens || res.SessionID != tc.wantSessionID) {
			t.Errorf("%s: result %+v", tc.name, res)
		}
		if !tc.parsed && res.Stdout != tc.stdout {
			t.Errorf("%s: stdout changed to %q", tc.name, res.Stdout)
		}
	}
}

func TestRunJSONResult(t *testing.T) {
	t.Parallel()
	n := &mocks.RecordingNotifier{}
	r, _ := newTestRunner(t, "json-ok", n)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/s"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "done" || res.Tokens != 100 || res.SessionID != "s-1" {
		t.Errorf("result %+v", res)
	}
	if got := n.Notifications(); len(got) != 1 || got[0].Title != "Jev: /s ✓" {
		t.Errorf("notifications %+v", got)
	}
}

func TestRunJSONReportedError(t *testing.T) {
	t.Parallel()
	n := &mocks.RecordingNotifier{}
	r, _ := newTestRunner(t, "json-error", n)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/s"})
	if !errors.Is(err, schema.ErrRunFailed) {
		t.Fatalf("err = %v, want ErrRunFailed", err)
	}
	if res.ExitCode != 0 || res.Tokens != 6 || res.Stdout != "bad" {
		t.Errorf("result %+v", res)
	}
	if got := n.Notifications(); len(got) != 1 || got[0].Title != "Jev: /s ✗" {
		t.Errorf("notifications %+v", got)
	}
}
