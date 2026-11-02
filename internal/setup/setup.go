// Package setup installs, updates, checks, and removes Jev on a user's
// machine: the jev binary, the API key in the macOS keychain, the launchd
// agent that runs `jev router`, and the Claude Code hook and status line.
//
// Everything that touches the host goes through Env so it can be faked in
// tests. Nothing here prints to stdout directly; output goes to Env.Out.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// Repo is the GitHub repository releases come from.
	Repo = "swiftj/jevons"
	// KeychainService is the keychain item holding the Anthropic API key.
	KeychainService = "jev-pro"
	// AgentLabel is the launchd label of the daemon agent.
	AgentLabel = "com.github.swiftj.jev-router"
	// BinaryName is the installed executable's name.
	BinaryName = "jev"
)

// Env is the host as setup sees it. Zero fields are not defaulted; use
// DefaultEnv for a real machine.
type Env struct {
	GOOS, GOARCH string
	Home         string // user home directory
	BinDir       string // where jev is installed
	Exe          string // path of the running binary
	Version      string // version of the running binary
	User         string // login name, the keychain account
	UID          int
	DaemonAddr   string // host:port of the daemon
	APIBase      string // GitHub API base URL
	APIKeyEnv    string // env var the classifier reads
	// HealthTimeout bounds waiting for the daemon after (re)starting it
	// (default 10s).
	HealthTimeout time.Duration

	HTTP   *http.Client
	Getenv func(string) string
	// Exec runs name with args, feeding stdin (may be nil), and returns stdout.
	Exec func(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
	// Confirm asks a yes/no question; true means yes.
	Confirm func(question string) bool
	// ReadSecret prompts for a value without echoing it.
	ReadSecret func(prompt string) (string, error)
	Out        io.Writer
}

// DefaultExec runs a process with exec.CommandContext and an argv slice.
func DefaultExec(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed tool paths, argv slice
	cmd.Stdin = stdin
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return out, nil
}

func (e *Env) healthTimeout() time.Duration {
	if e.HealthTimeout > 0 {
		return e.HealthTimeout
	}
	return 10 * time.Second
}

// Target is where the jev binary is installed.
func (e *Env) Target() string { return filepath.Join(e.BinDir, BinaryName) }

// JevDir is Jev's state directory (~/.jev).
func (e *Env) JevDir() string { return filepath.Join(e.Home, ".jev") }

// AgentPlist is the launchd agent file.
func (e *Env) AgentPlist() string {
	return filepath.Join(e.Home, "Library", "LaunchAgents", AgentLabel+".plist")
}

// ClaudeSettings is the user-level Claude Code settings file.
func (e *Env) ClaudeSettings() string { return filepath.Join(e.Home, ".claude", "settings.json") }

// darwin reports whether launchd and the keychain are available.
func (e *Env) darwin() bool { return e.GOOS == "darwin" }

func (e *Env) printf(format string, a ...any) {
	fmt.Fprintf(e.Out, format, a...)
}

// step prints a progress line: "✓ msg" or "! msg".
func (e *Env) step(ok bool, format string, a ...any) {
	mark := "✓"
	if !ok {
		mark = "!"
	}
	e.printf("%s %s\n", mark, fmt.Sprintf(format, a...))
}
