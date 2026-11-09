package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Without this wiring every /api/update call is a 503 and the button is dead.
func TestAPIOptions_WiresTheUpdater(t *testing.T) {
	d := newTestDaemon(t)
	repo := t.TempDir()
	old := repoResolver
	repoResolver = func() string { return repo }
	t.Cleanup(func() { repoResolver = old })

	u := d.apiOptions("tok").Update
	if u == nil {
		t.Fatal("Options.Update is nil: /api/update would answer 503")
	}
	if u.Repo != repo || u.Home != d.home {
		t.Errorf("repo %q home %q", u.Repo, u.Home)
	}
	for name, ok := range map[string]bool{
		"Check": u.Check != nil, "Prompt": u.Prompt != nil, "PromptAvailable": u.PromptAvailable != nil, "Export": u.Export != nil, "Build": u.Build != nil, "BinaryRevision": u.BinaryRevision != nil,
		"Install": u.Install != nil, "Rollback": u.Rollback != nil, "Restart": u.Restart != nil, "Busy": u.Busy != nil, "Branch": u.Branch != nil,
	} {
		if !ok {
			t.Errorf("seam %s is not wired", name)
		}
	}
}

func TestAPIOptions_NoCheckoutNoUpdater(t *testing.T) {
	d := newTestDaemon(t)
	old := repoResolver
	repoResolver = func() string { return "" }
	t.Cleanup(func() { repoResolver = old })
	if d.apiOptions("tok").Update != nil {
		t.Error("an updater without a checkout")
	}
}

func TestPromptCommand_Darwin(t *testing.T) {
	text := `Install "x"; do shell script "touch /tmp/pwned"` + "\n\\"
	argv, parse := promptCommand("darwin", "Install", text, nil)
	if argv[0] != "osascript" || argv[len(argv)-1] != text {
		t.Fatalf("argv = %q", argv)
	}
	script := strings.Join(argv[:len(argv)-2], "\n")
	if strings.Contains(script, "pwned") {
		t.Error("untrusted text is inside the script, not argv")
	}
	for _, want := range []string{`buttons {"Cancel", "Install"}`, `default button "Cancel"`, "giving up after 120"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	for out, want := range map[string]bool{
		"button returned:Install, gave up:false\n": true,
		"button returned:, gave up:true\n":         false, // timed out
		"button returned:Cancel, gave up:false\n":  false,
		"": false,
	} {
		if got := parse(out, nil); got != want {
			t.Errorf("parse(%q) = %v", out, got)
		}
	}
	if parse("button returned:Install, gave up:false", errNoGUI) {
		t.Error("an error counted as Install")
	}
}

func TestPromptCommand_Linux(t *testing.T) {
	has := func(tool string) func(string) (string, error) {
		return func(n string) (string, error) {
			if n == tool {
				return n, nil
			}
			return "", errNoGUI
		}
	}
	argv, parse := promptCommand("linux", "t", "body", has("zenity"))
	if argv[0] != "zenity" || !parse("", nil) || parse("", errNoGUI) {
		t.Errorf("zenity: %q", argv)
	}
	if argv, _ := promptCommand("linux", "t", "body", has("kdialog")); argv[0] != "kdialog" {
		t.Errorf("kdialog: %q", argv)
	}
	if argv, _ := promptCommand("linux", "t", "body", has("none")); argv != nil {
		t.Errorf("no tool: %q", argv)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// BLOCKER1: what the live checkout holds beyond the confirmed commit (an
// untracked init() file, a go.work, an uncommitted edit) is not in the export.
func TestExportCommit_BLOCKER1_OnlyTrackedFilesAtTheTarget(t *testing.T) {
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.go\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "one")
	target := gitIn(t, repo, "rev-parse", "HEAD")

	// the agent's additions in the live checkout
	os.WriteFile(filepath.Join(repo, "zz_init.go"), []byte("package main\nfunc init() { panic(1) }\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "ignored.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "go.work"), []byte("go 1.25\nuse .\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// edited\n"), 0o644)

	dir := filepath.Join(t.TempDir(), "export")
	cleanup, err := exportCommit(context.Background(), repo, target, dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if e.Name() != ".git" {
			names = append(names, e.Name())
		}
	}
	if strings.Join(names, ",") != ".gitignore,main.go" {
		t.Errorf("export holds %v, want only the tracked files", names)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "main.go")); string(got) != "package main\n" {
		t.Errorf("export main.go = %q, the uncommitted edit leaked in", got)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); head != target {
		t.Errorf("export HEAD %s, want %s", head, target)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("export is dirty: %s", st)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("export dir left behind")
	}
	if wl := gitIn(t, repo, "worktree", "list", "--porcelain"); strings.Count(wl, "worktree ") != 1 {
		t.Errorf("worktree record left behind:\n%s", wl)
	}
}

func TestExportCommit_RefusesANonFullHash(t *testing.T) {
	for _, bad := range []string{"main", "HEAD", "abc1234", "--detach", strings.Repeat("g", 40)} {
		if _, err := exportCommit(context.Background(), t.TempDir(), bad, filepath.Join(t.TempDir(), "x")); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBuildEnv_DropsWhatChangesTheBuild(t *testing.T) {
	env := buildEnv([]string{"PATH=/bin", "GOFLAGS=-tags=evil", "GOWORK=/x/go.work", "GOENV=/x/env", "CMDR_STAGE_DIR=/live", "HOME=/h"}, "/stage")
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nPATH=/bin\n", "\nHOME=/h\n", "\nGOFLAGS=\n", "\nGOWORK=off\n", "\nGOENV=off\n", "\nCMDR_STAGE_DIR=/stage\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env lacks %q: %v", want, env)
		}
	}
	for _, bad := range []string{"evil", "/x/go.work", "/x/env", "/live"} {
		if strings.Contains(joined, bad) {
			t.Errorf("env still has %q: %v", bad, env)
		}
	}
}
