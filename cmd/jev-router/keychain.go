package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// keychainTimeout bounds the `security` lookup.
const keychainTimeout = 5 * time.Second

// keychainCommand builds the macOS keychain lookup for service; tests replace it.
var keychainCommand = func(ctx context.Context, service string) *exec.Cmd {
	return exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-w") // #nosec G204 -- fixed binary, argv slice, operator-supplied service name
}

// loadKeychainKey sets env to the login keychain's generic password for
// service, so a launchd-started daemon (which never sees shell profiles) can
// reach the classifier. It does nothing when service is empty or env is
// already set. The key is never logged or included in errors.
func loadKeychainKey(ctx context.Context, service, env string) error {
	if service == "" || os.Getenv(env) != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()
	cmd := keychainCommand(ctx, service)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// ee.Stderr is security's own message, never the secret.
			return fmt.Errorf("keychain: read %q: %w: %s", service, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return fmt.Errorf("keychain: read %q: %w", service, err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return fmt.Errorf("keychain: %q is empty", service)
	}
	if err := os.Setenv(env, key); err != nil {
		return fmt.Errorf("keychain: set %s: %w", env, err)
	}
	return nil
}
