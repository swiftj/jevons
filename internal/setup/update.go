package setup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxDownloadBytes caps a release asset download.
const maxDownloadBytes = 200 << 20

// Release is the part of a GitHub release jev needs.
type Release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetURL returns the download URL of the named asset.
func (r Release) assetURL(name string) (string, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			if !strings.HasPrefix(a.URL, "https://") {
				return "", fmt.Errorf("setup: asset %s is not served over https", name)
			}
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("setup: release has no %s asset", name)
}

// AssetName is the release file for this platform. macOS gets one universal
// binary for both Apple silicon and Intel.
func (e *Env) AssetName() string {
	if e.darwin() {
		return "jev_darwin_universal"
	}
	return "jev_" + e.GOOS + "_" + e.GOARCH
}

// LatestRelease asks GitHub for the newest published release.
func (e *Env) LatestRelease(ctx context.Context) (Release, error) {
	var r Release
	body, err := e.get(ctx, e.APIBase+"/repos/"+Repo+"/releases/latest", 1<<20)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Tag == "" {
		return r, fmt.Errorf("setup: unexpected release response (tag %q): %w", r.Tag, err)
	}
	return r, nil
}

func (e *Env) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("setup: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("setup: fetch %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("setup: fetch %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("setup: %s is larger than %d bytes", url, limit)
	}
	return b, nil
}

// checksumFor finds name's SHA-256 in a SHA256SUMS file.
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("setup: SHA256SUMS has no entry for %s", name)
}

// semver parses vX.Y.Z (ignoring any -suffix) into numbers.
func semver(v string) ([3]int, bool) {
	var n [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// Newer reports whether release tag is newer than version. A version that is
// not semver (a dev build) is never considered current.
func Newer(tag, version string) bool {
	t, ok := semver(tag)
	if !ok {
		return false
	}
	v, ok := semver(version)
	if !ok {
		return true
	}
	for i := range t {
		if t[i] != v[i] {
			return t[i] > v[i]
		}
	}
	return false
}

// UpdateOptions control Update.
type UpdateOptions struct {
	Force     bool // reinstall even when already current
	CheckOnly bool // report, change nothing
}

// Update replaces the installed binary with the latest release after checking
// its SHA-256, restarts the daemon, and rolls back if the daemon does not come
// back healthy.
func (e *Env) Update(ctx context.Context, opt UpdateOptions) error {
	rel, err := e.LatestRelease(ctx)
	if err != nil {
		return err
	}
	if !Newer(rel.Tag, e.Version) && !opt.Force {
		e.step(true, "jev %s is the latest release", e.Version)
		return nil
	}
	if opt.CheckOnly {
		e.step(false, "jev %s is available (you have %s); run `jev update`", rel.Tag, e.Version)
		return nil
	}
	target := e.Target()
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("setup: %s is not installed; run `jev install` first", target)
	}

	name := e.AssetName()
	newPath, err := e.download(ctx, rel, name)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(newPath) }() // gone after a successful rename
	out, err := e.Exec(ctx, nil, newPath, "version")
	if got := strings.TrimSpace(string(out)); err != nil || got != rel.Tag {
		return fmt.Errorf("setup: downloaded binary reports %q, want %s: %w", got, rel.Tag, err)
	}
	e.step(true, "downloaded %s %s and verified its SHA-256", name, rel.Tag)

	prev := target + ".prev"
	if err := os.Rename(target, prev); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if err := os.Rename(newPath, target); err != nil {
		_ = os.Rename(prev, target) // put the old binary back
		return fmt.Errorf("setup: %w", err)
	}
	e.step(true, "installed %s (previous version kept at %s)", target, prev)

	if !e.darwin() || !e.AgentLoaded(ctx) {
		return nil
	}
	if err := e.RestartAgent(ctx); err == nil && e.WaitHealthy(ctx, e.healthTimeout()) {
		e.step(true, "daemon restarted and healthy")
		return nil
	}
	// Roll back: the new daemon did not come up.
	if err := os.Rename(prev, target); err != nil {
		return fmt.Errorf("setup: new daemon unhealthy and rollback failed: %w", err)
	}
	_ = e.RestartAgent(ctx) // best effort; the error below already reports failure
	return fmt.Errorf("setup: %s did not start; rolled back to %s", rel.Tag, e.Version)
}

// download fetches asset name and SHA256SUMS from rel, verifies the hash, and
// returns the path of an executable temp file beside the install target.
func (e *Env) download(ctx context.Context, rel Release, name string) (string, error) {
	sumsURL, err := rel.assetURL("SHA256SUMS")
	if err != nil {
		return "", err
	}
	sums, err := e.get(ctx, sumsURL, 1<<16)
	if err != nil {
		return "", err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return "", err
	}
	url, err := rel.assetURL(name)
	if err != nil {
		return "", err
	}
	data, err := e.get(ctx, url, maxDownloadBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", errors.New("setup: checksum mismatch for " + name + "; nothing was changed")
	}
	tmp, err := os.CreateTemp(e.BinDir, ".jev-update-*")
	if err != nil {
		return "", fmt.Errorf("setup: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()           // already failing
		_ = os.Remove(tmp.Name()) // clean up the partial file
		return "", fmt.Errorf("setup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name()) // clean up the partial file
		return "", fmt.Errorf("setup: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { // #nosec G302 -- an executable
		_ = os.Remove(tmp.Name()) // clean up
		return "", fmt.Errorf("setup: %w", err)
	}
	return filepath.Clean(tmp.Name()), nil
}
