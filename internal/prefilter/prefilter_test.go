package prefilter

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

func TestMatch(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	e := New(cfg)

	big := "fix typo: " + strings.Repeat("a", 64*1024-len("fix typo: "))
	bigMiss := strings.Repeat("x", 64*1024)
	bigSpace := strings.Repeat(" ", 64*1024) + "fix typo: x"

	tests := []struct {
		name     string
		prompt   string
		wantRule string // empty = no match
	}{
		{"slash cleanup", "/cleanup the imports", "slash-mechanical"},
		{"slash grammar", "/grammar", "slash-mechanical"},
		{"slash format", "/format main.go", "slash-mechanical"},
		{"slash calibrate", "/calibrate now", "slash-mechanical"},
		{"slash upper", "/CLEANUP foo", "slash-mechanical"},
		{"fix typo", "fix typo: teh -> the", "fix-typo"},
		{"fix typo mixed case", "Fix Typo: x", "fix-typo"},
		{"format json", "format json: {\"a\":1}", "format-json"},
		{"format json upper", "FORMAT JSON: {}", "format-json"},
		{"list files", "list files: ./cmd", "list-files"},
		{"check spelling", "check spelling: recieve", "check-spelling"},
		{"leading whitespace", " \t\n\r fix typo: x", "fix-typo"},
		{"leading unicode space", "  /format x", "slash-mechanical"},
		{"64KiB hit", big, "fix-typo"},
		{"64KiB miss", bigMiss, ""},
		{"64KiB whitespace then hit", bigSpace, "fix-typo"},
		{"cleanupx near miss", "/cleanupx foo", ""},
		{"fix typos near miss", "fix typos in the readme", ""},
		{"fix typo no colon", "fix typo in main.go", ""},
		{"mid-sentence fix typo", "please fix typo: teh", ""},
		{"non-mechanical slash", "/deploy prod", ""},
		{"slash only", "/", ""},
		{"empty", "", ""},
		{"whitespace only", "   \n\t", ""},
		{"architecture prompt", "Design a distributed cache", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, ok := e.Match(tc.prompt)
			if tc.wantRule == "" {
				if ok {
					t.Fatalf("Match(%q) hit %q, want miss", trunc(tc.prompt), d.Rationale)
				}
				if d != (schema.RoutingDecision{}) {
					t.Fatalf("miss returned non-zero decision %+v", d)
				}
				return
			}
			if !ok {
				t.Fatalf("Match(%q) missed, want %s", trunc(tc.prompt), tc.wantRule)
			}
			if d.Rationale != "prefilter:"+tc.wantRule {
				t.Errorf("Rationale = %q, want prefilter:%s", d.Rationale, tc.wantRule)
			}
			if d.TaskTier != schema.Tier1 || d.SelectedModel != cfg.Models[schema.Tier1] {
				t.Errorf("tier/model = %s/%s", d.TaskTier, d.SelectedModel)
			}
			if d.EffortLevel != schema.EffortLow || d.Source != schema.SourcePrefilter {
				t.Errorf("effort/source = %s/%s", d.EffortLevel, d.Source)
			}
			if len(d.TaskID) != 16 {
				t.Errorf("TaskID %q len = %d, want 16", d.TaskID, len(d.TaskID))
			}
			if d.LatencyMicros < 0 {
				t.Errorf("LatencyMicros = %d", d.LatencyMicros)
			}
			if err := d.Validate(); err != nil {
				t.Errorf("Validate: %v", err)
			}
		})
	}
}

func trunc(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func TestInspectWindowBoundary(t *testing.T) {
	t.Parallel()
	e := New(config.Default(), Rule{Name: "tail", Pattern: `^a*b`})
	// "b" is the 512th byte: inside the window.
	in := strings.Repeat("a", InspectBytes-1) + "b"
	if _, ok := e.Match("  " + in); !ok {
		t.Fatal("byte 512 should be inspected")
	}
	// "b" is the 513th byte: outside the window.
	out := strings.Repeat("a", InspectBytes) + "b"
	if _, ok := e.Match(out); ok {
		t.Fatal("byte 513 must not be inspected")
	}
}

func TestUniqueTaskIDs(t *testing.T) {
	t.Parallel()
	e := New(config.Default())
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		d, ok := e.Match("fix typo: x")
		if !ok {
			t.Fatal("miss")
		}
		if seen[d.TaskID] {
			t.Fatalf("duplicate TaskID %s", d.TaskID)
		}
		seen[d.TaskID] = true
	}
}

