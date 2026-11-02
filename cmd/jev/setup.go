package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/internal/setup"
)

// runSetup handles install, update, doctor, and uninstall.
func runSetup(ctx context.Context, cmd string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("jev "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	binDir := fs.String("bin-dir", "", "install directory (default $JEV_BIN_DIR or ~/.local/bin)")
	cfgPath := fs.String("config", "", "path to JSON config file")
	var (
		yes, noAgent, noSettings, check, force, offline, purge bool
	)
	switch cmd {
	case "install":
		fs.BoolVar(&yes, "yes", false, "accept every prompt (non-interactive)")
		fs.BoolVar(&noAgent, "no-agent", false, "do not install the launchd agent")
		fs.BoolVar(&noSettings, "no-settings", false, "do not touch Claude Code settings")
	case "update":
		fs.BoolVar(&check, "check", false, "only report whether an update is available")
		fs.BoolVar(&force, "force", false, "reinstall even if already current")
	case "doctor":
		fs.BoolVar(&offline, "offline", false, "skip the GitHub release check")
	case "uninstall":
		fs.BoolVar(&yes, "yes", false, "accept every prompt (non-interactive)")
		fs.BoolVar(&purge, "purge", false, "also delete the keychain key and ~/.jev")
	}
	if err := fs.Parse(args); err != nil {
		return 2, err
	}

	env, err := newSetupEnv(ctx, *binDir, *cfgPath, stdin, stdout)
	if err != nil {
		return 1, err
	}
	switch cmd {
	case "install":
		err = env.Install(ctx, setup.InstallOptions{Yes: yes, NoAgent: noAgent, NoSettings: noSettings})
	case "update":
		err = env.Update(ctx, setup.UpdateOptions{CheckOnly: check, Force: force})
	case "uninstall":
		err = env.Uninstall(ctx, setup.UninstallOptions{Yes: yes, Purge: purge})
	case "doctor":
		failed := 0
		for _, c := range env.Doctor(ctx, !offline) {
			mark := "✓"
			if !c.OK {
				mark, failed = "✗", failed+1
			}
			fmt.Fprintf(stdout, "%s %-12s %s\n", mark, c.Name, c.Detail)
		}
		if failed > 0 {
			return 1, nil
		}
		return 0, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// newSetupEnv builds a setup.Env for this machine.
func newSetupEnv(ctx context.Context, binDir, cfgPath string, stdin io.Reader, stdout io.Writer) (*setup.Env, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if binDir == "" {
		binDir = os.Getenv("JEV_BIN_DIR")
	}
	if binDir == "" {
		binDir = filepath.Join(home, ".local", "bin")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	apiBase := os.Getenv("JEV_GITHUB_API")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	in := &lineReader{r: bufio.NewReader(stdin)}
	return &setup.Env{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Home: home, BinDir: binDir, Exe: exe, Version: version,
		User: name, UID: os.Getuid(),
		DaemonAddr: cfg.ListenAddr, APIBase: apiBase, APIKeyEnv: cfg.APIKeyEnv, TypeSafeKeyEnv: cfg.TypeSafeAPIKeyEnv,
		HTTP:   &http.Client{Timeout: 2 * time.Minute},
		Getenv: os.Getenv,
		Exec:   setup.DefaultExec,
		Out:    stdout,
		Confirm: func(q string) bool {
			fmt.Fprintf(stdout, "%s [Y/n] ", q)
			switch in.line() {
			case "", "y", "Y", "yes", "Yes":
				return true
			}
			return false
		},
		ReadSecret: func(prompt string) (string, error) {
			return readSecret(ctx, prompt, stdin, stdout, in)
		},
	}, nil
}

// readSecret reads one line with terminal echo turned off. It refuses when
// stdin is not a terminal, so a key is never read from a pipe by accident.
func readSecret(ctx context.Context, prompt string, stdin io.Reader, stdout io.Writer, in *lineReader) (string, error) {
	tty, ok := stdin.(*os.File)
	if !ok {
		return "", fmt.Errorf("stdin is not a terminal")
	}
	if _, err := setup.DefaultExec(ctx, tty, "/bin/stty", "-echo"); err != nil {
		return "", fmt.Errorf("stdin is not a terminal: %w", err)
	}
	defer func() {
		_, _ = setup.DefaultExec(ctx, tty, "/bin/stty", "echo") // restore echo
		fmt.Fprintln(stdout)
	}()
	fmt.Fprint(stdout, prompt)
	return in.line(), nil
}
