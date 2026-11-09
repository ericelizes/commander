package notify

import (
	"context"
	"fmt"
	"strings"
)

// notifyDarwin shells out to osascript to drive Notification Center. The
// AppleScript fragment is a single-line string with embedded title/body, so
// any literal `"` or `\` in the user-provided text would otherwise terminate
// or corrupt the script. We escape both before interpolation.
//
// We invoke osascript with `-e <one-line script>` (no shell, argv-form via
// os/exec.CommandContext) so quoting is handled by the kernel exec path,
// not by /bin/sh.
func (s *system) notifyDarwin(ctx context.Context, title, body string) error {
	script := fmt.Sprintf(
		`display notification "%s" with title "%s"`,
		escapeAppleScript(body),
		escapeAppleScript(title),
	)
	cmd := s.exec(ctx, "osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify(darwin): osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// escapeAppleScript escapes the two characters that can break a double-quoted
// AppleScript string literal: backslash (the escape character itself) and
// double-quote (the string terminator). Order matters — backslash must be
// escaped first so the replacements introduced for `"` aren't double-escaped.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
