package setup

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// withAgent puts the agent plist in place and loads it, as Install would.
func withAgent(ctx context.Context, t *testing.T, env *Env, host *fakeHost) {
	t.Helper()
	if err := env.InstallAgent(ctx); err != nil {
		t.Fatalf("InstallAgent: %v", err)
	}
	if !host.loaded {
		t.Fatal("agent not loaded after install")
	}
}

func checkNamed(cs []Check, name string) (Check, bool) {
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

func TestOffOnDarwin(t *testing.T) {
	env, host, out, healthy := testEnv(t, "darwin")
	ctx := context.Background()
	withAgent(ctx, t, env, host)

	if err := env.Off(ctx); err != nil {
		t.Fatalf("Off: %v", err)
	}
	if !env.IsOff() || host.loaded || !host.disabled {
		t.Fatalf("after off: marker=%v loaded=%v disabled=%v", env.IsOff(), host.loaded, host.disabled)
	}
	if info, err := os.Stat(env.OffMarker()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marker: %v %v", info, err)
	}
	healthy.Store(false) // the daemon is gone
	st := env.Status(ctx)
	for _, name := range []string{"jev", "login agent", "daemon"} {
		c, ok := checkNamed(st, name)
		if !ok || c.OK {
			t.Errorf("status %s = %+v, want failing", name, c)
		}
	}
	if c, _ := checkNamed(st, "daemon"); !strings.Contains(c.Detail, "jev off") {
		t.Errorf("daemon detail = %q", c.Detail)
	}
	if c, ok := checkNamed(env.Doctor(ctx, false), "on/off"); !ok || c.OK {
		t.Errorf("doctor on/off = %+v", c)
	}

	// Off twice is harmless.
	if err := env.Off(ctx); err != nil {
		t.Fatalf("second Off: %v", err)
	}

	healthy.Store(true)
	if err := env.On(ctx); err != nil {
		t.Fatalf("On: %v", err)
	}
	if env.IsOff() || !host.loaded || host.disabled {
		t.Fatalf("after on: marker=%v loaded=%v disabled=%v", env.IsOff(), host.loaded, host.disabled)
	}
	for _, c := range env.Status(ctx) {
		if !c.OK {
			t.Errorf("status after on: %+v", c)
		}
	}
	if _, ok := checkNamed(env.Doctor(ctx, false), "on/off"); ok {
		t.Error("doctor reports on/off while on")
	}
	if !strings.Contains(out.String(), "Jev is off.") || !strings.Contains(out.String(), "Jev is on.") {
		t.Errorf("output:\n%s", out)
	}

	// On while already loaded does not bootstrap again.
	before := len(host.calls)
	if err := env.On(ctx); err != nil {
		t.Fatalf("second On: %v", err)
	}
	for _, c := range host.calls[before:] {
		if strings.HasPrefix(c, launchctl+" bootstrap") {
			t.Errorf("bootstrapped a loaded agent: %s", c)
		}
	}
}

func TestInstallClearsOff(t *testing.T) {
	env, host, _, _ := testEnv(t, "darwin")
	ctx := context.Background()
	withAgent(ctx, t, env, host)
	if err := env.Off(ctx); err != nil {
		t.Fatal(err)
	}
	// A disabled agent cannot be bootstrapped, so install must enable it first.
	withAgent(ctx, t, env, host)
	if env.IsOff() || host.disabled {
		t.Fatalf("install left jev off: marker=%v disabled=%v", env.IsOff(), host.disabled)
	}
}

func TestUninstallClearsOff(t *testing.T) {
	env, host, _, _ := testEnv(t, "darwin")
	ctx := context.Background()
	withAgent(ctx, t, env, host)
	if err := env.Off(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.Uninstall(ctx, UninstallOptions{Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if env.IsOff() || host.disabled {
		t.Fatalf("uninstall left jev off: marker=%v disabled=%v", env.IsOff(), host.disabled)
	}
}

func TestOnWithoutAgent(t *testing.T) {
	env, host, out, _ := testEnv(t, "darwin")
	if err := env.On(context.Background()); err != nil {
		t.Fatalf("On: %v", err)
	}
	if host.called(launchctl) {
		t.Error("touched launchd without an installed agent")
	}
	if !strings.Contains(out.String(), "run `jev install`") {
		t.Errorf("output:\n%s", out)
	}
	if c, _ := checkNamed(env.Status(context.Background()), "login agent"); c.OK || !strings.Contains(c.Detail, "jev install") {
		t.Errorf("login agent = %+v", c)
	}
}

func TestOffOnLinux(t *testing.T) {
	env, host, out, _ := testEnv(t, "linux")
	ctx := context.Background()
	if err := env.Off(ctx); err != nil {
		t.Fatalf("Off: %v", err)
	}
	if !env.IsOff() {
		t.Fatal("marker missing")
	}
	if err := env.On(ctx); err != nil {
		t.Fatalf("On: %v", err)
	}
	if env.IsOff() || host.called(launchctl) {
		t.Fatalf("marker=%v launchctl=%v", env.IsOff(), host.called(launchctl))
	}
	if _, ok := checkNamed(env.Status(ctx), "login agent"); ok {
		t.Error("linux status reports a login agent")
	}
	if !strings.Contains(out.String(), "yourself") {
		t.Errorf("output:\n%s", out)
	}
}

func TestToggleErrors(t *testing.T) {
	ctx := context.Background()
	failOn := func(host *fakeHost, verb string) func(context.Context, io.Reader, string, ...string) ([]byte, error) {
		return func(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
			if name == launchctl && args[0] == verb {
				return nil, errors.New("launchctl failed")
			}
			return host.exec(ctx, stdin, name, args...)
		}
	}

	t.Run("state dir unwritable", func(t *testing.T) {
		env, _, _, _ := testEnv(t, "darwin")
		if err := os.WriteFile(env.JevDir(), nil, 0o600); err != nil { // ~/.jev is a file
			t.Fatal(err)
		}
		if err := env.Off(ctx); err == nil {
			t.Error("Off succeeded with ~/.jev unwritable")
		}
	})
	t.Run("disable fails", func(t *testing.T) {
		env, host, _, _ := testEnv(t, "darwin")
		env.Exec = failOn(host, "disable")
		if err := env.Off(ctx); err == nil || !strings.Contains(err.Error(), "disable agent") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("enable fails", func(t *testing.T) {
		env, host, _, _ := testEnv(t, "darwin")
		withAgent(ctx, t, env, host)
		env.Exec = failOn(host, "enable")
		if err := env.On(ctx); err == nil || !strings.Contains(err.Error(), "enable agent") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("bootstrap fails", func(t *testing.T) {
		env, host, _, _ := testEnv(t, "darwin")
		withAgent(ctx, t, env, host)
		if err := env.Off(ctx); err != nil {
			t.Fatal(err)
		}
		env.Exec = failOn(host, "bootstrap")
		if err := env.On(ctx); err == nil || !strings.Contains(err.Error(), "load agent") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("daemon slow to start", func(t *testing.T) {
		env, host, out, healthy := testEnv(t, "darwin")
		withAgent(ctx, t, env, host)
		if err := env.Off(ctx); err != nil {
			t.Fatal(err)
		}
		healthy.Store(false)
		if err := env.On(ctx); err != nil {
			t.Fatalf("On: %v", err)
		}
		if !strings.Contains(out.String(), "did not answer yet") {
			t.Errorf("output:\n%s", out)
		}
	})
}