func TestCustomRules(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	e := New(cfg,
		Rule{Name: "deep", Pattern: `^architect:`, Tier: schema.Tier3},
		Rule{Name: "default-tier", Pattern: `^quick:`},
	)
	d, ok := e.Match("ARCHITECT: a new system")
	if !ok || d.TaskTier != schema.Tier3 || d.SelectedModel != cfg.Models[schema.Tier3] || d.Rationale != "prefilter:deep" {
		t.Fatalf("custom tier3 rule: ok=%v d=%+v", ok, d)
	}
	d, ok = e.Match("quick: thing")
	if !ok || d.TaskTier != schema.Tier1 {
		t.Fatalf("empty tier should default to Tier1: ok=%v d=%+v", ok, d)
	}
	// Custom rules replace defaults.
	if _, ok := e.Match("fix typo: x"); ok {
		t.Fatal("defaults should not apply when custom rules are given")
	}
}

func TestCompileErrors(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if _, err := Compile(cfg, Rule{Name: "bad", Pattern: `^(`}); err == nil {
		t.Fatal("want regex error")
	}
	_, err := Compile(cfg, Rule{Name: "tier", Pattern: `^x`, Tier: "TIER_9"})
	if !errors.Is(err, schema.ErrInvalidDecision) {
		t.Fatalf("want ErrInvalidDecision, got %v", err)
	}
	e, err := Compile(cfg)
	if err != nil || len(e.rules) != len(DefaultRules()) {
		t.Fatalf("Compile defaults: %v", err)
	}
}

func TestNewPanicsOnInvalidRule(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("New should panic on invalid pattern")
		}
	}()
	New(config.Default(), Rule{Name: "bad", Pattern: `[`})
}

func TestConcurrentMatch(t *testing.T) {
	t.Parallel()
	e := New(config.Default())
	prompts := []string{"fix typo: a", "/deploy", "/format x", "hello world", ""}
	for i := 0; i < 8; i++ {
		t.Run("worker", func(t *testing.T) {
			t.Parallel()
			for j := 0; j < 500; j++ {
				p := prompts[(i+j)%len(prompts)]
				d, ok := e.Match(p)
				if ok && d.Source != schema.SourcePrefilter {
					t.Errorf("bad source %s", d.Source)
				}
			}
		})
	}
}

// TestMatchP99Latency enforces the < 1ms budget on 10k calls with 64 KiB prompts.
func TestMatchP99Latency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency budget test skipped in -short mode")
	}
	e := New(config.Default())
	hit := "list files: " + strings.Repeat("z", 64*1024)
	miss := strings.Repeat(" ", 1024) + strings.Repeat("q", 63*1024)
	const n = 10000
	durs := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		p := hit
		if i%2 == 1 {
			p = miss
		}
		start := time.Now()
		_, _ = e.Match(p) // result irrelevant; timing only
		durs[i] = time.Since(start)
	}
	sort.Slice(durs, func(a, b int) bool { return durs[a] < durs[b] })
	p99 := durs[n*99/100]
	t.Logf("Match p99 = %v (p50 = %v)", p99, durs[n/2])
	if p99 >= time.Millisecond {
		t.Fatalf("p99 = %v, want < 1ms", p99)
	}
}

func BenchmarkMatch(b *testing.B) {
	e := New(config.Default())
	cases := []struct {
		name   string
		prompt string
	}{
		{"hit", "check spelling: " + strings.Repeat("w", 64*1024)},
		{"miss", strings.Repeat("Design a scalable event pipeline. ", 2000)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = e.Match(c.prompt) // benchmark: result discarded
			}
		})
	}
}
