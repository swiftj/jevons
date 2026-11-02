package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/mocks"
	"github.com/johnswift/jevons/pkg/schema"
)

const (
	helperEnv     = "JEV_RUNNER_WANT_HELPER"
	helperModeEnv = "JEV_RUNNER_HELPER_MODE"
	tailMarker    = "<<END-OF-OUTPUT>>"
)

// TestHelperProcess is not a real test: it is the fake `claude` / notifier
// binary, re-executed by helperFactory.
func TestHelperProcess(_ *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch os.Getenv(helperModeEnv) {
	case "success":
		fmt.Fprint(os.Stdout, "hello from claude")
		fmt.Fprint(os.Stderr, "a warning")
	case "args":
		_ = json.NewEncoder(os.Stdout).Encode(args) // test helper; encode error irrelevant
		wd, _ := os.Getwd()                         // test helper
		fmt.Fprint(os.Stderr, wd)
	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	case "large":
		chunk := strings.Repeat("x", 64*1024)
		for i := 0; i < 20; i++ { // 1.25 MiB
			fmt.Fprint(os.Stdout, chunk)
		}
		fmt.Fprint(os.Stdout, tailMarker)
	case "stderr":
		fmt.Fprint(os.Stderr, "only stderr")
	case "json-ok":
		fmt.Fprint(os.Stdout, `{"type":"result","is_error":false,"result":"done","session_id":"s-1",`+
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":40}}`)
	case "json-error":
		fmt.Fprint(os.Stdout, `{"type":"result","is_error":true,"result":"bad","usage":{"input_tokens":5,"output_tokens":1}}`)
	case "hang":
		time.Sleep(time.Minute)
	default:
		os.Exit(99)
	}
	os.Exit(0)
}

// helperFactory returns a CommandFactory that re-execs this test binary in mode.
func helperFactory(mode string) CommandFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), helperEnv+"=1", helperModeEnv+"="+mode)
		return cmd
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.LogDir = filepath.Join(t.TempDir(), "logs")
	cfg.ClaudeBin = "claude-fake"
	cfg.RunTimeout = 30 * time.Second
	return cfg
}

func newTestRunner(t *testing.T, mode string, n schema.Notifier) (*Runner, config.Config) {
	t.Helper()
	cfg := testConfig(t)
	clk := mocks.NewFakeClock(time.Date(2026, 11, 1, 12, 30, 45, 0, time.UTC))
	return New(cfg, Options{Notifier: n, Clock: clk, Exec: helperFactory(mode)}), cfg
}

func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	return lines
}

func TestRunSuccess(t *testing.T) {
	t.Parallel()
	n := &mocks.RecordingNotifier{}
	r, cfg := newTestRunner(t, "success", n)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/daily", Prompt: "go", Model: "m1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "hello from claude" || res.Stderr != "a warning" {
		t.Fatalf("unexpected result %+v", res)
	}
	if len(res.RunID) != 16 {
		t.Fatalf("run id %q", res.RunID)
	}
	wantName := "20261101-123045-" + res.RunID + ".log"
	if res.LogPath != filepath.Join(cfg.LogDir, wantName) {
		t.Fatalf("log path %q, want suffix %q", res.LogPath, wantName)
	}
	fi, err := os.Stat(res.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("log perm %v", fi.Mode().Perm())
	}
	di, err := os.Stat(cfg.LogDir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm %v", di.Mode().Perm())
	}

	lines := readLogLines(t, res.LogPath)
	var h header
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil {
		t.Fatalf("header not JSON: %v (%q)", err, lines[0])
	}
	if h.RunID != res.RunID || h.Skill != "/daily" || h.Model != "m1" {
		t.Fatalf("header %+v", h)
	}
	wantArgv := []string{"claude-fake", "-p", "/daily go", "--model", "m1", "--output-format", "json"}
	if strings.Join(h.Argv, "|") != strings.Join(wantArgv, "|") {
		t.Fatalf("argv %v", h.Argv)
	}
	var f footer
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &f); err != nil {
		t.Fatalf("footer not JSON: %v (%q)", err, lines[len(lines)-1])
	}
	if f.ExitCode != 0 || f.Error != "" {
		t.Fatalf("footer %+v", f)
	}
	body := strings.Join(lines[1:len(lines)-1], "\n")
	if !strings.Contains(body, "hello from claude") || !strings.Contains(body, "a warning") {
		t.Fatalf("log body missing output: %q", body)
	}

	got := n.Notifications()
	if len(got) != 1 || got[0].Title != "Jev: /daily ✓" {
		t.Fatalf("notifications %+v", got)
	}
}

func TestRunArgvAndWorkDir(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, "args", nil)
	wd := t.TempDir()
	tests := []struct {
		name string
		req  schema.RunRequest
		want []string
	}{
		{"skill only", schema.RunRequest{Skill: "/s", WorkDir: wd}, []string{"claude-fake", "-p", "/s", "--output-format", "json"}},
		{"prompt only", schema.RunRequest{Prompt: "do it; rm -rf / \"$(x)\"", WorkDir: wd}, []string{"claude-fake", "-p", "do it; rm -rf / \"$(x)\"", "--output-format", "json"}},
		{"both with model", schema.RunRequest{Skill: "/s", Prompt: "p", Model: "m", WorkDir: wd}, []string{"claude-fake", "-p", "/s p", "--model", "m", "--output-format", "json"}},
	}
	for _, tc := range tests {
		res, err := r.Run(context.Background(), tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got []string
		if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Fatalf("%s: argv %q want %q", tc.name, got, tc.want)
		}
		gotWD, _ := filepath.EvalSymlinks(res.Stderr) // macOS /var -> /private/var
		wantWD, _ := filepath.EvalSymlinks(wd)
		if gotWD != wantWD {
			t.Fatalf("%s: wd %q want %q", tc.name, gotWD, wantWD)
		}
	}
}

func TestRunFailure(t *testing.T) {
	t.Parallel()
	n := &mocks.RecordingNotifier{}
	r, _ := newTestRunner(t, "fail", n)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/x"})
	if !errors.Is(err, schema.ErrRunFailed) {
		t.Fatalf("err %v, want ErrRunFailed", err)
	}
	if res.ExitCode != 3 || res.Stderr != "boom" || res.RunID == "" || res.LogPath == "" {
		t.Fatalf("result %+v", res)
	}
	lines := readLogLines(t, res.LogPath)
	var f footer
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &f); err != nil {
		t.Fatalf("footer: %v", err)
	}
	if f.ExitCode != 3 || f.Error == "" {
		t.Fatalf("footer %+v", f)
	}
	got := n.Notifications()
	if len(got) != 1 || got[0].Title != "Jev: /x ✗" {
		t.Fatalf("notifications %+v", got)
	}
}

func TestRunLargeOutputKeepsTail(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, "large", nil)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/big"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != MaxCapture {
		t.Fatalf("stdout len %d, want %d", len(res.Stdout), MaxCapture)
	}
	if !strings.HasSuffix(res.Stdout, tailMarker) {
		t.Fatal("tail not kept")
	}
	fi, err := os.Stat(res.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= 20*64*1024 {
		t.Fatalf("log should hold full output, size %d", fi.Size())
	}
}

