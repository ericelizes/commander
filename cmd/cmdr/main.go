// Command cmdr is the commander CLI entry point.
//
// Post-pivot architecture: cmdr is a tmux-session orchestrator. opencode owns
// chat in every pane natively; cmdr lays out the panes and renders the chrome
// columns inside them.
//
// Surface:
//   - (no args)                 Launch (or attach to) the Commander tmux session;
//     this, --project, --task, root, restart and reload refuse (exit 2) for an agent or with no terminal
//   - --project X / -p X        Add or focus the Manager window for project X
//   - --task ID / -t ID         Add or focus the Operator window for task ID
//   - install                   Copy staged binaries into place (explicit; see install_cmd.go)
//   - update                    Rebuild and reinstall cmdr from the Commander repo
//   - reload                    Update cmdr, then restart the Commander session
//   - refresh                   Recreate currently open Commander-owned windows
//   - restart                   Start a fresh Commander brain
//   - tab add|rm|ls|focus|move  Manage the inspector (right column) tabs
//   - browser login|status      Sign the inspector's screenshot profile in
//   - runtime <role> [claude|codex]  Print or switch the agent CLI a chat runs on
//   - account list|add|use|auto      Claude accounts: list with usage, add, switch a role, auto-switch
//   - __column <kind> [flags]   Run a single column renderer (internal use)
//   - internal-add-project ... Add a project (called by the Commander chat agent)
//   - internal-resolve-add-project
//     Resolve state/search outcome for bare add requests
//   - --version / -v / version  Print the version
//   - --help / -h / help        Print this help
//
// We deliberately use stdlib `flag` instead of Cobra. The CLI surface is
// small, and Cobra would add ceremony (and a dependency) that earns nothing
// at this stage.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/cli"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wsmgr"
)

type publicTargetKind int

const (
	targetCommander publicTargetKind = iota
	targetProject
	targetTask
)

type publicTarget struct {
	kind    publicTargetKind
	project string
	task    string
}

type resolvedOpenTarget struct {
	kind        publicTargetKind
	projectName string
	projectPath string
	taskBeadID  string
}

var (
	newDirectorFn        = layout.NewDirector
	resolveProjectFn     = resolveProjectPath
	resolveProjectRecFn  = resolveProject
	resolveTaskTargetFn  = resolveTaskOpenTarget
	ensureProjectReadyFn = ensureProjectManagerReady
	runUpdateCommandFn   = runUpdateCommand
	reloadRestartFn      = restartWithInstalledBinary
	stdioIsTerminalFn    = stdioIsTerminal
)

// cockpitNoTerminalEnv=1 waives the terminal check, never the role check:
// scripts/cockpit-test.sh creates its detached, isolated cockpit this way.
const cockpitNoTerminalEnv = "COMMANDER_COCKPIT_NO_TERMINAL"

func stdioIsTerminal() (stdin, stdout bool) {
	return term.IsTerminal(os.Stdin.Fd()), term.IsTerminal(os.Stdout.Fd())
}

// cockpitRefusal says why this process must not build or open the cockpit,
// empty when it may: an agent running cmdr to read its help text once built a
// whole cockpit holding every stopped Manager's session.
func cockpitRefusal() string {
	for _, name := range []string{"COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE"} {
		if role := os.Getenv(name); role != "" {
			return name + " is set (" + role + "), so this is an agent"
		}
	}
	if os.Getenv(cockpitNoTerminalEnv) == "1" {
		return ""
	}
	switch stdin, stdout := stdioIsTerminalFn(); {
	case !stdin && !stdout:
		return "stdin and stdout are not a terminal"
	case !stdin:
		return "stdin is not a terminal"
	case !stdout:
		return "stdout is not a terminal"
	}
	return ""
}

// refuseCockpit prints the refusal, if any, and reports whether it did.
func refuseCockpit(stderr io.Writer) bool {
	why := cockpitRefusal()
	if why == "" {
		return false
	}
	fmt.Fprintf(stderr, "cmdr: refusing to open the cockpit: %s\n", why)
	fmt.Fprintln(stderr, "usage: cmdr [--project <name> | --task <id> | root | restart | reload], from your own terminal")
	fmt.Fprintln(stderr, "run 'cmdr --help' for every command")
	return true
}

