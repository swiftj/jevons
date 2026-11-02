package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InstallOptions control Install.
type InstallOptions struct {
	Yes        bool // accept every prompt (non-interactive)
	NoAgent    bool // skip the launchd agent
	NoSettings bool // leave Claude Code settings alone
}

// Install puts jev in BinDir, stores the API key, starts the daemon at login,
// and wires the Claude Code hook and status line. Each step reports what it
// did; it is safe to run again.
func (e *Env) Install(ctx context.Context, opt InstallOptions) error {
	if err := e.installBinary(); err != nil {
		return err
	}
	if !onPath(e.BinDir, e.Getenv("PATH")) {
		e.step(false, "%s is not on your PATH; add this to ~/.zshrc:\n    export PATH=\"%s:$PATH\"", e.BinDir, e.BinDir)
	}

	if e.darwin() {
		e.installKey(ctx, opt, e.anthropicKey())
		e.installKey(ctx, opt, e.typesafeKey())
		if !opt.NoAgent {
			if err := e.InstallAgent(ctx); err != nil {
				return err
			}
			if e.WaitHealthy(ctx, e.healthTimeout()) {
				e.step(true, "daemon running at %s and starting at every login", e.DaemonAddr)
			} else {
				e.step(false, "daemon did not answer yet; check %s", filepath.Join(e.JevDir(), "jev-router.log"))
			}
		}
	} else {
		e.step(false, "launchd and keychain setup are macOS-only; start the daemon with `%s router`", e.Target())
	}

	if !opt.NoSettings {
		if err := e.installSettings(opt); err != nil {
			return err
		}
	}
	e.printf("\nDone. Run `jev doctor` any time to check the setup, and `jev update` for new releases.\n")
	return nil
}

// installBinary copies the running executable to BinDir/jev.
func (e *Env) installBinary() error {
	target := e.Target()
	if same(e.Exe, target) {
		e.step(true, "jev %s already at %s", e.Version, target)
		return nil
	}
	if err := os.MkdirAll(e.BinDir, 0o755); err != nil { // #nosec G301 -- a bin directory
		return fmt.Errorf("setup: %w", err)
	}
	src, err := os.Open(e.Exe)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer func() { _ = src.Close() }() // read-only
	data, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("setup: read %s: %w", e.Exe, err)
	}
	if err := writeFileAtomic(target, data, 0o755); err != nil {
		return err
	}
	e.step(true, "installed jev %s to %s", e.Version, target)
	return nil
}

// apiKey describes one API key the daemon reads from the keychain.
type apiKey struct {
	Name    string // shown to the user, e.g. "Anthropic API key"
	Service string // keychain service
	Env     string // env var it may already be in
	Missing string // consequence of not storing it
}

func (e *Env) anthropicKey() apiKey {
	return apiKey{"Anthropic API key", KeychainService, e.APIKeyEnv, "free-text prompts route to Tier 2"}
}

func (e *Env) typesafeKey() apiKey {
	return apiKey{"TypeSafe API key", TypeSafeKeychainService, e.TypeSafeKeyEnv, "Jev routing is off and Haiku classifies prompts"}
}

// installKey makes sure the keychain holds k, offering to store the value of
// its env var or prompting for one.
func (e *Env) installKey(ctx context.Context, opt InstallOptions, k apiKey) {
	if e.HasKey(ctx, k.Service) {
		e.step(true, "%s found in the keychain (%s)", k.Name, k.Service)
		return
	}
	key := ""
	if k.Env != "" {
		key = e.Getenv(k.Env)
	}
	switch {
	case key != "" && (opt.Yes || e.Confirm(fmt.Sprintf("Store $%s in the macOS keychain for the daemon?", k.Env))):
	case key == "" && !opt.Yes:
		var err error
		if key, err = e.ReadSecret(k.Name + " (input hidden, Enter to skip): "); err != nil {
			key = ""
		}
	default:
		key = ""
	}
	if strings.TrimSpace(key) == "" {
		e.step(false, "no %s stored; %s. Re-run `jev install` to add one", k.Name, k.Missing)
		return
	}
	if err := e.StoreKey(ctx, k.Service, key); err != nil {
		e.step(false, "%s: %v", k.Name, err)
		return
	}
	e.step(true, "%s stored in the keychain (%s)", k.Name, k.Service)
}

// installSettings shows the planned Claude Code settings change and applies it
// on confirmation.
func (e *Env) installSettings(opt InstallOptions) error {
	plan, err := e.PlanSettings()
	if err != nil {
		e.step(false, "%v; leaving Claude Code settings alone", err)
		return nil
	}
	for _, n := range plan.Notes {
		e.step(false, "%s", n)
	}
	if len(plan.Changes) == 0 {
		e.step(true, "Claude Code hook and status line already set up")
		return nil
	}
	e.printf("Planned changes to %s:\n", plan.Path)
	for _, c := range plan.Changes {
		e.printf("  - %s\n", c)
	}
	if !opt.Yes && !e.Confirm("Apply them? (a backup is kept; keys are re-sorted)") {
		e.step(false, "Claude Code settings left unchanged")
		return nil
	}
	backup, err := plan.Apply()
	if err != nil {
		return err
	}
	if backup != "" {
		e.step(true, "Claude Code settings updated (backup: %s)", backup)
	} else {
		e.step(true, "Claude Code settings created")
	}
	return nil
}

// UninstallOptions control Uninstall.
type UninstallOptions struct {
	Yes   bool // accept every prompt
	Purge bool // also delete the keychain item and ~/.jev
}

// Uninstall reverses Install: agent, settings, binary, and with Purge the key
// and state directory.
func (e *Env) Uninstall(ctx context.Context, opt UninstallOptions) error {
	// Only touch launchd when this home has the agent installed.
	if _, err := os.Stat(e.AgentPlist()); e.darwin() && err == nil {
		if err := e.RemoveAgent(ctx); err != nil {
			return err
		}
		e.step(true, "daemon stopped and login agent removed")
	}
	plan, err := e.PlanRemoveSettings()
	switch {
	case err != nil:
		e.step(false, "%v; leaving Claude Code settings alone", err)
	case len(plan.Changes) > 0 && (opt.Yes || e.Confirm("Remove the jev hook and status line from "+plan.Path+"?")):
		if _, err := plan.Apply(); err != nil {
			return err
		}
		e.step(true, "Claude Code settings cleaned up")
	}
	for _, p := range []string{e.Target(), e.Target() + ".prev"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("setup: %w", err)
		}
	}
	e.step(true, "removed %s", e.Target())
	if !opt.Purge {
		return nil
	}
	if e.darwin() {
		for _, k := range []apiKey{e.anthropicKey(), e.typesafeKey()} {
			if err := e.DeleteKey(ctx, k.Service); err != nil {
				return err
			}
			e.step(true, "%s removed from the keychain", k.Name)
		}
	}
	if err := os.RemoveAll(e.JevDir()); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	e.step(true, "removed %s", e.JevDir())
	return nil
}

// same reports whether a and b are the same file.
func same(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// onPath reports whether dir is an entry of the PATH list.
func onPath(dir, path string) bool {
	for _, p := range filepath.SplitList(path) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}
