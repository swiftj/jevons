// Command jev-runner executes a headless `claude -p` skill run, optionally
// asking the jev-router daemon which model to use, reporting token usage back
// to it, and retrying once on Tier 2 when a routed Tier 1 run fails.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/johnswift/jevons/internal/client"
	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/runner"
	"github.com/johnswift/jevons/pkg/schema"
)

var version = "dev"

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jev-runner:", err)
	}
	os.Exit(code)
}

// options are the parsed command-line flags.
type options struct {
	skill, prompt, model, tier, dir, cfgPath string
	timeout                                  time.Duration
	route, notify, report, escalate          bool
}

func run(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("jev-runner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.StringVar(&o.skill, "skill", "", "skill / slash command to run, e.g. /cleanup (required)")
	fs.StringVar(&o.prompt, "prompt", "", "prompt text appended to the skill")
	fs.StringVar(&o.model, "model", "", "explicit model ID")
	fs.StringVar(&o.tier, "tier", "", "model tier: TIER_1, TIER_2, or TIER_3")
	fs.StringVar(&o.dir, "dir", "", "working directory for the run")
	fs.DurationVar(&o.timeout, "timeout", 0, "run timeout (default from config)")
	fs.BoolVar(&o.route, "route", false, "ask the jev-router daemon to pick the model")
	fs.BoolVar(&o.escalate, "escalate", true, "with -route, retry once on Tier 2 if a Tier 1 run fails")
	fs.BoolVar(&o.report, "report", true, "report token usage to the jev-router daemon")
	fs.BoolVar(&o.notify, "notify", true, "send a desktop notification on completion")
	fs.StringVar(&o.cfgPath, "config", "", "path to JSON config file")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0, nil
	}
	if o.skill == "" {
		return 2, errors.New("-skill is required")
	}

	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		return 1, err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var ropt runner.Options
	if o.notify {
		ropt.Notifier = runner.NewDesktopNotifier()
	}
	f := &flow{
		cfg:    cfg,
		runner: runner.New(cfg, ropt),
		daemon: client.New(cfg.ListenAddr, nil),
		out:    stdout,
		errOut: stderr,
	}
	return f.execute(ctx, o)
}

// flow orchestrates one jev-runner invocation against injectable dependencies.
type flow struct {
	cfg    config.Config
	runner schema.Runner
	daemon *client.Client
	out    io.Writer
	errOut io.Writer
}

// execute resolves the model, runs, reports usage, and escalates a failed
// routed Tier 1 run once. It returns the process exit code.
func (f *flow) execute(ctx context.Context, o options) (int, error) {
	prompt := strings.TrimSpace(o.skill + " " + o.prompt)
	model, decision, err := f.resolveModel(ctx, o, prompt)
	if err != nil {
		return 2, err
	}

	req := schema.RunRequest{Skill: o.skill, Prompt: o.prompt, Model: model, WorkDir: o.dir, Timeout: o.timeout}
	res, runErr := f.runOnce(ctx, req, o.report)

	if o.escalate && decision != nil && decision.TaskTier == schema.Tier1 && errors.Is(runErr, schema.ErrRunFailed) {
		req.Model = f.escalatedModel(ctx, *decision, runErr)
		fmt.Fprintf(f.errOut, "jev-runner: Tier 1 run failed; retrying on %s\n", req.Model)
		res, runErr = f.runOnce(ctx, req, o.report)
	}

	if runErr == nil {
		return 0, nil
	}
	if res.ExitCode > 0 {
		return res.ExitCode, runErr
	}
	return 1, runErr
}

// runOnce runs req, prints a summary line, and reports token usage.
func (f *flow) runOnce(ctx context.Context, req schema.RunRequest, report bool) (schema.RunResult, error) {
	res, err := f.runner.Run(ctx, req)
	fmt.Fprintf(f.out, "run_id=%s model=%s exit=%d tokens=%d duration=%s log=%s\n",
		res.RunID, displayModel(req.Model), res.ExitCode, res.Tokens, res.Duration.Round(time.Millisecond), res.LogPath)
	if report && res.Tokens > 0 {
		// Usage reporting is best effort; a missing daemon is not worth a warning.
		if rerr := f.daemon.ReportUsage(ctx, res.Tokens); rerr != nil && !errors.Is(rerr, client.ErrUnreachable) {
			fmt.Fprintf(f.errOut, "jev-runner: usage not reported: %v\n", rerr)
		}
	}
	return res, err
}

// resolveModel picks the model by precedence: -model, -tier, -route, then the
// claude CLI default (empty string). The decision is non-nil only when the
// daemon chose the model.
func (f *flow) resolveModel(ctx context.Context, o options, prompt string) (string, *schema.RoutingDecision, error) {
	switch {
	case o.model != "":
		return o.model, nil, nil
	case o.tier != "":
		t := schema.Tier(o.tier)
		if !t.Valid() {
			return "", nil, fmt.Errorf("unknown tier %q", o.tier)
		}
		return f.cfg.ModelFor(t), nil, nil
	case o.route:
		d, err := f.daemon.Route(ctx, schema.RouteRequest{Prompt: prompt})
		if err != nil {
			fmt.Fprintf(f.errOut, "jev-runner: router unavailable (%v); using %s\n", err, schema.Tier2)
			return f.cfg.ModelFor(schema.Tier2), nil, nil
		}
		return d.SelectedModel, &d, nil
	}
	return "", nil, nil
}

// escalatedModel asks the daemon to escalate prev, falling back to the local
// Tier 2 model when the daemon cannot.
func (f *flow) escalatedModel(ctx context.Context, prev schema.RoutingDecision, cause error) string {
	d, err := f.daemon.Escalate(ctx, prev, cause.Error())
	if err != nil {
		fmt.Fprintf(f.errOut, "jev-runner: escalation via router failed (%v); using local %s\n", err, schema.Tier2)
		return f.cfg.ModelFor(schema.Tier2)
	}
	return d.SelectedModel
}

func displayModel(m string) string {
	if m == "" {
		return "(claude default)"
	}
	return m
}
