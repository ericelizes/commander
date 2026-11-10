package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowOwnRunRefusesMissingLauncherAndLegacy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("COMMANDER_HOME", root)
	t.Setenv("COMMANDER_RUN_ID", "")
	t.Setenv("COMMANDER_RUN_SESSION", "session")
	t.Setenv("COMMANDER_ROLE", "operator-trial")
	out, e := os.CreateTemp(t.TempDir(), "out")
	if e != nil {
		t.Fatal(e)
	}
	defer out.Close()
	errout, e := os.CreateTemp(t.TempDir(), "err")
	if e != nil {
		t.Fatal(e)
	}
	defer errout.Close()
	args := []string{"own-run", "--project", "trial", "--project-dir", root, "--task", "trial-1"}
	if code := runWorkflow(args, out, errout); code != 2 {
		t.Fatal(code)
	}
	t.Setenv("COMMANDER_RUN_ID", "7")
	if code := runWorkflow(args, out, errout); code != 1 {
		t.Fatal(code)
	}
	b, _ := os.ReadFile(errout.Name())
	if !strings.Contains(string(b), "legacy fallback disabled") {
		t.Fatal(string(b))
	}
	b, _ = os.ReadFile(out.Name())
	if len(b) != 0 {
		t.Fatal("disclosed unverified output")
	}
	if _, e := os.Stat(filepath.Join(root, "state.db")); !os.IsNotExist(e) {
		t.Fatal("opened legacy state")
	}
	if code := runWorkflow(append(args, "--run-id", "9"), out, errout); code != 2 {
		t.Fatal("allowed launcher override")
	}
}
