package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDarwin(t *testing.T) {
	t.Parallel()
	env, host, out, _ := testEnv(t, "darwin")
	env.Getenv = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-test-0123456789"
		}
		return ""
	}
	ctx := context.Background()
	if err := env.Install(ctx, InstallOptions{}); err != nil {
		t.Fatalf("Install: %v\n%s", err, out)
	}

	if b, err := os.ReadFile(env.Target()); err != nil || string(b) != "binary v0.1.0" {
		t.Errorf("binary not copied: %q %v", b, err)
	}
	if len(host.stdin) != 1 || !strings.Contains(host.stdin[0], "-w sk-ant-test-0123456789") {
		t.Errorf("key not sent on stdin: %q", host.stdin)
	}
	for _, c := range host.calls {
		if strings.Contains(c, "sk-ant-test") {
			t.Errorf("key in argv: %s", c)
		}
	}
	plist, err := os.ReadFile(env.AgentPlist())
	if err != nil {
		t.Fatalf("plist: %v", err)
	}
	for _, want := range []string{env.Target(), "<string>router</string>", "<string>jev-pro</string>", AgentLabel} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if !host.called(launchctl + " bootstrap gui/501 " + env.AgentPlist()) {
		t.Errorf("agent not bootstrapped: %v", host.calls)
	}
	hook, status := env.SettingsWired()
	if !hook || !status {
		t.Errorf("settings wired = %v %v", hook, status)
	}
	if !strings.Contains(out.String(), "not on your PATH") {
		t.Errorf("missing PATH hint:\n%s", out)
	}

	// Running it again changes nothing and stores no second key.
	out.Reset()
	env.Exe = env.Target()
	if err := env.Install(ctx, InstallOptions{}); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	for _, want := range []string{"already at", "API key found", "already set up"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("second run missing %q:\n%s", want, out)
		}
	}
	if len(host.stdin) != 1 {
		t.Errorf("key stored again: %d", len(host.stdin))
	}
}

func TestInstallKeyPrompts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// No env key: prompt; empty answer skips.
	env, host, out, _ := testEnv(t, "darwin")
	env.installKey(ctx, InstallOptions{})
	if host.hasKey || !strings.Contains(out.String(), "no API key stored") {
		t.Errorf("empty prompt: hasKey=%v\n%s", host.hasKey, out)
	}

	// Prompted key is stored.
	env, host, _, _ = testEnv(t, "darwin")
	env.ReadSecret = func(string) (string, error) { return "sk-ant-typed-0123456789", nil }
	env.installKey(ctx, InstallOptions{})
	if !host.hasKey {
		t.Error("typed key not stored")
	}

	// Env key declined: nothing stored.
	env, host, _, _ = testEnv(t, "darwin")
	env.Getenv = func(string) string { return "sk-ant-env-0123456789" }
	env.Confirm = func(string) bool { return false }
	env.installKey(ctx, InstallOptions{})
	if host.hasKey {
		t.Error("declined key stored")
	}

	// -yes without an env key never prompts.
	env, host, _, _ = testEnv(t, "darwin")
	env.ReadSecret = func(string) (string, error) { t.Error("prompted under -yes"); return "", nil }
	env.installKey(ctx, InstallOptions{Yes: true})
	if host.hasKey {
		t.Error("key stored without one")
	}

	// A malformed key is rejected before reaching security.
	env, host, out, _ = testEnv(t, "darwin")
	env.ReadSecret = func(string) (string, error) { return "bad key; rm -rf", nil }
	env.installKey(ctx, InstallOptions{})
	if host.hasKey || len(host.stdin) != 0 || !strings.Contains(out.String(), "does not look like") {
		t.Errorf("bad key: hasKey=%v stdin=%q\n%s", host.hasKey, host.stdin, out)
	}
}

func TestInstallLinuxAndOptions(t *testing.T) {
	t.Parallel()
	env, host, out, _ := testEnv(t, "linux")
	env.Getenv = func(k string) string {
		if k == "PATH" {
			return "/usr/bin:" + env.BinDir
		}
		return ""
	}
	if err := env.Install(context.Background(), InstallOptions{NoSettings: true}); err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 0 {
		t.Errorf("linux install ran %v", host.calls)
	}
	if strings.Contains(out.String(), "PATH;") || !strings.Contains(out.String(), "macOS-only") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(env.ClaudeSettings()); !os.IsNotExist(err) {
		t.Error("settings touched with NoSettings")
	}

	env, host, _, _ = testEnv(t, "darwin")
	if err := env.Install(context.Background(), InstallOptions{NoAgent: true, Yes: true}); err != nil {
		t.Fatal(err)
	}
	if host.loaded {
		t.Error("agent loaded with NoAgent")
	}
}

func TestInstallUnhealthyDaemonAndDeclinedSettings(t *testing.T) {
	t.Parallel()
	env, _, out, healthy := testEnv(t, "darwin")
	healthy.Store(false)
	env.Confirm = func(string) bool { return false }
	if err := env.Install(context.Background(), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"daemon did not answer", "settings left unchanged"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(env.ClaudeSettings()); !os.IsNotExist(err) {
		t.Error("declined settings were written")
	}
}

func TestUninstall(t *testing.T) {
	t.Parallel()
	env, host, out, _ := testEnv(t, "darwin")
	ctx := context.Background()
	host.hasKey = true
	if err := env.Install(ctx, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.Target()+".prev", []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := env.Uninstall(ctx, UninstallOptions{Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	for _, p := range []string{env.Target(), env.Target() + ".prev", env.AgentPlist()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", p)
		}
	}
	if hook, status := env.SettingsWired(); hook || status {
		t.Error("settings still wired")
	}
	if !host.hasKey {
		t.Error("key removed without -purge")
	}

	if err := os.MkdirAll(filepath.Join(env.JevDir(), "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := env.Uninstall(ctx, UninstallOptions{Yes: true, Purge: true}); err != nil {
		t.Fatal(err)
	}
	if host.hasKey {
		t.Error("key kept with -purge")
	}
	if _, err := os.Stat(env.JevDir()); !os.IsNotExist(err) {
		t.Error("~/.jev kept with -purge")
	}
}
