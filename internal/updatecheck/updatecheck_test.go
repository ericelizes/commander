package updatecheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, repo, subject, body string) string {
	t.Helper()
	msg := subject
	if body != "" {
		msg += "\n\n" + body
	}
	run(t, repo, "commit", "--allow-empty", "-m", msg)
	return run(t, repo, "rev-parse", "HEAD")
}

func newRepo(t *testing.T) string {
	repo := t.TempDir()
	run(t, repo, "init", "-b", "main")
	return repo
}

func installDir(t *testing.T, rev string) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(rev+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCheck_BehindListsCommitsWithHonestMarkers(t *testing.T) {
	repo := newRepo(t)
	base := commit(t, repo, "base", "")
	a := commit(t, repo, "feat: a", "")
	b := commit(t, repo, "fix: b", "Reviewed-by: Rae Reviewer")
	c := commit(t, repo, "feat: c", "")

	beads := t.TempDir()
	issues := `{"id":"commander-x.1","comments":[{"text":"review of ` + a[:10] + `\nfindings none\nCLEAN"}]}
{"id":"commander-x.2","comments":[{"text":"review of ` + c[:10] + `\nNOT CLEAN\nmajor finding"}]}
`
	os.WriteFile(filepath.Join(beads, "issues.jsonl"), []byte(issues), 0o644)

	st := Check(context.Background(), Options{Repo: repo, InstallDir: installDir(t, base), BeadsDir: beads, Running: base})
	if st.Error != "" {
		t.Fatal(st.Error)
	}
	if st.Behind != 3 || st.UpToDate || st.Main != c || len(st.Commits) != 3 {
		t.Fatalf("status = %+v", st)
	}
	want := map[string]string{a: Reviewed, b: Reviewed, c: NotConfirmed}
	for _, cm := range st.Commits {
		if cm.Review != want[cm.Hash] {
			t.Errorf("%s (%s): review %q, want %q", cm.Hash[:7], cm.Subject, cm.Review, want[cm.Hash])
		}
	}
	if st.Commits[0].Hash != c {
		t.Errorf("newest first expected, got %s", st.Commits[0].Subject)
	}
	if got := st.Commits[1].Evidence; got != "Reviewed-by: Rae Reviewer" {
		t.Errorf("trailer evidence = %q", got)
	}
	if got := st.Commits[2].Evidence; got != "bead commander-x.1" {
		t.Errorf("bead evidence = %q", got)
	}
}

func TestCheck_UpToDateAndDirtySuffix(t *testing.T) {
	repo := newRepo(t)
	head := commit(t, repo, "base", "")
	st := Check(context.Background(), Options{Repo: repo, InstallDir: installDir(t, head+"-dirty")})
	if st.Error != "" || !st.UpToDate || st.Behind != 0 || len(st.Commits) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestCheck_CapsAt50(t *testing.T) {
	repo := newRepo(t)
	base := commit(t, repo, "base", "")
	for i := 0; i < MaxCommits+5; i++ {
		commit(t, repo, "c", "")
	}
	st := Check(context.Background(), Options{Repo: repo, InstallDir: installDir(t, base)})
	if st.Behind != MaxCommits+5 || len(st.Commits) != MaxCommits || !st.Truncated {
		t.Fatalf("behind %d, listed %d, truncated %v", st.Behind, len(st.Commits), st.Truncated)
	}
}

func TestCheck_ReportsErrorsWithoutPanicking(t *testing.T) {
	repo := newRepo(t)
	commit(t, repo, "base", "")
	for name, o := range map[string]Options{
		"no repo":        {InstallDir: t.TempDir()},
		"no stamp":       {Repo: repo, InstallDir: t.TempDir(), Running: ""},
		"unknown commit": {Repo: repo, InstallDir: installDir(t, strings.Repeat("a", 40))},
	} {
		if st := Check(context.Background(), o); st.Error == "" {
			// "no stamp" falls back to the test binary's own build info, which may carry a stamp
			if name != "no stamp" {
				t.Errorf("%s: want an error, got %+v", name, st)
			}
		}
	}
}

func TestCheck_RemoteIsHintOnly(t *testing.T) {
	repo := newRepo(t)
	base := commit(t, repo, "base", "")
	remote := strings.Repeat("b", 40)
	git := func(ctx context.Context, dir string, args ...string) (string, error) {
		switch args[0] {
		case "remote":
			return "origin\n", nil
		case "ls-remote":
			return remote + "\trefs/heads/main\n", nil
		}
		return ExecGit(ctx, dir, args...)
	}
	st := Check(context.Background(), Options{Repo: repo, InstallDir: installDir(t, base), Git: git})
	if st.Error != "" || st.Remote != remote || !st.RemoteAhead || !st.UpToDate {
		t.Fatalf("status = %+v", st)
	}
}

func TestBeadReviews_RuleDetails(t *testing.T) {
	dir := t.TempDir()
	issues := `{"id":"r1","comments":[{"text":"commit abcdef1234 looks fine\nverdict: CLEAN"}]}
{"id":"r2","comments":[{"text":"abcdef9999 is CLEAN-ish but not a verdict line"}]}
{"id":"r3","comments":[{"text":"abc12 short\nCLEAN"}]}
`
	os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(issues), 0o644)
	got := beadReviews(dir)
	if got["abcdef1234"] != "r1" || len(got) != 1 {
		t.Fatalf("beadReviews = %v", got)
	}
}
