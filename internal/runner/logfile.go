package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// header is the JSON metadata line opening every run log.
type header struct {
	RunID   string    `json:"run_id"`
	Skill   string    `json:"skill"`
	Model   string    `json:"model,omitempty"`
	WorkDir string    `json:"workdir,omitempty"`
	Start   time.Time `json:"start"`
	Argv    []string  `json:"argv"`
}

// footer is the JSON line closing every run log.
type footer struct {
	ExitCode   int    `json:"exit_code"`
	Duration   string `json:"duration"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// logFile is a mutex-guarded log writer shared by the stdout and stderr copiers.
type logFile struct {
	mu   sync.Mutex
	f    *os.File
	path string
	last byte // last byte written; 0 when empty
}

// openLog creates dir (0700) and a new exclusive 0600 log file inside it.
func openLog(dir string, start time.Time, runID string) (*logFile, error) {
	if dir == "" {
		return nil, errors.New("runner: log dir not configured")
	}
	abs, err := absDir(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("runner: create log dir: %w", err)
	}
	name := start.UTC().Format("20060102-150405") + "-" + runID + ".log"
	path := filepath.Join(abs, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path built from config dir + generated name
	if err != nil {
		return nil, fmt.Errorf("runner: create log: %w", err)
	}
	return &logFile{f: f, path: path}, nil
}

// Write appends p to the log under the mutex.
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.f.Write(p)
	if n > 0 {
		l.last = p[n-1]
	}
	return n, err
}

// writeJSON writes v as one JSON line, starting on a fresh line.
func (l *logFile) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("runner: encode log line: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last != 0 && l.last != '\n' {
		b = append([]byte{'\n'}, b...)
	}
	l.last = '\n'
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("runner: write log: %w", err)
	}
	return nil
}

// Close closes the underlying file.
func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// rotate deletes the oldest *.log files (by name) in dir until at most maxFiles remain.
// Non-.log entries, directories, and symlinks are never touched. maxFiles < 1 disables rotation.
func rotate(dir string, maxFiles int) error {
	if maxFiles < 1 || dir == "" {
		return nil
	}
	abs, err := absDir(dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return fmt.Errorf("runner: rotate: %w", err)
	}
	var logs []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	if len(logs) <= maxFiles {
		return nil
	}
	sort.Strings(logs)
	var errs []error
	for _, name := range logs[:len(logs)-maxFiles] {
		p := filepath.Join(abs, name)
		if filepath.Dir(p) != abs || filepath.Ext(p) != ".log" {
			errs = append(errs, fmt.Errorf("runner: rotate: refusing to remove %q", p))
			continue
		}
		if err := os.Remove(p); err != nil {
			errs = append(errs, fmt.Errorf("runner: rotate: %w", err))
		}
	}
	return errors.Join(errs...)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{max: limit} }

// Write appends p, discarding the oldest bytes beyond max. It never fails.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if n >= t.max {
		t.buf = append(t.buf[:0], p[n-t.max:]...)
		return n, nil
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return n, nil
}

// String returns the retained tail.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