func main() {
	chat.MaybeRunCodexBridge() // `cmdr __codex-bridge ...`: a Codex Operator's stream-json bridge
	installWorkstreamManagerHook()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	args, leaveUnheldReason = stripLeaveUnheld(args)
	if len(args) == 0 {
		return openPublicTarget(publicTarget{kind: targetCommander}, stderr)
	}

	switch args[0] {
	case "--version", "-v", "version":
		cli.PrintVersion(stdout)
		return 0
	case "--help", "-h", "help":
		cli.PrintHelp(stdout)
		return 0
	case "root", "projects":
		return focusCommanderRoot(stderr)
	case "app":
		return runApp(args[1:], stdout, stderr)
	case "install":
		return runInstall(args[1:], stdout, stderr, os.Stdin)
	case "update":
		return updateCmdr(args[1:], stdout, stderr)
	case "reload":
		return reloadCmdr(args[1:], stdout, stderr)
	case "refresh":
		return refreshLayout(args[1:], stderr)
	case "restart":
		return restartCommander(args[1:], stderr)
	case "__column":
		// Internal: each tmux pane runs one of these to render its chrome.
		return runColumn(args[1:], stderr)
	case "bead", "beads-compat":
		return runBeadsCompat(args[1:], stdout, stderr)
	case "supervision":
		return runSupervision(args[1:], stdout, stderr)
	case "jurisdiction":
		return runJurisdiction(args[1:], stdout, stderr)
	case "execution":
		return runExecution(args[1:], stdout, stderr)
	case "orchestration":
		return runOrchestration(args[1:], stdout, stderr)
	case "plan":
		return runPlan(args[1:], stdout, stderr)
	case "decision":
		return runDecision(args[1:], stdout, stderr)
	case "question":
		return runQuestion(args[1:], stdout, stderr)
	case "storage":
		return runStoragePostgres(args[1:], stdout, stderr)
	case "board":
		// The board as the user's sidebar shows it, for the agents.
		return runBoard(args[1:], stdout, stderr)
	case "graph":
		// Graph-vs-beads consistency check (read-only).
		return runGraph(args[1:], stdout, stderr)
	case "pitches":
		return runPitches(args[1:], stdout, stderr)
	case "decisions":
		// The user's standing decisions, carried into every Commander and Manager turn.
		return runDecisions(args[1:], stdout, stderr)
	case "proposals":
		// Commander's review of the Managers' proposed gates, asks, reviews, waits and parks.
		return runProposals(args[1:], stdout, stderr)
	case "tab":
		// Inspector tabs: Managers (and the user) pin PRs, issues, specs, previews.
		return runTab(args[1:], stdout, stderr)
	case "project":
		return runProject(args[1:], stdout, stderr)
	case "worktrees":
		// Cleanup of finished Operator worktrees (dry run by default).
		return runWorktrees(args[1:], stdout, stderr)
	case "runtime":
		// Which agent CLI (Claude or Codex) a chat runs on.
		return runRuntime(args[1:], stdout, stderr)
	case "account":
		// Which Claude account a chat runs on.
		return runAccount(args[1:], stdout, stderr)
	case "fresh":
		// A clean restart: a new session with today's prompt that reads its predecessor first.
		return runFresh(args[1:], stdout, stderr)
	case "place":
		// Paired places (the user's Mac): pair/revoke/list, and the run verbs on the Mac.
		return runPlace(args[1:], stdout, stderr)
	case "internal-operator-provider":
		// Respawn a running Operator on another account or runtime.
		return runInternalOperatorProvider(args[1:], stdout, stderr)
	case "linear":
		return runLinear(args[1:], os.Stdin, stdout, stderr)
	case "browser":
		// Sign the inspector's screenshot profile in to Notion, Linear, ...
		return runBrowser(args[1:], stdout, stderr)
	case "internal-add-project", "internal-open-project", "internal-activate":
		// Internal subcommand the Commander chat agent shells out to.
		return runInternalOpenProject(args[1:], stdout, stderr)
	case "internal-comment":
		return runInternalComment(args[1:], os.Stdin, stdout, stderr)
	case "internal-phase":
		return runInternalPhase(args[1:], stdout, stderr)
	case "internal-label":
		return runInternalLabel(args[1:], stdout, stderr)
	case "internal-restore-approved-context":
		return runInternalRestoreApprovedContext(args[1:], stdout, stderr)
	case "internal-update":
		return runInternalUpdate(args[1:], os.Stdin, stdout, stderr)
	case "internal-dep":
		return runInternalDep(args[1:], stdout, stderr)
	case "internal-create-objective":
		return runInternalCreateObjective(args[1:], stdout, stderr)
	case "internal-rename-objective":
		return runInternalRenameObjective(args[1:], stdout, stderr)
	case "internal-close-objective":
		return runInternalCloseObjective(args[1:], stdout, stderr)
	case "internal-gate":
		return runInternalGate(args[1:], stdout, stderr)
	case "internal-ship":
		return runInternalShip(args[1:], stdout, stderr)
	case "internal-slot":
		return runInternalSlot(args[1:], stdout, stderr)
	case "internal-operator-guard":
		// Claude Code PreToolUse hook on Bash for Operators: full suites only inside the build slot.
		return runInternalOperatorGuardMain(args[1:], stdout)
	case "__shot":
		return runShot(args[1:], stdout, stderr)
	case "__sidebar-key":
		return runSidebarKey(args[1:], stderr)
	case "__relayout":
		// Called by tmux hooks (client-resized / client-attached): put every window's
		// columns back to their absolute widths.
		release, ok := relayoutLock()
		if !ok {
			return 0 // another relayout is running; it applies the same widths
		}
		defer release()
		if err := newDirectorFn().Relayout(context.Background()); err != nil {
			fmt.Fprintf(stderr, "cmdr __relayout: %v\n", err)
			return 1
		}
		return 0
	case "internal-adopt-manager":
		return runInternalAdoptManager(args[1:], stdout, stderr)
	case "internal-sync-windows":
		return runInternalSyncWindows(stdout, stderr)
	case "internal-unregister-workstream":
		return runInternalUnregisterWorkstream(args[1:], stdout, stderr)
	case "internal-open-workstream":
		return runInternalOpenWorkstream(args[1:], stdout, stderr)
	case "internal-spawn-operator":
		return runInternalSpawnOperator(args[1:], stdout, stderr)
	case "internal-move-objective":
		return runInternalMoveObjective(args[1:], stdout, stderr)
	case "internal-move-card":
		return runInternalMoveCard(args[1:], stdout, stderr)
	case "internal-resolve-needs":
		return runInternalResolveNeeds(args[1:], stdout, stderr)
	case "internal-needs-you":
		return runInternalNeedsYou(args[1:], stdout, stderr)
	case "internal-workstreams":
		return runInternalWorkstreams(stdout, stderr)
	case "internal-workflow-gaps":
		return runInternalWorkflowGaps(args[1:], stdout, stderr)
	case "workflow":
		return runWorkflow(args[1:], stdout, stderr)
	case "internal-migrate-workstreams":
		return runInternalMigrateWorkstreams(args[1:], stdout, stderr)
	case "internal-place":
		return runInternalPlace(args[1:], stdout, stderr)
	case "internal-track":
		return runInternalTrack(args[1:], stdout, stderr)
	case "internal-set-objective":
		return runInternalSetObjective(args[1:], stdout, stderr)
	case "internal-mail":
		// Role-aware mail send (Commander/Manager agents shell out to this).
		return runInternalMail(args[1:], stdout, stderr)
	case "internal-checkin":
		// A Manager sets when it next checks in on its running Operators.
		return runInternalCheckin(args[1:], stdout, stderr)
	case "internal-nest-managers":
		// Commander/user: turn a project's nested Managers on or off (dry run by default).
		return runInternalNestManagers(args[1:], stdout, stderr)
	case "internal-inbox":
		// Role-aware inbox read (resolves role -> am name, fetches via server).
		return runInternalInbox(args[1:], stdout, stderr)
	case "internal-state-hook":
		return runInternalStateHook(args[1:], os.Stdin)
	case "internal-mail-hook":
		// Claude Code UserPromptSubmit/Stop hook: injects unread mail. Never fails.
		return runInternalMailHookMain(args[1:], stdout)
	case "internal-manager-guard":
		// Claude Code PreToolUse hook on Bash for Manager roles: refuses doing the work.
		return runInternalManagerGuardMain(args[1:], stdout)
	case "internal-resolve-add-project", "internal-resolve-open-project", "internal-resolve-activation":
		// Internal subcommand the Commander chat agent uses for pathless project-add resolution.
		return runInternalResolveOpenProject(args[1:], stdout, stderr)
	default:
		target, code := parsePublicTarget(args, stderr)
		if code != 0 {
			return code
		}
		return openPublicTarget(target, stderr)
	}
}

