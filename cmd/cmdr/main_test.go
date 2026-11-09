package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/store"
)

func withTempOutputFiles(t *testing.T) (*os.File, *os.File, func() (string, string)) {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatalf("create stderr: %v", err)
	}
	return stdout, stderr, func() (string, string) {
		t.Helper()
		_ = stdout.Close()
		_ = stderr.Close()
		out, _ := os.ReadFile(filepath.Join(dir, "stdout"))
		errOut, _ := os.ReadFile(filepath.Join(dir, "stderr"))
		return string(out), string(errOut)
	}
}

func TestRun_CommanderTarget(t *testing.T) {
	t.Setenv("TMUX", "1")
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(home, layout.CommanderSessionFileName), []byte("ses_test123\n"), 0o644); err != nil {
		t.Fatalf("write session id: %v", err)
	}
	stdout, stderr, readOutput := withTempOutputFiles(t)
	mr := &layout.MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, []byte("can't find session: commander"), errors.New("exit status 1")
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		case len(args) >= 1 && args[0] == "switch-client":
			return nil, nil, nil
		default:
			return nil, nil, nil
		}
	})
	orig := newDirectorFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	t.Cleanup(func() { newDirectorFn = orig })

	exitCode := run(nil, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !containsCall(mr.CallStrings(), "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+workspaceRoot+" opencode --session ses_test123") {
		t.Fatalf("expected commander launch, calls=%v", mr.CallStrings())
	}
}

