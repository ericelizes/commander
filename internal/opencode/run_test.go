package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeBinary writes a /bin/sh script that emits stdout and optionally
// stderr / exits non-zero. Returns the absolute path to the script.
func writeFakeBinary(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary tests assume a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func TestRun_CaptureThenResume(t *testing.T) {
	// Fake opencode prints two events. The first carries a sessionID; the
	// wrapper must capture it on the no-session call. On the resume call,
	// the wrapper must pass --session and we assert the script saw it.
	script := `#!/bin/sh
# Echo the args to a sidechannel so the test can verify --session handling.
echo "$@" > "$ARGS_FILE"

# Branch on whether --session was passed.
case "$*" in
  *--session*)
    # Resume call. Emit a single text event echoing the captured ID.
    cat <<'JSON'
{"type":"text","timestamp":2,"sessionID":"ses_captured123","part":{"text":"resumed"}}
JSON
    ;;
  *)
    # First call. Emit one text event then a step_finish; both carry the
    # session ID for capture.
    cat <<'JSON'
{"type":"text","timestamp":1,"sessionID":"ses_captured123","part":{"text":"first"}}
{"type":"step_finish","timestamp":2,"sessionID":"ses_captured123","part":{"cost":0.5,"tokens":{"input":100,"output":50}}}
JSON
    ;;
esac
`
	bin := writeFakeBinary(t, "opencode", script)

	argsFile1 := filepath.Join(t.TempDir(), "args1")
	t.Setenv("ARGS_FILE", argsFile1)

	c := NewWithBinary(bin)
	res, err := c.Run(context.Background(), "", "operator-code", "hi", RunOptions{})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Drain events to confirm channel is closed.
	count := 0
	for range res.Events {
		count++
	}
	if count != 2 {
		t.Errorf("event count = %d, want 2", count)
	}
	if res.SessionID != "ses_captured123" {
		t.Errorf("captured SessionID = %q, want ses_captured123", res.SessionID)
	}
	if res.FinalText != "first" {
		t.Errorf("FinalText = %q, want %q", res.FinalText, "first")
	}
	if res.TotalCostUSD != 0.5 {
		t.Errorf("TotalCostUSD = %v, want 0.5", res.TotalCostUSD)
	}
	if res.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", res.TotalTokens)
	}
	// First call should NOT have --session.
	got1, _ := os.ReadFile(argsFile1)
	if strings.Contains(string(got1), "--session") {
		t.Errorf("first call had --session: %s", got1)
	}

	// Resume.
	argsFile2 := filepath.Join(t.TempDir(), "args2")
	t.Setenv("ARGS_FILE", argsFile2)

	res2, err := c.Run(context.Background(), res.SessionID, "operator-code", "again", RunOptions{})
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	for range res2.Events {
	}
	if res2.SessionID != "ses_captured123" {
		t.Errorf("resume SessionID = %q", res2.SessionID)
	}
	got2, _ := os.ReadFile(argsFile2)
	if !strings.Contains(string(got2), "--session ses_captured123") {
		t.Errorf("resume call missing --session ses_captured123: %s", got2)
	}
}

func TestRun_AccumulatesCostAndTokens(t *testing.T) {
	// Three step_finish events; assert sums.
	script := `#!/bin/sh
cat <<'JSON'
{"type":"step_finish","timestamp":1,"sessionID":"ses_x","part":{"cost":0.10,"tokens":{"input":100,"output":10}}}
{"type":"step_finish","timestamp":2,"sessionID":"ses_x","part":{"cost":0.20,"tokens":{"input":200,"output":20}}}
{"type":"step_finish","timestamp":3,"sessionID":"ses_x","part":{"cost":0,"tokens":{"input":300,"output":30}}}
JSON
`
	bin := writeFakeBinary(t, "opencode", script)
	c := NewWithBinary(bin)

	res, err := c.Run(context.Background(), "ses_x", "operator-code", "go", RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range res.Events {
	}
	// 0.10 + 0.20 + 0 == 0.30; float math but values are exact in binary64.
	if res.TotalCostUSD < 0.299 || res.TotalCostUSD > 0.301 {
		t.Errorf("TotalCostUSD = %v, want ~0.30", res.TotalCostUSD)
	}
	if res.TotalTokens != 660 {
		t.Errorf("TotalTokens = %d, want 660", res.TotalTokens)
	}
}

func TestRun_SessionNotFound(t *testing.T) {
	// Fake opencode emits the verified NotFoundError on stderr and exits 1.
	script := `#!/bin/sh
echo "NotFoundError: Session not found: ses_bogus" >&2
exit 1
`
	bin := writeFakeBinary(t, "opencode", script)
	c := NewWithBinary(bin)

	_, err := c.Run(context.Background(), "ses_bogus", "operator-code", "hi", RunOptions{})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestBuildRunArgs_FlagOrdering(t *testing.T) {
	// Spot-check that all the verified flags map correctly. Order is stable
	// for snapshot-friendly diffing.
	got := buildRunArgs("ses_x", "operator-code", "hello", RunOptions{
		Model:           "anthropic/claude-sonnet-4-5",
		AttachURL:       "http://localhost:9997",
		Dir:             "/workspace",
		SkipPermissions: true,
		Files:           []string{"a.txt", "b.txt"},
	})
	want := []string{
		"run", "--format", "json",
		"--session", "ses_x",
		"--agent", "operator-code",
		"--model", "anthropic/claude-sonnet-4-5",
		"--attach", "http://localhost:9997",
		"--dir", "/workspace",
		"--file", "a.txt",
		"--file", "b.txt",
		"--dangerously-skip-permissions",
		"hello",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildRunArgs_NoSessionMeansNoSessionFlag(t *testing.T) {
	// Capture-then-resume: empty sessionID must NOT pass --session.
	got := buildRunArgs("", "operator-code", "hi", RunOptions{})
	for _, a := range got {
		if a == "--session" {
			t.Fatalf("empty sessionID should not emit --session: %v", got)
		}
	}
}
