// Package layout is the tmux-session orchestrator that wires up Commander's
// three view tiers (Commander, Manager, Operator) as tmux windows + panes.
//
// Architecture (per the pivot to opencode-everywhere):
//
//   - `cmdr` (no args) → LaunchCommander: ensures the `commander` tmux session
//     and a `cmdr-fleet` window exists with three panes:
//     pane 0 = `opencode --session <commander-session-id> --agent commander`
//     (blank-import then resume),
//     pane 1 = `cmdr __column cmdr-objectives`,
//     pane 2 = `cmdr __column inspector` (older windows: `cmdr-fleet`).
//   - `cmdr --project NAME` → LaunchLead: adds a `manager-NAME` window
//     to the `commander` session with three panes scoped to that project.
//   - `cmdr --task ID` → LaunchOperator: adds an `op-<flat>` window with two
//     panes (chat + side panel).
//   - `cmdr refresh` → Refresh: recreates the currently open Commander-owned
//     windows so layout or chrome code changes land during development.
//
// The package is tmux-CLI-only — no Bubble Tea, no business logic. Per-pane
// chrome (objectives tree, fleet metrics, side panel) is rendered by the
// `cmdr __column <kind>` subcommand's Bubble Tea programs; this package's job
// is just to lay the panes out and start the right command in each.
//
// Testability: every shell-out goes through a Runner abstraction, so unit
// tests assert on argv without spawning tmux for real.
package layout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osx "os/exec"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/activation"
	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

// CommanderSessionName is the tmux session name owned by `cmdr`. A single
// session hosts every window (Commander view + per-project Manager views +
// per-task Operator views). Hard-coded so detach-and-reattach always lands
// on the same session.
const CommanderSessionName = "commander"

// FleetWindowName is the tmux window inside the commander session that hosts
// the cross-fleet Commander view (3 panes: opencode chat, objectives tree,
// fleet metrics + operators).
const FleetWindowName = "commander"
const legacyFleetWindowName = "cmdr-fleet"

var (
	listStartupProjectsFn    = listStartupProjects
	ensureManagerReadyFn     = ensureManagerReadyForProject
	activateStartupProjectFn = activateStartupProject
)

