package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/reviewpolicy"
)

func TestWorkflowReviewPolicyCLI(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %s %v", args, b, e)
		}
	}
	git("init", "-q")
	file := filepath.Join(dir, "guide.md")
	os.WriteFile(file, []byte("one\n"), 0600)
	git("add", ".")
	git("commit", "-qm", "base")
	os.WriteFile(file, []byte("two\n"), 0600)
	git("add", ".")
	git("commit", "-qm", "head")
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "err")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	if code := runWorkflow([]string{"review-policy", "--repo", dir, "--base", "HEAD~1"}, out, stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out.Seek(0, 0)
	var result reviewpolicy.Result
	if err = json.NewDecoder(out).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Depth != "focused" || len(result.Base) != 40 || len(result.Head) != 40 || len(result.ScopeHash) != 64 || len(result.Files) != 1 {
		t.Fatalf("%+v", result)
	}
	if code := runWorkflow([]string{"review-policy", "--repo", dir, "--base", "--help"}, out, stderr); code == 0 {
		t.Fatal("option accepted as base")
	}
}
