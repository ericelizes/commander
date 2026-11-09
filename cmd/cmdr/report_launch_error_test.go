package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestReportLaunchError(t *testing.T) {
	t.Run("opencode failure prints actionable hint", func(t *testing.T) {
		// mirrors how captureCommanderSession wraps a failed opencode import
		err := fmt.Errorf("commander session id: capture commander session: %w: opencode import: exit status 1", ErrOpencodeUnavailable)
		var buf bytes.Buffer
		if code := reportLaunchError(&buf, err); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		out := buf.String()
		if !strings.Contains(out, "cmdr: ") {
			t.Errorf("missing raw error line:\n%s", out)
		}
		for _, want := range []string{"opencode --version", "opencode mcp list", "~/.opencode/opencode.json"} {
			if !strings.Contains(out, want) {
				t.Errorf("hint missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("unrelated failure prints no opencode hint", func(t *testing.T) {
		err := fmt.Errorf("tmux new-session: exit status 1: no server running")
		var buf bytes.Buffer
		if code := reportLaunchError(&buf, err); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		out := buf.String()
		if !strings.Contains(out, "tmux new-session") {
			t.Errorf("missing raw error line:\n%s", out)
		}
		if strings.Contains(out, "opencode mcp list") {
			t.Errorf("should not print opencode hint for unrelated error:\n%s", out)
		}
	})
}
