// Package runner executes headless `claude -p` runs for self-triggered and
// scheduled routines, capturing output to capped buffers and per-run log files.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/johnswift/jevons/internal/config"
	"github.com/johnswift/jevons/pkg/schema"
)

const (
	// MaxCapture is the per-stream in-memory capture limit; only the tail is kept.
	MaxCapture = 1 << 20
	// WaitDelay is how long the runner waits after SIGTERM before SIGKILL.
	WaitDelay = 10 * time.Second
	// maxLoggedPrompt bounds the prompt length written to log metadata.
	maxLoggedPrompt = 200
	// notifyTimeout bounds a completion notification.
	notifyTimeout = 10 * time.Second
)

// CommandFactory builds a command bound to ctx. The default wraps
// exec.CommandContext. Implementations must create the command with
// exec.CommandContext so the runner can install cmd.Cancel.
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// LookPathFunc resolves a binary name to a path, like exec.LookPath.
type LookPathFunc func(file string) (string, error)

// Options configures a Runner. All fields are optional.
type Options struct {
	// Notifier receives one completion notification per run; nil disables notifications.
	Notifier schema.Notifier
	// Clock supplies log timestamps and durations; nil means schema.SystemClock{}.
	Clock schema.Clock
	// Exec builds the child process; nil means exec.CommandContext.
	Exec CommandFactory
	// LookPath verifies cfg.ClaudeBin before each run. When nil it defaults to
	// exec.LookPath if Exec is also nil, and the check is skipped when a custom
	// Exec is supplied (the factory decides what actually runs).
	LookPath LookPathFunc
	// Logger receives non-fatal diagnostics (e.g. rotation failures); nil discards.
	Logger *slog.Logger
}

// Runner implements schema.Runner by spawning cfg.ClaudeBin headlessly.
type Runner struct {
	cfg      config.Config
	notifier schema.Notifier
	clock    schema.Clock
	exec     CommandFactory
	lookPath LookPathFunc
	log      *slog.Logger
}

var _ schema.Runner = (*Runner)(nil)

// New builds a Runner from cfg and opt, filling defaults for nil options.
func New(cfg config.Config, opt Options) *Runner {
	r := &Runner{
		cfg:      cfg,
		notifier: opt.Notifier,
		clock:    opt.Clock,
		exec:     opt.Exec,
		lookPath: opt.LookPath,
		log:      opt.Logger,
	}
	if r.clock == nil {
		r.clock = schema.SystemClock{}
	}
	if r.exec == nil {
		r.exec = exec.CommandContext
		if r.lookPath == nil {
			r.lookPath = exec.LookPath
		}
	}
	if r.log == nil {
		r.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return r
}

// BuildPrompt joins skill and prompt with a single space, omitting empty parts.
func BuildPrompt(skill, prompt string) string {
	switch {
	case skill != "" && prompt != "":
		return skill + " " + prompt
	case skill != "":
		return skill
	default:
		return prompt
	}
}

// BuildArgs returns the argv (excluding the binary) for a headless run.
func BuildArgs(prompt, model string) []string {
	args := []string{"-p", prompt}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "--output-format", "text")
}

