package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swiftj/jevons/internal/mocks"
	"github.com/swiftj/jevons/pkg/schema"
)

func countLogs(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			n++
		}
	}
	return n
}

func seedLogs(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("20000101-000000-%04d.log", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRotationAfterRun(t *testing.T) {
	t.Parallel()
	r, cfg := newTestRunner(t, "success", nil)
	seedLogs(t, cfg.LogDir, 205)
	res, err := r.Run(context.Background(), schema.RunRequest{Skill: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	if got := countLogs(t, cfg.LogDir); got != 200 {
		t.Fatalf("logs after rotation = %d, want 200", got)
	}
	if _, err := os.Stat(res.LogPath); err != nil {
		t.Fatalf("newest log removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.LogDir, "keep.txt")); err != nil {
		t.Fatalf(".txt touched: %v", err)
	}
	// Oldest six (0000..0005) are gone, 0006 remains.
	if _, err := os.Stat(filepath.Join(cfg.LogDir, "20000101-000000-0005.log")); !os.IsNotExist(err) {
		t.Fatalf("oldest not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.LogDir, "20000101-000000-0006.log")); err != nil {
		t.Fatalf("0006 removed: %v", err)
	}
}

func TestRotateBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		seed, max int
		want      int
	}{
		{"exactly at limit", 200, 200, 200},
		{"one over", 201, 200, 200},
		{"under", 5, 200, 5},
		{"disabled", 10, 0, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			seedLogs(t, dir, tc.seed)
			if err := os.Mkdir(filepath.Join(dir, "sub.log"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := rotate(dir, tc.max); err != nil {
				t.Fatal(err)
			}
			if got := countLogs(t, dir) - 1; got != tc.want { // minus the sub.log directory
				t.Fatalf("got %d want %d", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "sub.log")); err != nil {
				t.Fatal("directory named *.log removed")
			}
		})
	}
}

func TestRotateMissingDir(t *testing.T) {
	t.Parallel()
	if err := rotate(filepath.Join(t.TempDir(), "nope"), 1); err == nil {
		t.Fatal("expected error")
	}
	if err := rotate("", 1); err != nil {
		t.Fatal(err)
	}
}

func TestRotationMaxOneKeepsNewest(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.LogMaxFiles = 1
	r := New(cfg, Options{Exec: helperFactory("success"), Clock: mocks.NewFakeClock(schemaTime())})
	if _, err := r.Run(context.Background(), schema.RunRequest{Skill: "/a"}); err != nil {
		t.Fatal(err)
	}
	if got := countLogs(t, cfg.LogDir); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestTailBuffer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{"under", []string{"ab", "cd"}, "abcd"},
		{"exact boundary", []string{"abcde"}, "abcde"},
		{"one over", []string{"abcdef"}, "bcdef"},
		{"spill across writes", []string{"abc", "defg"}, "cdefg"},
		{"huge single write", []string{"x", "0123456789"}, "56789"},
	}
	for _, tc := range tests {
		b := newTailBuffer(5)
		for _, w := range tc.writes {
			if n, err := b.Write([]byte(w)); err != nil || n != len(w) {
				t.Fatalf("%s: write %d %v", tc.name, n, err)
			}
		}
		if got := b.String(); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestWriteJSONStartsNewLine(t *testing.T) {
	t.Parallel()
	lf, err := openLog(t.TempDir(), schemaTime(), "id")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lf.Write([]byte("no newline")); err != nil {
		t.Fatal(err)
	}
	if err := lf.writeJSON(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := lf.writeJSON(func() {}); err == nil {
		t.Fatal("expected encode error")
	}
	if err := lf.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lf.writeJSON(map[string]int{"b": 2}); err == nil {
		t.Fatal("expected write error on closed file")
	}
	data, err := os.ReadFile(lf.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "no newline\n{\"a\":1}\n" {
		t.Fatalf("got %q", data)
	}
}
