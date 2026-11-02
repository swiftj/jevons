package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name     string
		args     []string
		stdin    string
		wantCode int
		wantOut  string
		wantErr  bool
	}{
		{"no args", nil, "", 2, "", true},
		{"unknown", []string{"frob"}, "", 2, "", true},
		{"version", []string{"version"}, "", 0, "dev\n", false},
		{"--version", []string{"--version"}, "", 0, "dev\n", false},
		{"help", []string{"help"}, "", 0, "Usage: jev <command>", false},
		{"statusline", []string{"statusline", "-state-dir", "/nonexistent"}, `{"session_id":"s"}`, 0, "Jev: –\n", false},
		{"hook empty prompt", []string{"hook"}, `{"prompt":""}`, 0, "", false},
		{"run needs skill", []string{"run"}, "", 2, "", true},
		{"router bad flag", []string{"router", "-nope"}, "", 1, "", true},
		{"install bad flag", []string{"install", "-nope"}, "", 2, "", true},
		{"off bad flag", []string{"off", "-nope"}, "", 2, "", true},
		{"help lists on/off", []string{"help"}, "", 0, "off         pause jev", false},
		{"update bad config", []string{"update", "-config", "/does/not/exist.json"}, "", 1, "", true},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		code, err := dispatch(ctx, tc.args, strings.NewReader(tc.stdin), &out, &errOut)
		if code != tc.wantCode || (err != nil) != tc.wantErr || !strings.Contains(out.String(), tc.wantOut) {
			t.Errorf("%s: code=%d err=%v out=%q", tc.name, code, err, out.String())
		}
	}
}

func TestDoctorReportsMissingInstall(t *testing.T) {
	// A daemon address nothing listens on and an empty bin dir: doctor must fail.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // free the port so connections are refused
	t.Setenv("JEV_LISTEN_ADDR", addr)
	var out, errOut bytes.Buffer
	code, err := dispatch(context.Background(), []string{"doctor", "-offline", "-bin-dir", filepath.Join(t.TempDir(), "bin")},
		strings.NewReader(""), &out, &errOut)
	if code != 1 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, want := range []string{"✗ binary", "✗ daemon"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestPromptHelpers(t *testing.T) {
	t.Parallel()
	in := &lineReader{r: bufio.NewReader(strings.NewReader("  y \nno\n"))}
	if in.line() != "y" || in.line() != "no" || in.line() != "" {
		t.Error("lineReader")
	}
	var out bytes.Buffer
	if _, err := readSecret(context.Background(), "key: ", strings.NewReader("x\n"), &out, in); err == nil {
		t.Error("readSecret accepted a non-terminal")
	}
	env, err := newSetupEnv(context.Background(), t.TempDir(), "", strings.NewReader("n\n\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if env.Confirm("first?") || !env.Confirm("second?") {
		t.Error("Confirm: n should be no, empty should be yes")
	}
}

// TestStatusReportsOff runs `jev status` against a temp home. on and off are
// not run here: on macOS they would drive the real launchd agent.
func TestStatusReportsOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // free the port so connections are refused
	t.Setenv("JEV_LISTEN_ADDR", addr)
	if err := os.MkdirAll(filepath.Join(home, ".jev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".jev", "off"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code, err := dispatch(context.Background(), []string{"status"}, strings.NewReader(""), &out, &errOut)
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, want := range []string{"✗ jev ", "run `jev on`", "✗ daemon       stopped by `jev off`"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}