// Runner abstracts the `tmux <args...>` shell-out so tests can capture argv
// without spawning a real tmux process. The real implementation in DefaultRunner
// is a thin os/exec wrapper.
type Runner interface {
	// Run executes `tmux args...`. Returns stdout, stderr, error. tmux's exit
	// code is reflected in the error.
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// DefaultRunner is the production Runner — runs the real `tmux` binary.
type DefaultRunner struct{}

// Run shells out to `tmux args...` via os/exec.
func (DefaultRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := osx.CommandContext(ctx, "tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Director is the layout package's API surface. It wraps a Runner so callers
// can inject a fake. In production code use NewDirector(); in tests use
// NewDirectorWith(runner) with a recording fake.
type Director struct {
	r Runner
	// cmdrBinary is the path to the `cmdr` binary used in pane commands. The
	// default ("cmdr") relies on the user's PATH; tests can override to point
	// at a freshly-built binary.
	cmdrBinary string
	// opencodeBinary is the path to the `opencode` binary. Default: "opencode".
	opencodeBinary string
	// claudeBinary is the Claude Code CLI used when a tier's runtime is
	// "claude". Default: "claude".
	claudeBinary   string
	insideTmuxFn   func() bool
	attachFn       func(context.Context, string) error
	progressWriter io.Writer
	// keepChats: SoftRestart never respawns a chat pane (see SetKeepChats).
	keepChats bool
}

type ResolveProjectPathFunc func(projectName string) (string, error)
type ResolveTaskTargetFunc func(taskBeadID string) (projectName, projectPath string, err error)

// NewDirector returns a Director backed by the real tmux binary. The cmdr +
// opencode binaries are looked up on PATH at exec time.
func NewDirector() *Director {
	return &Director{
		r:              DefaultRunner{},
		cmdrBinary:     "cmdr",
		opencodeBinary: "opencode",
		claudeBinary:   agentrt.ClaudeBinary,
		insideTmuxFn: func() bool {
			return os.Getenv("TMUX") != ""
		},
		attachFn: attachSession,
	}
}

// NewDirectorWith returns a Director using the supplied Runner. Used by
// tests to capture argv without a real tmux.
func NewDirectorWith(r Runner) *Director {
	return &Director{
		r:              r,
		cmdrBinary:     "cmdr",
		opencodeBinary: "opencode",
		claudeBinary:   agentrt.ClaudeBinary,
		insideTmuxFn: func() bool {
			return os.Getenv("TMUX") != ""
		},
		attachFn: attachSession,
	}
}

// SetBinaries overrides the cmdr/opencode binary paths. Empty values are
// ignored (the default is preserved). Used by tests + by callers that want
// to dispatch a freshly-built binary out of a build/ dir.
func (d *Director) SetBinaries(cmdr, opencode string) {
	if cmdr != "" {
		d.cmdrBinary = cmdr
	}
	if opencode != "" {
		d.opencodeBinary = opencode
	}
}

// SetClaudeBinary overrides the Claude Code binary path. Empty is ignored.
func (d *Director) SetClaudeBinary(claude string) {
	if claude != "" {
		d.claudeBinary = claude
	}
}

func (d *Director) SetProgressWriter(w io.Writer) {
	d.progressWriter = w
}

func (d *Director) reportProgress(format string, args ...any) {
	if d.progressWriter == nil {
		return
	}
	fmt.Fprintf(d.progressWriter, format+"\n", args...)
}

// SessionExists returns true iff `tmux has-session -t <name>` exits 0. tmux
// returns exit 1 with a "can't find session" or "no server running" stderr
// when the session is absent — both collapse to (false, nil).
func (d *Director) SessionExists(ctx context.Context, name string) (bool, error) {
	_, stderr, err := d.r.Run(ctx, "has-session", "-t", name)
	if err == nil {
		return true, nil
	}
	s := strings.TrimSpace(string(stderr))
	if strings.Contains(s, "can't find session") || strings.Contains(s, "no server running") || strings.Contains(s, "No such file or directory") {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session: %w: %s", err, s)
}

// WindowExists returns true iff a window named `window` exists in `session`.
// Implemented via `tmux list-windows -t <session> -F '#W'` and a substring
// match — tmux doesn't expose has-window directly.
func (d *Director) WindowExists(ctx context.Context, session, window string) (bool, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-windows", "-t", session, "-F", "#W")
	if err != nil {
		s := strings.TrimSpace(string(stderr))
		if strings.Contains(s, "can't find session") || strings.Contains(s, "no server running") || strings.Contains(s, "No such file or directory") {
			return false, nil
		}
		return false, fmt.Errorf("tmux list-windows: %w: %s", err, s)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if strings.TrimSpace(line) == window {
			return true, nil
		}
	}
	return false, nil
}

// LaunchCommander ensures the commander session + cmdr-fleet window exist
// with the right 3-pane layout, then attaches the current terminal to it.
// Idempotent: if the session already exists this is a pure attach.
//
// Pane layout (horizontal split; left → right):
//
//	pane 0 (~30%): `opencode --session <commander-session-id> --agent commander`
//	              (blank-import then resume)
//	pane 1 (~30%): `cmdr __column cmdr-objectives`
//	pane 2 (~40%): `cmdr __column inspector`
//
// commanderHome is ~/.commander (or $COMMANDER_HOME) — the session-id file
// lives there. attach controls whether we end this call by attaching the
// current terminal to the session (true in production; false in tests).
func (d *Director) LaunchCommander(ctx context.Context, commanderHome string, attach bool) error {
	if err := ensureOpencodeCommanderPrefs(); err != nil {
		return err
	}
	d.reportProgress("Preparing Commander tabs...")
	exists, err := d.SessionExists(ctx, CommanderSessionName)
	if err != nil {
		return err
	}

	healthy, err := d.commanderFleetWindowHealthy(ctx)
	if err != nil {
		return err
	}
	if !healthy {
		d.reportProgress("Opening commander tab...")
		if err := d.rebuildCommanderFleetWindow(ctx, commanderHome, exists); err != nil {
			return err
		}
	}
	if err := d.ensureCommanderSessionChrome(ctx); err != nil {
		return err
	}
	if err := d.ensureCommanderKeyBindings(ctx); err != nil {
		return err
	}
	if err := d.ensureLinkedManagerWindows(ctx); err != nil {
		return err
	}
	d.reportProgress("Selecting commander tab...")
	if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", CommanderSessionName+":"+FleetWindowName); runErr != nil {
		return fmt.Errorf("tmux select-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	d.reportProgress("Commander ready.")

	if attach {
		return d.attachOrSwitch(ctx, CommanderSessionName)
	}
	return nil
}

func (d *Director) ensureCommanderSessionChrome(ctx context.Context) error {
	for _, args := range [][]string{
		// No tmux status bar: the Workstreams sidebar is the navigator (0 Commander,
		// then each workstream) and shows which window you are in.
		{"set-option", "-t", CommanderSessionName + ":", "status", "off"},
		{"set-option", "-t", CommanderSessionName + ":", "status-left", ""},
		{"set-option", "-t", CommanderSessionName + ":", "status-right", ""},
		// Windows are numbered by workstream rank; never let tmux close the gaps.
		{"set-option", "-t", CommanderSessionName + ":", "renumber-windows", "off"},
		// Lets the sidebar learn when its window becomes the visible one.
		{"set-option", "-s", "focus-events", "on"},
		// The inspector uploads kitty-graphics pictures through DCS passthrough.
		// "all" (not "on") so a hidden window's inspector can upload too; the
		// picture itself is drawn by placeholder text cells.
		{"set-option", "-g", "allow-passthrough", "all"},
	} {
		if _, stderr, err := d.r.Run(ctx, args...); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
		}
	}
	// Re-apply the absolute column widths whenever the terminal changes size or a
	// client attaches (tmux rescales panes proportionally on resize).
	bin := d.cmdrBinary
	if bin == "" {
		bin = "cmdr"
	}
	for _, hook := range []string{"client-resized", "client-attached"} {
		args := []string{"set-hook", "-t", CommanderSessionName + ":", hook, fmt.Sprintf("run-shell -b '%s __relayout'", bin)}
		if _, stderr, err := d.r.Run(ctx, args...); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
		}
	}
	// Solid colors for every remaining default-background surface.
	for _, kv := range sessionStyles() {
		args := []string{"set-option", "-t", CommanderSessionName + ":", kv[0], kv[1]}
		if _, stderr, err := d.r.Run(ctx, args...); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
		}
	}
	return nil
}

func (d *Director) ensureCommanderKeyBindings(ctx context.Context) error {
	if _, stderr, err := d.r.Run(ctx, "bind-key", "-n", "C-g", "select-window", "-t", CommanderSessionName+":"+FleetWindowName); err != nil {
		return fmt.Errorf("tmux bind-key C-g: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return d.ensureSidebarBindings(ctx)
}

func (d *Director) ensureLinkedManagerWindows(ctx context.Context) error {
	template, err := CommanderPromptTemplate(ctx, "")
	if err != nil {
		return err
	}
	if template == promptassets.RolePlanningCommander {
		return nil
	}

	if handled, err := d.ensureWorkstreamManagerWindows(ctx); handled {
		return err
	}
	projects, err := listStartupProjectsFn(ctx)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		d.reportProgress("No linked managers to open.")
		return nil
	}

	d.reportProgress("Opening %d linked manager tabs...", len(projects))
	for _, project := range projects {
		d.reportProgress("Preparing manager (%s)...", project.Name)
		if err := ensureManagerReadyFn(ctx, project); err != nil {
			return fmt.Errorf("ensure manager readiness for %q: %w", project.Name, err)
		}
		if err := d.LaunchLead(ctx, project.Name, project.Path, false); err != nil {
			return fmt.Errorf("open manager window for %q: %w", project.Name, err)
		}
	}
	return nil
}

// FocusCommanderRoot ensures the Commander session exists, selects the root
// fleet window, and attaches/switches into that session.
func (d *Director) FocusCommanderRoot(ctx context.Context, commanderHome string, attach bool) error {
	if err := d.LaunchCommander(ctx, commanderHome, false); err != nil {
		return err
	}
	if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", CommanderSessionName+":"+FleetWindowName); runErr != nil {
		return fmt.Errorf("tmux select-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	if attach {
		if d.insideTmuxFn != nil && d.insideTmuxFn() {
			return nil
		}
		return d.attachOrSwitch(ctx, CommanderSessionName)
	}
	return nil
}

// LaunchLead ensures the commander session exists, then ensures (or creates)
// a `manager-<flat>` window scoped to projectName. Manager view is 3 panes:
//
//	pane 0 (~30%): opencode --session <manager-session-id>
//	pane 1 (~30%): cmdr __column lead-tasks --project NAME
//	pane 2 (~40%): cmdr __column lead-fleet --project NAME
//
// projectPath is the absolute path to the project; <projectPath>/.commander/
// is where manager.session-id lives. attach selects the window after creating it.
func (d *Director) LaunchLead(ctx context.Context, projectName, projectPath string, attach bool) error {
	if err := d.ensureCommanderRoot(ctx, "", false); err != nil {
		return err
	}

	windowName, winExists, err := d.projectManagerWindow(ctx, projectName)
	if err != nil {
		return err
	}
	if windowName == "" {
		windowName = managerWindowName(projectName)
	}
	target := CommanderSessionName + ":" + windowName

	if !winExists {
		if err := d.createLeadWindow(ctx, projectName, projectPath); err != nil {
			return err
		}
	}

	if attach {
		// Select the window inside the existing session, then attach if
		// we're not already attached. Inside an attached client tmux's
		// switch-client is the right verb; outside, attach.
		if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", target); runErr != nil {
			return fmt.Errorf("tmux select-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
		}
		if d.insideTmuxFn != nil && d.insideTmuxFn() {
			return nil
		}
		return d.attachOrSwitch(ctx, CommanderSessionName)
	}
	return nil
}

// LaunchOperator ensures the commander session exists, then ensures (or
// creates) an `op-<flat>` window scoped to taskBeadID. Operator view is 2
// panes:
//
//	pane 0 (~60%): opencode --session <operator-session-id>
//	pane 1 (~40%): cmdr __column operator-side --task <taskBeadID>
//
// projectPath is the absolute path; the operator session-id lives at
// <projectPath>/.commander/operators/<flat-task>.session-id.
func (d *Director) LaunchOperator(ctx context.Context, taskBeadID, projectName, projectPath string, attach bool) error {
	if err := d.ensureCommanderRoot(ctx, "", false); err != nil {
		return err
	}

	flatTask := flattenName(taskBeadID)
	windowName := "op-" + flatTask
	target := CommanderSessionName + ":" + windowName

	winExists, err := d.WindowExists(ctx, CommanderSessionName, windowName)
	if err != nil {
		return err
	}

	if !winExists {
		if err := d.createOperatorWindow(ctx, taskBeadID, projectPath); err != nil {
			return err
		}
	}

	if attach {
		if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", target); runErr != nil {
			return fmt.Errorf("tmux select-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
		}
		if d.insideTmuxFn != nil && d.insideTmuxFn() {
			return nil
		}
		return d.attachOrSwitch(ctx, CommanderSessionName)
	}
	return nil
}

// Refresh recreates the currently open Commander-owned windows in the running
// tmux session. This is a development-only refresh path: it rebuilds the root
// `cmdr-fleet` window plus any existing `manager-*`/legacy `lead-*` and `op-*`
// windows, but does
// not create windows that were not already open.
func (d *Director) Refresh(ctx context.Context, commanderHome string, resolveProjectPath ResolveProjectPathFunc, resolveTask ResolveTaskTargetFunc) error {
	if resolveProjectPath == nil {
		return fmt.Errorf("refresh: project resolver is required")
	}
	if resolveTask == nil {
		return fmt.Errorf("refresh: task resolver is required")
	}
	if err := d.ensureCommanderRoot(ctx, commanderHome, false); err != nil {
		return err
	}

	selectedWindow, err := d.selectedWindowName(ctx, CommanderSessionName)
	if err != nil {
		return err
	}
	windowNames, err := d.listWindowNames(ctx, CommanderSessionName)
	if err != nil {
		return err
	}

	if err := d.rebuildCommanderFleetWindow(ctx, commanderHome, true); err != nil {
		return err
	}
	for _, windowName := range windowNames {
		switch {
		case windowName == FleetWindowName || windowName == legacyFleetWindowName:
			continue
		case isManagerWindowName(windowName):
			projectName := parseManagerWindowProjectName(windowName)
			projectPath, err := resolveProjectPath(projectName)
			if err != nil {
				return fmt.Errorf("refresh %s: %w", windowName, err)
			}
			if err := d.rebuildLeadWindow(ctx, projectName, projectPath); err != nil {
				return err
			}
		case strings.HasPrefix(windowName, "lead-"):
			projectName := strings.TrimPrefix(windowName, "lead-")
			projectPath, err := resolveProjectPath(projectName)
			if err != nil {
				return fmt.Errorf("refresh %s: %w", windowName, err)
			}
			if err := d.rebuildLeadWindow(ctx, projectName, projectPath); err != nil {
				return err
			}
		case strings.HasPrefix(windowName, "op-"):
			taskBeadID := strings.TrimPrefix(windowName, "op-")
			_, projectPath, err := resolveTask(taskBeadID)
			if err != nil {
				return fmt.Errorf("refresh %s: %w", windowName, err)
			}
			if err := d.rebuildOperatorWindow(ctx, taskBeadID, projectPath); err != nil {
				return err
			}
		}
	}

	if selectedWindow != "" {
		switch {
		case strings.HasPrefix(selectedWindow, "lead-"):
			selectedWindow = managerWindowName(strings.TrimPrefix(selectedWindow, "lead-"))
		case strings.HasPrefix(selectedWindow, "manager-"):
			selectedWindow = managerWindowName(strings.TrimPrefix(selectedWindow, "manager-"))
		}
		target := CommanderSessionName + ":" + selectedWindow
		if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", target); runErr != nil {
			// Best-effort restore: if the window vanished for a legit reason, keep
			// the refresh successful rather than masking the actual rebuild work.
			_ = stderr
		}
	}
	return nil
}

// Restart tears down the current Commander tmux session and launches a new
// one that RESUMES the existing Commander chat session (same session id, same
// as Managers). Project state in SQLite is left untouched. Use RestartFresh to
// also drop the Commander conversation.
func (d *Director) Restart(ctx context.Context, commanderHome string, attach bool) error {
	exists, err := d.SessionExists(ctx, CommanderSessionName)
	if err != nil {
		return err
	}
	if exists {
		d.reportProgress("Stopping existing Commander session...")
		if _, stderr, runErr := d.r.Run(ctx, "kill-session", "-t", CommanderSessionName); runErr != nil {
			return fmt.Errorf("tmux kill-session: %w: %s", runErr, strings.TrimSpace(string(stderr)))
		}
	}
	return d.LaunchCommander(ctx, commanderHome, attach)
}

// RestartFresh is Restart but first clears the persisted Commander session id,
// so Commander comes back as a brand-new chat.
func (d *Director) RestartFresh(ctx context.Context, commanderHome string, attach bool) error {
	d.reportProgress("Clearing Commander session state...")
	if err := ClearCommanderSessionID(commanderHome); err != nil {
		return err
	}
	return d.Restart(ctx, commanderHome, attach)
}

// ensureCommanderRoot is a thin wrapper that returns an error when the
// commander session doesn't already exist. Set createIfMissing to true if
// you'd rather create-on-demand.
func (d *Director) ensureCommanderRoot(ctx context.Context, commanderHome string, createIfMissing bool) error {
	exists, err := d.SessionExists(ctx, CommanderSessionName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if createIfMissing {
		return d.LaunchCommander(ctx, commanderHome, false)
	}
	return errors.New("commander tmux session not running: the user starts it by running `cmdr` in their own terminal; an agent is refused that, so tell the user")
}

func (d *Director) commanderFleetWindowHealthy(ctx context.Context) (bool, error) {
	exists, err := d.SessionExists(ctx, CommanderSessionName)
	if err != nil || !exists {
		return false, err
	}
	rootWindowName, winExists, err := d.rootWindow(ctx)
	if err != nil || !winExists {
		return false, err
	}
	stdout, stderr, err := d.r.Run(ctx, "list-panes", "-t", CommanderSessionName+":"+rootWindowName, "-F", "#{pane_start_command}")
	if err != nil {
		return false, fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	lines := nonEmptyLines(string(stdout))
	if len(lines) != 3 && len(lines) != 4 { // sidebar, [title bar], chat, context
		return false, nil
	}
	// The chat pane must match the configured runtime, so switching runtimes
	// in config rebuilds the window on the next `cmdr`.
	chatMarker := "opencode --session "
	if agentrt.For(agentrt.TierCommander) == agentrt.Claude {
		chatMarker = "claude -n " + promptassets.CommanderAgentName + " "
	}
	// The navigator sits on the left: an old chat-first window is rebuilt.
	if !strings.Contains(lines[0], "cmdr __column cmdr-objectives") {
		return false, nil
	}
	var hasChat, hasObjectives, hasFleet bool
	for _, line := range lines {
		switch {
		case strings.Contains(line, "cmdr __column cmdr-objectives"):
			hasObjectives = true
		case isRightContextColumn(line):
			hasFleet = true
		case strings.Contains(line, chatMarker):
			hasChat = true
		}
	}
	return hasChat && hasObjectives && hasFleet, nil
}

func (d *Director) rebuildCommanderFleetWindow(ctx context.Context, commanderHome string, sessionExists bool) error {
	if sessionExists {
		rootWindowName, winExists, err := d.rootWindow(ctx)
		if err != nil {
			return err
		}
		if winExists {
			windowCount, err := d.windowCount(ctx, CommanderSessionName)
			if err != nil {
				return err
			}
			if windowCount <= 1 {
				if _, stderr, runErr := d.r.Run(ctx, "kill-session", "-t", CommanderSessionName); runErr != nil {
					return fmt.Errorf("tmux kill-session: %w: %s", runErr, strings.TrimSpace(string(stderr)))
				}
				sessionExists = false
			} else {
				if _, stderr, runErr := d.r.Run(ctx, "kill-window", "-t", CommanderSessionName+":"+rootWindowName); runErr != nil {
					return fmt.Errorf("tmux kill-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
				}
			}
		}
	}
	return d.createCommanderFleetWindow(ctx, commanderHome, sessionExists)
}

func (d *Director) rebuildLeadWindow(ctx context.Context, projectName, projectPath string) error {
	for _, windowName := range []string{managerWindowName(projectName), legacyManagerWindowName(projectName), legacyLeadWindowName(projectName)} {
		if err := d.killWindowIfExists(ctx, CommanderSessionName, windowName); err != nil {
			return err
		}
	}
	return d.createLeadWindow(ctx, projectName, projectPath)
}

func (d *Director) createLeadWindow(ctx context.Context, projectName, projectPath string) error {
	commanderDir := filepath.Join(projectPath, ".commander")
	kind := agentrt.For(agentrt.TierLead)
	sessionID, sessionPath, err := readProjectManagerSessionID(commanderDir, kind)
	if err != nil {
		return fmt.Errorf("manager session id for %q: %w", projectName, err)
	}
	if sessionID == "" {
		return fmt.Errorf("project %q has no manager session id at %s — run `cmdr internal-add-project` first",
			projectName, sessionPath)
	}

	windowName := managerWindowName(projectName)
	opencodeCmd := d.buildOpencodeResumeCmd(sessionID)
	if kind == agentrt.Claude {
		if opencodeCmd, err = d.claudeLaunchCmd(ctx, claudeChat{activation.ManagerAgentName(projectName), agentrt.Model(agentrt.TierLead), sessionID}); err != nil {
			return err
		}
	}
	stdout, stderr, runErr := d.r.Run(ctx,
		"new-window",
		"-P", "-F", "#{pane_id}",
		"-t", CommanderSessionName+":",
		"-n", windowName,
		"-c", projectPath,
		opencodeCmd,
	)
	if runErr != nil {
		return fmt.Errorf("tmux new-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	chatPaneID := strings.TrimSpace(string(stdout))
	if chatPaneID == "" {
		return fmt.Errorf("tmux new-window: empty pane id")
	}

	return d.layoutThreeColumns(ctx, chatPaneID, projectPath,
		d.buildColumnCmd("cmdr-objectives"),
		d.buildColumnCmdWithFlags("lead-fleet", "--project", projectName), "manager", "Manager | "+projectName)
}

func (d *Director) createOperatorWindow(ctx context.Context, taskBeadID, projectPath string) error {
	flatTask := flattenName(taskBeadID)
	sessionID, kind, sessionFile, err := readOperatorSessionID(filepath.Join(projectPath, ".commander", "operators"), flatTask)
	if err != nil {
		return fmt.Errorf("operator session id for task %q: %w", taskBeadID, err)
	}
	if sessionID == "" {
		return fmt.Errorf("operator session id missing at %s — task not yet spawned", sessionFile)
	}

	windowName := "op-" + flatTask
	opencodeCmd := d.buildOpencodeResumeCmd(sessionID)
	if kind == agentrt.Claude {
		if opencodeCmd, err = d.claudeLaunchCmd(ctx, claudeChat{"operator-" + flatTask, agentrt.Model(agentrt.TierOperator), sessionID}); err != nil {
			return err
		}
	}
	stdout, stderr, runErr := d.r.Run(ctx,
		"new-window",
		"-P", "-F", "#{pane_id}",
		"-t", CommanderSessionName+":",
		"-n", windowName,
		"-c", projectPath,
		opencodeCmd,
	)
	if runErr != nil {
		return fmt.Errorf("tmux new-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	chatPaneID := strings.TrimSpace(string(stdout))
	if chatPaneID == "" {
		return fmt.Errorf("tmux new-window: empty pane id")
	}

	return d.layoutThreeColumns(ctx, chatPaneID, projectPath,
		d.buildColumnCmd("cmdr-objectives"),
		d.buildColumnCmdWithFlags("operator-side", "--task", taskBeadID), "operator", "Operator | "+taskBeadID)
}

func (d *Director) rebuildOperatorWindow(ctx context.Context, taskBeadID, projectPath string) error {
	windowName := "op-" + flattenName(taskBeadID)
	if err := d.killWindowIfExists(ctx, CommanderSessionName, windowName); err != nil {
		return err
	}
	return d.createOperatorWindow(ctx, taskBeadID, projectPath)
}

func (d *Director) createCommanderFleetWindow(ctx context.Context, commanderHome string, sessionExists bool) error {
	opencodeCmd, claude, err := d.commanderChatCmd(ctx, commanderHome)
	if err != nil {
		return err
	}
	if claude != nil {
		if opencodeCmd, err = d.claudeLaunchCmd(ctx, *claude); err != nil {
			return err
		}
	}
	workspaceRoot, err := promptassets.CommanderWorkspaceRoot()
	if err != nil {
		return fmt.Errorf("commander workspace root: %w", err)
	}

	var stdout, stderr []byte
	if !sessionExists {
		stdout, stderr, err = d.r.Run(ctx,
			"new-session", "-d",
			"-P", "-F", "#{pane_id}",
			"-s", CommanderSessionName,
			"-n", FleetWindowName,
			"-c", workspaceRoot,
			opencodeCmd,
		)
		if err != nil {
			return fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	} else {
		stdout, stderr, err = d.r.Run(ctx,
			"new-window",
			"-P", "-F", "#{pane_id}",
			"-t", CommanderSessionName+":",
			"-n", FleetWindowName,
			"-c", workspaceRoot,
			opencodeCmd,
		)
		if err != nil {
			return fmt.Errorf("tmux new-window: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}

	chatPaneID := strings.TrimSpace(string(stdout))
	if chatPaneID == "" {
		return fmt.Errorf("tmux create commander window: empty pane id")
	}

	return d.layoutThreeColumns(ctx, chatPaneID, workspaceRoot,
		d.buildColumnCmd("cmdr-objectives"), d.buildColumnCmd("inspector"), "commander", "Commander")
}

// layoutThreeColumns arranges every window the same way around its chat pane:
// Workstreams column on the left, chat in the middle, context column on the
// right. The chat pane is tagged so other code can find it wherever it sits.
func (d *Director) layoutThreeColumns(ctx context.Context, chatPaneID, cwd, leftCmd, rightCmd, kind, title string) error {
	if _, stderr, err := d.r.Run(ctx, "set-option", "-p", "-t", chatPaneID, ChatPaneOption, "1"); err != nil {
		return fmt.Errorf("tmux set-option: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	// Solid backgrounds first, so no pane ever shows the wallpaper through.
	if err := d.applyWindowStyle(ctx, chatPaneID); err != nil {
		return err
	}
	leftSize, rightSize := []string{"-p", "25"}, []string{"-p", "33"}
	if w := d.windowWidth(ctx, chatPaneID); w > 0 {
		l, r := ColumnWidths(w)
		leftSize, rightSize = []string{"-l", fmt.Sprint(l)}, []string{"-l", fmt.Sprint(r)}
	}
	if _, stderr, err := d.r.Run(ctx, append([]string{
		"split-window", "-h", "-b",
		"-P", "-F", "#{pane_id}",
		"-t", chatPaneID},
		append(leftSize, "-c", cwd, leftCmd)...)...,
	); err != nil {
		return fmt.Errorf("tmux split-window left: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	if _, stderr, err := d.r.Run(ctx, append([]string{
		"split-window", "-h",
		"-P", "-F", "#{pane_id}",
		"-t", chatPaneID},
		append(rightSize, "-c", cwd, rightCmd)...)...,
	); err != nil {
		return fmt.Errorf("tmux split-window right: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	if err := d.setWindowTitle(ctx, chatPaneID, kind, title); err != nil {
		return err
	}
	if err := d.setWindowMeta(ctx, chatPaneID, kind, cwd); err != nil {
		return err
	}
	if err := d.addTitleBar(ctx, chatPaneID, cwd); err != nil {
		return err
	}
	if err := d.addPads(ctx, chatPaneID, cwd); err != nil {
		return err
	}
	if _, stderr, err := d.r.Run(ctx, "select-pane", "-t", chatPaneID); err != nil {
		return fmt.Errorf("tmux select-pane: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// windowWidth is the width of the window holding pane, or 0 if tmux won't say.
func (d *Director) windowWidth(ctx context.Context, pane string) int {
	out, _, err := d.r.Run(ctx, "display-message", "-p", "-t", pane, "#{window_width}")
	if err != nil {
		return 0
	}
	var w int
	if n, _ := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &w); n != 1 {
		return 0
	}
	return w
}

// Window options read by the title bar (cmdr __column chat-title).
const (
	TitleOption = "@cmdr_title"
	KindOption  = "@cmdr_kind"
	MetaOption  = "@cmdr_meta"
)

// WindowMeta is the right-aligned meta of the title bar: "<cwd> · <model>",
// with the home directory shown as "~" and the provider dropped from the model.
func WindowMeta(kind, cwd string) string {
	tier := agentrt.TierLead
	switch kind {
	case "commander":
		tier = agentrt.TierCommander
	case "operator":
		tier = agentrt.TierOperator
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if cwd == home {
			cwd = "~"
		} else if strings.HasPrefix(cwd, home+"/") {
			cwd = "~" + strings.TrimPrefix(cwd, home)
		}
	}
	var parts []string
	if cwd != "" {
		parts = append(parts, cwd)
	}
	if model := agentrt.ModelDisplay(agentrt.Model(tier)); model != "" {
		parts = append(parts, model) // the model actually running, e.g. "Sonnet 5.5"
	}
	return strings.Join(parts, " · ")
}

// setWindowMeta records the title bar's meta text (see WindowMeta).
func (d *Director) setWindowMeta(ctx context.Context, target, kind, cwd string) error {
	if _, stderr, err := d.r.Run(ctx, "set-option", "-w", "-t", target, MetaOption, WindowMeta(kind, cwd)); err != nil {
		return fmt.Errorf("tmux set-option %s: %w: %s", MetaOption, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// setWindowTitle records who the window's chat is: kind is commander, manager
// or operator; title is the exact bar text.
func (d *Director) setWindowTitle(ctx context.Context, target, kind, title string) error {
	for _, kv := range [][2]string{{KindOption, kind}, {TitleOption, title}} {
		if _, stderr, err := d.r.Run(ctx, "set-option", "-w", "-t", target, kv[0], kv[1]); err != nil {
			return fmt.Errorf("tmux set-option %s: %w: %s", kv[0], err, strings.TrimSpace(string(stderr)))
		}
	}
	return nil
}

// addTitleBar puts the title bar (TitleBarRows tall) pane directly above the chat. It does
// not take focus.
func (d *Director) addTitleBar(ctx context.Context, chatPane, cwd string) error {
	args := []string{"split-window", "-v", "-b", "-d", "-l", fmt.Sprint(TitleBarRows), "-P", "-F", "#{pane_id}", "-t", chatPane}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if _, stderr, err := d.r.Run(ctx, append(args, d.buildColumnCmd("chat-title"))...); err != nil {
		return fmt.Errorf("tmux split-window title bar: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (d *Director) windowCount(ctx context.Context, session string) (int, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-windows", "-t", session, "-F", "#W")
	if err != nil {
		return 0, fmt.Errorf("tmux list-windows: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return len(nonEmptyLines(string(stdout))), nil
}

func (d *Director) listWindowNames(ctx context.Context, session string) ([]string, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-windows", "-t", session, "-F", "#W")
	if err != nil {
		return nil, fmt.Errorf("tmux list-windows: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nonEmptyLines(string(stdout)), nil
}

func (d *Director) selectedWindowName(ctx context.Context, session string) (string, error) {
	stdout, stderr, err := d.r.Run(ctx, "display-message", "-p", "-t", session, "#W")
	if err != nil {
		return "", fmt.Errorf("tmux display-message: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

func (d *Director) killWindowIfExists(ctx context.Context, session, window string) error {
	winExists, err := d.WindowExists(ctx, session, window)
	if err != nil {
		return err
	}
	if !winExists {
		return nil
	}
	if _, stderr, runErr := d.r.Run(ctx, "kill-window", "-t", session+":"+window); runErr != nil {
		return fmt.Errorf("tmux kill-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (d *Director) projectManagerWindow(ctx context.Context, projectName string) (string, bool, error) {
	for _, windowName := range []string{managerWindowName(projectName), legacyManagerWindowName(projectName), legacyLeadWindowName(projectName)} {
		exists, err := d.WindowExists(ctx, CommanderSessionName, windowName)
		if err != nil {
			return "", false, err
		}
		if exists {
			return windowName, true, nil
		}
	}
	return "", false, nil
}

func (d *Director) rootWindow(ctx context.Context) (string, bool, error) {
	for _, windowName := range []string{FleetWindowName, legacyFleetWindowName} {
		exists, err := d.WindowExists(ctx, CommanderSessionName, windowName)
		if err != nil {
			return "", false, err
		}
		if exists {
			return windowName, true, nil
		}
	}
	return "", false, nil
}

func managerWindowName(projectName string) string {
	return "manager (" + projectName + ")"
}

func legacyManagerWindowName(projectName string) string {
	return "manager-" + flattenName(projectName)
}

func isManagerWindowName(windowName string) bool {
	return strings.HasPrefix(windowName, "manager (") || strings.HasPrefix(windowName, "manager-")
}

func parseManagerWindowProjectName(windowName string) string {
	if strings.HasPrefix(windowName, "manager (") && strings.HasSuffix(windowName, ")") {
		return strings.TrimSuffix(strings.TrimPrefix(windowName, "manager ("), ")")
	}
	if strings.HasPrefix(windowName, "manager-") {
		return strings.TrimPrefix(windowName, "manager-")
	}
	return windowName
}

func legacyLeadWindowName(projectName string) string {
	return "lead-" + flattenName(projectName)
}

func readProjectManagerSessionID(commanderDir string, kind agentrt.Kind) (sessionID string, sessionPath string, err error) {
	if kind == agentrt.Claude {
		sessionPath = filepath.Join(commanderDir, agentrt.SessionFileName("manager", agentrt.Claude))
		sessionID, err = readSessionIDFile(sessionPath)
		return sessionID, sessionPath, err
	}
	for _, sessionPath := range []string{
		filepath.Join(commanderDir, "manager.session-id"),
		filepath.Join(commanderDir, "lead.session-id"),
	} {
		sessionID, err := readSessionIDFile(sessionPath)
		if err == nil && strings.TrimSpace(sessionID) != "" {
			return sessionID, sessionPath, nil
		}
		if err == nil {
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return "", sessionPath, err
	}
	return "", filepath.Join(commanderDir, "manager.session-id"), nil
}

func nonEmptyLines(s string) []string {
	raw := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// attachOrSwitch attaches the current process to the named tmux session if
// we're outside tmux, or switches the existing client to it if we're inside
// tmux already. Detection: $TMUX env var is set when we're inside.
//
// In tests, attaching is always skipped; this is only called when attach=true.
func (d *Director) attachOrSwitch(ctx context.Context, name string) error {
	if d.insideTmuxFn != nil && d.insideTmuxFn() {
		if _, stderr, err := d.r.Run(ctx, "switch-client", "-t", name); err == nil {
			return nil
		} else {
			msg := strings.TrimSpace(string(stderr))
			if strings.Contains(msg, "no current client") {
				return nil
			}
			return fmt.Errorf("tmux switch-client: %w: %s", err, msg)
		}
	}
	if d.attachFn == nil {
		d.attachFn = attachSession
	}
	if err := d.attachFn(ctx, name); err != nil {
		return fmt.Errorf("tmux attach-session: %w", err)
	}
	return nil
}

func attachSession(ctx context.Context, name string) error {
	cmd := osx.CommandContext(ctx, "tmux", "attach-session", "-t", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// buildOpencodeResumeCmd builds the shell string used as the pane command:
// `opencode --session <id>`. The string is run by tmux through `sh -c` (not
// through this code), so we keep it simple and shell-safe.
func (d *Director) buildOpencodeResumeCmd(sessionID string) string {
	bin := d.opencodeBinary
	if bin == "" {
		bin = "opencode"
	}
	return fmt.Sprintf("%s --session %s", bin, sessionID)
}

// buildCommanderResumeCmd opens the top-level Commander session with the
// Commander agent explicitly selected. Blank imported sessions have no prior
// transcript to carry agent identity, so resuming must pass --agent.
func (d *Director) buildCommanderResumeCmd(sessionID string) string {
	bin := d.opencodeBinary
	if bin == "" {
		bin = "opencode"
	}
	return fmt.Sprintf("%s --session %s --agent %s", bin, sessionID, promptassets.CommanderAgentName)
}

// claudeChat names a Claude chat pane's launch.
type claudeChat struct{ agent, model, sessionID string }

func (d *Director) claudeChatCmd(c claudeChat) string {
	return agentrt.ClaudeInteractiveCmd(d.claudeBinary, c.agent, c.model, c.sessionID)
}

// claudeLaunchCmd is the command of a pane about to start: it first carries the
// session's newest transcript into ~/.claude, where the pane resumes from. The
// transcript may lie in another account or in the config dir an older pane
// inherited from the tmux server.
func (d *Director) claudeLaunchCmd(ctx context.Context, c claudeChat) (string, error) {
	home := agentrt.CommanderHome()
	if _, err := claudeacct.CarryTranscript(home, claudeacct.ProjectsDir(home, claudeacct.Default), c.sessionID, claudeacct.Default, claudeacct.TmuxConfigDir(ctx, d.r.Run)); err != nil {
		return "", err
	}
	return d.claudeChatCmd(c), nil
}

// commanderChatCmd returns the Commander chat pane command for the configured
// runtime, minting and persisting that runtime's session id if needed, and on
// Claude the chat it launches.
func (d *Director) commanderChatCmd(ctx context.Context, commanderHome string) (string, *claudeChat, error) {
	if agentrt.For(agentrt.TierCommander) == agentrt.Claude {
		sessionID, err := EnsureClaudeCommanderSessionID(ctx, commanderHome)
		if err != nil {
			return "", nil, fmt.Errorf("commander session id: %w", err)
		}
		c := claudeChat{promptassets.CommanderAgentName, agentrt.Model(agentrt.TierCommander), sessionID}
		return d.claudeChatCmd(c), &c, nil
	}
	template, err := CommanderPromptTemplate(ctx, commanderHome)
	if err != nil {
		return "", nil, err
	}
	if err = promptassets.EnsureAgentFor(agentrt.Opencode, template, promptassets.CommanderAgentName); err != nil {
		return "", nil, err
	}
	sessionID, err := EnsureCommanderSessionID(ctx, commanderHome, captureCommanderSessionFn)
	if err != nil {
		return "", nil, fmt.Errorf("commander session id: %w", err)
	}
	return d.buildCommanderResumeCmd(sessionID), nil, nil
}

// isRightContextColumn reports whether a pane command is the Commander window's
// right-hand column: the inspector, or the cmdr-fleet column older windows still
// run (accepted so an upgrade does not rebuild the window).
func isRightContextColumn(cmd string) bool {
	return strings.Contains(cmd, "cmdr __column inspector") || strings.Contains(cmd, "cmdr __column cmdr-fleet")
}

// buildColumnCmd builds the pane command for a no-flag `cmdr __column <kind>`.
func (d *Director) buildColumnCmd(kind string) string {
	bin := d.cmdrBinary
	if bin == "" {
		bin = "cmdr"
	}
	return fmt.Sprintf("%s __column %s", bin, kind)
}

// buildColumnCmdWithFlags builds the pane command for `cmdr __column <kind>
// <flag1> <val1> ...`. Used by per-project columns (`--project <name>`) and
// per-task columns (`--task <id>`).
func (d *Director) buildColumnCmdWithFlags(kind string, flags ...string) string {
	bin := d.cmdrBinary
	if bin == "" {
		bin = "cmdr"
	}
	parts := []string{bin, "__column", kind}
	parts = append(parts, flags...)
	return strings.Join(parts, " ")
}

// flattenName turns a project name or bead id into something safe for use as
// a tmux window name + filesystem path component. tmux window names allow
// most chars, but slashes confuse `select-window -t session:manager-foo/bar`,
// and shell-special chars are best avoided in pane commands. We collapse
// non-alnum into `-` and trim the result.
func flattenName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9':
			out = append(out, c)
		default:
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	// Trim trailing dash.
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}

func stateDBPath() (string, error) {
	if home := os.Getenv("COMMANDER_HOME"); home != "" {
		return filepath.Join(home, "state.db"), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".commander", "state.db"), nil
}

func listStartupProjects(ctx context.Context) ([]store.Project, error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, fmt.Errorf("resolve state dir: %w", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		if os.IsNotExist(err) {
			if _, pgErr := os.Lstat(filepath.Join(filepath.Dir(statePath), "state.postgres")); pgErr != nil {
				if os.IsNotExist(pgErr) {
					return nil, nil
				}
				return nil, pgErr
			}
		} else {
			return nil, fmt.Errorf("stat state db: %w", err)
		}
	}
	st, err := store.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()
	return st.ListProjects(ctx)
}

func ensureManagerReadyForProject(ctx context.Context, project store.Project) error {
	statePath, err := stateDBPath()
	if err != nil {
		return fmt.Errorf("resolve state dir: %w", err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()
	err = activateStartupProjectFn(ctx, st, project)
	if err != nil && hasProjectManagerSessionID(project.Path) {
		return nil
	}
	return err
}

func activateStartupProject(ctx context.Context, st *store.Store, project store.Project) error {
	reg, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		return fmt.Errorf("agent-mail client: %w", err)
	}
	activator := activation.New(st, opencode.New(), reg, commanderRepoPath())
	_, err = activator.Activate(ctx, activation.ActivateRequest{
		Name: project.Name,
		Type: project.Type,
		Path: project.Path,
	})
	return err
}

// commanderRepoPath resolves the commander checkout used as the fleet am-project
// key: $COMMANDER_REPO, else ~/code/commander.
func commanderRepoPath() string {
	if v := os.Getenv("COMMANDER_REPO"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "code", "commander")
	}
	return ""
}

// readOperatorSessionID returns the operator's session id and the runtime it
// was spawned under. The configured runtime's file wins; the other runtime's
// file is the fallback so operators spawned before a switch stay openable.
func readOperatorSessionID(operatorsDir, flatTask string) (string, agentrt.Kind, string, error) {
	kinds := []agentrt.Kind{agentrt.Opencode, agentrt.Claude}
	if agentrt.For(agentrt.TierOperator) == agentrt.Claude {
		kinds = []agentrt.Kind{agentrt.Claude, agentrt.Opencode}
	}
	for _, kind := range kinds {
		path := filepath.Join(operatorsDir, agentrt.SessionFileName(flatTask, kind))
		id, err := readSessionIDFile(path)
		if err != nil {
			return "", kind, path, err
		}
		if id != "" {
			return id, kind, path, nil
		}
	}
	return "", kinds[0], filepath.Join(operatorsDir, agentrt.SessionFileName(flatTask, kinds[0])), nil
}

func hasProjectManagerSessionID(projectPath string) bool {
	sessionID, _, err := readProjectManagerSessionID(filepath.Join(projectPath, ".commander"), agentrt.For(agentrt.TierLead))
	return err == nil && strings.TrimSpace(sessionID) != ""
}
