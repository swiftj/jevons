// Package config holds Jev-Pro runtime configuration, defaults, and loading.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/swiftj/jevons/pkg/schema"
)

// Config is the complete runtime configuration.
type Config struct {
	ListenAddr        string
	Models            map[schema.Tier]string
	ClassifierModel   string
	AnthropicBaseURL  string
	APIKeyEnv         string // name of the env var holding the key, never the value
	ClassifierTimeout time.Duration
	TypeSafeBaseURL   string
	TypeSafeModel     string  // Jev model ID or alias sent to /v1/systemone
	TypeSafeAPIKeyEnv string  // env var holding the TypeSafe key; Jev is skipped when it is unset
	JevMinConfidence  float64 // Jev tier answers below this confidence fall back to the LLM classifier
	WarmIdleMax       time.Duration
	WarmMinTokens     int64
	QuotaThreshold    float64
	Capacity5h        int64 // 0 = unknown, throttle disabled for the window
	Capacity7d        int64
	ClaudeBin         string
	RunTimeout        time.Duration
	LogDir            string
	LogMaxFiles       int
}

// Default returns the built-in configuration.
func Default() Config {
	models := map[schema.Tier]string{
		schema.Tier1: "claude-haiku-5-5",
		schema.Tier2: "claude-sonnet-5-5",
		schema.Tier3: "claude-opus-5-5",
	}
	return Config{
		ListenAddr:        "127.0.0.1:8942",
		Models:            models,
		ClassifierModel:   models[schema.Tier1],
		AnthropicBaseURL:  "https://api.anthropic.com",
		APIKeyEnv:         "ANTHROPIC_API_KEY", // #nosec G101 -- env var name, not a credential
		ClassifierTimeout: 3 * time.Second,
		TypeSafeBaseURL:   "https://api.typesafe.ai",
		TypeSafeModel:     "jev-latest",
		TypeSafeAPIKeyEnv: "TYPESAFE_API_KEY", // #nosec G101 -- env var name, not a credential
		JevMinConfidence:  0.5,
		WarmIdleMax:       50 * time.Minute,
		WarmMinTokens:     100_000,
		QuotaThreshold:    0.80,
		ClaudeBin:         "claude",
		RunTimeout:        30 * time.Minute,
		LogDir:            expandHome("~/.jev/logs"),
		LogMaxFiles:       200,
	}
}

// ModelFor returns the model ID configured for tier.
func (c Config) ModelFor(t schema.Tier) string { return c.Models[t] }

// TierForModel reverse-maps a model ID to its tier, reporting whether it was found.
func (c Config) TierForModel(model string) (schema.Tier, bool) {
	for _, t := range []schema.Tier{schema.Tier1, schema.Tier2, schema.Tier3} {
		if c.Models[t] == model {
			return t, true
		}
	}
	return "", false
}

// Validate reports configuration errors.
func (c Config) Validate() error {
	var errs []error
	for _, t := range []schema.Tier{schema.Tier1, schema.Tier2, schema.Tier3} {
		if c.Models[t] == "" {
			errs = append(errs, fmt.Errorf("config: no model for %s", t))
		}
	}
	if c.ListenAddr == "" {
		errs = append(errs, errors.New("config: empty listen addr"))
	}
	if c.QuotaThreshold <= 0 || c.QuotaThreshold > 1 {
		errs = append(errs, fmt.Errorf("config: quota threshold %v not in (0,1]", c.QuotaThreshold))
	}
	if c.WarmIdleMax <= 0 || c.ClassifierTimeout <= 0 || c.RunTimeout <= 0 {
		errs = append(errs, errors.New("config: durations must be positive"))
	}
	if c.JevMinConfidence < 0 || c.JevMinConfidence > 1 {
		errs = append(errs, fmt.Errorf("config: jev min confidence %v not in [0,1]", c.JevMinConfidence))
	}
	if c.LogMaxFiles < 1 {
		errs = append(errs, errors.New("config: log max files must be >= 1"))
	}
	return errors.Join(errs...)
}

// fileConfig is the JSON file shape; durations are Go duration strings.
type fileConfig struct {
	ListenAddr        *string           `json:"listen_addr"`
	Models            map[string]string `json:"models"`
	ClassifierModel   *string           `json:"classifier_model"`
	AnthropicBaseURL  *string           `json:"anthropic_base_url"`
	APIKeyEnv         *string           `json:"api_key_env"`
	ClassifierTimeout *string           `json:"classifier_timeout"`
	TypeSafeBaseURL   *string           `json:"typesafe_base_url"`
	TypeSafeModel     *string           `json:"typesafe_model"`
	TypeSafeAPIKeyEnv *string           `json:"typesafe_api_key_env"`
	JevMinConfidence  *float64          `json:"jev_min_confidence"`
	WarmIdleMax       *string           `json:"warm_idle_max"`
	WarmMinTokens     *int64            `json:"warm_min_tokens"`
	QuotaThreshold    *float64          `json:"quota_threshold"`
	Capacity5h        *int64            `json:"capacity_5h"`
	Capacity7d        *int64            `json:"capacity_7d"`
	ClaudeBin         *string           `json:"claude_bin"`
	RunTimeout        *string           `json:"run_timeout"`
	LogDir            *string           `json:"log_dir"`
	LogMaxFiles       *int              `json:"log_max_files"`
}