func parsePublicTarget(args []string, stderr *os.File) (publicTarget, int) {
	fs := flag.NewFlagSet("cmdr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project slug (must already be activated)")
	projectShort := fs.String("p", "", "project slug (must already be activated)")
	task := fs.String("task", "", "task bead id (e.g. fro-001)")
	taskShort := fs.String("t", "", "task bead id (e.g. fro-001)")
	if err := fs.Parse(args); err != nil {
		return publicTarget{}, 2
	}

	projectValue := firstNonEmpty(*project, *projectShort)
	taskValue := firstNonEmpty(*task, *taskShort)

	if projectValue != "" && taskValue != "" {
		fmt.Fprintln(stderr, "cmdr: --project and --task are mutually exclusive")
		return publicTarget{}, 2
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(stderr, "cmdr: unknown command or argument: %s\n", rest[0])
		fmt.Fprintln(stderr, "run 'cmdr --help' for usage")
		return publicTarget{}, 2
	}
	if projectValue != "" {
		return publicTarget{kind: targetProject, project: projectValue}, 0
	}
	if taskValue != "" {
		return publicTarget{kind: targetTask, task: taskValue}, 0
	}
	return publicTarget{kind: targetCommander}, 0
}

// reportLaunchError prints a launch failure and, when the cause is opencode
// being unavailable/misconfigured, an actionable hint instead of just the raw
// error chain. Returns the process exit code (always 1).
func reportLaunchError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "cmdr: %v\n", err)
	if errors.Is(err, ErrOpencodeUnavailable) {
		fmt.Fprint(stderr, opencodeHint)
	}
	return 1
}

