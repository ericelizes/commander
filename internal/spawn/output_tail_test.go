package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// The tail a Manager reads of a dead Operator says what it last did: control
// bytes and heartbeats dropped, stream-json reduced to its type and text.
func TestOutputTail(t *testing.T) {
	dir := t.TempDir()
	run := store.AgentRun{AgentName: "operator-op-x-p-1.2"}
	path := filepath.Join(dir, ".commander", "operators", "op-x-p-1.2.fifo")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out := strings.Repeat("old line\n", 5000) +
		"\x1b[32mplain shell line\x1b[0m\r\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Running the   suite now"}]}}` + "\n" +
		`{"type":"tool_progress","elapsed_time_seconds":30,"heartbeat":true}` + "\n" +
		`{"type":"result","subtype":"error_during_execution","result":"API Error: overloaded"}` + "\n"
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	got := OutputTail(run, dir, 3)
	want := "plain shell line\nassistant: Running the suite now\nresult/error_during_execution: API Error: overloaded"
	if got != want {
		t.Fatalf("tail:\n%s\nwant:\n%s", got, want)
	}
	if OutputTail(store.AgentRun{AgentName: "operator-none"}, dir, 3) != "" {
		t.Fatal("a run with no output has no tail")
	}
}

// A crash restart tells the Operator why it was cut off.
func TestHostResumePromptSaysWhy(t *testing.T) {
	req := SpawnRequest{ProjectPath: "/p", TaskBeadID: "p-1.2"}
	if p := hostResumePrompt(req); !strings.HasPrefix(p, "[resume] your process was cut off by a restart; continue") {
		t.Fatalf("restart: %s", p)
	}
	req.ResumeNote = "your process ended before it wrote its final report on the bead"
	if p := hostResumePrompt(req); !strings.HasPrefix(p, "[resume] your process ended before it wrote its final report on the bead; continue") {
		t.Fatalf("crash: %s", p)
	}
}
