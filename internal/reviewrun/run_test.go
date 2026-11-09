package reviewrun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) Request {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "-q")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.test")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("before\n"), 0600)
	run("add", ".")
	run("commit", "-qm", "baseline")
	base := run("rev-parse", "HEAD")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("after\n"), 0600)
	run("commit", "-qam", "docs")
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	_ = os.WriteFile(prompt, []byte("review only committed documentation"), 0600)
	return Request{Repo: dir, Base: base, Task: "fixture-task", RunID: 7, Registry: t.TempDir(), Actor: "operator-fixture", PromptFile: prompt, Dir: filepath.Join(t.TempDir(), "run"), Command: []string{"/bin/sh", "-c", "test -r \"$COMMANDER_REVIEW_RUN_DIR/started.json\" && printf done"}}
}

func TestOriginalManifestExistsBeforeReviewerRuns(t *testing.T) {
	r := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Run(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exit != 0 || result.Execution != "complete" || result.PID <= 0 {
		t.Fatalf("%+v", result)
	}
	registered, err := ReadRecorded(r.Registry, r.RunID, result.ManifestHash)
	if err != nil || registered.Manifest.Task != r.Task || registered.Terminal.PID != result.PID {
		t.Fatalf("original execution registry: %+v %v", registered, err)
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if digest(b) != result.ManifestHash {
		t.Fatal("manifest digest mismatch")
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.RunID != 7 || m.Task != r.Task || m.Actor != r.Actor || m.Policy.Head == "" || m.Policy.Base != r.Base || m.PromptHash == "" {
		t.Fatalf("%+v", m)
	}
	_ = os.WriteFile(r.PromptFile, []byte("later mutation"), 0600)
	b, _ = os.ReadFile(filepath.Join(r.Dir, "prompt.txt"))
	if digest(b) != m.PromptHash {
		t.Fatal("original prompt was not preserved")
	}
	if _, err = Run(ctx, r); err == nil {
		t.Fatal("a second run overwrote original evidence")
	}
}

func TestFailedExecutionDoesNotClaimSourceDefect(t *testing.T) {
	r := fixture(t)
	r.Command = []string{"/bin/sh", "-c", "printf unavailable >&2; exit 42"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Run(ctx, r)
	if err == nil || result.Exit != 42 || result.Execution != "failed" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = os.Stat(filepath.Join(r.Dir, "terminal.json")); err != nil {
		t.Fatal("failed terminal evidence missing")
	}
}

func TestDeadlineStopsOwnedReviewAndRetainsEvidence(t *testing.T) {
	r := fixture(t)
	r.Command = []string{"/bin/sh", "-c", "sleep 30 & wait"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := Run(ctx, r)
	if err == nil || result.Execution != "blocked" {
		t.Fatalf("%+v %v", result, err)
	}
	if time.Since(result.Started) > 3*time.Second {
		t.Fatal("deadline did not bound child collection")
	}
}