const opencodeHint = `
Commander runs its chat panes through opencode, so it cannot launch without a
working opencode. Check:
  • opencode is installed and on PATH:   opencode --version
  • your opencode config is valid:       opencode mcp list
    config lives at ~/.opencode/opencode.json (top-level key is "mcp", not "mcpServers")
`

func openPublicTarget(target publicTarget, stderr *os.File) int {
	if refuseCockpit(stderr) {
		return 2
	}
	resolved, err := resolveOpenTarget(target)
	if err != nil {
		return reportLaunchError(stderr, err)
	}
	d := newDirectorFn()
	if err := dispatchOpenTarget(context.Background(), d, resolved, true); err != nil {
		return reportLaunchError(stderr, err)
	}
	return 0
}

func focusCommanderRoot(stderr *os.File) int {
	if refuseCockpit(stderr) {
		return 2
	}
	d := newDirectorFn()
	if err := d.FocusCommanderRoot(context.Background(), "", true); err != nil {
		return reportLaunchError(stderr, err)
	}
	return 0
}

func resolveOpenTarget(target publicTarget) (resolvedOpenTarget, error) {
	switch target.kind {
	case targetCommander:
		return resolvedOpenTarget{kind: targetCommander}, nil
	case targetProject:
		project, err := resolveProjectRecFn(target.project)
		if err != nil {
			return resolvedOpenTarget{}, err
		}
		if err := ensureProjectReadyFn(project); err != nil {
			return resolvedOpenTarget{}, err
		}
		return resolvedOpenTarget{
			kind:        targetProject,
			projectName: target.project,
			projectPath: project.Path,
		}, nil
	case targetTask:
		return resolveTaskTargetFn(target.task)
	default:
		return resolvedOpenTarget{}, fmt.Errorf("unknown target kind %d", target.kind)
	}
}

