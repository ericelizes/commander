package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestSharedReportPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/tmp/report.md": true, "/tmp/../tmp/r.md": true, "/var/tmp/x": true, "/dev/shm/r.md": true,
		"/tmp/op.abc/report.md": false, "/work/tree/.report": false,
	} {
		if got := sharedReportPath(p); got != want {
			t.Errorf("%s: got %v want %v", p, got, want)
		}
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if !sharedReportPath(filepath.Join(tmp, "report.md")) {
		t.Errorf("a bare file in $TMPDIR is shared")
	}
	if sharedReportPath(filepath.Join(tmp, "op.x", "report.md")) {
		t.Errorf("a private dir under $TMPDIR is not shared")
	}
	// Relative paths resolve from the working directory: report.md run from /tmp is /tmp/report.md.
	t.Chdir("/tmp")
	if !sharedReportPath("report.md") {
		t.Errorf("relative report.md from /tmp is shared")
	}
	wt := t.TempDir()
	t.Chdir(wt)
	if sharedReportPath("report.md") || sharedReportPath(filepath.Join(wt, "sub", "r.md")) {
		t.Errorf("a file in a private dir is not shared")
	}
}

// End to end: --file naming a bare file in a shared temp dir is refused before
// anything is read or written; the same report from a private dir is posted.
func TestInternalCommentRefusesASharedReportFile(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	shared := t.TempDir()
	t.Setenv("TMPDIR", shared)
	bare := filepath.Join(shared, "report.md")
	if err := os.WriteFile(bare, []byte("someone's report"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalComment([]string{"--project", "manifest", "--bead", "x-1", "--file", bare}, strings.NewReader(""), stdout, stderr); code == 0 {
		t.Fatal("a shared report path was accepted")
	}
	if _, e := read(); !strings.Contains(e, "shared fixed path") || !strings.Contains(e, "mktemp -p") {
		t.Fatalf("refusal does not say why or the way out: %s", e)
	}
	private := filepath.Join(shared, "op.abc", "report.md")
	if err := os.MkdirAll(filepath.Dir(private), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, []byte("my report"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalComment([]string{"--project", "manifest", "--bead", "x-1", "--file", private}, strings.NewReader(""), stdout, stderr); code != 0 {
		out, e := read()
		t.Fatalf("private report refused: exit=%d %s %s", code, out, e)
	}
}
