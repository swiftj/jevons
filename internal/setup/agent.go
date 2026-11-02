package setup

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"text/template"
	"time"
)

const launchctl = "/bin/launchctl"

//go:embed agent.plist.tmpl
var agentTemplate string

var agentTmpl = template.Must(template.New("agent").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(agentTemplate))

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s)) // writes to a bytes.Buffer cannot fail
	return b.String()
}

// AgentPlistData renders the launchd plist for this Env.
func (e *Env) AgentPlistData() ([]byte, error) {
	var b bytes.Buffer
	err := agentTmpl.Execute(&b, map[string]string{
		"Label":            AgentLabel,
		"Binary":           e.Target(),
		"Keychain":         KeychainService,
		"TypeSafeKeychain": TypeSafeKeychainService,
		"Log":              filepath.Join(e.JevDir(), "jev-router.log"),
	})
	if err != nil {
		return nil, fmt.Errorf("setup: render agent: %w", err)
	}
	return b.Bytes(), nil
}

func (e *Env) domain() string  { return "gui/" + strconv.Itoa(e.UID) }
func (e *Env) service() string { return e.domain() + "/" + AgentLabel }

// InstallAgent writes the plist and (re)loads it, so the daemon starts now and
// at every login.
func (e *Env) InstallAgent(ctx context.Context) error {
	data, err := e.AgentPlistData()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.JevDir(), 0o700); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if err := writeFileAtomic(e.AgentPlist(), data, 0o644); err != nil {
		return err
	}
	_, _ = e.Exec(ctx, nil, launchctl, "bootout", e.service()) // not loaded yet is fine
	if _, err := e.Exec(ctx, nil, launchctl, "bootstrap", e.domain(), e.AgentPlist()); err != nil {
		return fmt.Errorf("setup: load agent: %w", err)
	}
	return nil
}

// RemoveAgent unloads the agent and deletes its plist.
func (e *Env) RemoveAgent(ctx context.Context) error {
	_, _ = e.Exec(ctx, nil, launchctl, "bootout", e.service()) // already unloaded is fine
	if err := os.Remove(e.AgentPlist()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("setup: %w", err)
	}
	return nil
}

// AgentLoaded reports whether launchd knows the agent.
func (e *Env) AgentLoaded(ctx context.Context) bool {
	_, err := e.Exec(ctx, nil, launchctl, "print", e.service())
	return err == nil
}

// RestartAgent restarts the running daemon.
func (e *Env) RestartAgent(ctx context.Context) error {
	if _, err := e.Exec(ctx, nil, launchctl, "kickstart", "-k", e.service()); err != nil {
		return fmt.Errorf("setup: restart agent: %w", err)
	}
	return nil
}

// Healthy reports whether the daemon answers /healthz.
func (e *Env) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.DaemonAddr+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close() // only the status matters
	return resp.StatusCode == http.StatusOK
}

// WaitHealthy polls /healthz until it answers or timeout passes.
func (e *Env) WaitHealthy(ctx context.Context, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if e.Healthy(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// writeFileAtomic writes data to a temp file beside path and renames it in.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- LaunchAgents/.claude are normally 0755
		return fmt.Errorf("setup: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // already failing
		return fmt.Errorf("setup: write %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close() // already failing
		return fmt.Errorf("setup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("setup: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	return nil
}
