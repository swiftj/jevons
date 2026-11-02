package setup

import (
	"os"
	"strings"
	"testing"
)

func TestPlanSettingsMergesAndKeepsOthers(t *testing.T) {
	t.Parallel()
	env, _, _, _ := testEnv(t, "darwin")
	existing := `{
  "model": "opus",
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "/usr/local/bin/other-hook"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]
  },
  "statusLine": {"type": "command", "command": "~/.claude/my-status.sh"}
}`
	if err := os.MkdirAll(env.Home+"/.claude", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.ClaudeSettings(), []byte(existing), 0o640); err != nil {
		t.Fatal(err)
	}
	plan, err := env.PlanSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || len(plan.Notes) != 1 || !strings.Contains(plan.Notes[0], "statusline") {
		t.Fatalf("changes=%q notes=%q", plan.Changes, plan.Notes)
	}
	backup, err := plan.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != existing {
		t.Error("backup does not hold the original")
	}
	if info, _ := os.Stat(env.ClaudeSettings()); info.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", info.Mode().Perm())
	}
	s := readJSON(t, env.ClaudeSettings())
	if s["model"] != "opus" {
		t.Error("unrelated key lost")
	}
	hooks := s["hooks"].(map[string]any)
	if len(hooks["UserPromptSubmit"].([]any)) != 2 || hooks["Stop"] == nil {
		t.Errorf("hooks = %v", hooks)
	}
	if s["statusLine"].(map[string]any)["command"] != "~/.claude/my-status.sh" {
		t.Error("user status line replaced")
	}
	hook, status := env.SettingsWired()
	if !hook || status {
		t.Errorf("wired = %v %v", hook, status)
	}

	// Planning again is a no-op.
	plan, err = env.PlanSettings()
	if err != nil || len(plan.Changes) != 0 {
		t.Errorf("second plan: %q %v", plan.Changes, err)
	}
	if b, err := plan.Apply(); b != "" || err != nil {
		t.Errorf("no-op apply: %q %v", b, err)
	}

	// Removal takes out only jev's hook.
	rm, err := env.PlanRemoveSettings()
	if err != nil || len(rm.Changes) != 1 {
		t.Fatalf("remove plan: %q %v", rm.Changes, err)
	}
	if _, err := rm.Apply(); err != nil {
		t.Fatal(err)
	}
	s = readJSON(t, env.ClaudeSettings())
	ups := s["hooks"].(map[string]any)["UserPromptSubmit"].([]any)
	if len(ups) != 1 || s["statusLine"] == nil {
		t.Errorf("after removal: %v", s)
	}
}

func TestPlanSettingsUpdatesStalePaths(t *testing.T) {
	t.Parallel()
	env, _, _, _ := testEnv(t, "darwin")
	old := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"/old/bin/jev hook"},{"type":"command","command":"/x/jev-hook"}]}]},
"statusLine":{"type":"command","command":"/old/bin/jev statusline"}}`
	if err := os.MkdirAll(env.Home+"/.claude", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.ClaudeSettings(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := env.PlanSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 3 {
		t.Fatalf("changes = %q", plan.Changes)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	s := readJSON(t, env.ClaudeSettings())
	if s["statusLine"].(map[string]any)["command"] != env.Target()+" statusline" {
		t.Errorf("status line = %v", s["statusLine"])
	}

	// Removing everything drops the empty hooks map and the status line.
	rm, err := env.PlanRemoveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rm.Apply(); err != nil {
		t.Fatal(err)
	}
	s = readJSON(t, env.ClaudeSettings())
	if len(s) != 0 {
		t.Errorf("leftover settings: %v", s)
	}
}

func TestSettingsEdgeCases(t *testing.T) {
	t.Parallel()
	env, _, _, _ := testEnv(t, "darwin")
	// No file: removal is a no-op; planning creates one.
	if rm, err := env.PlanRemoveSettings(); err != nil || len(rm.Changes) != 0 {
		t.Errorf("remove on missing: %v %v", rm, err)
	}
	if h, s := env.SettingsWired(); h || s {
		t.Error("missing file reported wired")
	}
	plan, err := env.PlanSettings()
	if err != nil || len(plan.Changes) != 2 {
		t.Fatalf("plan: %q %v", plan.Changes, err)
	}
	if b, err := plan.Apply(); b != "" || err != nil {
		t.Errorf("fresh apply: %q %v", b, err)
	}

	// Invalid JSON is reported, never overwritten.
	if err := os.WriteFile(env.ClaudeSettings(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.PlanSettings(); err == nil {
		t.Error("invalid JSON accepted")
	}
	if _, err := env.PlanRemoveSettings(); err == nil {
		t.Error("invalid JSON accepted for removal")
	}

	// An empty file counts as an empty object.
	if err := os.WriteFile(env.ClaudeSettings(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if plan, err := env.PlanSettings(); err != nil || len(plan.Changes) != 2 {
		t.Errorf("empty file: %v %v", plan, err)
	}

	if got := shellQuote("/Users/a b/jev"); got != `'/Users/a b/jev'` {
		t.Errorf("shellQuote = %s", got)
	}
	if got := shellQuote("/plain/jev"); got != "/plain/jev" {
		t.Errorf("shellQuote = %s", got)
	}
}
