package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeGitHub serves a latest release with one asset and SHA256SUMS. sumOverride,
// when set, replaces the asset's real checksum.
func fakeGitHub(t *testing.T, env *Env, tag string, asset []byte, sumOverride string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			rel := map[string]any{"tag_name": tag, "assets": []map[string]string{
				{"name": env.AssetName(), "browser_download_url": srv.URL + "/dl/asset"},
				{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/dl/sums"},
			}}
			_ = json.NewEncoder(w).Encode(rel)
		case "/dl/asset":
			_, _ = w.Write(asset)
		case "/dl/sums":
			sum := sha(asset)
			if sumOverride != "" {
				sum = sumOverride
			}
			fmt.Fprintf(w, "%s  other_file\n%s  %s\n", strings.Repeat("0", 64), sum, env.AssetName())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env.APIBase = srv.URL
	env.HTTP = srv.Client()
}

// installed puts a current binary at the install target.
func installed(t *testing.T, env *Env) {
	t.Helper()
	if err := os.MkdirAll(env.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.Target(), []byte("binary v0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestNewer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tag, version string
		want         bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.1", "v0.1.0-3-gabcdef-dirty", true},
		{"v0.1.0", "v0.1.0-3-gabcdef", false},
		{"v0.1.0", "dev", true},
		{"v0.1.0", "4ab22b2-dirty", true},
		{"latest", "v0.1.0", false},
		{"v1.2", "v0.1.0", false},
		{"v1.x.0", "v0.1.0", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.tag, tc.version); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v", tc.tag, tc.version, got)
		}
	}
}

func TestUpdateReplacesAndRestarts(t *testing.T) {
	t.Parallel()
	env, host, out, _ := testEnv(t, "darwin")
	installed(t, env)
	host.loaded = true
	fakeGitHub(t, env, "v0.2.0", []byte("binary v0.2.0"), "")
	if err := env.Update(context.Background(), UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(env.Target()); string(b) != "binary v0.2.0" {
		t.Errorf("target = %q", b)
	}
	if b, _ := os.ReadFile(env.Target() + ".prev"); string(b) != "binary v0.1.0" {
		t.Errorf("prev = %q", b)
	}
	if !host.called(launchctl + " kickstart -k gui/501/" + AgentLabel) {
		t.Errorf("not restarted: %v", host.calls)
	}
	if info, _ := os.Stat(env.Target()); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(env.BinDir); len(entries) != 2 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestUpdateRollsBackWhenDaemonUnhealthy(t *testing.T) {
	t.Parallel()
	env, host, _, healthy := testEnv(t, "darwin")
	installed(t, env)
	host.loaded = true
	healthy.Store(false)
	fakeGitHub(t, env, "v0.2.0", []byte("binary v0.2.0"), "")
	err := env.Update(context.Background(), UpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(env.Target()); string(b) != "binary v0.1.0" {
		t.Errorf("not rolled back: %q", b)
	}
}

func TestUpdateRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	env, _, out, _ := testEnv(t, "linux")
	installed(t, env)
	fakeGitHub(t, env, "v0.1.0", []byte("binary v0.1.0"), "")
	if err := env.Update(ctx, UpdateOptions{}); err != nil || !strings.Contains(out.String(), "is the latest") {
		t.Errorf("current: %v %s", err, out)
	}

	env, _, out, _ = testEnv(t, "linux")
	installed(t, env)
	fakeGitHub(t, env, "v0.2.0", []byte("binary v0.2.0"), "")
	if err := env.Update(ctx, UpdateOptions{CheckOnly: true}); err != nil || !strings.Contains(out.String(), "v0.2.0 is available") {
		t.Errorf("check: %v %s", err, out)
	}
	if b, _ := os.ReadFile(env.Target()); string(b) != "binary v0.1.0" {
		t.Error("check-only changed the binary")
	}

	env, _, _, _ = testEnv(t, "linux")
	installed(t, env)
	fakeGitHub(t, env, "v0.2.0", []byte("binary v0.2.0"), strings.Repeat("a", 64))
	if err := env.Update(ctx, UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("mismatch: %v", err)
	}
	if b, _ := os.ReadFile(env.Target()); string(b) != "binary v0.1.0" {
		t.Error("mismatch changed the binary")
	}
	if entries, _ := os.ReadDir(env.BinDir); len(entries) != 1 {
		t.Errorf("leftover files after mismatch: %v", entries)
	}

	env, _, _, _ = testEnv(t, "linux")
	installed(t, env)
	fakeGitHub(t, env, "v0.2.0", []byte("binary v9.9.9"), "")
	if err := env.Update(ctx, UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "reports") {
		t.Errorf("wrong version: %v", err)
	}

	env, _, _, _ = testEnv(t, "linux")
	fakeGitHub(t, env, "v0.2.0", []byte("binary v0.2.0"), "")
	if err := env.Update(ctx, UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("not installed: %v", err)
	}

	env, _, _, _ = testEnv(t, "linux")
	installed(t, env)
	fakeGitHub(t, env, "v0.1.0", []byte("binary v0.1.0"), "")
	if err := env.Update(ctx, UpdateOptions{Force: true}); err != nil {
		t.Errorf("force: %v", err)
	}
}

func TestReleaseHelpers(t *testing.T) {
	t.Parallel()
	env, _, _, _ := testEnv(t, "linux")
	if env.AssetName() != "jev_linux_arm64" {
		t.Errorf("asset = %s", env.AssetName())
	}
	env.GOOS = "darwin"
	if env.AssetName() != "jev_darwin_universal" {
		t.Errorf("asset = %s", env.AssetName())
	}
	r := Release{Tag: "v1.0.0"}
	r.Assets = append(r.Assets, struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{"plain", "http://insecure/x"})
	if _, err := r.assetURL("plain"); err == nil {
		t.Error("http asset accepted")
	}
	if _, err := r.assetURL("missing"); err == nil {
		t.Error("missing asset accepted")
	}
	if _, err := checksumFor([]byte("short  x\n"), "x"); err == nil {
		t.Error("short checksum accepted")
	}
	if got, err := checksumFor([]byte(strings.Repeat("AB", 32)+" *x\n"), "x"); err != nil || got != strings.Repeat("ab", 32) {
		t.Errorf("binary-mode line: %q %v", got, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write(make([]byte, 100))
			return
		}
		if r.URL.Path == "/bad" {
			_, _ = w.Write([]byte(`{"assets":[]}`))
			return
		}
		http.Error(w, "nope", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	env.HTTP = srv.Client()
	if _, err := env.get(context.Background(), srv.URL+"/big", 10); err == nil {
		t.Error("oversized body accepted")
	}
	if _, err := env.get(context.Background(), srv.URL+"/x", 10); err == nil || !strings.Contains(err.Error(), "418") {
		t.Errorf("status: %v", err)
	}
	env.APIBase = srv.URL + "/bad#"
	if _, err := env.LatestRelease(context.Background()); err == nil {
		t.Error("tagless release accepted")
	}
}
