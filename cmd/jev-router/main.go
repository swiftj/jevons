// Command jev-router runs the Jev-Pro routing daemon and its loopback
// telemetry server for Claude Mods.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/johnswift/jevons/internal/classifier"
	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/internal/prefilter"
	"github.com/johnswift/jevons/internal/proxy"
	"github.com/johnswift/jevons/internal/router"
	"github.com/johnswift/jevons/pkg/schema"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jev-router:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("jev-router", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to JSON config file")
	addr := fs.String("addr", "", "listen address (overrides config)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.ListenAddr = *addr
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if os.Getenv(cfg.APIKeyEnv) == "" {
		logger.Warn("classifier API key not set; non-prefilter prompts fall back to Tier 2", "env", cfg.APIKeyEnv)
	}

	clock := schema.SystemClock{}
	eng := router.New(cfg, router.Deps{
		Prefilter:  prefilter.New(cfg),
		Classifier: classifier.New(cfg, nil),
		Sessions:   router.NewSessionTracker(cfg, clock),
		Usage:      router.NewCountingUsage(cfg, clock),
		Clock:      clock,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("jev-router starting", "version", version, "addr", cfg.ListenAddr)
	return proxy.NewServer(cfg, eng, logger).ListenAndServe(ctx)
}
