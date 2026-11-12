package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `cmdr board --workflow` prints the workflow the app shows, derived from the
// snapshot when cmdrd sends none (a cmdrd older than workflows).
func TestBoardWorkflow(t *testing.T) {
	raw, err := os.ReadFile("../../internal/livedata/testdata/workflow/gates.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture wraps the snapshot with its clock; the command reads a bare snapshot.
	start := strings.Index(string(raw), `"snapshot":`) + len(`"snapshot":`)
	snap := strings.TrimSuffix(strings.TrimSpace(string(raw[start:])), "}")
	dir := t.TempDir()
	file := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(file, []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(which string) string {
		out, err := os.Create(filepath.Join(dir, "out"))
		if err != nil {
			t.Fatal(err)
		}
		if code := runBoard([]string{"--snapshot", file, "--workflow", which}, out, os.Stderr); code != 0 {
			t.Fatalf("--workflow %s: exit %d", which, code)
		}
		out.Close()
		b, _ := os.ReadFile(out.Name())
		return string(b)
	}
	got := run("1")
	for _, want := range []string{"▶ [gate, you] Workflow approved: compare sample text variants: Needs you (lab-t06.6)", "+   Verify the sample list guide: Not started (lab-t06.2.3)", "approving it starts these 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("workflow 1 lacks %q:\n%s", want, got)
		}
	}
	if got := run("lab-t06.3"); !strings.Contains(got, "▶ Restore the test VM: Agent working (lab-t06.3.2)") {
		t.Errorf("track workflow:\n%s", got)
	}
}

// `cmdr board --diagram` and `cmdr graph check <ws|bead>` read the same
// derived diagram the app draws: lanes, gates, findings, and an exit code an
// agent can act on.
func TestBoardDiagramAndGraphCheck(t *testing.T) {
	raw, err := os.ReadFile("../../internal/livedata/testdata/diagram/tracks.json")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(raw), `"snapshot":`) + len(`"snapshot":`)
	snap := strings.TrimSuffix(strings.TrimSpace(string(raw[start:])), "}")
	dir := t.TempDir()
	file := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(file, []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(f func([]string, *os.File, *os.File) int, args ...string) (string, int) {
		out, err := os.Create(filepath.Join(dir, "out"))
		if err != nil {
			t.Fatal(err)
		}
		code := f(append(args, "--snapshot", file), out, os.Stderr)
		out.Close()
		b, _ := os.ReadFile(out.Name())
		return string(b), code
	}
	got, code := run(runBoard, "--diagram", "lab-bex.2")
	if code != 0 || !strings.Contains(got, "[Reviewers, reviewer-a, reviewer-b] [gate, reviewer-a, reviewer-b] Export PR approved by reviewers") ||
		!strings.Contains(got, "passes when: Both reviewers approve the PR on GitHub.") {
		t.Errorf("track diagram (exit %d):\n%s", code, got)
	}
	got, code = run(runGraph, "check", "lab-bex.3")
	if code != 1 || !strings.Contains(got, "[gate-criteria] lab-bex.3.3") {
		t.Errorf("graph check of a track with a gate lacking criteria: exit %d\n%s", code, got)
	}
	got, code = run(runGraph, "check", "lab-bex.2")
	if code != 0 || !strings.Contains(got, "OK ") {
		t.Errorf("a compliant track: exit %d\n%s", code, got)
	}
	if _, code = run(runGraph, "check", "nope"); code != 2 {
		t.Errorf("an unknown bead: exit %d, want 2", code)
	}
}