func dispatchOpenTarget(ctx context.Context, d *layout.Director, target resolvedOpenTarget, attach bool) error {
	switch target.kind {
	case targetCommander:
		return d.LaunchCommander(ctx, "", attach)
	case targetProject:
		if workstreamMode {
			opener, cleanup, err := newWorkstreamOpener()
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = opener.OpenProject(ctx, target.projectName)
			if errors.Is(err, wsmgr.ErrNoWorkstreams) {
				// Not a failure: there is simply no Manager to show yet.
				fmt.Fprintf(os.Stderr, "note: %v\n", err)
				return nil
			}
			return err
		}
		return d.LaunchLead(ctx, target.projectName, target.projectPath, attach)
	case targetTask:
		return d.LaunchOperator(ctx, target.taskBeadID, target.projectName, target.projectPath, attach)
	default:
		return fmt.Errorf("unknown target kind %d", target.kind)
	}
}

func refreshLayout(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(stderr, "cmdr refresh: unknown argument: %s\n", rest[0])
		return 2
	}
	d := newDirectorFn()
	taskResolver := func(taskBeadID string) (string, string, error) {
		target, err := resolveTaskTargetFn(taskBeadID)
		if err != nil {
			return "", "", err
		}
		return target.projectName, target.projectPath, nil
	}
	if err := d.Refresh(context.Background(), "", resolveProjectFn, taskResolver); err != nil {
		fmt.Fprintf(stderr, "cmdr refresh: %v\n", err)
		return 1
	}
	return 0
}

func restartCommander(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hard := fs.Bool("hard", false, "kill the whole cockpit (every Claude pane) and relaunch it, resuming sessions")
	keepChats := fs.Bool("keep-chats", false, "soft restart only: never restart a chat pane, even if its launch command changed")
	fresh := fs.Bool("fresh", false, "like --hard, but start a brand-new Commander chat instead of resuming it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(stderr, "cmdr restart: unknown argument: %s\n", rest[0])
		return 2
	}
	if refuseCockpit(stderr) {
		return 2
	}
	d := newDirectorFn()
	d.SetProgressWriter(stderr)
	fmt.Fprintln(stderr, "Restarting Commander...")
	d.SetKeepChats(*keepChats)
	// Default is the soft restart: reload chrome, keep running Claude sessions.
	restart := d.SoftRestart
	switch {
	case *fresh:
		restart = d.RestartFresh
	case *hard:
		restart = d.Restart
	}
	if err := restart(context.Background(), "", true); err != nil {
		fmt.Fprintf(stderr, "cmdr restart: %v\n", err)
		return 1
	}
	return 0
}

func updateCmdr(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(stderr, "cmdr update: unknown argument: %s\n", rest[0])
		return 2
	}

	repo := resolveCommanderRepo()
	if repo == "" {
		fmt.Fprintln(stderr, "cmdr update: could not resolve commander repo; set COMMANDER_REPO")
		return 1
	}
	scriptPath := repo + "/install/install.sh"
	if _, err := os.Stat(scriptPath); err != nil {
		fmt.Fprintf(stderr, "cmdr update: install script missing at %s: %v\n", scriptPath, err)
		return 1
	}
	if err := runUpdateCommandFn(context.Background(), stdout, stderr, repo, scriptPath); err != nil {
		fmt.Fprintf(stderr, "cmdr update: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Updated cmdr. Run `cmdr restart` to start a fresh Commander session with the new binary.")
	return 0
}

