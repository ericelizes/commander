package layout

import (
	"context"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
)

// WorkstreamOption is the tmux window option holding a workstream Manager
// window's workstream bead id. Windows are found by it, never by name: the name
// carries the workstream's current rank, which changes when the user reorders.
const WorkstreamOption = "@cmdr_ws"

// WorkstreamWindow describes one workstream Manager's window.
type WorkstreamWindow struct {
	ProjectName string
	ProjectPath string
	BeadID      string // workstream bead id
	Title       string
	Rank        int
	AgentName   string // claude -n name; also the prompt file name
	SessionID   string
}

// workstreamWindowsFn lists every open workstream, ready to open (session,
// prompt, mail registration done). cmdr wires it at startup so layout does not
// import the beads/mail stack. nil keeps the legacy one-Manager-per-project
// behaviour.
var workstreamWindowsFn func(ctx context.Context) ([]WorkstreamWindow, error)

// SetWorkstreamWindowsFn installs the provider used by cmdr start/restart to
// bring back a Manager window for every workstream.
func SetWorkstreamWindowsFn(fn func(ctx context.Context) ([]WorkstreamWindow, error)) {
	workstreamWindowsFn = fn
}

// ManagerTitle is the exact title-bar text in a workstream window.
func ManagerTitle(workstreamTitle string) string {
	return "Manager | " + strings.Join(strings.Fields(workstreamTitle), " ")
}

// WorkstreamWindowName is the cosmetic window title: "#<rank> <short title>".
func WorkstreamWindowName(rank int, title string) string {
	// tmux rejects ':' (and '.') in window names, so "Audit tracker: script
	// audits" must not carry them or the window silently fails to open.
	title = strings.NewReplacer(":", "", ".", "").Replace(title)
	title = strings.Join(strings.Fields(title), " ")
	if i := strings.Index(title, " ("); i > 0 && strings.HasSuffix(title, ")") {
		title = title[:i] // drop a trailing "(TEAM-1234)" ticket
	}
	r := []rune(title)
	if len(r) > 22 {
		title = strings.TrimSpace(string(r[:21])) + "…"
	}
	if rank <= 0 {
		return title
	}
	return fmt.Sprintf("#%d %s", rank, title)
}

// FindWorkstreamWindow returns the tmux window id of the workstream's Manager
// window, or "" when none is open.
func (d *Director) FindWorkstreamWindow(ctx context.Context, beadID string) (string, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-windows", "-t", CommanderSessionName, "-F", "#{window_id} #{"+WorkstreamOption+"}")
	if err != nil {
		s := strings.TrimSpace(string(stderr))
		if strings.Contains(s, "can't find session") || strings.Contains(s, "no server running") {
			return "", nil
		}
		return "", fmt.Errorf("tmux list-windows: %w: %s", err, s)
	}
	for _, line := range nonEmptyLines(string(stdout)) {
		id, opt, _ := strings.Cut(strings.TrimSpace(line), " ")
		if opt == beadID {
			return id, nil
		}
	}
	return "", nil
}

