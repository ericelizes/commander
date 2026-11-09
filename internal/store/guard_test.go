package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHome points HOME at a temp dir with ~/.commander and ~/.local/bin.
func fakeHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(AllowMigrateEnv, "")
	for _, d := range []string{".commander", ".local/bin"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	underTest = func() bool { return false }
	t.Cleanup(func() { underTest = func() bool { return true } })
	return home
}

func setExe(t *testing.T, p string) {
	t.Helper()
	old := executablePath
	executablePath = func() (string, error) { return p, nil }
	t.Cleanup(func() { executablePath = old })
}

// seedOld creates a db whose schema is older than this build (version 0).
func seedOld(t *testing.T, path string) {
	t.Helper()
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE TABLE legacy (x)"); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDBRefusesNonInstalledMigration(t *testing.T) {
	home := fakeHome(t)
	live := filepath.Join(home, ".commander", "state.db")
	seedOld(t, live)
	setExe(t, filepath.Join(t.TempDir(), "cmdr"))
	_, err := Open(live)
	if err == nil || !strings.Contains(err.Error(), "refusing to migrate the live db from a non-installed build") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestLiveDBInstalledBinaryMigrates(t *testing.T) {
	home := fakeHome(t)
	live := filepath.Join(home, ".commander", "state.db")
	seedOld(t, live)
	real := filepath.Join(home, "repo-bin-cmdr")
	if err := os.WriteFile(real, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, ".local", "bin", "cmdr")); err != nil {
		t.Fatal(err)
	}
	setExe(t, real)
	s, err := Open(live)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestLiveDBOverrideEnvMigrates(t *testing.T) {
	home := fakeHome(t)
	live := filepath.Join(home, ".commander", "state.db")
	seedOld(t, live)
	setExe(t, filepath.Join(t.TempDir(), "cmdr"))
	t.Setenv(AllowMigrateEnv, "1")
	s, err := Open(live)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestLiveDBUpToDateOpensFromAnyBuild(t *testing.T) {
	home := fakeHome(t)
	live := filepath.Join(home, ".commander", "state.db")
	t.Setenv(AllowMigrateEnv, "1")
	s, err := Open(live)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	t.Setenv(AllowMigrateEnv, "")
	setExe(t, filepath.Join(t.TempDir(), "cmdr"))
	s, err = Open(live)
	if err != nil {
		t.Fatalf("compatible schema must open without migrating: %v", err)
	}
	s.Close()
}

func TestOldDBRefusedNeverBumped(t *testing.T) {
	home := fakeHome(t)
	live := filepath.Join(home, ".commander", "state.db")
	seedOld(t, live)
	setExe(t, filepath.Join(t.TempDir(), "cmdr"))
	if _, err := Open(live); err == nil {
		t.Fatal("want refusal")
	}
	t.Setenv(AllowMigrateEnv, "")
	// Peek at the version through a raw handle.
	raw, err := openRaw(live)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var v int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 0 {
		t.Fatalf("version = %d, %v; want 0", v, err)
	}
}

func TestTestBinariesNeverOpenLiveDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".commander"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowMigrateEnv, "1")
	_, err := Open(filepath.Join(home, ".commander", "state.db"))
	if err == nil || !strings.Contains(err.Error(), "tests must not open the live state db") {
		t.Fatalf("got %v", err)
	}
}

func openRaw(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }
