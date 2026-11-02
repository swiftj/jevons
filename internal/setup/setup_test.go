package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHost records Exec calls and answers them from simple state.
type fakeHost struct {
	mu        sync.Mutex
	calls     []string
	stdin     []string
	keys      map[string]bool // keychain service -> item present
	loaded    bool
	versionOf map[string]string // binary path -> `version` output
}

func (f *fakeHost) exec(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, string(b))
	}
	switch {
	case name == securityBin && args[0] == "find-generic-password":
		if f.keys[args[2]] {
			return nil, nil
		}
		return nil, errors.New("exit status 44")
	case name == securityBin && args[0] == "-i":
		if f.keys == nil {
			f.keys = map[string]bool{}
		}
		f.keys[strings.Fields(f.stdin[len(f.stdin)-1])[4]] = true
		return nil, nil
	case name == securityBin && args[0] == "delete-generic-password":
		delete(f.keys, args[2])
		return nil, nil
	case name == launchctl && args[0] == "print":
		if f.loaded {
			return nil, nil
		}
		return nil, errors.New("not loaded")
	case name == launchctl && args[0] == "bootstrap":
		f.loaded = true
		return nil, nil
	case name == launchctl && args[0] == "bootout":
		f.loaded = false
		return nil, nil
	case name == launchctl:
		return nil, nil
	case len(args) == 1 && args[0] == "version":
		if v, ok := f.versionOf[name]; ok {
			return []byte(v + "\n"), nil
		}
		// A freshly downloaded temp file: report what its contents say.
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		return bytes.TrimPrefix(b, []byte("binary ")), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
}

// hasKey reports whether the fake keychain holds service.
func (f *fakeHost) hasKey(service string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[service]
}

func (f *fakeHost) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// testEnv builds an Env rooted in a temp home with a healthy fake daemon.
func testEnv(t *testing.T, goos string) (*Env, *fakeHost, *bytes.Buffer, *atomic.Bool) {
	t.Helper()
	home := t.TempDir()
	healthy := &atomic.Bool{}
	healthy.Store(true)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/status" {
			_, _ = io.WriteString(w, `{"uptime":"1m","classifier_errors":0}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(daemon.Close)

	exe := filepath.Join(t.TempDir(), "jev")
	if err := os.WriteFile(exe, []byte("binary v0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{versionOf: map[string]string{}}
	out := &bytes.Buffer{}
	env := &Env{
		GOOS: goos, GOARCH: "arm64",
		Home: home, BinDir: filepath.Join(home, ".local", "bin"), Exe: exe, Version: "v0.1.0",
		User: "tester", UID: 501,
		DaemonAddr: strings.TrimPrefix(daemon.URL, "http://"),
		APIBase:    "http://unused.invalid", APIKeyEnv: "ANTHROPIC_API_KEY", TypeSafeKeyEnv: "TYPESAFE_API_KEY",
		HealthTimeout: 300 * time.Millisecond,
		HTTP:          daemon.Client(),
		Getenv:        func(string) string { return "" },
		Exec:          host.exec,
		Confirm:       func(string) bool { return true },
		ReadSecret:    func(string) (string, error) { return "", nil },
		Out:           out,
	}
	return env, host, out, healthy
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
