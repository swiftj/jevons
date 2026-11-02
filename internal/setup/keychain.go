package setup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const securityBin = "/usr/bin/security"

// keyPattern limits what StoreKey accepts, so the value is safe on a
// `security -i` command line.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,256}$`)

// HasKey reports whether the keychain holds the item for service. It never
// reads the secret itself.
func (e *Env) HasKey(ctx context.Context, service string) bool {
	_, err := e.Exec(ctx, nil, securityBin, "find-generic-password", "-s", service)
	return err == nil
}

// StoreKey adds or replaces the item for service. The key is passed on stdin
// to `security -i`, so it never appears in a process argument list.
func (e *Env) StoreKey(ctx context.Context, service, key string) error {
	key = strings.TrimSpace(key)
	if !keyPattern.MatchString(key) {
		return errors.New("setup: that does not look like an API key")
	}
	cmd := fmt.Sprintf("add-generic-password -a %s -s %s -T %s -U -w %s\n",
		quoteArg(e.User), service, securityBin, key)
	if _, err := e.Exec(ctx, strings.NewReader(cmd), securityBin, "-i"); err != nil {
		// Exec errors carry security's stderr, never stdin, so the key stays out.
		return fmt.Errorf("setup: store key: %w", err)
	}
	if !e.HasKey(ctx, service) {
		return errors.New("setup: store key: item not found after adding it")
	}
	return nil
}

// DeleteKey removes the item for service; a missing item is not an error.
func (e *Env) DeleteKey(ctx context.Context, service string) error {
	if !e.HasKey(ctx, service) {
		return nil
	}
	if _, err := e.Exec(ctx, nil, securityBin, "delete-generic-password", "-s", service); err != nil {
		return fmt.Errorf("setup: delete key: %w", err)
	}
	return nil
}

// quoteArg double-quotes s for a `security -i` command line.
func quoteArg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