func reloadCmdr(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(stderr, "cmdr reload: unknown argument: %s\n", rest[0])
		return 2
	}
	if refuseCockpit(stderr) {
		return 2
	}

	repo := resolveCommanderRepo()
	if repo == "" {
		fmt.Fprintln(stderr, "cmdr reload: could not resolve commander repo; set COMMANDER_REPO")
		return 1
	}
	scriptPath := repo + "/install/install.sh"
	if _, err := os.Stat(scriptPath); err != nil {
		fmt.Fprintf(stderr, "cmdr reload: install script missing at %s: %v\n", scriptPath, err)
		return 1
	}
	if err := runUpdateCommandFn(context.Background(), stdout, stderr, repo, scriptPath); err != nil {
		fmt.Fprintf(stderr, "cmdr reload: update failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "Reloading Commander: restarting session and opening linked manager tabs...")
	if err := reloadRestartFn(context.Background(), stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "cmdr reload: restart failed: %v\n", err)
		return 1
	}
	return 0
}

func runUpdateCommand(ctx context.Context, stdout, stderr *os.File, repo, scriptPath string) error {
	cmd := exec.CommandContext(ctx, "bash", scriptPath, "--update")
	cmd.Dir = repo
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if out.Len() > 0 {
			_, _ = stderr.Write(out.Bytes())
		}
		return err
	}
	return nil
}

func restartWithInstalledBinary(ctx context.Context, stdout, stderr *os.File) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "restart")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Run()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// runColumn dispatches the right column renderer based on kind. Each renderer
// is a Bubble Tea program that owns the pane's stdout until the user quits
// (q / Ctrl-C). Errors propagate as exit 1.
func runColumn(args []string, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "cmdr __column: missing column kind")
		return 2
	}
	kind := args[0]
	rest := args[1:]
	routeColumnLogs()

	switch kind {
	case "cmdr-objectives":
		// The panel is identical in every window; --project (older layouts) is ignored.
		if err := tui.RunObjectivesColumn(); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "pad":
		if err := tui.RunPadColumn(); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "chat-title":
		if err := tui.RunChatTitleColumn(); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "inspector":
		// The right column: tabs for PRs, issues, specs, previews. In a workstream
		// window it carries --ws and --project; in window 0 it has no flags.
		if err := tui.RunInspectorColumn(optionalFlag(rest, "project"), optionalFlag(rest, "ws")); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "cmdr-fleet":
		if err := tui.RunFleetColumn(); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "lead-tasks":
		project, code := requireFlag(rest, "project", stderr)
		if code != 0 {
			return code
		}
		if err := tui.RunLeadTasksColumn(project); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "lead-fleet":
		project, code := requireFlag(rest, "project", stderr)
		if code != 0 {
			return code
		}
		if err := tui.RunLeadFleetColumn(project); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	case "operator-side":
		task, code := requireFlag(rest, "task", stderr)
		if code != 0 {
			return code
		}
		if err := tui.RunOperatorSideColumn(task); err != nil {
			fmt.Fprintf(stderr, "cmdr __column %s: %v\n", kind, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "cmdr __column: unknown kind: %s\n", kind)
		return 2
	}
}

// optionalFlag returns --<name>'s value from args ("--name v" or "--name=v"), or ""
// when absent. It scans instead of using a FlagSet so other flags in args (e.g.
// "--ws X --project Y") are ignored rather than failing the whole parse.
func optionalFlag(args []string, name string) string {
	long := "--" + name
	for i, a := range args {
		if a == long && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, long+"="); ok {
			return v
		}
	}
	return ""
}

// requireFlag pulls --<name> <value> out of args using a fresh FlagSet.
// Returns (value, 0) on success or ("", 2) on missing/invalid flags. The
// helper exists so the dispatch in runColumn isn't repetitive.
func requireFlag(args []string, name string, stderr *os.File) (string, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	v := fs.String(name, "", "")
	if err := fs.Parse(args); err != nil {
		return "", 2
	}
	if *v == "" {
		fmt.Fprintf(stderr, "cmdr __column: --%s is required\n", name)
		return "", 2
	}
	return *v, 0
}

// routeColumnLogs sends the standard logger to a file: a column owns its tmux
// pane, so anything logged to stderr would be printed into the sidebar itself.
func routeColumnLogs() {
	home := os.Getenv("COMMANDER_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			log.SetOutput(io.Discard)
			return
		}
		home = filepath.Join(h, ".commander")
	}
	dir := filepath.Join(home, "logs")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "columns.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.SetOutput(io.Discard)
		return
	}
	log.SetOutput(f)
}
