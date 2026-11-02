package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestHelperProcess stands in for /usr/bin/security; JEV_HELPER_MODE picks its behaviour.
func TestHelperProcess(_ *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("JEV_HELPER_MODE") {
	case "ok":
		fmt.Println("sk-test-key")
	case "empty":
		fmt.Println("  ")
	default:
		fmt.Fprintln(os.Stderr, "The specified item could not be found in the keychain.")
		os.Exit(44)
	}
	os.Exit(0)
}

// fakeKeychain points keychainCommand at TestHelperProcess in mode and counts calls.
func fakeKeychain(t *testing.T, mode string) *int {
	t.Helper()
	calls := 0
	orig := keychainCommand
	keychainCommand = func(ctx context.Context, service string) *exec.Cmd {
		calls++
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$", "--", service)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "JEV_HELPER_MODE="+mode)
		return cmd
	}
	t.Cleanup(func() { keychainCommand = orig })
	return &calls
}

func TestLoadKeychainKey(t *testing.T) {
	const env = "JEV_TEST_KEYCHAIN_KEY"
	cases := []struct {
		name, service, preset, mode string
		wantKey, wantErr            string
		wantCalls                   int
	}{
		{"no service", "", "", "ok", "", "", 0},
		{"env already set", "jev-pro", "from-env", "ok", "from-env", "", 0},
		{"found", "jev-pro", "", "ok", "sk-test-key", "", 1},
		{"empty secret", "jev-pro", "", "empty", "", "is empty", 1},
		{"missing item", "jev-pro", "", "fail", "", "could not be found", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(env, tc.preset)
			calls := fakeKeychain(t, tc.mode)
			err := loadKeychainKey(context.Background(), tc.service, env)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "sk-test-key") {
				t.Error("secret leaked into error")
			}
			if got := os.Getenv(env); got != tc.wantKey {
				t.Errorf("env = %q, want %q", got, tc.wantKey)
			}
			if *calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", *calls, tc.wantCalls)
			}
		})
	}
}