// EnsureWorkstreamWindow opens the workstream Manager's window if it is not
// already open (three columns: Workstreams | chat | context) and returns its
// chat pane. created reports whether a new window (and so a freshly started
// Claude) was made.
func (d *Director) EnsureWorkstreamWindow(ctx context.Context, w WorkstreamWindow) (pane string, created bool, err error) {
	if err := d.ensureCommanderRoot(ctx, "", false); err != nil {
		return "", false, err
	}
	if id, err := d.FindWorkstreamWindow(ctx, w.BeadID); err != nil {
		return "", false, err
	} else if id != "" {
		pane, err := d.chatPaneOf(ctx, id)
		return pane, false, err
	}
	if agentrt.For(agentrt.TierLead) != agentrt.Claude {
		return "", false, fmt.Errorf("workstream Managers need the claude runtime (runtime for the lead tier is opencode)")
	}
	chatCmd, err := d.claudeLaunchCmd(ctx, claudeChat{w.AgentName, agentrt.Model(agentrt.TierLead), w.SessionID})
	if err != nil {
		return "", false, err
	}
	stdout, stderr, runErr := d.r.Run(ctx,
		"new-window", "-d",
		"-P", "-F", "#{pane_id}",
		"-t", CommanderSessionName+":",
		"-n", WorkstreamWindowName(w.Rank, w.Title),
		"-c", w.ProjectPath,
		chatCmd,
	)
	if runErr != nil {
		return "", false, fmt.Errorf("tmux new-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	chatPaneID := strings.TrimSpace(string(stdout))
	if chatPaneID == "" {
		return "", false, fmt.Errorf("tmux new-window: empty pane id")
	}
	if _, stderr, runErr := d.r.Run(ctx, "set-option", "-w", "-t", chatPaneID, WorkstreamOption, w.BeadID); runErr != nil {
		return "", false, fmt.Errorf("tmux set-option %s: %w: %s", WorkstreamOption, runErr, strings.TrimSpace(string(stderr)))
	}
	if err := d.layoutThreeColumns(ctx, chatPaneID, w.ProjectPath,
		d.buildColumnCmd("cmdr-objectives"),
		d.buildColumnCmdWithFlags("inspector", "--ws", w.BeadID, "--project", w.ProjectName), "manager", ManagerTitle(w.Title)); err != nil {
		return "", false, err
	}
	return chatPaneID, true, nil
}

// FocusWorkstream selects the workstream Manager's window.
func (d *Director) FocusWorkstream(ctx context.Context, beadID string) error {
	id, err := d.FindWorkstreamWindow(ctx, beadID)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("no window open for workstream %s", beadID)
	}
	if _, stderr, runErr := d.r.Run(ctx, "select-window", "-t", id); runErr != nil {
		return fmt.Errorf("tmux select-window: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// TagWorkstreamWindow marks an existing window (found by its current name) as
// the workstream's Manager window and renames it. Used to adopt a live
// per-project Manager without restarting it.
func (d *Director) TagWorkstreamWindow(ctx context.Context, currentName, beadID string, rank int, title string) error {
	target := CommanderSessionName + ":" + currentName
	if _, stderr, err := d.r.Run(ctx, "set-option", "-w", "-t", target, WorkstreamOption, beadID); err != nil {
		return fmt.Errorf("tmux set-option on %q: %w: %s", currentName, err, strings.TrimSpace(string(stderr)))
	}
	if _, stderr, err := d.r.Run(ctx, "rename-window", "-t", target, WorkstreamWindowName(rank, title)); err != nil {
		return fmt.Errorf("tmux rename-window %q: %w: %s", currentName, err, strings.TrimSpace(string(stderr)))
	}
	return d.setWindowTitle(ctx, target, "manager", ManagerTitle(title))
}

// SyncWorkstreamNames brings every cockpit window in line with the current
// ranks: Manager windows are named "#<rank> <title>" and numbered by rank,
// Commander is window 0, and any other window (operators) goes after them.
// names: workstream bead id -> {Rank, Title}.
func (d *Director) SyncWorkstreamNames(ctx context.Context, names map[string]WorkstreamWindow) error {
	stdout, stderr, err := d.r.Run(ctx, "list-windows", "-t", CommanderSessionName, "-F", "#{window_id}|#{window_index}|#{"+WorkstreamOption+"}|#{window_name}")
	if err != nil {
		s := strings.TrimSpace(string(stderr))
		if strings.Contains(s, "can't find session") || strings.Contains(s, "no server running") {
			return nil
		}
		return fmt.Errorf("tmux list-windows: %w: %s", err, s)
	}
	type win struct {
		id, bead, name string
		index          int
	}
	var wins []win
	for _, line := range nonEmptyLines(string(stdout)) {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 4)
		if len(parts) < 4 {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(parts[1], "%d", &idx); err != nil {
			continue
		}
		wins = append(wins, win{id: parts[0], index: idx, bead: parts[2], name: parts[3]})
	}

	// Names.
	for _, w := range wins {
		info, ok := names[w.bead]
		if !ok || w.bead == "" {
			continue
		}
		if want := WorkstreamWindowName(info.Rank, info.Title); w.name != want {
			if _, stderr, err := d.r.Run(ctx, "rename-window", "-t", w.id, want); err != nil {
				return fmt.Errorf("tmux rename-window: %w: %s", err, strings.TrimSpace(string(stderr)))
			}
		}
		if info.Title != "" {
			// The title bar reads this; it follows renames without a respawn.
			if err := d.setWindowTitle(ctx, w.id, "manager", ManagerTitle(info.Title)); err != nil {
				return err
			}
		}
	}

	// Numbers: Commander 0, Managers by rank, everything else after the ranks.
	desired := make(map[string]int, len(wins))
	used := map[int]bool{}
	maxRank := 0
	for _, w := range wins {
		switch info, ok := names[w.bead]; {
		case w.bead != "" && ok && info.Rank > 0 && !used[info.Rank]:
			desired[w.id] = info.Rank
			used[info.Rank] = true
			if info.Rank > maxRank {
				maxRank = info.Rank
			}
		case w.bead == "" && (w.name == FleetWindowName || w.name == legacyFleetWindowName) && !used[0]:
			desired[w.id] = 0
			used[0] = true
		}
	}
	next := maxRank + 1
	for _, w := range wins {
		if _, ok := desired[w.id]; ok {
			continue
		}
		if !used[w.index] {
			desired[w.id] = w.index // keep it where it is
			used[w.index] = true
			continue
		}
		for used[next] {
			next++
		}
		desired[w.id] = next
		used[next] = true
	}
	var moving []win
	for _, w := range wins {
		if desired[w.id] != w.index {
			moving = append(moving, w)
		}
	}
	// Two phases so no move ever lands on an occupied index.
	for i, w := range moving {
		if _, stderr, err := d.r.Run(ctx, "move-window", "-d", "-s", w.id, "-t", fmt.Sprintf("%s:%d", CommanderSessionName, 1000+i)); err != nil {
			return fmt.Errorf("tmux move-window: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}
	for _, w := range moving {
		if _, stderr, err := d.r.Run(ctx, "move-window", "-d", "-s", w.id, "-t", fmt.Sprintf("%s:%d", CommanderSessionName, desired[w.id])); err != nil {
			return fmt.Errorf("tmux move-window: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
	}
	// Follow the attached client (no-op when none is attached), then put the
	// columns back to their absolute widths: tmux rescales panes proportionally.
	for _, w := range wins {
		_, _, _ = d.r.Run(ctx, "resize-window", "-A", "-t", w.id)
	}
	return d.Relayout(ctx)
}

// ensureWorkstreamManagerWindows brings back a Manager window for every open
// workstream (start/restart). Returns false when no provider is installed, so
// the caller can fall back to the legacy per-project windows.
func (d *Director) ensureWorkstreamManagerWindows(ctx context.Context) (bool, error) {
	if workstreamWindowsFn == nil {
		return false, nil
	}
	windows, err := workstreamWindowsFn(ctx)
	if err != nil && len(windows) == 0 {
		d.reportProgress("Could not list workstreams (Manager windows not opened): %v", err)
		return true, nil
	}
	if err != nil { // some workstreams were skipped; the rest still get their windows
		d.reportProgress("Some workstream Managers were skipped: %v", err)
	}
	d.reportProgress("Opening %d workstream Manager windows...", len(windows))
	names := make(map[string]WorkstreamWindow, len(windows))
	for _, w := range windows {
		names[w.BeadID] = w
		if _, _, err := d.EnsureWorkstreamWindow(ctx, w); err != nil {
			d.reportProgress("Could not open Manager for %s: %v", w.BeadID, err)
		}
	}
	if err := d.SyncWorkstreamNames(ctx, names); err != nil {
		d.reportProgress("Could not number the Manager windows: %v", err)
	}
	return true, nil
}
