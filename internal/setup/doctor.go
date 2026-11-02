package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Check is one doctor result.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Doctor checks every part of the setup and returns the results in order.
// With online set it also asks GitHub for the latest release.
func (e *Env) Doctor(ctx context.Context, online bool) []Check {
	var cs []Check
	add := func(name string, ok bool, detail string) { cs = append(cs, Check{name, ok, detail}) }

	if _, err := os.Stat(e.Target()); err != nil {
		add("binary", false, e.Target()+" missing; run `jev install`")
	} else {
		add("binary", true, e.Target()+" ("+e.Version+")")
	}
	if onPath(e.BinDir, e.Getenv("PATH")) {
		add("PATH", true, e.BinDir+" is on PATH")
	} else {
		add("PATH", false, "add "+e.BinDir+" to PATH")
	}
	if online {
		if rel, err := e.LatestRelease(ctx); err != nil {
			add("update", false, "could not check: "+err.Error())
		} else if Newer(rel.Tag, e.Version) {
			add("update", false, rel.Tag+" is available; run `jev update`")
		} else {
			add("update", true, "latest release is "+rel.Tag)
		}
	}
	if e.darwin() {
		switch {
		case e.HasKey(ctx):
			add("API key", true, "in the keychain ("+KeychainService+")")
		case e.Getenv(e.APIKeyEnv) != "":
			add("API key", false, "only in $"+e.APIKeyEnv+"; the login agent cannot see it. Run `jev install` to store it")
		default:
			add("API key", false, "not set; free-text prompts route to Tier 2. Run `jev install`")
		}
		if e.AgentLoaded(ctx) {
			add("login agent", true, AgentLabel+" loaded")
		} else {
			add("login agent", false, "not loaded; run `jev install`")
		}
	}

	st, err := e.daemonStatus(ctx)
	switch {
	case err != nil:
		add("daemon", false, "not answering at "+e.DaemonAddr+"; see "+filepath.Join(e.JevDir(), "jev-router.log"))
	case st.ClassifierErrors > 0:
		add("daemon", true, "running for "+st.Uptime)
		add("classifier", false, "recent failure: "+st.LastClassifierError)
	default:
		add("daemon", true, "running for "+st.Uptime)
		add("classifier", true, "no failures")
	}

	switch hook, status := e.SettingsWired(); {
	case hook && status:
		add("Claude Code", true, "hook and status line set up")
	case hook:
		add("Claude Code", true, "hook set up; status line is not jev's")
	default:
		add("Claude Code", false, "hook not set up; run `jev install`")
	}
	return cs
}

type daemonStatus struct {
	Uptime              string `json:"uptime"`
	ClassifierErrors    int64  `json:"classifier_errors"`
	LastClassifierError string `json:"last_classifier_error"`
}

func (e *Env) daemonStatus(ctx context.Context) (daemonStatus, error) {
	var st daemonStatus
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.DaemonAddr+"/status", nil)
	if err != nil {
		return st, err
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return st, err
	}
	defer func() { _ = resp.Body.Close() }() // read-only response
	if resp.StatusCode != http.StatusOK {
		return st, errStatus(resp.Status)
	}
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

type errStatus string

func (e errStatus) Error() string { return "status " + string(e) }
