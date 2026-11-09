package reviewpolicy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRiskOverridesSmallScope(t *testing.T) {
	for _, path := range []string{"internal/auth/login.go", "db/migrations/002.sql", "internal/data/users.go", "internal/runtime/start.go", "infra/main.tf", "internal/backup/store.go", "internal/guard/check.go", "ansible/tasks/main.yml", ".github/workflows/release.yml"} {
		r := Select([]File{{Path: path, Added: 1}}, "+return true", false)
		if r.Depth != "full" {
			t.Errorf("%s selected %s", path, r.Depth)
		}
	}
	for _, patch := range []string{"+authorize(user)", "+db.Exec(\"DROP TABLE users\")", "+os.RemoveAll(path)", "+cancel()"} {
		r := Select([]File{{Path: "internal/helper.go", Added: 1}}, patch, false)
		if r.Depth != "full" {
			t.Errorf("sensitive behavior %q selected %s", patch, r.Depth)
		}
	}
}

func TestCommittedScopeBinding(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	path := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(path, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	os.WriteFile(path, []byte("second\n"), 0600)
	git("add", ".")
	git("commit", "-qm", "head")
	head := git("rev-parse", "HEAD")
	r, e := Inspect(context.Background(), dir, base, "HEAD", false)
	if e != nil {
		t.Fatal(e)
	}
	if r.Head != head || r.Base != base || len(r.ScopeHash) != 64 || r.Depth != "focused" || r.Lines != 2 {
		t.Fatalf("%+v", r)
	}
	os.WriteFile(path, []byte("uncommitted secret never returned\n"), 0600)
	again, e := Inspect(context.Background(), dir, base, "HEAD", false)
	if e != nil {
		t.Fatal(e)
	}
	if again.ScopeHash != r.ScopeHash {
		t.Fatal("working tree altered committed scope")
	}
	override, e := Inspect(context.Background(), dir, base, head, true)
	if e != nil {
		t.Fatal(e)
	}
	if override.Depth != "full" || override.ScopeHash != r.ScopeHash {
		t.Fatal(override)
	}
	if _, e = Inspect(context.Background(), dir, "--help", "HEAD", false); e == nil {
		t.Fatal("accepted option as ref")
	}
}

func TestFocusedScopeAndBroadBinaryEscalation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []File
		want  string
	}{
		{"small presentation", []File{{Path: "internal/display.go", Added: 3}}, "focused"},
		{"binary", []File{{Path: "assets/picture.png", Binary: true}}, "full"},
		{"broad", []File{{Path: "a", Added: 1}, {Path: "b", Added: 1}, {Path: "c", Added: 1}, {Path: "d", Added: 1}, {Path: "e", Added: 1}, {Path: "f", Added: 1}, {Path: "g", Added: 1}}, "full"},
		{"empty", nil, "full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := Select(tc.files, "+render()", false); r.Depth != tc.want {
				t.Fatal(r)
			}
		})
	}
}
