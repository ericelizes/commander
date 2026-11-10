package diskguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, by map[string]Usage) {
	t.Helper()
	orig := Stat
	t.Cleanup(func() { Stat = orig })
	Stat = func(p string) (Usage, error) {
		u, ok := by[p]
		if !ok {
			return Usage{}, errors.New("no such disk")
		}
		u.Path = p
		return u, nil
	}
}

func TestStatReadsARealDisk(t *testing.T) {
	u, err := Stat(t.TempDir())
	if err != nil || u.Total == 0 || u.UsedPct < 0 || u.UsedPct > 100 || u.Mount == "" || u.Dev == 0 {
		t.Fatalf("Stat = %+v, %v", u, err)
	}
}

func TestCheckSpawnRefusesAtTheLineAndNamesTheDisk(t *testing.T) {
	fake(t, map[string]Usage{
		"/p":    {Mount: "/", Dev: 1, UsedPct: 95, Free: 3 << 30},
		"/p2":   {Mount: "/", Dev: 1, UsedPct: 95},
		"/home": {Mount: "/home", Dev: 2, UsedPct: 50},
	})
	err := CheckSpawn("/home", "/p", "/missing")
	var r *RefuseError
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "disk / is 95% full (3.0 GB free)") {
		t.Fatalf("CheckSpawn = %v", err)
	}
	if got := Disks("/p", "/p2", "/home", "/missing"); len(got) != 2 || got[0].Mount != "/" {
		t.Fatalf("Disks deduped by device, fullest first: %+v", got)
	}
	if err := CheckSpawn("/home", "/missing"); err != nil {
		t.Fatalf("a 50%% disk and an unreadable one refused: %v", err)
	}
	t.Setenv("COMMANDER_DISK_REFUSE_PCT", "40")
	if CheckSpawn("/home") == nil {
		t.Fatal("COMMANDER_DISK_REFUSE_PCT=40 did not refuse a 50% disk")
	}
	t.Setenv("COMMANDER_DISK_REFUSE_PCT", "nonsense")
	if RefusePct() != DefaultRefusePct || WarnPct() != DefaultWarnPct {
		t.Fatalf("bad override not ignored: %v %v", RefusePct(), WarnPct())
	}
}

func TestRunTmpDirIsShortAndUnderTheRoot(t *testing.T) {
	home := "/srv/example/.commander"
	d := RunTmpDir(home, "cmdr-a-very-long-project-name-feature-branch-TEAM-12.3.4")
	if filepath.Dir(d) != TmpRoot(home) || len(filepath.Base(d)) != 13 || !strings.HasPrefix(filepath.Base(d), "op-") {
		t.Fatalf("RunTmpDir = %s", d)
	}
	if RunTmpDir(home, "a") == RunTmpDir(home, "b") {
		t.Fatal("two sessions share a TMPDIR")
	}
}

func TestSweepTmpRemovesOnlyOldLeftoversUnderTheRoot(t *testing.T) {
	home := t.TempDir()
	root := TmpRoot(home)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "precious"), []byte("x"), 0o600)
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	mk := func(name string, at time.Time) string {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Join(p, "ro", "deep"), 0o755)
		os.WriteFile(filepath.Join(p, "ro", "deep", "f"), []byte("x"), 0o400)
		os.Chmod(filepath.Join(p, "ro", "deep"), 0o500)
		os.Chmod(filepath.Join(p, "ro"), 0o500)
		os.Chtimes(p, at, at)
		return p
	}
	leftover := mk(RunTmpName("cmdr-p-gone"), old)
	live := mk(RunTmpName("cmdr-p-live"), old)
	fresh := mk(RunTmpName("cmdr-p-fresh"), now)
	notOurs := mk("op-leftover", old)               // op- prefix, not our exact shape
	upper := mk("op-ABCDEF0123", old)               // hex in the wrong case
	agentFile := filepath.Join(root, "ci-watch.sh") // an agent's own file
	os.WriteFile(agentFile, []byte("x"), 0o600)
	os.Chtimes(agentFile, old, old)
	link := filepath.Join(root, RunTmpName("cmdr-p-link")) // op-shaped link
	os.Symlink(outside, link)
	os.Chtimes(link, old, old) // a link's own mtime is not settable everywhere; it stays fresh or old, never followed
	t.Cleanup(func() {
		filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error { os.Chmod(p, 0o755); return nil })
	})

	removed, err := SweepTmp(root, map[string]bool{RunTmpName("cmdr-p-live"): true}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("old leftover kept (removed %v)", removed)
	}
	for _, p := range []string{live, fresh, notOurs, upper, agentFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: live runs, fresh dirs and anything not op-<10 hex> stay", p)
		}
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("op-shaped link removed: only real directories go")
	}
	if len(removed) != 1 || removed[0] != filepath.Base(leftover) {
		t.Fatalf("removed %v, want only %s", removed, filepath.Base(leftover))
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("sweep reached through a link out of the root: %v", err)
	}
	// Never call SweepTmp itself on a real system path: checkRoot is the guard.
	for _, bad := range []string{"/tmp", "/tmp/", "/", "", outside, "relative/tmp", "/private/tmp", "/var/tmp", "/tmp/tmp", root + "/", os.TempDir(), filepath.Join(os.TempDir(), "tmp")} {
		if err := checkRoot(bad); err == nil {
			t.Fatalf("checkRoot(%q) accepted a root that is not <home>/tmp", bad)
		}
	}
	if err := checkRoot(root); err != nil {
		t.Fatalf("checkRoot refused a commander home root: %v", err)
	}
	// Links: only the resolution decides these (the text is a clean <dir>/tmp).
	links := t.TempDir()
	toSys := filepath.Join(links, "sys")
	os.Symlink(os.TempDir(), toSys) // <link to the system temp dir>/tmp
	os.MkdirAll(filepath.Join(links, "h"), 0o755)
	tmpLink := filepath.Join(links, "h", "tmp") // a root that is itself a link
	os.Symlink(outside, tmpLink)
	userHome := filepath.Join(links, "userHome")
	os.MkdirAll(userHome, 0o755)
	t.Setenv("HOME", userHome)
	for _, bad := range []string{filepath.Join(toSys, "tmp"), tmpLink, filepath.Join(userHome, "tmp")} {
		if err := checkRoot(bad); err == nil {
			t.Fatalf("checkRoot(%q) accepted a linked or userHome root", bad)
		}
	}
	if got, err := SweepTmp(filepath.Join(t.TempDir(), "tmp"), nil, 0, now); err != nil || got != nil {
		t.Fatalf("missing root: %v %v", got, err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[uint64]string{12: "12 B", 2048: "2.0 KB", 3 << 30: "3.0 GB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %s, want %s", n, got, want)
		}
	}
}
