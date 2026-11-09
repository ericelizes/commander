package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// AllowMigrateEnv lets any build migrate the live db (a deliberate override).
const AllowMigrateEnv = "COMMANDER_ALLOW_MIGRATE"

// Replaced in tests.
var (
	executablePath = os.Executable
	underTest      = testing.Testing
)

// installedBinaries are the ~/.local/bin links that resolve to the installed
// bin/cmdr and bin/cmdrd.
var installedBinaries = []string{"cmdr", "cmdrd", "commander"}

// checkMigrateAllowed refuses to migrate the live state db (~/.commander/
// state.db) unless this process is an installed binary or
// COMMANDER_ALLOW_MIGRATE=1. A worktree or /tmp build that migrates the live
// db leaves a schema the installed binaries cannot open. Other dbs (temp
// dirs, private COMMANDER_HOME) migrate freely. Test binaries never get to
// migrate the live db, override or not.
func checkMigrateAllowed(path string, current int) error {
	if !isLiveDB(path) {
		return nil
	}
	if underTest() {
		return fmt.Errorf("store: tests must not open the live state db %s", path)
	}
	if os.Getenv(AllowMigrateEnv) == "1" || isInstalledBinary() {
		return nil
	}
	return fmt.Errorf(
		"store: refusing to migrate the live db from a non-installed build (schema %d is older than this build's); install first or set %s=1",
		current, AllowMigrateEnv,
	)
}

// isLiveDB reports whether path is the real home's state.db.
func isLiveDB(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || path == "" || path == ":memory:" {
		return false
	}
	return resolve(path) == resolve(filepath.Join(home, ".commander", "state.db"))
}

// isInstalledBinary reports whether the running executable is one of the
// installed commander binaries.
func isInstalledBinary() bool {
	exe, err := executablePath()
	if err != nil {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	exe = resolve(exe)
	for _, name := range installedBinaries {
		if resolve(filepath.Join(home, ".local", "bin", name)) == exe {
			return true
		}
	}
	return false
}

// resolve follows symlinks; a path that does not exist yet resolves through
// its directory.
func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return p
}