func TestRun_ProjectTarget(t *testing.T) {
	t.Setenv("TMUX", "1")
	stdout, stderr, readOutput := withTempOutputFiles(t)
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".commander", "manager.session-id"), []byte("ses_manager\n"), 0o644); err != nil {
		t.Fatalf("write session id: %v", err)
	}

	mr := &layout.MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte(""), nil, nil
		case len(args) >= 1 && args[0] == "new-window":
			return []byte("%11\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%11" {
				return []byte("%12\n"), nil, nil
			}
			return []byte("%13\n"), nil, nil
		case len(args) >= 1 && args[0] == "select-window":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "switch-client":
			return nil, nil, nil
		default:
			return nil, nil, nil
		}
	})

	origDirector := newDirectorFn
	origProject := resolveProjectRecFn
	origEnsure := ensureProjectReadyFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	resolveProjectRecFn = func(name string) (*store.Project, error) {
		return &store.Project{Name: name, Type: "code", Path: tmp}, nil
	}
	ensureProjectReadyFn = func(project *store.Project) error { return nil }
	t.Cleanup(func() {
		newDirectorFn = origDirector
		resolveProjectRecFn = origProject
		ensureProjectReadyFn = origEnsure
	})

	exitCode := run([]string{"-p", "frontend"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !containsCall(mr.CallStrings(), "tmux new-window -P -F #{pane_id} -t commander: -n manager (frontend) -c "+tmp+" opencode --session ses_manager") {
		t.Fatalf("expected manager launch, calls=%v", mr.CallStrings())
	}
}

func TestRun_ProjectTarget_RepairsManagerReadinessBeforeLaunch(t *testing.T) {
	t.Setenv("TMUX", "1")
	stdout, stderr, readOutput := withTempOutputFiles(t)
	tmp := t.TempDir()

	mr := &layout.MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte(""), nil, nil
		case len(args) >= 1 && args[0] == "new-window":
			return []byte("%11\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%11" {
				return []byte("%12\n"), nil, nil
			}
			return []byte("%13\n"), nil, nil
		case len(args) >= 1 && args[0] == "select-window":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "switch-client":
			return nil, nil, nil
		default:
			return nil, nil, nil
		}
	})

	origDirector := newDirectorFn
	origProject := resolveProjectRecFn
	origEnsure := ensureProjectReadyFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	resolveProjectRecFn = func(name string) (*store.Project, error) {
		return &store.Project{Name: name, Type: "code", Path: tmp}, nil
	}
	ensureProjectReadyFn = func(project *store.Project) error {
		if err := os.MkdirAll(filepath.Join(project.Path, ".commander"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(project.Path, ".commander", "manager.session-id"), []byte("ses_repaired\n"), 0o644)
	}
	t.Cleanup(func() {
		newDirectorFn = origDirector
		resolveProjectRecFn = origProject
		ensureProjectReadyFn = origEnsure
	})

	exitCode := run([]string{"-p", "manifest"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !containsCall(mr.CallStrings(), "tmux new-window -P -F #{pane_id} -t commander: -n manager (manifest) -c "+tmp+" opencode --session ses_repaired") {
		t.Fatalf("expected repaired manager launch, calls=%v", mr.CallStrings())
	}
}

func TestRun_TaskTarget(t *testing.T) {
	t.Setenv("TMUX", "1")
	stdout, stderr, readOutput := withTempOutputFiles(t)
	tmp := t.TempDir()
	opsDir := filepath.Join(tmp, ".commander", "operators")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsDir, "fro-001.session-id"), []byte("ses_op\n"), 0o644); err != nil {
		t.Fatalf("write session id: %v", err)
	}

	mr := &layout.MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte(""), nil, nil
		case len(args) >= 1 && args[0] == "new-window":
			return []byte("%21\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			return []byte("%22\n"), nil, nil
		case len(args) >= 1 && args[0] == "select-window":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "switch-client":
			return nil, nil, nil
		default:
			return nil, nil, nil
		}
	})

	origDirector := newDirectorFn
	origTask := resolveTaskTargetFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	resolveTaskTargetFn = func(taskID string) (resolvedOpenTarget, error) {
		return resolvedOpenTarget{kind: targetTask, taskBeadID: taskID, projectName: "frontend", projectPath: tmp}, nil
	}
	t.Cleanup(func() {
		newDirectorFn = origDirector
		resolveTaskTargetFn = origTask
	})

	exitCode := run([]string{"--task", "fro-001"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !containsCall(mr.CallStrings(), "tmux new-window -P -F #{pane_id} -t commander: -n op-fro-001 -c "+tmp+" opencode --session ses_op") {
		t.Fatalf("expected operator launch, calls=%v", mr.CallStrings())
	}
}

func TestRun_ConflictingFlagsFail(t *testing.T) {
	stdout, stderr, readOutput := withTempOutputFiles(t)

	exitCode := run([]string{"--project", "frontend", "--task", "fro-001"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 2 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
}

func TestRun_RemovedVerbFailsAsUnknown(t *testing.T) {
	for _, args := range [][]string{
		{"launch"},
		{"drill-lead", "--project", "frontend"},
		{"drill-operator", "--task", "fro-001", "--project", "frontend"},
	} {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		if exitCode := run(args, stdout, stderr); exitCode != 2 {
			_, errOut := readOutput()
			t.Fatalf("args=%v exit=%d stderr=%s", args, exitCode, errOut)
		}
	}
}

func TestRun_RestartHardKillsTheSession(t *testing.T) {
	t.Setenv("TMUX", "1")
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(home, layout.CommanderSessionFileName), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatalf("write session id: %v", err)
	}

	stdout, stderr, readOutput := withTempOutputFiles(t)
	mr := &layout.MockRunner{}
	hasSessionCalls := 0
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			hasSessionCalls++
			if hasSessionCalls == 1 {
				return nil, nil, nil
			}
			return nil, []byte("can't find session: commander"), errors.New("exit status 1")
		case len(args) >= 1 && args[0] == "kill-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		case len(args) >= 1 && args[0] == "switch-client":
			return nil, nil, nil
		default:
			return nil, nil, nil
		}
	})

	orig := newDirectorFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	t.Cleanup(func() { newDirectorFn = orig })
	layout.SetCommanderCaptureFn(func(context.Context) (string, error) { return "ses_new", nil })
	t.Cleanup(func() { layout.SetCommanderCaptureFn(nil) })

	exitCode := run([]string{"restart", "--hard"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !strings.Contains(errOut, "Restarting Commander...") {
		t.Fatalf("expected restart progress message, stderr=%s", errOut)
	}
	if !containsCall(mr.CallStrings(), "tmux kill-session -t commander") {
		t.Fatalf("expected restart to kill existing session, calls=%v", mr.CallStrings())
	}
}

func TestRun_RootCommandFocusesFleetWindow(t *testing.T) {
	t.Setenv("TMUX", "1")
	t.Setenv("COMMANDER_HOME", t.TempDir())
	stdout, stderr, readOutput := withTempOutputFiles(t)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)

	mr := &layout.MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "list-panes":
			return []byte("cmdr __column cmdr-objectives\nopencode --session ses_test123 --agent commander\ncmdr __column cmdr-fleet\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte("commander\nmanager (commander)\n"), nil, nil
		case len(args) >= 1 && args[0] == "select-window":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "switch-client":
			return nil, nil, nil
		default:
			return nil, nil, nil
		}
	})

	origDirector := newDirectorFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	t.Cleanup(func() { newDirectorFn = origDirector })

	exitCode := run([]string{"root"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}
	if !containsCall(mr.CallStrings(), "tmux select-window -t commander:commander") {
		t.Fatalf("expected commander root focus, calls=%v", mr.CallStrings())
	}
}