// Run executes one headless run. A non-zero exit returns a populated result
// and an error wrapping schema.ErrRunFailed; a deadline wraps schema.ErrTimeout.
func (r *Runner) Run(ctx context.Context, req schema.RunRequest) (schema.RunResult, error) {
	prompt := BuildPrompt(req.Skill, req.Prompt)
	if prompt == "" {
		return schema.RunResult{}, errors.New("runner: run: empty skill and prompt")
	}
	if r.cfg.ClaudeBin == "" {
		return schema.RunResult{}, errors.New("runner: run: claude binary not configured")
	}
	if r.lookPath != nil {
		if _, err := r.lookPath(r.cfg.ClaudeBin); err != nil {
			return schema.RunResult{}, fmt.Errorf("runner: claude binary %q not found on PATH: %w", r.cfg.ClaudeBin, err)
		}
	}
	if req.WorkDir != "" {
		fi, err := os.Stat(req.WorkDir)
		if err != nil {
			return schema.RunResult{}, fmt.Errorf("runner: workdir: %w", err)
		}
		if !fi.IsDir() {
			return schema.RunResult{}, fmt.Errorf("runner: workdir %q is not a directory", req.WorkDir)
		}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = r.cfg.RunTimeout
	}
	if timeout <= 0 {
		return schema.RunResult{}, errors.New("runner: run: no positive timeout configured")
	}

	runID := schema.NewID()
	start := r.clock.Now()
	args := BuildArgs(prompt, req.Model)

	lf, err := openLog(r.cfg.LogDir, start, runID)
	if err != nil {
		return schema.RunResult{}, err
	}
	res := schema.RunResult{RunID: runID, ExitCode: -1, LogPath: lf.path}
	if err := lf.writeJSON(header{
		RunID:   runID,
		Skill:   req.Skill,
		Model:   req.Model,
		WorkDir: req.WorkDir,
		Start:   start.UTC(),
		Argv:    append([]string{r.cfg.ClaudeBin}, BuildArgs(truncate(prompt, maxLoggedPrompt), req.Model)...),
	}); err != nil {
		r.closeLog(lf)
		return res, err
	}

	runErr := r.execute(ctx, req, timeout, args, lf, &res)
	res.Duration = r.clock.Now().Sub(start)
	r.notify(ctx, req.Skill, runErr, lf)

	ft := footer{ExitCode: res.ExitCode, Duration: res.Duration.String(), DurationMS: res.Duration.Milliseconds()}
	if runErr != nil {
		ft.Error = runErr.Error()
	}
	if err := lf.writeJSON(ft); err != nil && runErr == nil {
		runErr = err
	}
	r.closeLog(lf)
	if err := rotate(r.cfg.LogDir, r.cfg.LogMaxFiles); err != nil {
		r.log.Warn("runner: log rotation failed", "err", err)
	}
	return res, runErr
}

// execute spawns the child, streams its output, and fills res.
func (r *Runner) execute(ctx context.Context, req schema.RunRequest, timeout time.Duration, args []string, lf *logFile, res *schema.RunResult) error {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := r.exec(runCtx, r.cfg.ClaudeBin, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = WaitDelay
	stdout, stderr := newTailBuffer(MaxCapture), newTailBuffer(MaxCapture)
	cmd.Stdout = io.MultiWriter(stdout, lf)
	cmd.Stderr = io.MultiWriter(stderr, lf)

	err := cmd.Run()
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("runner: run %s exceeded %s: %w", res.RunID, timeout, schema.ErrTimeout)
	case ctx.Err() != nil:
		return fmt.Errorf("runner: run %s: %w", res.RunID, ctx.Err())
	case err == nil:
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("runner: run %s exited %d: %w", res.RunID, res.ExitCode, schema.ErrRunFailed)
	}
	return fmt.Errorf("runner: run %s: start: %w", res.RunID, err)
}

// notify sends the completion notification, recording failures in the log.
func (r *Runner) notify(ctx context.Context, skill string, runErr error, lf *logFile) {
	if r.notifier == nil {
		return
	}
	name := skill
	if name == "" {
		name = "run"
	}
	title, body := "Jev: "+name+" ✓", "Run completed successfully."
	if runErr != nil {
		title, body = "Jev: "+name+" ✗", truncate(runErr.Error(), maxLoggedPrompt)
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	if err := r.notifier.Notify(nctx, title, body); err != nil {
		if werr := lf.writeJSON(map[string]string{"notify_error": err.Error()}); werr != nil {
			r.log.Warn("runner: record notify error", "err", werr)
		}
	}
}

func (r *Runner) closeLog(lf *logFile) {
	if err := lf.Close(); err != nil {
		r.log.Warn("runner: close log", "path", lf.path, "err", err)
	}
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// absDir returns the cleaned absolute form of dir.
func absDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("runner: resolve %s: %w", dir, err)
	}
	return filepath.Clean(abs), nil
}
