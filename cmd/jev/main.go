// Command jev is Jev-Pro in one binary: the routing daemon, the headless
// runner, the Claude Code hook and status line, and the installer/updater.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

var version = "dev"

const usage = `jev — pick the cheapest capable Claude model for each prompt.

Usage: jev <command> [flags]

Setup:
  install     install jev, store the API key, start the daemon at login, wire Claude Code
  update      replace jev with the latest GitHub release (-check to only look)
  doctor      check every part of the setup
  uninstall   remove the daemon, Claude Code wiring and binary (-purge: key and ~/.jev too)

Use:
  router      run the routing daemon (normally started by launchd)
  run         run a skill headlessly with claude -p, e.g. jev run -skill=/cleanup -route
  hook        Claude Code UserPromptSubmit hook (reads hook JSON on stdin)
  statusline  Claude Code status line (reads status JSON on stdin)
  version     print the version

Run "jev <command> -h" for a command's flags. Docs: https://github.com/swiftj/jevons/tree/main/docs
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code, err := dispatch(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if err != nil && !errors.Is(err, errUsage) {
		fmt.Fprintln(os.Stderr, "jev:", err)
	}
	os.Exit(code)
}

// errUsage means usage text was already printed.
var errUsage = errors.New("usage")

// dispatch runs the subcommand named by args[0] and returns the exit code.
func dispatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2, errUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "router":
		if err := runRouter(ctx, rest, stdout, stderr); err != nil {
			return 1, err
		}
		return 0, nil
	case "run":
		return runRunner(ctx, rest, stdout, stderr)
	case "hook":
		return runHook(ctx, rest, stdin, stdout, stderr)
	case "statusline":
		return runHook(ctx, append([]string{"-statusline"}, rest...), stdin, stdout, stderr)
	case "install", "update", "doctor", "uninstall":
		return runSetup(ctx, cmd, rest, stdin, stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version)
		return 0, nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0, nil
	}
	fmt.Fprint(stderr, usage)
	return 2, fmt.Errorf("unknown command %q", cmd)
}

// lineReader reads answers to prompts from stdin.
type lineReader struct{ r *bufio.Reader }

func (l *lineReader) line() string {
	s, _ := l.r.ReadString('\n') // EOF reads as an empty answer
	return strings.TrimSpace(s)
}
