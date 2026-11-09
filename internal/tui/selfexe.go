package tui

import (
	"errors"
	"os"
	"testing"
)

// selfExe is the path of the running cmdr binary, for re-exec'ing a cmdr verb.
// Inside a test binary os.Executable() is the test binary itself, so exec'ing it
// re-runs the whole suite, whose tests exec it again: an unbounded fork storm
// (incident 2026-10-22). It refuses under go test instead. A real cmdr started
// by a test (the eval harness) is not a test binary and still works.
func selfExe() (string, error) {
	if testing.Testing() {
		return "", errors.New("refusing to exec the test binary as cmdr")
	}
	return os.Executable()
}
