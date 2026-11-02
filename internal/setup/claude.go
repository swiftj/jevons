package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// hookTimeoutSeconds bounds the UserPromptSubmit hook in Claude Code.
const hookTimeoutSeconds = 10

// SettingsPlan is the change jev wants to make to Claude Code settings.
type SettingsPlan struct {
	Path    string
	Changes []string // human-readable, empty when nothing to do
	Notes   []string // things left alone on purpose
	data    []byte   // new file contents
	exists  bool
}

// shellQuote quotes p for the shell Claude Code runs hook commands in.
func shellQuote(p string) string {
	if strings.ContainsAny(p, " \t'\"$`\\") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}

// isJevCommand reports whether a hook or status line command runs jev.
func isJevCommand(cmd string) bool {
	return strings.Contains(cmd, "/"+BinaryName+" ") || strings.HasPrefix(cmd, BinaryName+" ") ||
		strings.Contains(cmd, "jev-hook")
}

// readSettings loads path as a JSON object; a missing file is an empty object.
func readSettings(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the user's own Claude settings path
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("setup: read %s: %w", path, err)
	}
	s := map[string]any{}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, true, fmt.Errorf("setup: %s is not valid JSON: %w", path, err)
		}
	}
	return s, true, nil
}

// PlanSettings works out how to add the jev hook and status line to the
// Claude Code settings. An existing status line that is not jev's is kept.
func (e *Env) PlanSettings() (*SettingsPlan, error) {
	path := e.ClaudeSettings()
	s, exists, err := readSettings(path)
	if err != nil {
		return nil, err
	}
	p := &SettingsPlan{Path: path, exists: exists}
	bin := shellQuote(e.Target())

	hooks, _ := s["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	groups, _ := hooks["UserPromptSubmit"].([]any)
	hookCmd := bin + " hook"
	found := false
	for _, g := range groups {
		for _, h := range hookEntries(g) {
			if c, _ := h["command"].(string); isJevCommand(c) {
				found = true
				if c != hookCmd {
					h["command"] = hookCmd
					p.Changes = append(p.Changes, "update the UserPromptSubmit hook to run "+hookCmd)
				}
			}
		}
	}
	if !found {
		groups = append(groups, map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": hookCmd, "timeout": hookTimeoutSeconds,
		}}})
		p.Changes = append(p.Changes, "add a UserPromptSubmit hook: "+hookCmd)
	}
	hooks["UserPromptSubmit"] = groups
	s["hooks"] = hooks

	statusCmd := bin + " statusline"
	switch sl, _ := s["statusLine"].(map[string]any); {
	case sl == nil:
		s["statusLine"] = map[string]any{"type": "command", "command": statusCmd}
		p.Changes = append(p.Changes, "set the status line to: "+statusCmd)
	case isJevCommand(fmt.Sprint(sl["command"])):
		if sl["command"] != statusCmd {
			sl["command"] = statusCmd
			p.Changes = append(p.Changes, "update the status line to: "+statusCmd)
		}
	default:
		p.Notes = append(p.Notes, "you already have a status line; to show Jev in it, call `"+statusCmd+"` from it with the same stdin")
	}

	if len(p.Changes) > 0 {
		if p.data, err = json.MarshalIndent(s, "", "  "); err != nil {
			return nil, fmt.Errorf("setup: encode settings: %w", err)
		}
		p.data = append(p.data, '\n')
	}
	return p, nil
}

// PlanRemoveSettings works out how to take jev's hook and status line out.
func (e *Env) PlanRemoveSettings() (*SettingsPlan, error) {
	path := e.ClaudeSettings()
	s, exists, err := readSettings(path)
	if err != nil || !exists {
		return &SettingsPlan{Path: path}, err
	}
	p := &SettingsPlan{Path: path, exists: true}
	if hooks, ok := s["hooks"].(map[string]any); ok {
		groups, _ := hooks["UserPromptSubmit"].([]any)
		var kept []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			var keepHooks []any
			for _, h := range hookEntries(g) {
				if c, _ := h["command"].(string); isJevCommand(c) {
					p.Changes = append(p.Changes, "remove the UserPromptSubmit hook: "+c)
					continue
				}
				keepHooks = append(keepHooks, h)
			}
			if len(keepHooks) > 0 {
				gm["hooks"] = keepHooks
				kept = append(kept, gm)
			}
		}
		if len(kept) > 0 {
			hooks["UserPromptSubmit"] = kept
		} else {
			delete(hooks, "UserPromptSubmit")
		}
		if len(hooks) == 0 {
			delete(s, "hooks")
		}
	}
	if sl, ok := s["statusLine"].(map[string]any); ok && isJevCommand(fmt.Sprint(sl["command"])) {
		delete(s, "statusLine")
		p.Changes = append(p.Changes, "remove the jev status line")
	}
	if len(p.Changes) > 0 {
		if p.data, err = json.MarshalIndent(s, "", "  "); err != nil {
			return nil, fmt.Errorf("setup: encode settings: %w", err)
		}
		p.data = append(p.data, '\n')
	}
	return p, nil
}

// hookEntries returns the command hooks inside one hook group.
func hookEntries(g any) []map[string]any {
	gm, _ := g.(map[string]any)
	list, _ := gm["hooks"].([]any)
	var out []map[string]any
	for _, h := range list {
		if hm, ok := h.(map[string]any); ok {
			out = append(out, hm)
		}
	}
	return out
}

// Apply writes the planned settings, first copying the old file to
// <path>.jev-backup. It returns the backup path ("" when there was no file).
func (p *SettingsPlan) Apply() (string, error) {
	if len(p.Changes) == 0 {
		return "", nil
	}
	perm := os.FileMode(0o600)
	backup := ""
	if p.exists {
		old, err := os.ReadFile(p.Path)
		if err != nil {
			return "", fmt.Errorf("setup: %w", err)
		}
		if info, err := os.Stat(p.Path); err == nil {
			perm = info.Mode().Perm()
		}
		backup = p.Path + ".jev-backup"
		// #nosec G703 -- backup sits beside the user's own settings file
		if err := os.WriteFile(backup, old, 0o600); err != nil {
			return "", fmt.Errorf("setup: back up settings: %w", err)
		}
	}
	return backup, writeFileAtomic(p.Path, p.data, perm)
}

// SettingsWired reports whether the Claude settings run jev's hook and status line.
func (e *Env) SettingsWired() (hook, status bool) {
	s, _, err := readSettings(e.ClaudeSettings())
	if err != nil {
		return false, false
	}
	hooks, _ := s["hooks"].(map[string]any)
	groups, _ := hooks["UserPromptSubmit"].([]any)
	for _, g := range groups {
		for _, h := range hookEntries(g) {
			if c, _ := h["command"].(string); isJevCommand(c) {
				hook = true
			}
		}
	}
	sl, _ := s["statusLine"].(map[string]any)
	return hook, sl != nil && isJevCommand(fmt.Sprint(sl["command"]))
}
