// `jev hook` connects interactive Claude Code sessions to the daemon. As a
// UserPromptSubmit hook it routes each prompt and shows the suggested tier;
// `jev statusline` prints that suggestion next to the session's current
// model. Hooks cannot switch models, so it only advises.
//
// It never blocks a prompt: any failure (daemon down, bad input) exits 0
// with no output.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/swiftj/jevons/internal/client"
	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

const (
	// maxInputBytes caps the hook JSON read from stdin.
	maxInputBytes = 1 << 20
	// staleAfter is the age at which suggestion files are pruned.
	staleAfter = 7 * 24 * time.Hour
)

// safeID matches session IDs usable as file names.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// hookInput is the subset of Claude Code hook / status line stdin jev-hook reads.
type hookInput struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	Model     struct {
		ID string `json:"id"`
	} `json:"model"`
}

// suggestion is what the prompt hook stores for the status line. It never
// holds prompt text.
type suggestion struct {
	Tier   schema.Tier           `json:"tier"`
	Model  string                `json:"model"`
	Source schema.DecisionSource `json:"source"`
}

func runHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("jev-hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	statusline := fs.Bool("statusline", false, "print a status line instead of routing a prompt")
	cfgPath := fs.String("config", "", "path to JSON config file")
	addr := fs.String("addr", "", "daemon address (overrides config)")
	stateDir := fs.String("state-dir", "", "where suggestions are kept for the status line (default ~/.jev/hook)")
	timeout := fs.Duration("timeout", 5*time.Second, "how long to wait for the daemon")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 1, err // never 2: that would block the user's prompt
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0, nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return 0, err
	}
	if *addr != "" {
		cfg.ListenAddr = *addr
	}
	if *stateDir == "" {
		*stateDir = filepath.Join(filepath.Dir(cfg.LogDir), "hook")
	}

	var in hookInput
	if err := json.NewDecoder(io.LimitReader(stdin, maxInputBytes)).Decode(&in); err != nil {
		return 0, fmt.Errorf("read hook input: %w", err)
	}

	if *statusline {
		fmt.Fprintln(stdout, statusText(readSuggestion(*stateDir, in.SessionID), in.Model.ID, cfg))
		return 0, nil
	}
	return 0, promptHook(ctx, in, cfg, *stateDir, *timeout, stdout)
}

// promptHook routes the prompt, stores the suggestion for the status line,
// and prints a one-line systemMessage for the user.
func promptHook(ctx context.Context, in hookInput, cfg config.Config, stateDir string, timeout time.Duration, stdout io.Writer) error {
	if strings.TrimSpace(in.Prompt) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := client.New(cfg.ListenAddr, &http.Client{Timeout: timeout})
	d, err := c.Route(ctx, schema.RouteRequest{Prompt: in.Prompt, SessionID: in.SessionID})
	if err != nil {
		if errors.Is(err, client.ErrUnreachable) {
			return nil // daemon not running: stay silent
		}
		return err
	}
	s := suggestion{Tier: d.TaskTier, Model: d.SelectedModel, Source: d.Source}
	if err := writeSuggestion(stateDir, in.SessionID, s); err != nil {
		return err
	}
	out := map[string]string{"systemMessage": fmt.Sprintf("Jev: %s → %s (%s)", tierLabel(s.Tier), s.Model, s.Source)}
	return json.NewEncoder(stdout).Encode(out)
}

// statusText renders the status line: the last suggestion for this session,
// marked ✓ when the current model already matches it.
func statusText(s *suggestion, current string, cfg config.Config) string {
	if s == nil {
		return "Jev: –"
	}
	label := tierLabel(s.Tier)
	if current != "" && strings.HasPrefix(current, s.Model) {
		return fmt.Sprintf("Jev ✓ %s", label)
	}
	if cur := tierOf(current, cfg); cur != "" {
		return fmt.Sprintf("Jev → %s (%s) · now %s", label, s.Model, tierLabel(cur))
	}
	return fmt.Sprintf("Jev → %s (%s)", label, s.Model)
}

// tierOf maps a model ID back to its configured tier, or "" if unknown.
func tierOf(model string, cfg config.Config) schema.Tier {
	if model == "" {
		return ""
	}
	for t, m := range cfg.Models {
		if strings.HasPrefix(model, m) {
			return t
		}
	}
	return ""
}

// tierLabel turns TIER_1 into "Tier 1".
func tierLabel(t schema.Tier) string {
	if n, ok := strings.CutPrefix(string(t), "TIER_"); ok {
		return "Tier " + n
	}
	return string(t)
}

// suggestionPath returns the file for sessionID, or "" if the ID is unusable.
func suggestionPath(dir, sessionID string) string {
	if !safeID.MatchString(sessionID) {
		return ""
	}
	return filepath.Join(dir, sessionID+".json")
}

// writeSuggestion stores s for sessionID (owner-only) and prunes stale files.
func writeSuggestion(dir, sessionID string, s suggestion) error {
	path := suggestionPath(dir, sessionID)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode suggestion: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write suggestion: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write suggestion: %w", err)
	}
	pruneStale(dir, time.Now())
	return nil
}

// readSuggestion loads the stored suggestion for sessionID, or nil.
func readSuggestion(dir, sessionID string) *suggestion {
	path := suggestionPath(dir, sessionID)
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path) // #nosec G304 -- state dir + validated session ID
	if err != nil {
		return nil
	}
	var s suggestion
	if err := json.Unmarshal(b, &s); err != nil || s.Model == "" {
		return nil
	}
	return &s
}

// pruneStale removes *.json suggestion files in dir older than staleAfter.
// Errors are ignored: pruning is best-effort housekeeping.
func pruneStale(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleAfter {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name())) // best-effort cleanup
	}
}