func TestRun_UpdateCommand(t *testing.T) {
	stdout, stderr, readOutput := withTempOutputFiles(t)
	repo := t.TempDir()
	installDir := filepath.Join(repo, "install")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatalf("mkdir install dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatalf("write install script: %v", err)
	}
	t.Setenv("COMMANDER_REPO", repo)

	called := false
	var gotRepo, gotScript string
	orig := runUpdateCommandFn
	runUpdateCommandFn = func(_ context.Context, stdout, stderr *os.File, repo, scriptPath string) error {
		called = true
		gotRepo = repo
		gotScript = scriptPath
		return nil
	}
	t.Cleanup(func() { runUpdateCommandFn = orig })

	exitCode := run([]string{"update"}, stdout, stderr)
	out, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, out, errOut)
	}
	if !called {
		t.Fatal("expected update command to run installer")
	}
	if gotRepo != repo {
		t.Fatalf("repo=%q want %q", gotRepo, repo)
	}
	if gotScript != filepath.Join(repo, "install", "install.sh") {
		t.Fatalf("script=%q", gotScript)
	}
	if !strings.Contains(out, "Run `cmdr restart`") {
		t.Fatalf("unexpected stdout: %s", out)
	}
}

func TestRun_ReloadCommand(t *testing.T) {
	t.Setenv("TMUX", "1")
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	repo := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	installDir := filepath.Join(repo, "install")
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	t.Setenv("COMMANDER_REPO", repo)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatalf("mkdir install dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatalf("write install script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, layout.CommanderSessionFileName), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatalf("write session id: %v", err)
	}

	stdout, stderr, readOutput := withTempOutputFiles(t)
	updateCalled := false
	var gotRepo, gotScript string
	origUpdate := runUpdateCommandFn
	runUpdateCommandFn = func(_ context.Context, stdout, stderr *os.File, repo, scriptPath string) error {
		updateCalled = true
		gotRepo = repo
		gotScript = scriptPath
		return nil
	}
	origRestart := reloadRestartFn
	restartCalled := false
	reloadRestartFn = func(_ context.Context, stdout, stderr *os.File) error {
		restartCalled = true
		return nil
	}
	t.Cleanup(func() {
		runUpdateCommandFn = origUpdate
		reloadRestartFn = origRestart
	})

	mr := &layout.MockRunner{}
	origDirector := newDirectorFn
	newDirectorFn = func() *layout.Director { return layout.NewDirectorWith(mr) }
	t.Cleanup(func() { newDirectorFn = origDirector })

	exitCode := run([]string{"reload"}, stdout, stderr)
	out, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, out, errOut)
	}
	if !updateCalled {
		t.Fatal("expected reload to run update first")
	}
	if gotRepo != repo || gotScript != filepath.Join(repo, "install", "install.sh") {
		t.Fatalf("got repo/script = %q %q", gotRepo, gotScript)
	}
	if !restartCalled {
		t.Fatal("expected reload to hand off restart to installed binary")
	}
	if !strings.Contains(errOut, "Reloading Commander: restarting session and opening linked manager tabs...") {
		t.Fatalf("expected reload progress message, stderr=%s", errOut)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if strings.Contains(call, want) {
			return true
		}
	}
	return false
}

// Regression: the Inspector column was launched with "--ws X --project Y" and,
// because the parser only knew one of the two flags, both came back empty, so
// every workstream window showed Commander's (empty) tab set.
func TestOptionalFlagIgnoresOtherFlags(t *testing.T) {
	args := []string{"--ws", "commander-il9", "--project", "commander"}
	if got := optionalFlag(args, "project"); got != "commander" {
		t.Errorf("project = %q", got)
	}
	if got := optionalFlag(args, "ws"); got != "commander-il9" {
		t.Errorf("ws = %q", got)
	}
	if got := optionalFlag([]string{"--project=x"}, "project"); got != "x" {
		t.Errorf("--project=x = %q", got)
	}
	if got := optionalFlag([]string{"--project"}, "project"); got != "" {
		t.Errorf("a dangling flag has no value, got %q", got)
	}
	if got := optionalFlag(nil, "ws"); got != "" {
		t.Errorf("absent = %q", got)
	}
}
