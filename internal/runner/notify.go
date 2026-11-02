package runner

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/swiftj/jevons/pkg/schema"
)

// NewDesktopNotifier returns a schema.Notifier for the current platform:
// osascript on macOS, notify-send on Linux when it is on PATH, otherwise a no-op.
func NewDesktopNotifier() schema.Notifier {
	return newDesktopNotifier(runtime.GOOS, exec.LookPath, exec.CommandContext)
}

func newDesktopNotifier(goos string, lookPath LookPathFunc, factory CommandFactory) schema.Notifier {
	switch goos {
	case "darwin":
		if bin, err := lookPath("osascript"); err == nil {
			return &cmdNotifier{bin: bin, argv: OSAScriptArgs, exec: factory}
		}
	case "linux":
		if bin, err := lookPath("notify-send"); err == nil {
			return &cmdNotifier{bin: bin, argv: notifySendArgs, exec: factory}
		}
	}
	return noopNotifier{}
}

// noopNotifier discards notifications.
type noopNotifier struct{}

// Notify does nothing.
func (noopNotifier) Notify(context.Context, string, string) error { return nil }

// cmdNotifier sends a notification by running bin with argv(title, body).
type cmdNotifier struct {
	bin  string
	argv func(title, body string) []string
	exec CommandFactory
}

// Notify runs the notification command, bounded by ctx.
func (n *cmdNotifier) Notify(ctx context.Context, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	cmd := n.exec(ctx, n.bin, n.argv(title, body)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("runner: notify via %s: %w (%s)", n.bin, err, truncate(strings.TrimSpace(string(out)), maxLoggedPrompt))
	}
	return nil
}

// AppleScriptQuote returns s as a double-quoted AppleScript string literal,
// escaping backslashes and double quotes.
func AppleScriptQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// OSAScriptArgs returns the osascript argv (excluding the binary) that displays
// a notification. The script is a single -e argument; no shell is involved.
func OSAScriptArgs(title, body string) []string {
	return []string{"-e", "display notification " + AppleScriptQuote(body) + " with title " + AppleScriptQuote(title)}
}

func notifySendArgs(title, body string) []string {
	return []string{"--", title, body}
}
