package reviewrun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagedContextBindsExactBytes(t *testing.T) {
	r := fixture(t)
	dir := t.TempDir()
	artifacts, err := stageContext(dir, []string{r.PromptFile})
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(r.PromptFile)
	_ = os.WriteFile(r.PromptFile, []byte("changed after staging"), 0600)
	copied, err := os.ReadFile(filepath.Join(dir, artifacts[0].Path))
	if err != nil || string(copied) != string(original) || artifacts[0].SHA256 != digest(original) {
		t.Fatalf("%+v %v", artifacts, err)
	}
	m := Manifest{Context: artifacts}
	hash, err := seal(filepath.Join(dir, "manifest.json"), m)
	if err != nil {
		t.Fatal(err)
	}
	m.Context[0].SHA256 = digest([]byte("changed"))
	b, _ := json.MarshalIndent(m, "", "  ")
	if digest(append(b, '\n')) == hash {
		t.Fatal("context is not bound to manifest")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(r.PromptFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := stageContext(t.TempDir(), []string{link}); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestPrivateGitRemergeLeavesSharedObjectsUntouched(t *testing.T) {
	r := fixture(t)
	git := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", r.Repo}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, b, err)
		}
		return string(b)
	}
	git(nil, "checkout", "-qb", "side", r.Base)
	_ = os.WriteFile(filepath.Join(r.Repo, "README.md"), []byte("side\n"), 0600)
	git(nil, "commit", "-qam", "side")
	side := strings.TrimSpace(git(nil, "rev-parse", "HEAD"))
	git(nil, "checkout", "--detach", "HEAD~1")
	// Start a second side with a conflicting edit and record its resolution.
	_ = os.WriteFile(filepath.Join(r.Repo, "README.md"), []byte("other\n"), 0600)
	git(nil, "commit", "-qam", "other")
	cmd := exec.Command("git", "-C", r.Repo, "merge", "--no-edit", side)
	if err := cmd.Run(); err == nil {
		t.Fatal("fixture merge must conflict")
	}
	_ = os.WriteFile(filepath.Join(r.Repo, "README.md"), []byte("resolved\n"), 0600)
	git(nil, "add", "README.md")
	git(nil, "commit", "-qm", "resolution")
	shared := strings.TrimSpace(git(nil, "rev-parse", "--git-path", "objects"))
	if !filepath.IsAbs(shared) {
		shared = filepath.Join(r.Repo, shared)
	}
	count := func() int {
		n := 0
		_ = filepath.WalkDir(shared, func(_ string, d os.DirEntry, e error) error {
			if e == nil && !d.IsDir() {
				n++
			}
			return e
		})
		return n
	}
	before := count()
	// Reproduce the sealed review's shared-object write denial, then leave the
	// shared store read-only while exercising the supported private route.
	if err := os.Chmod(shared, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shared, 0700) })
	if os.Geteuid() != 0 {
		denied := exec.Command("git", "-C", r.Repo, "show", "--remerge-diff", "HEAD")
		b, deniedErr := denied.CombinedOutput()
		if deniedErr == nil || !strings.Contains(string(b), "temporary object directory") {
			t.Fatalf("expected shared temporary object denial: %v %s", deniedErr, b)
		}
	}
	r.Dir = t.TempDir()
	r.PrivateGit = true
	env, scratch, err := privateGit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	out := git(env, "show", "--remerge-diff", "HEAD")
	if !strings.Contains(out, "resolved") || !strings.Contains(out, "<<<<<<<") {
		t.Fatalf("missing conflict resolution: %s", out)
	}
	if count() != before {
		t.Fatal("shared objects changed")
	}
	if scratch != filepath.Join(r.Dir, "git-scratch") {
		t.Fatal(scratch)
	}
}

func TestReviewRuntimeContextScenario(t *testing.T) {
	b, err := os.ReadFile("../../tests/evals/scenarios/review-runtime-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Schema string   `json:"schema"`
		Cases  []string `json:"cases"`
	}
	if err := json.Unmarshal(b, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.Schema != "review-runtime-context@v1" || len(scenario.Cases) != 2 {
		t.Fatal("invalid scenario")
	}
	for _, name := range scenario.Cases {
		switch name {
		case "context":
			t.Run(name, TestStagedContextBindsExactBytes)
		case "remerge":
			t.Run(name, TestPrivateGitRemergeLeavesSharedObjectsUntouched)
		default:
			t.Fatalf("unknown case %s", name)
		}
	}
}
