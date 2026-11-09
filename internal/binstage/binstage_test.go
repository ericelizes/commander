package binstage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

type fixture struct {
	stage, install, local, repo string
}

func newFixture(t *testing.T) fixture {
	root := t.TempDir()
	f := fixture{filepath.Join(root, "stage"), filepath.Join(root, "inst"), filepath.Join(root, "home", ".local", "bin"), filepath.Join(root, "repo")}
	for _, d := range []string{f.stage, f.repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) stageBuild(t *testing.T, rev string) {
	for _, name := range Binaries {
		if err := os.WriteFile(filepath.Join(f.stage, name), []byte(name+" "+rev), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.stage, "VERSION"), []byte(rev+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) opts(out *bytes.Buffer) Options {
	return Options{StageDir: f.stage, InstallDir: f.install, LocalBin: f.local, Repo: f.repo, Out: out}
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStagingLeavesInstalledUntouched(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	if err := Install(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	f.stageBuild(t, "bbb") // a new build only rewrites the stage
	if got := read(t, filepath.Join(f.install, "cmdr")); got != "cmdr aaa" {
		t.Fatalf("installed changed by a build: %q", got)
	}
	if got := Revision(f.install); got != "aaa" {
		t.Fatalf("revision = %q", got)
	}
}

func TestInstallCopiesRealFilesAndRelinks(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	// the old repo-bin symlink and a foreign regular file
	if err := os.MkdirAll(f.local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/repo/bin/cmdr", filepath.Join(f.local, "cmdr")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.local, "commander"), []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(f.install, "cmdrd"))
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("cmdrd should be an executable real file: %v %v", fi, err)
	}
	for link, want := range map[string]string{"cmdr": "cmdr", "cmdrd": "cmdrd"} {
		got, err := os.Readlink(filepath.Join(f.local, link))
		if err != nil || got != filepath.Join(f.install, want) {
			t.Errorf("%s -> %q (%v)", link, got, err)
		}
	}
	if read(t, filepath.Join(f.local, "commander")) != "mine" {
		t.Error("a non-symlink was replaced")
	}
	if RepoHint(filepath.Join(f.install, "cmdr")) != f.repo {
		t.Error("repo hint not recorded")
	}
	if _, err := os.Stat(filepath.Join(f.install, "cmdr.new")); err == nil {
		t.Error("temp file left behind")
	}
}

func TestInstallKeepsPrevAndRollbackSwaps(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	if err := Install(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	f.stageBuild(t, "bbb")
	out.Reset()
	if err := Install(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("revision: aaa -> bbb")) {
		t.Errorf("revision change not printed: %s", out.String())
	}
	if got := read(t, filepath.Join(f.install, "cmdr.prev")); got != "cmdr aaa" {
		t.Fatalf("prev = %q", got)
	}
	if err := Rollback(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(f.install, "cmdrd")); got != "cmdrd aaa" {
		t.Fatalf("after rollback cmdrd = %q", got)
	}
	if Revision(f.install) != "aaa" {
		t.Fatalf("revision after rollback = %q", Revision(f.install))
	}
	if err := Rollback(f.opts(&out)); err != nil || read(t, filepath.Join(f.install, "cmdr")) != "cmdr bbb" {
		t.Fatalf("second rollback should go forward: %v", err)
	}
}

func TestRollbackWithoutPrevFails(t *testing.T) {
	f := newFixture(t)
	f.stageBuild(t, "aaa")
	var out bytes.Buffer
	if err := Install(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(f.opts(&out)); err == nil {
		t.Fatal("rollback with no previous should fail")
	}
}

func TestInstallNeedsStagedBuild(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	if err := Install(f.opts(&out)); err == nil {
		t.Fatal("expected error with nothing staged")
	}
	if _, err := os.Stat(f.install); err == nil {
		t.Error("install dir created despite failure")
	}
}

func TestInstallDirOverrides(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := InstallDir(env(map[string]string{"CMDR_INSTALL_DIR": "/a", "COMMANDER_HOME": "/b"}), "/h"); got != "/a" {
		t.Errorf("got %s", got)
	}
	if got := InstallDir(env(map[string]string{"COMMANDER_HOME": "/b"}), "/h"); got != "/b/bin" {
		t.Errorf("got %s", got)
	}
	if got := InstallDir(env(nil), "/h"); got != "/h/.commander/bin" {
		t.Errorf("got %s", got)
	}
}

func TestInstallSameRevisionKeepsPrev(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	Install(f.opts(&out))
	f.stageBuild(t, "bbb")
	Install(f.opts(&out))
	out.Reset()
	if err := Install(f.opts(&out)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(f.install, "cmdr.prev")); got != "cmdr aaa" {
		t.Fatalf("prev lost its rollback target: %q", got)
	}
	if Revision2(f.install) != "aaa" {
		t.Fatalf("VERSION.prev = %q", Revision2(f.install))
	}
}

func TestInstallAllOrNothing(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	Install(f.opts(&out))
	f.stageBuild(t, "bbb")
	if err := os.Remove(filepath.Join(f.stage, "cmdrd")); err != nil { // cmdrd copy fails
		t.Fatal(err)
	}
	if err := Install(f.opts(&out)); err == nil {
		t.Fatal("expected failure")
	}
	for _, name := range []string{"cmdr", "cmdrd"} {
		if got := read(t, filepath.Join(f.install, name)); got != name+" aaa" {
			t.Errorf("%s mixed: %q", name, got)
		}
	}
	if Revision(f.install) != "aaa" {
		t.Error("VERSION changed")
	}
}

func TestInstallNeverLeavesDestAbsent(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	Install(f.opts(&out))
	dest := filepath.Join(f.install, "cmdr")
	if err := keepPrev(dest); err != nil {
		t.Fatal(err)
	}
	if read(t, dest) != "cmdr aaa" || read(t, dest+".prev") != "cmdr aaa" {
		t.Fatal("dest must stay while .prev is made")
	}
}

func TestValidateRepo(t *testing.T) {
	f := newFixture(t)
	if err := ValidateRepo(f.repo); err != nil {
		t.Fatal(err)
	}
	if ValidateRepo(filepath.Join(f.repo, "missing")) == nil {
		t.Error("missing dir accepted")
	}
	noMod := t.TempDir()
	if ValidateRepo(noMod) == nil {
		t.Error("dir without go.mod accepted")
	}
	wt := t.TempDir()
	os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n"), 0o644)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	if ValidateRepo(wt) == nil {
		t.Error("git worktree accepted")
	}
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	o := f.opts(&out)
	o.Repo = wt
	if Install(o) == nil {
		t.Error("install with a worktree repo should fail")
	}
	if _, err := os.Stat(f.install); err == nil {
		t.Error("install dir written before validation")
	}
}

func TestRepoHintIgnoresMissingCheckout(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	f.stageBuild(t, "aaa")
	Install(f.opts(&out))
	exe := filepath.Join(f.install, "cmdr")
	if RepoHint(exe) != f.repo {
		t.Fatal("hint missing")
	}
	os.RemoveAll(f.repo)
	if RepoHint(exe) != "" {
		t.Error("hint for a removed checkout should be ignored")
	}
}
