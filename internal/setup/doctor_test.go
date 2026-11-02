package setup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func checksByName(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestDoctorHealthy(t *testing.T) {
	t.Parallel()
	env, host, _, _ := testEnv(t, "darwin")
	host.keys = map[string]bool{KeychainService: true, TypeSafeKeychainService: true}
	if err := env.Install(context.Background(), InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	env.Getenv = func(k string) string {
		if k == "PATH" {
			return env.BinDir
		}
		return ""
	}
	fakeGitHub(t, env, "v0.1.0", []byte("x"), "")
	daemonClient := env.HTTP
	_ = daemonClient
	// fakeGitHub swapped HTTP to the TLS test client, which can still reach
	// the plain-HTTP daemon.
	cs := checksByName(env.Doctor(context.Background(), true))
	for _, name := range []string{"binary", "PATH", "update", "Anthropic API key", "TypeSafe API key", "login agent", "daemon", "classifier", "Claude Code"} {
		if c, ok := cs[name]; !ok || !c.OK {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestDoctorProblems(t *testing.T) {
	t.Parallel()
	env, _, _, healthy := testEnv(t, "darwin")
	healthy.Store(false)
	env.Getenv = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-env-0123456789"
		}
		return ""
	}
	cs := checksByName(env.Doctor(context.Background(), false))
	for _, name := range []string{"binary", "PATH", "Anthropic API key", "TypeSafe API key", "login agent", "daemon", "Claude Code"} {
		if cs[name].OK {
			t.Errorf("%s unexpectedly OK: %+v", name, cs[name])
		}
	}
	if !strings.Contains(cs["Anthropic API key"].Detail, "login agent cannot see it") {
		t.Errorf("API key detail = %q", cs["Anthropic API key"].Detail)
	}
	if _, ok := cs["update"]; ok {
		t.Error("offline doctor checked for updates")
	}

	env.Getenv = func(string) string { return "" }
	if c := checksByName(env.Doctor(context.Background(), false))["Anthropic API key"]; c.OK || !strings.Contains(c.Detail, "not set") {
		t.Errorf("no key: %+v", c)
	}
}

func TestDoctorClassifierAndUpdate(t *testing.T) {
	t.Parallel()
	env, _, _, _ := testEnv(t, "linux")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			_, _ = io.WriteString(w, `{"uptime":"5m","classifier_errors":3,"last_classifier_error":"timeout"}`)
		case "/repos/" + Repo + "/releases/latest":
			_, _ = io.WriteString(w, `{"tag_name":"v9.0.0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env.DaemonAddr = strings.TrimPrefix(srv.URL, "http://")
	env.APIBase = srv.URL
	env.HTTP = srv.Client()
	cs := checksByName(env.Doctor(context.Background(), true))
	if c := cs["classifier"]; c.OK || !strings.Contains(c.Detail, "timeout") {
		t.Errorf("classifier: %+v", c)
	}
	if c := cs["update"]; c.OK || !strings.Contains(c.Detail, "v9.0.0") {
		t.Errorf("update: %+v", c)
	}
	if _, ok := cs["login agent"]; ok {
		t.Error("linux doctor checked launchd")
	}

	env.APIBase = srv.URL + "/missing#"
	if c := checksByName(env.Doctor(context.Background(), true))["update"]; c.OK || !strings.Contains(c.Detail, "could not check") {
		t.Errorf("update failure: %+v", c)
	}
}
