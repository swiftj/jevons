package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func schemaTime() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }

func TestAppleScriptQuote(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"plain", `"plain"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{`\"`, `"\\\""`},
		{`" & do shell script "rm -rf ~" & "`, `"\" & do shell script \"rm -rf ~\" & \""`},
		{"", `""`},
		{"Jev: x ✓", `"Jev: x ✓"`},
	}
	for _, tc := range tests {
		if got := AppleScriptQuote(tc.in); got != tc.want {
			t.Fatalf("AppleScriptQuote(%q)=%s want %s", tc.in, got, tc.want)
		}
	}
}

func TestOSAScriptArgs(t *testing.T) {
	t.Parallel()
	got := OSAScriptArgs(`T"1`, `B\2`)
	want := []string{"-e", `display notification "B\\2" with title "T\"1"`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if got := notifySendArgs("-t", "b"); strings.Join(got, "|") != "--|-t|b" {
		t.Fatalf("notify-send args %q", got)
	}
}

func TestNewDesktopNotifierSelection(t *testing.T) {
	t.Parallel()
	found := func(string) (string, error) { return "/bin/fake", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	tests := []struct {
		goos   string
		look   LookPathFunc
		cmd    bool
		wantEx string
	}{
		{"darwin", found, true, "-e"},
		{"darwin", missing, false, ""},
		{"linux", found, true, "--"},
		{"linux", missing, false, ""},
		{"windows", found, false, ""},
	}
	for _, tc := range tests {
		n := newDesktopNotifier(tc.goos, tc.look, helperFactory("success"))
		cn, ok := n.(*cmdNotifier)
		if ok != tc.cmd {
			t.Fatalf("%s: cmd notifier = %v", tc.goos, ok)
		}
		if ok && cn.argv("t", "b")[0] != tc.wantEx {
			t.Fatalf("%s: argv %v", tc.goos, cn.argv("t", "b"))
		}
		if !ok {
			if err := n.Notify(context.Background(), "t", "b"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if NewDesktopNotifier() == nil {
		t.Fatal("nil notifier")
	}
}

func TestCmdNotifierRunsArgv(t *testing.T) {
	t.Parallel()
	n := &cmdNotifier{bin: "osascript", argv: OSAScriptArgs, exec: helperFactory("args")}
	if err := n.Notify(context.Background(), `a"b`, "c"); err != nil {
		t.Fatal(err)
	}
	// Verify the argv the child would receive via a capturing factory.
	var captured []string
	capture := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return helperFactory("args")(ctx, name, args...)
	}
	n = &cmdNotifier{bin: "osascript", argv: OSAScriptArgs, exec: capture}
	if err := n.Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(captured) // test diagnostics
	if len(captured) != 3 || captured[1] != "-e" {
		t.Fatalf("argv %s", b)
	}

	bad := &cmdNotifier{bin: "osascript", argv: OSAScriptArgs, exec: helperFactory("fail")}
	err := bad.Notify(context.Background(), "t", "b")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
}
