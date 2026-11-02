package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/swiftj/jevons/internal/classifier"
	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/internal/prefilter"
	"github.com/swiftj/jevons/internal/proxy"
	"github.com/swiftj/jevons/internal/router"
	"github.com/swiftj/jevons/pkg/schema"
)

// runRouter (`jev router`) parses flags, wires the daemon, and serves until
// ctx is cancelled.
func runRouter(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("jev router", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to JSON config file")
	addr := fs.String("addr", "", "listen address (overrides config)")
	keychain := fs.String("keychain", "", "macOS keychain service holding the API key, used when the key env var is unset")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.ListenAddr = *addr
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	if err := loadKeychainKey(ctx, *keychain, cfg.APIKeyEnv); err != nil {
		logger.Warn("could not read API key from keychain", "err", err)
	}
	if os.Getenv(cfg.APIKeyEnv) == "" {
		logger.Warn("classifier API key not set; non-prefilter prompts fall back to Tier 2", "env", cfg.APIKeyEnv)
	}

	if cfg.Capacity5h == 0 && cfg.Capacity7d == 0 {
		logger.Info("no quota capacities configured; usage is recorded but throttling is off")
	}

	clock := schema.SystemClock{}
	sessions := router.NewSessionTracker(cfg, clock)
	usage := router.NewCountingUsage(cfg, clock)
	eng := router.New(cfg, router.Deps{
		Prefilter:  prefilter.New(cfg),
		Classifier: classifier.New(cfg, nil),
		Sessions:   sessions,
		Usage:      usage,
		Clock:      clock,
		Logger:     logger,
	})

	logger.Info("jev-router starting", "version", version, "addr", cfg.ListenAddr)
	srv := proxy.NewServer(cfg, eng, logger).
		WithFeeds(proxy.Feeds{Sessions: sessions, Usage: usage, Clock: clock})
	return srv.ListenAndServe(ctx)
}
