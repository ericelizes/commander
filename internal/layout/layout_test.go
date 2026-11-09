package layout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestSessionExists_TrueOnExitZero(t *testing.T) {
	mr := &MockRunner{}
	mr.SetSessionExists("commander", true)
	d := NewDirectorWith(mr)
	ok, err := d.SessionExists(context.Background(), "commander")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if !ok {
		t.Error("expected exists=true on exit 0")
	}
}

func TestSessionExists_FalseOnAbsent(t *testing.T) {
	mr := &MockRunner{}
	mr.SetSessionExists("commander", false)
	d := NewDirectorWith(mr)
	ok, err := d.SessionExists(context.Background(), "commander")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if ok {
		t.Error("expected exists=false when tmux says \"can't find session\"")
	}
}

func TestSessionExists_FalseWhenSocketMissing(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		return nil, []byte("error connecting to /tmp/tmux-501/default (No such file or directory)"), errExit1
	})
	d := NewDirectorWith(mr)
	ok, err := d.SessionExists(context.Background(), "commander")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if ok {
		t.Error("expected exists=false when tmux socket is absent")
	}
}

func TestLaunchCommander_FreshSessionArgv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	// Pre-write the session id so capture path is skipped.
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_test123\n"), 0o644); err != nil {
		t.Fatalf("seed session id: %v", err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 3 && args[0] == "has-session" && args[1] == "-t" && args[2] == CommanderSessionName:
			return nil, []byte("can't find session: " + CommanderSessionName), errExit1
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchCommander(context.Background(), tmp, false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux set-option -t commander: status off")
	mustContainCall(t, calls, "tmux bind-key -n C-g select-window -t commander:commander")
	mustContainCall(t, calls, "tmux has-session -t commander")
	mustContainCall(t, calls, "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+workspaceRoot+" opencode --session ses_test123 --agent commander")
	mustContainCall(t, calls, "tmux set-option -p -t %1 @cmdr_chat 1")
	mustContainCall(t, calls, "tmux split-window -h -b -P -F #{pane_id} -t %1 -p 25 -c "+workspaceRoot+" cmdr __column cmdr-objectives")
	mustContainCall(t, calls, "tmux split-window -h -P -F #{pane_id} -t %1 -p 33 -c "+workspaceRoot+" cmdr __column inspector")
	mustContainCall(t, calls, "tmux select-pane -t %1")
}

func TestLaunchCommander_ExistingSessionIsAttachOnly(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetWindowExists(CommanderSessionName, FleetWindowName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(FleetWindowName + "\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "list-panes" {
			return []byte("cmdr __column cmdr-objectives\nopencode --session ses_test123 --agent commander\ncmdr __column cmdr-fleet\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	// attach=false skips the switch/attach calls — we should see only one
	// tmux call (has-session).
	if err := d.LaunchCommander(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	calls := mr.CallStrings()
	if len(calls) == 0 {
		t.Fatal("expected health-check calls")
	}
	for _, c := range calls {
		if strings.Contains(c, "new-session") || strings.Contains(c, "split-window") || strings.Contains(c, "kill-session") || strings.Contains(c, "kill-window") {
			t.Errorf("healthy session should not rebuild: %s", c)
		}
	}
}

func TestLaunchCommander_ExistingBrokenWindowRebuilds(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_test123\n"), 0o644); err != nil {
		t.Fatalf("seed session id: %v", err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte(FleetWindowName + "\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-panes":
			return []byte("cmdr __column cmdr-objectives\ncmdr __column cmdr-fleet\n"), nil, nil
		case len(args) >= 1 && args[0] == "kill-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchCommander(context.Background(), tmp, false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux kill-session -t commander")
	mustContainCall(t, calls, "tmux set-option -t commander: status off")
	mustContainCall(t, calls, "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+workspaceRoot+" opencode --session ses_test123 --agent commander")
	mustContainCall(t, calls, "tmux set-option -p -t %1 @cmdr_chat 1")
	mustContainCall(t, calls, "tmux split-window -h -b -P -F #{pane_id} -t %1 -p 25 -c "+workspaceRoot+" cmdr __column cmdr-objectives")
	mustContainCall(t, calls, "tmux split-window -h -P -F #{pane_id} -t %1 -p 33 -c "+workspaceRoot+" cmdr __column inspector")
}

func TestLaunchCommander_OpensManagerWindowsForLinkedProjects(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_test123\n"), 0o644); err != nil {
		t.Fatalf("seed session id: %v", err)
	}
	projectPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir project commander dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".commander", "manager.session-id"), []byte("ses_manager789\n"), 0o644); err != nil {
		t.Fatalf("seed manager session: %v", err)
	}

	mr := &MockRunner{}
	hasSessionCalls := 0
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			hasSessionCalls++
			if hasSessionCalls == 1 {
				return nil, []byte("can't find session: " + CommanderSessionName), errExit1
			}
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte(""), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			target := ""
			if len(args) >= 7 {
				target = args[6]
			}
			switch target {
			case "%1":
				return []byte("%2\n"), nil, nil
			case "%2":
				return []byte("%3\n"), nil, nil
			case "%11":
				return []byte("%12\n"), nil, nil
			}
			return []byte("%13\n"), nil, nil
		case len(args) >= 1 && args[0] == "new-window":
			return []byte("%11\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	origList := listStartupProjectsFn
	origEnsure := ensureManagerReadyFn
	listStartupProjectsFn = func(ctx context.Context) ([]store.Project, error) {
		return []store.Project{{Name: "manifest", Type: "code", Path: projectPath}}, nil
	}
	ensureManagerReadyFn = func(ctx context.Context, project store.Project) error { return nil }
	t.Cleanup(func() {
		listStartupProjectsFn = origList
		ensureManagerReadyFn = origEnsure
	})

	if err := d.LaunchCommander(context.Background(), tmp, false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}
	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux set-option -t commander: status off")
	mustContainCall(t, calls, "tmux new-window -P -F #{pane_id} -t commander: -n manager (manifest) -c "+projectPath+" opencode --session ses_manager789")
}

func TestEnsureManagerReadyForProject_ToleratesActivationErrorAfterSessionCapture(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	projectPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir project commander dir: %v", err)
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	origActivate := activateStartupProjectFn
	activateStartupProjectFn = func(ctx context.Context, st *store.Store, project store.Project) error {
		if err := os.WriteFile(filepath.Join(project.Path, ".commander", "manager.session-id"), []byte("ses_partial\n"), 0o644); err != nil {
			return err
		}
		return errors.New("activation: am register manager-manifest: project not found")
	}
	t.Cleanup(func() { activateStartupProjectFn = origActivate })

	err = ensureManagerReadyForProject(context.Background(), store.Project{Name: "manifest", Type: "code", Path: projectPath})
	if err != nil {
		t.Fatalf("expected graceful success after session capture, got: %v", err)
	}
}

func TestEnsureManagerReadyForProject_ReconcilesExistingSessionIDsToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	projectPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, ".commander"), 0o755); err != nil {
		t.Fatalf("mkdir project commander dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".commander", "manager.session-id"), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatalf("seed manager session id: %v", err)
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	origActivate := activateStartupProjectFn
	activateStartupProjectFn = func(ctx context.Context, st *store.Store, project store.Project) error {
		return os.WriteFile(filepath.Join(project.Path, ".commander", "manager.session-id"), []byte("ses_reconciled\n"), 0o644)
	}
	t.Cleanup(func() { activateStartupProjectFn = origActivate })

	err = ensureManagerReadyForProject(context.Background(), store.Project{Name: "manifest", Type: "code", Path: projectPath})
	if err != nil {
		t.Fatalf("ensureManagerReadyForProject: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(projectPath, ".commander", "manager.session-id"))
	if err != nil {
		t.Fatalf("read manager session id: %v", err)
	}
	if strings.TrimSpace(string(body)) != "ses_reconciled" {
		t.Fatalf("manager session id = %q", strings.TrimSpace(string(body)))
	}
}

func TestSessionChrome_NoStatusBarAndWindowsKeepTheirNumbers(t *testing.T) {
	mr := &MockRunner{}
	d := NewDirectorWith(mr)
	if err := d.ensureCommanderSessionChrome(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux set-option -t commander: status off")
	mustContainCall(t, calls, "tmux set-option -t commander: renumber-windows off")
	mustContainCall(t, calls, "tmux set-option -s focus-events on")
	mustContainCall(t, calls, "tmux set-option -g allow-passthrough all")
	// Every surface that would otherwise fall back to the default (translucent) bg.
	for _, want := range []string{
		"tmux set-option -t commander: message-style fg=#cdd6f4,bg=#313244",
		"tmux set-option -t commander: popup-style fg=#cdd6f4,bg=#181825",
		"tmux set-option -t commander: popup-border-style fg=#585b70,bg=#181825",
		"tmux set-option -t commander: menu-selected-style fg=#11111b,bg=#cba6f7",
		"tmux set-option -t commander: status-style fg=#a6adc8,bg=#181825",
	} {
		mustContainCall(t, calls, want)
	}
	for _, c := range calls {
		if strings.Contains(c, "status on") || strings.Contains(c, "status-right C-g") {
			t.Fatalf("the bottom status bar must stay off: %s", c)
		}
	}
}

func TestRestartFresh_ReportsProgress(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_test123\n"), 0o644); err != nil {
		t.Fatalf("seed session id: %v", err)
	}
	SetCommanderCaptureFn(func(context.Context) (string, error) { return "ses_new", nil })
	t.Cleanup(func() { SetCommanderCaptureFn(nil) })

	mr := &MockRunner{}
	hasSessionCalls := 0
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			hasSessionCalls++
			if hasSessionCalls == 1 {
				return nil, nil, nil
			}
			return nil, []byte("can't find session: commander"), errExit1
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)
	var buf bytes.Buffer
	d.SetProgressWriter(&buf)

	if err := d.RestartFresh(context.Background(), tmp, false); err != nil {
		t.Fatalf("RestartFresh: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Stopping existing Commander session...",
		"Clearing Commander session state...",
		"Preparing Commander tabs...",
		"Selecting commander tab...",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("progress output missing %q: %s", want, out)
		}
	}
}

func TestLaunchCommander_SeedsHideThinkingDefaultWhenMissing(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	kvPath, err := opencodeKVPath()
	if err != nil {
		t.Fatalf("opencodeKVPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(kvPath), 0o755); err != nil {
		t.Fatalf("mkdir kv dir: %v", err)
	}
	if err := os.WriteFile(kvPath, []byte("{\n  \"theme\": \"opencode\"\n}\n"), 0o644); err != nil {
		t.Fatalf("seed kv: %v", err)
	}

	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetWindowExists(CommanderSessionName, FleetWindowName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(FleetWindowName + "\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "list-panes" {
			return []byte("cmdr __column cmdr-objectives\nopencode --session ses_test123 --agent commander\ncmdr __column cmdr-fleet\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchCommander(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	body, err := os.ReadFile(kvPath)
	if err != nil {
		t.Fatalf("read kv: %v", err)
	}
	if !strings.Contains(string(body), "\"thinking_visibility\": false") {
		t.Fatalf("expected thinking_visibility=false seeded in kv.json, got:\n%s", string(body))
	}

	tuiPath, err := opencodeTUIConfigPath()
	if err != nil {
		t.Fatalf("opencodeTUIConfigPath: %v", err)
	}
	tuiBody, err := os.ReadFile(tuiPath)
	if err != nil {
		t.Fatalf("read tui config: %v", err)
	}
	if !strings.Contains(string(tuiBody), "\"agent_cycle\": \"none\"") {
		t.Fatalf("expected agent_cycle=none seeded in tui.json, got:\n%s", string(tuiBody))
	}
	if !strings.Contains(string(tuiBody), "\"agent_cycle_reverse\": \"none\"") {
		t.Fatalf("expected agent_cycle_reverse=none seeded in tui.json, got:\n%s", string(tuiBody))
	}
}

func TestLaunchCommander_PreservesExistingThinkingVisibility(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	kvPath, err := opencodeKVPath()
	if err != nil {
		t.Fatalf("opencodeKVPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(kvPath), 0o755); err != nil {
		t.Fatalf("mkdir kv dir: %v", err)
	}
	if err := os.WriteFile(kvPath, []byte("{\n  \"thinking_visibility\": true,\n  \"theme\": \"opencode\"\n}\n"), 0o644); err != nil {
		t.Fatalf("seed kv: %v", err)
	}

	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetWindowExists(CommanderSessionName, FleetWindowName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(FleetWindowName + "\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "list-panes" {
			return []byte("cmdr __column cmdr-objectives\nopencode --session ses_test123 --agent commander\ncmdr __column cmdr-fleet\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchCommander(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	body, err := os.ReadFile(kvPath)
	if err != nil {
		t.Fatalf("read kv: %v", err)
	}
	if !strings.Contains(string(body), "\"thinking_visibility\": true") {
		t.Fatalf("expected existing thinking_visibility=true preserved, got:\n%s", string(body))
	}
}

func TestLaunchCommander_MergesCommanderKeybindOverridesIntoTUIConfig(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	tuiPath, err := opencodeTUIConfigPath()
	if err != nil {
		t.Fatalf("opencodeTUIConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tuiPath), 0o755); err != nil {
		t.Fatalf("mkdir tui dir: %v", err)
	}
	if err := os.WriteFile(tuiPath, []byte("{\n  \"$schema\": \"https://opencode.ai/tui.json\",\n  \"theme\": \"system\",\n  \"keybinds\": {\n    \"foo\": \"bar\",\n    \"agent_cycle\": \"tab\"\n  }\n}\n"), 0o644); err != nil {
		t.Fatalf("seed tui config: %v", err)
	}

	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetWindowExists(CommanderSessionName, FleetWindowName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(FleetWindowName + "\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "list-panes" {
			return []byte("cmdr __column cmdr-objectives\nopencode --session ses_test123 --agent commander\ncmdr __column cmdr-fleet\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchCommander(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	body, err := os.ReadFile(tuiPath)
	if err != nil {
		t.Fatalf("read tui config: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "\"theme\": \"system\"") {
		t.Fatalf("expected theme preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "\"foo\": \"bar\"") {
		t.Fatalf("expected unrelated keybind preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "\"agent_cycle\": \"none\"") {
		t.Fatalf("expected agent_cycle overridden to none, got:\n%s", got)
	}
	if !strings.Contains(got, "\"agent_cycle_reverse\": \"none\"") {
		t.Fatalf("expected agent_cycle_reverse seeded as none, got:\n%s", got)
	}
}

func TestLaunchLead_FreshWindowArgv(t *testing.T) {
	tmp := t.TempDir()
	commanderDir := filepath.Join(tmp, ".commander")
	if err := os.MkdirAll(commanderDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commanderDir, "manager.session-id"), []byte("ses_manager789\n"), 0o644); err != nil {
		t.Fatalf("seed manager session: %v", err)
	}

	mr := &MockRunner{}
	// has-session true, list-windows returns no matching window.
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(""), nil, nil
		}
		if len(args) >= 1 && args[0] == "new-window" {
			return []byte("%11\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "split-window" {
			if len(args) >= 7 && args[6] == "%11" {
				return []byte("%12\n"), nil, nil
			}
			return []byte("%13\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchLead(context.Background(), "frontend", tmp, false); err != nil {
		t.Fatalf("LaunchLead: %v", err)
	}

	calls := mr.CallStrings()
	// Sanity check the key argv shapes.
	mustContainCall(t, calls, "tmux new-window -P -F #{pane_id} -t commander: -n manager (frontend) -c "+tmp+" opencode --session ses_manager789")
	mustContainCall(t, calls, "tmux split-window -h -b -P -F #{pane_id} -t %11 -p 25 -c "+tmp+" cmdr __column cmdr-objectives")
	mustContainCall(t, calls, "tmux split-window -h -P -F #{pane_id} -t %11 -p 33 -c "+tmp+" cmdr __column lead-fleet --project frontend")
}

func TestLaunchLead_ExistingWindowSkipsCreate(t *testing.T) {
	tmp := t.TempDir()

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte("manager (frontend)\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchLead(context.Background(), "frontend", tmp, false); err != nil {
		t.Fatalf("LaunchLead: %v", err)
	}

	for _, c := range mr.CallStrings() {
		if strings.Contains(c, "new-window") {
			t.Errorf("existing window should not trigger new-window: %s", c)
		}
		if strings.Contains(c, "split-window") {
			t.Errorf("existing window should not trigger split-window: %s", c)
		}
	}
}

func TestLaunchLead_MissingSessionFileError(t *testing.T) {
	tmp := t.TempDir()

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(""), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	err := d.LaunchLead(context.Background(), "frontend", tmp, false)
	if err == nil {
		t.Fatal("expected error when manager.session-id missing")
	}
	if !strings.Contains(err.Error(), "manager session id") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestLaunchLead_FallsBackToLegacyLeadSessionFile(t *testing.T) {
	tmp := t.TempDir()
	commanderDir := filepath.Join(tmp, ".commander")
	if err := os.MkdirAll(commanderDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commanderDir, "lead.session-id"), []byte("ses_legacy789\n"), 0o644); err != nil {
		t.Fatalf("seed legacy lead session: %v", err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(""), nil, nil
		}
		if len(args) >= 1 && args[0] == "new-window" {
			return []byte("%11\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "split-window" {
			if len(args) >= 7 && args[6] == "%11" {
				return []byte("%12\n"), nil, nil
			}
			return []byte("%13\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchLead(context.Background(), "frontend", tmp, false); err != nil {
		t.Fatalf("LaunchLead: %v", err)
	}

	mustContainCall(t, mr.CallStrings(), "tmux new-window -P -F #{pane_id} -t commander: -n manager (frontend) -c "+tmp+" opencode --session ses_legacy789")
}

func TestLaunchOperator_FreshWindowArgv(t *testing.T) {
	tmp := t.TempDir()
	opsDir := filepath.Join(tmp, ".commander", "operators")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// flattenName("fro-001") == "fro-001"
	if err := os.WriteFile(filepath.Join(opsDir, "fro-001.session-id"), []byte("ses_op999\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "has-session" {
			return nil, nil, nil
		}
		if len(args) >= 1 && args[0] == "list-windows" {
			return []byte(""), nil, nil
		}
		if len(args) >= 1 && args[0] == "new-window" {
			return []byte("%21\n"), nil, nil
		}
		if len(args) >= 1 && args[0] == "split-window" {
			return []byte("%22\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	if err := d.LaunchOperator(context.Background(), "fro-001", "frontend", tmp, false); err != nil {
		t.Fatalf("LaunchOperator: %v", err)
	}

	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux new-window -P -F #{pane_id} -t commander: -n op-fro-001 -c "+tmp+" opencode --session ses_op999")
	mustContainCall(t, calls, "tmux split-window -h -b -P -F #{pane_id} -t %21 -p 25 -c "+tmp+" cmdr __column cmdr-objectives")
	mustContainCall(t, calls, "tmux split-window -h -P -F #{pane_id} -t %21 -p 33 -c "+tmp+" cmdr __column operator-side --task fro-001")
}

func TestRefresh_RebuildsExistingCommanderOwnedWindows(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_cmdr\n"), 0o644); err != nil {
		t.Fatalf("seed commander session: %v", err)
	}

	projectPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, ".commander", "operators"), 0o755); err != nil {
		t.Fatalf("mkdir project commander dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".commander", "manager.session-id"), []byte("ses_manager\n"), 0o644); err != nil {
		t.Fatalf("seed manager session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".commander", "operators", "fro-001.session-id"), []byte("ses_op\n"), 0o644); err != nil {
		t.Fatalf("seed operator session: %v", err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "display-message":
			return []byte("manager (frontend)\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte("commander\nmanager (frontend)\nop-fro-001\nnotes\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-panes":
			return []byte("opencode --session ses_old --agent commander\ncmdr __column cmdr-objectives\ncmdr __column cmdr-fleet\n"), nil, nil
		case len(args) >= 1 && args[0] == "kill-window":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "new-window":
			switch {
			case len(args) >= 8 && args[7] == FleetWindowName:
				return []byte("%1\n"), nil, nil
			case len(args) >= 8 && args[7] == "manager (frontend)":
				return []byte("%11\n"), nil, nil
			case len(args) >= 8 && args[7] == "op-fro-001":
				return []byte("%21\n"), nil, nil
			}
			return []byte("%99\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			target := ""
			if len(args) >= 7 {
				target = args[6]
			}
			switch target {
			case "%1":
				return []byte("%2\n"), nil, nil
			case "%2":
				return []byte("%3\n"), nil, nil
			case "%11":
				return []byte("%12\n"), nil, nil
			case "%12":
				return []byte("%13\n"), nil, nil
			case "%21":
				return []byte("%22\n"), nil, nil
			}
			return []byte("%50\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	resolveProject := func(name string) (string, error) {
		if name != "frontend" {
			t.Fatalf("unexpected project %q", name)
		}
		return projectPath, nil
	}
	resolveTask := func(taskBeadID string) (string, string, error) {
		if taskBeadID != "fro-001" {
			t.Fatalf("unexpected task %q", taskBeadID)
		}
		return "frontend", projectPath, nil
	}

	if err := d.Refresh(context.Background(), tmp, resolveProject, resolveTask); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux kill-window -t commander:commander")
	mustContainCall(t, calls, "tmux kill-window -t commander:manager (frontend)")
	mustContainCall(t, calls, "tmux kill-window -t commander:op-fro-001")
	mustContainCall(t, calls, "tmux select-window -t commander:manager (frontend)")
	for _, c := range calls {
		if strings.Contains(c, "notes") && strings.Contains(c, "kill-window") {
			t.Fatalf("refresh should not touch unrelated window: %s", c)
		}
	}
}

func TestRefresh_DoesNotCreateUnopenedLeadOrOperatorWindows(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_cmdr\n"), 0o644); err != nil {
		t.Fatalf("seed commander session: %v", err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "display-message":
			return []byte("commander\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte("commander\nnotes\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-panes":
			return []byte("opencode --session ses_old --agent commander\ncmdr __column cmdr-objectives\ncmdr __column cmdr-fleet\n"), nil, nil
		case len(args) >= 1 && args[0] == "kill-window":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "new-window":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	if err := d.Refresh(context.Background(), tmp, func(string) (string, error) {
		t.Fatal("project resolver should not be called")
		return "", nil
	}, func(string) (string, string, error) {
		t.Fatal("task resolver should not be called")
		return "", "", nil
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	for _, c := range mr.CallStrings() {
		if strings.Contains(c, "lead-") || strings.Contains(c, "op-") {
			t.Fatalf("unexpected lead/operator rebuild call: %s", c)
		}
	}
}

func TestRestart_KillsSessionAndResumesCommanderChat(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatalf("seed commander session: %v", err)
	}
	SetCommanderCaptureFn(func(ctx context.Context) (string, error) {
		t.Error("restart must not capture a new commander session")
		return "ses_new", nil
	})
	t.Cleanup(func() { SetCommanderCaptureFn(nil) })

	mr := &MockRunner{}
	hasSessionCalls := 0
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			hasSessionCalls++
			if hasSessionCalls == 1 {
				return nil, nil, nil
			}
			return nil, []byte("can't find session: commander"), errExit1
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			return []byte("%2\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	var buf bytes.Buffer
	d := NewDirectorWith(mr)
	d.SetProgressWriter(&buf)

	if err := d.Restart(context.Background(), tmp, false); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if strings.Contains(buf.String(), "Clearing Commander session state") {
		t.Fatalf("restart must keep the session: %s", buf.String())
	}
	body, err := os.ReadFile(filepath.Join(tmp, CommanderSessionFileName))
	if err != nil || strings.TrimSpace(string(body)) != "ses_old" {
		t.Fatalf("session id = %q (%v), want ses_old kept", strings.TrimSpace(string(body)), err)
	}
	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux kill-session -t commander")
	mustContainCall(t, calls, "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+workspaceRoot+" opencode --session ses_old --agent commander")
}

func TestRestartFresh_KillsSessionClearsSessionIDAndRebuilds(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatalf("seed commander session: %v", err)
	}

	captured := false
	SetCommanderCaptureFn(func(ctx context.Context) (string, error) {
		captured = true
		return "ses_new", nil
	})
	t.Cleanup(func() { SetCommanderCaptureFn(nil) })

	mr := &MockRunner{}
	hasSessionCalls := 0
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			hasSessionCalls++
			if hasSessionCalls == 1 {
				return nil, nil, nil
			}
			return nil, []byte("can't find session: commander"), errExit1
		case len(args) >= 1 && args[0] == "kill-session":
			return nil, nil, nil
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	if err := d.RestartFresh(context.Background(), tmp, false); err != nil {
		t.Fatalf("RestartFresh: %v", err)
	}
	if !captured {
		t.Fatal("expected restart to capture a new commander session id")
	}
	body, err := os.ReadFile(filepath.Join(tmp, CommanderSessionFileName))
	if err != nil {
		t.Fatalf("read session id: %v", err)
	}
	if strings.TrimSpace(string(body)) != "ses_new" {
		t.Fatalf("session id = %q want ses_new", strings.TrimSpace(string(body)))
	}

	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux kill-session -t commander")
	mustContainCall(t, calls, "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+workspaceRoot+" opencode --session ses_new --agent commander")
}

func TestRestart_WithoutRunningSessionStillLaunchesFresh(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("COMMANDER_HOME", tmp)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)

	SetCommanderCaptureFn(func(ctx context.Context) (string, error) {
		return "ses_new", nil
	})
	t.Cleanup(func() { SetCommanderCaptureFn(nil) })

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, []byte("can't find session: commander"), errExit1
		case len(args) >= 1 && args[0] == "new-session":
			return []byte("%1\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			if len(args) >= 7 && args[6] == "%1" {
				return []byte("%2\n"), nil, nil
			}
			return []byte("%3\n"), nil, nil
		default:
			return nil, nil, nil
		}
	})
	d := NewDirectorWith(mr)

	if err := d.Restart(context.Background(), tmp, false); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	for _, call := range mr.CallStrings() {
		if strings.Contains(call, "kill-session") {
			t.Fatalf("unexpected kill-session call: %s", call)
		}
	}
}

func TestFlattenName(t *testing.T) {
	cases := map[string]string{
		"frontend":     "frontend",
		"fro-001":      "fro-001",
		"foo/bar":      "foo-bar",
		"weird name!":  "weird-name",
		"":             "x",
		"////":         "x",
		"keep_under":   "keep-under",
		"Cap-IT-aL123": "Cap-IT-aL123",
	}
	for in, want := range cases {
		got := flattenName(in)
		if got != want {
			t.Errorf("flattenName(%q) = %q want %q", in, got, want)
		}
	}
}

func TestAttachOrSwitch_OutsideTmuxUsesAttach(t *testing.T) {
	mr := &MockRunner{}
	d := NewDirectorWith(mr)
	d.insideTmuxFn = func() bool { return false }
	called := false
	d.attachFn = func(ctx context.Context, name string) error {
		called = true
		if name != CommanderSessionName {
			t.Fatalf("attach target = %q", name)
		}
		return nil
	}

	if err := d.attachOrSwitch(context.Background(), CommanderSessionName); err != nil {
		t.Fatalf("attachOrSwitch: %v", err)
	}
	if !called {
		t.Fatal("expected attachFn to be called")
	}
	for _, c := range mr.CallStrings() {
		if strings.Contains(c, "switch-client") {
			t.Fatalf("outside tmux should not call switch-client: %s", c)
		}
	}
}

func TestAttachOrSwitch_InsideTmuxUsesSwitchClient(t *testing.T) {
	mr := &MockRunner{}
	d := NewDirectorWith(mr)
	d.insideTmuxFn = func() bool { return true }
	d.attachFn = func(ctx context.Context, name string) error {
		t.Fatal("inside tmux should not call attachFn")
		return nil
	}

	if err := d.attachOrSwitch(context.Background(), CommanderSessionName); err != nil {
		t.Fatalf("attachOrSwitch: %v", err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux switch-client -t commander")
}

func TestAttachOrSwitch_InsideDetachedTmuxToleratesNoCurrentClient(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && args[0] == "switch-client" {
			return nil, []byte("no current client"), errExit1
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)
	d.insideTmuxFn = func() bool { return true }

	if err := d.attachOrSwitch(context.Background(), CommanderSessionName); err != nil {
		t.Fatalf("attachOrSwitch: %v", err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux switch-client -t commander")
}

func TestEnsureCommanderSessionID_UsesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, CommanderSessionFileName), []byte("ses_existing\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	called := false
	id, err := EnsureCommanderSessionID(context.Background(), tmp, func(ctx context.Context) (string, error) {
		called = true
		return "ses_should_not_be_used", nil
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if id != "ses_existing" {
		t.Errorf("id: got %q want ses_existing", id)
	}
	if called {
		t.Error("capture fn should not be called when file exists")
	}
}

func TestEnsureCommanderSessionID_CapturesAndPersists(t *testing.T) {
	tmp := t.TempDir()
	id, err := EnsureCommanderSessionID(context.Background(), tmp, func(ctx context.Context) (string, error) {
		return "ses_fresh123", nil
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if id != "ses_fresh123" {
		t.Errorf("id: got %q want ses_fresh123", id)
	}
	b, err := os.ReadFile(filepath.Join(tmp, CommanderSessionFileName))
	if err != nil {
		t.Fatalf("read persisted: %v", err)
	}
	if strings.TrimSpace(string(b)) != "ses_fresh123" {
		t.Errorf("persisted: got %q", string(b))
	}
}

func mustContainCall(t *testing.T, calls []string, want string) {
	t.Helper()
	for _, c := range calls {
		if c == want {
			return
		}
	}
	t.Errorf("missing call %q\nall calls:\n  %s", want, strings.Join(calls, "\n  "))
}
