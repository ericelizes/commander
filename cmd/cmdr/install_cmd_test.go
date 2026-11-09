package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type installEnv struct{ home, stage, inst string }

func newInstallEnv(t *testing.T) installEnv {
	root := t.TempDir()
	e := installEnv{filepath.Join(root, "home"), filepath.Join(root, "stage"), filepath.Join(root, "inst")}
	if err := os.MkdirAll(e.stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"cmdr", "cmdrd"} {
		if err := os.WriteFile(filepath.Join(e.stage, n), []byte(n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.stage, "VERSION"), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", e.home)
	t.Setenv("CMDR_INSTALL_DIR", e.inst)
	t.Setenv("COMMANDER_ROLE", "")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_REPO", repo)
	restartCmdrdFn = func() error { t.Fatal("cmdrd restarted"); return nil }
	t.Cleanup(func() { restartCmdrdFn = restartCmdrd })
	fakeProcs(t, nil)
	return e
}

func runInstallArgs(stdin string, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := runInstall(args, &out, &errs, strings.NewReader(stdin))
	return code, out.String(), errs.String()
}

func TestInstallCmd_InstallsAndLeavesCmdrdAlone(t *testing.T) {
	e := newInstallEnv(t)
	code, out, errs := runInstallArgs("", "--yes", "--from", e.stage)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got, _ := os.Readlink(filepath.Join(e.home, ".local", "bin", "cmdrd")); got != filepath.Join(e.inst, "cmdrd") {
		t.Errorf("link = %q", got)
	}
	if !strings.Contains(out, "none -> abc123") || !strings.Contains(out, "left running") {
		t.Errorf("output: %s", out)
	}
}

func TestInstallCmd_RefusesAgentRole(t *testing.T) {
	e := newInstallEnv(t)
	t.Setenv("COMMANDER_ROLE", "operator-x")
	if code, _, _ := runInstallArgs("", "--yes", "--from", e.stage); code == 0 {
		t.Fatal("agent install should be refused")
	}
	if _, err := os.Stat(e.inst); err == nil {
		t.Error("install dir written")
	}
}

func TestInstallCmd_NeedsConfirmation(t *testing.T) {
	e := newInstallEnv(t)
	if code, _, _ := runInstallArgs("", "--from", e.stage); code == 0 {
		t.Fatal("no confirmation should refuse")
	}
	if _, err := os.Stat(e.inst); err == nil {
		t.Error("install dir written without confirmation")
	}
	if code, _, errs := runInstallArgs("y\n", "--from", e.stage); code != 0 {
		t.Fatalf("confirmed install failed: %s", errs)
	}
}

func TestInstallCmd_RestartOnlyWithFlag(t *testing.T) {
	e := newInstallEnv(t)
	called := 0
	restartCmdrdFn = func() error { called++; return nil }
	if code, _, errs := runInstallArgs("", "--yes", "--restart-cmdrd", "--from", e.stage); code != 0 || called != 1 {
		t.Fatalf("code %d called %d: %s", code, called, errs)
	}
}

func TestInstallCmd_Rollback(t *testing.T) {
	e := newInstallEnv(t)
	runInstallArgs("", "--yes", "--from", e.stage)
	if err := os.WriteFile(filepath.Join(e.stage, "cmdr"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.stage, "VERSION"), []byte("def456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runInstallArgs("", "--yes", "--from", e.stage)
	if code, _, errs := runInstallArgs("", "--yes", "--rollback"); code != 0 {
		t.Fatalf("rollback: %s", errs)
	}
	if b, _ := os.ReadFile(filepath.Join(e.inst, "cmdr")); string(b) != "cmdr" {
		t.Errorf("cmdr = %q", b)
	}
}

func TestInstallCmd_RestartOnlyTouchesNothingElse(t *testing.T) {
	e := newInstallEnv(t)
	runInstallArgs("", "--yes", "--from", e.stage)
	called := 0
	restartCmdrdFn = func() error { called++; return nil }
	if code, _, errs := runInstallArgs("", "--yes", "--restart-only"); code != 0 || called != 1 {
		t.Fatalf("code %d called %d: %s", code, called, errs)
	}
	if _, err := os.Stat(filepath.Join(e.inst, "cmdr.prev")); err == nil {
		t.Error("restart-only changed .prev")
	}
	if code, _, _ := runInstallArgs("", "--restart-only"); code == 0 || called != 1 {
		t.Error("restart-only without confirmation should refuse")
	}
}

func TestInstallCmd_RestartOnlyRefusedForAgent(t *testing.T) {
	newInstallEnv(t)
	t.Setenv("COMMANDER_ROLE", "manager-x")
	restartCmdrdFn = func() error { t.Fatal("restarted"); return nil }
	if code, _, _ := runInstallArgs("", "--yes", "--restart-only"); code == 0 {
		t.Fatal("agent restart should be refused")
	}
}

func TestInstallCmd_RestartCmdrdOnSameRevisionKeepsPrev(t *testing.T) {
	e := newInstallEnv(t)
	runInstallArgs("", "--yes", "--from", e.stage)
	restartCmdrdFn = func() error { return nil }
	for range 2 {
		if code, _, errs := runInstallArgs("", "--yes", "--restart-cmdrd", "--from", e.stage); code != 0 {
			t.Fatal(errs)
		}
	}
	if _, err := os.Stat(filepath.Join(e.inst, "cmdr.prev")); err == nil {
		t.Error("an unchanged install created .prev")
	}
}