// Load starts from Default, applies the JSON file at path (skipped when path is
// empty), then JEV_* environment overrides, and validates the result.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		var fc fileConfig
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fc); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		if err := cfg.applyFile(fc); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.applyEnv(os.Getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyFile(fc fileConfig) error {
	setStr(&c.ListenAddr, fc.ListenAddr)
	setStr(&c.ClassifierModel, fc.ClassifierModel)
	setStr(&c.AnthropicBaseURL, fc.AnthropicBaseURL)
	setStr(&c.APIKeyEnv, fc.APIKeyEnv)
	setStr(&c.TypeSafeBaseURL, fc.TypeSafeBaseURL)
	setStr(&c.TypeSafeModel, fc.TypeSafeModel)
	setStr(&c.TypeSafeAPIKeyEnv, fc.TypeSafeAPIKeyEnv)
	setStr(&c.ClaudeBin, fc.ClaudeBin)
	if fc.LogDir != nil {
		c.LogDir = expandHome(*fc.LogDir)
	}
	for k, v := range fc.Models {
		t := schema.Tier(k)
		if !t.Valid() {
			return fmt.Errorf("config: unknown tier %q in models", k)
		}
		c.Models[t] = v
	}
	for _, d := range []struct {
		dst *time.Duration
		src *string
	}{
		{&c.ClassifierTimeout, fc.ClassifierTimeout},
		{&c.WarmIdleMax, fc.WarmIdleMax},
		{&c.RunTimeout, fc.RunTimeout},
	} {
		if d.src == nil {
			continue
		}
		v, err := time.ParseDuration(*d.src)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		*d.dst = v
	}
	if fc.WarmMinTokens != nil {
		c.WarmMinTokens = *fc.WarmMinTokens
	}
	if fc.JevMinConfidence != nil {
		c.JevMinConfidence = *fc.JevMinConfidence
	}
	if fc.QuotaThreshold != nil {
		c.QuotaThreshold = *fc.QuotaThreshold
	}
	if fc.Capacity5h != nil {
		c.Capacity5h = *fc.Capacity5h
	}
	if fc.Capacity7d != nil {
		c.Capacity7d = *fc.Capacity7d
	}
	if fc.LogMaxFiles != nil {
		c.LogMaxFiles = *fc.LogMaxFiles
	}
	return nil
}

func (c *Config) applyEnv(getenv func(string) string) error {
	strs := map[string]*string{
		"JEV_LISTEN_ADDR":        &c.ListenAddr,
		"JEV_CLASSIFIER_MODEL":   &c.ClassifierModel,
		"JEV_ANTHROPIC_BASE_URL": &c.AnthropicBaseURL,
		"JEV_CLAUDE_BIN":         &c.ClaudeBin,
		"JEV_TYPESAFE_BASE_URL":  &c.TypeSafeBaseURL,
		"JEV_TYPESAFE_MODEL":     &c.TypeSafeModel,
	}
	for k, dst := range strs {
		if v := getenv(k); v != "" {
			*dst = v
		}
	}
	if v := getenv("JEV_LOG_DIR"); v != "" {
		c.LogDir = expandHome(v)
	}
	for k, t := range map[string]schema.Tier{
		"JEV_MODEL_TIER_1": schema.Tier1,
		"JEV_MODEL_TIER_2": schema.Tier2,
		"JEV_MODEL_TIER_3": schema.Tier3,
	} {
		if v := getenv(k); v != "" {
			c.Models[t] = v
		}
	}
	for k, dst := range map[string]*int64{
		"JEV_CAPACITY_5H": &c.Capacity5h,
		"JEV_CAPACITY_7D": &c.Capacity7d,
	} {
		if v := getenv(k); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("config: %s: %w", k, err)
			}
			*dst = n
		}
	}
	if v := getenv("JEV_MIN_CONFIDENCE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("config: JEV_MIN_CONFIDENCE: %w", err)
		}
		c.JevMinConfidence = f
	}
	if v := getenv("JEV_QUOTA_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("config: JEV_QUOTA_THRESHOLD: %w", err)
		}
		c.QuotaThreshold = f
	}
	return nil
}

func setStr(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}
