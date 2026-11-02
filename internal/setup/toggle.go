package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// OffFile is the marker in ~/.jev that `jev off` writes. While it exists the
// Claude Code hook stays silent and the status line shows "Jev: off".
const OffFile = "off"

// OffMarker is the path of the off marker.
func (e *Env) OffMarker() string { return filepath.Join(e.JevDir(), OffFile) }

// IsOff reports whether `jev off` is in effect.
func (e *Env) IsOff() bool {
	_, err := os.Stat(e.OffMarker())
	return err == nil
}

// Off pauses Jev without uninstalling it: it stops the daemon, keeps launchd
// from starting it at login, and silences the hook. Keys, settings, and the
// binary stay in place; On undoes it.
func (e *Env) Off(ctx context.Context) error {
	if err := os.MkdirAll(e.JevDir(), 0o700); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if err := os.WriteFile(e.OffMarker(), []byte("jev off\n"), 0o600); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	e.step(true, "Claude Code hook paused")
	if !e.darwin() {
		e.step(false, "stop `jev router` yourself if it is running")
		return nil
	}
	// disable is what keeps it off across logins; bootout stops it now.
	if _, err := e.Exec(ctx, nil, launchctl, "disable", e.service()); err != nil {
		return fmt.Errorf("setup: disable agent: %w", err)
	}
	_, _ = e.Exec(ctx, nil, launchctl, "bootout", e.service()) // not loaded is fine
	e.step(true, "daemon stopped and will not start at login")
	e.printf("Jev is off. Run `jev on` to turn it back on.\n")
	return nil
}

// On undoes Off: it removes the marker, re-enables the login agent, and starts
// the daemon.
func (e *Env) On(ctx context.Context) error {
	if err := os.Remove(e.OffMarker()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("setup: %w", err)
	}
	e.step(true, "Claude Code hook active")
	if !e.darwin() {
		e.step(false, "start `jev router` yourself")
		return nil
	}
	if _, err := os.Stat(e.AgentPlist()); err != nil {
		e.step(false, "login agent not installed; run `jev install`")
		return nil
	}
	if err := e.startAgent(ctx); err != nil {
		return err
	}
	if e.WaitHealthy(ctx, e.healthTimeout()) {
		e.step(true, "daemon running at %s and starting at every login", e.DaemonAddr)
	} else {
		e.step(false, "daemon did not answer yet; check %s", filepath.Join(e.JevDir(), "jev-router.log"))
	}
	e.printf("Jev is on.\n")
	return nil
}

// startAgent clears any `jev off` override and loads the agent if launchd
// does not have it yet.
func (e *Env) startAgent(ctx context.Context) error {
	if _, err := e.Exec(ctx, nil, launchctl, "enable", e.service()); err != nil {
		return fmt.Errorf("setup: enable agent: %w", err)
	}
	if e.AgentLoaded(ctx) {
		return nil
	}
	if _, err := e.Exec(ctx, nil, launchctl, "bootstrap", e.domain(), e.AgentPlist()); err != nil {
		return fmt.Errorf("setup: load agent: %w", err)
	}
	return nil
}

// Status reports whether Jev is on and whether its daemon is running: a
// shorter, offline subset of Doctor.
func (e *Env) Status(ctx context.Context) []Check {
	var cs []Check
	add := func(name string, ok bool, detail string) { cs = append(cs, Check{name, ok, detail}) }
	off := e.IsOff()
	if off {
		add("jev", false, "off; run `jev on` to turn it back on")
	} else {
		add("jev", true, "on")
	}
	if e.darwin() {
		switch {
		case e.AgentLoaded(ctx):
			add("login agent", true, AgentLabel+" loaded")
		case off:
			add("login agent", false, "stopped by `jev off`")
		default:
			add("login agent", false, "not loaded; run `jev on` or `jev install`")
		}
	}
	switch _, err := e.daemonStatus(ctx); {
	case err == nil:
		add("daemon", true, "answering at "+e.DaemonAddr)
	case off:
		add("daemon", false, "stopped by `jev off`")
	default:
		add("daemon", false, "not answering at "+e.DaemonAddr+"; see "+filepath.Join(e.JevDir(), "jev-router.log"))
	}
	return cs
}