func TestRunStderrOnly(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, "stderr", nil)
	res, err := r.Run(context.Background(), schema.RunRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "" || res.Stderr != "only stderr" {
		t.Fatalf("result %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	t.Parallel()
	n := &mocks.RecordingNotifier{}
	r, _ := newTestRunner(t, "hang", n)
	start := time.Now()
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/slow", Timeout: 300 * time.Millisecond})
	if !errors.Is(err, schema.ErrTimeout) {
		t.Fatalf("err %v, want ErrTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("SIGTERM did not stop the child promptly")
	}
	if res.ExitCode == 0 {
		t.Fatalf("exit code %d", res.ExitCode)
	}
	if got := n.Notifications(); len(got) != 1 || got[0].Title != "Jev: /slow ✗" {
		t.Fatalf("notifications %+v", got)
	}
}

func TestRunParentCancel(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, "hang", nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond) // trigger cancellation mid-run
		cancel()
	}()
	_, err := r.Run(ctx, schema.RunRequest{Skill: "/c"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, schema.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
}

func TestRunNotifierErrorLoggedNotFatal(t *testing.T) {
	t.Parallel()
	n := &mocks.RecordingNotifier{Err: errors.New("no display")}
	r, _ := newTestRunner(t, "success", n)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/n"})
	if err != nil {
		t.Fatalf("notify failure must not fail run: %v", err)
	}
	data, err := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"notify_error":"no display"`) {
		t.Fatalf("notify error not logged: %s", data)
	}
	lines := readLogLines(t, res.LogPath)
	var f footer
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &f); err != nil {
		t.Fatalf("footer after notify error: %v", err)
	}
}

func TestRunValidationErrors(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*config.Config, *Options)
		req    schema.RunRequest
		want   string
	}{
		{"empty prompt", nil, schema.RunRequest{}, "empty skill and prompt"},
		{"no bin", func(c *config.Config, _ *Options) { c.ClaudeBin = "" }, schema.RunRequest{Skill: "s"}, "not configured"},
		{"missing workdir", nil, schema.RunRequest{Skill: "s", WorkDir: filepath.Join(file, "nope")}, "workdir"},
		{"workdir is file", nil, schema.RunRequest{Skill: "s", WorkDir: file}, "not a directory"},
		{"no timeout", func(c *config.Config, _ *Options) { c.RunTimeout = 0 }, schema.RunRequest{Skill: "s"}, "timeout"},
		{"no log dir", func(c *config.Config, _ *Options) { c.LogDir = "" }, schema.RunRequest{Skill: "s"}, "log dir"},
		{"binary missing", func(_ *config.Config, o *Options) {
			o.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		}, schema.RunRequest{Skill: "s"}, "not found on PATH"},
		{"log dir is a file", func(c *config.Config, _ *Options) { c.LogDir = file }, schema.RunRequest{Skill: "s"}, "create log dir"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t)
			opt := Options{Exec: helperFactory("success")}
			if tc.mutate != nil {
				tc.mutate(&cfg, &opt)
			}
			_, err := New(cfg, opt).Run(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestRunStartFailure(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	factory := func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "does-not-exist"), args...)
	}
	res, err := New(cfg, Options{Exec: factory}).Run(context.Background(), schema.RunRequest{Skill: "s"})
	if err == nil || errors.Is(err, schema.ErrRunFailed) || !strings.Contains(err.Error(), "start") {
		t.Fatalf("err %v", err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("exit code %d", res.ExitCode)
	}
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	r := New(config.Default(), Options{})
	if r.clock == nil || r.exec == nil || r.lookPath == nil || r.log == nil {
		t.Fatal("defaults not filled")
	}
	// Default LookPath catches a missing binary before anything spawns.
	cfg := testConfig(t)
	cfg.ClaudeBin = "jev-definitely-not-a-real-binary-xyz"
	_, err := New(cfg, Options{}).Run(context.Background(), schema.RunRequest{Skill: "s"})
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if custom := New(cfg, Options{Exec: helperFactory("success")}); custom.lookPath != nil {
		t.Fatal("custom Exec should skip LookPath")
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{strings.Repeat("a", 200), 200, strings.Repeat("a", 200)},
		{strings.Repeat("a", 201), 200, strings.Repeat("a", 200)},
		{"héllo", 2, "hé"},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Fatalf("truncate(%q,%d)=%q", tc.in, tc.n, got)
		}
	}
}

func TestHeaderPromptTruncated(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, "success", nil)
	long := strings.Repeat("p", 500)
	res, err := r.Run(context.Background(), schema.RunRequest{Prompt: long})
	if err != nil {
		t.Fatal(err)
	}
	var h header
	if err := json.Unmarshal([]byte(readLogLines(t, res.LogPath)[0]), &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Argv[2]) != maxLoggedPrompt {
		t.Fatalf("logged prompt len %d", len(h.Argv[2]))
	}
}
