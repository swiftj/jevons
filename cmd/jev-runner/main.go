// Command jev-runner executes a headless `claude -p` skill run, optionally
// asking the jev-router daemon which model to use.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/runner"
	"github.com/johnswift/jevons/pkg/schema"
)

var version = "dev"

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "jev-runner:", err)
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	fs := flag.NewFlagSet("jev-runner", flag.ContinueOnError)
	skill := fs.String("skill", "", "skill / slash command to run, e.g. /cleanup (required)")
	prompt := fs.String("prompt", "", "prompt text appended to the skill")
	model := fs.String("model", "", "explicit model ID")
	tier := fs.String("tier", "", "model tier: TIER_1, TIER_2, or TIER_3")
	dir := fs.String("dir", "", "working directory for the run")
	timeout := fs.Duration("timeout", 0, "run timeout (default from config)")
	route := fs.Bool("route", false, "ask the jev-router daemon to pick the model")
	notify := fs.Bool("notify", true, "send a desktop notification on completion")
	cfgPath := fs.String("config", "", "path to JSON config file")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if *showVersion {
		fmt.Println(version)
		return 0, nil
	}
	if *skill == "" {
		return 2, errors.New("-skill is required")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return 1, err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	selected, err := resolveModel(ctx, cfg, *model, *tier, *route, strings.TrimSpace(*skill+" "+*prompt))
	if err != nil {
		return 2, err
	}

	var opt runner.Options
	if *notify {
		opt.Notifier = runner.NewDesktopNotifier()
	}
	res, runErr := runner.New(cfg, opt).Run(ctx, schema.RunRequest{
		Skill:   *skill,
		Prompt:  *prompt,
		Model:   selected,
		WorkDir: *dir,
		Timeout: *timeout,
	})

	fmt.Printf("run_id=%s model=%s exit=%d duration=%s log=%s\n",
		res.RunID, displayModel(selected), res.ExitCode, res.Duration.Round(time.Millisecond), res.LogPath)
	if runErr == nil {
		return 0, nil
	}
	if res.ExitCode > 0 {
		return res.ExitCode, runErr
	}
	return 1, runErr
}

// resolveModel picks the model by precedence: -model, -tier, -route, then the
// claude CLI default (empty string).
func resolveModel(ctx context.Context, cfg config.Config, model, tier string, route bool, prompt string) (string, error) {
	switch {
	case model != "":
		return model, nil
	case tier != "":
		t := schema.Tier(tier)
		if !t.Valid() {
			return "", fmt.Errorf("unknown tier %q", tier)
		}
		return cfg.ModelFor(t), nil
	case route:
		d, err := askRouter(ctx, cfg.ListenAddr, prompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jev-runner: router unreachable (%v); using %s\n", err, schema.Tier2)
			return cfg.ModelFor(schema.Tier2), nil
		}
		return d.SelectedModel, nil
	}
	return "", nil
}

func askRouter(ctx context.Context, addr, prompt string) (schema.RoutingDecision, error) {
	body, err := json.Marshal(schema.RouteRequest{Prompt: prompt})
	if err != nil {
		return schema.RoutingDecision{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/route", bytes.NewReader(body))
	if err != nil {
		return schema.RoutingDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return schema.RoutingDecision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return schema.RoutingDecision{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var d schema.RoutingDecision
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return schema.RoutingDecision{}, err
	}
	return d, d.Validate()
}

func displayModel(m string) string {
	if m == "" {
		return "(claude default)"
	}
	return m
}
