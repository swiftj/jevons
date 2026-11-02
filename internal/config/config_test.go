package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swiftj/jevons/pkg/schema"
)

func TestDefaultIsValid(t *testing.T) {
	t.Parallel()
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8942" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if strings.HasPrefix(cfg.LogDir, "~") {
		t.Errorf("LogDir not expanded: %q", cfg.LogDir)
	}
}

func TestTierForModel(t *testing.T) {
	t.Parallel()
	cfg := Default()
	if tier, ok := cfg.TierForModel("claude-opus-5-5"); !ok || tier != schema.Tier3 {
		t.Errorf("TierForModel(opus) = %v, %v", tier, ok)
	}
	if _, ok := cfg.TierForModel("unknown"); ok {
		t.Error("TierForModel(unknown) found")
	}
	if cfg.ModelFor(schema.Tier2) != "claude-sonnet-5-5" {
		t.Errorf("ModelFor(Tier2) = %q", cfg.ModelFor(schema.Tier2))
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jev.json")
	body := `{"listen_addr":"127.0.0.1:9000","models":{"TIER_1":"m1"},"warm_idle_max":"10m",
"quota_threshold":0.5,"capacity_5h":1000,"log_dir":"/tmp/x","log_max_files":5}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" || cfg.Models[schema.Tier1] != "m1" ||
		cfg.WarmIdleMax != 10*time.Minute || cfg.QuotaThreshold != 0.5 ||
		cfg.Capacity5h != 1000 || cfg.LogDir != "/tmp/x" || cfg.LogMaxFiles != 5 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"unknown field": `{"nope":1}`,
		"bad tier":      `{"models":{"TIER_9":"x"}}`,
		"bad duration":  `{"run_timeout":"soon"}`,
		"invalid":       `{"quota_threshold":2}`,
	}
	for name, body := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: expected error")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("JEV_LISTEN_ADDR", "127.0.0.1:1")
	t.Setenv("JEV_MODEL_TIER_3", "big")
	t.Setenv("JEV_CAPACITY_7D", "42")
	t.Setenv("JEV_QUOTA_THRESHOLD", "0.9")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:1" || cfg.Models[schema.Tier3] != "big" ||
		cfg.Capacity7d != 42 || cfg.QuotaThreshold != 0.9 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	t.Setenv("JEV_CAPACITY_5H", "lots")
	if _, err := Load(""); err == nil {
		t.Error("expected parse error")
	}
}
