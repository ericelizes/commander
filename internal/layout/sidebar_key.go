package layout

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ExpandedOption is the session option that says which sidebar row is open:
// the workstream number ("0" = Commander). Every window's sidebar reads it, so
// they all show the same dropdown. Unset = each sidebar opens its own window's.
const ExpandedOption = "@cmdr_expanded"

// SidebarKeyResult says what C-a <n> did.
type SidebarKeyResult struct {
	Action string // "expand" or "go"
	N      int
}

// windowNumber is the workstream number a window stands for: 0 for the
// Commander window, n for "#n title", -1 for anything else.
func windowNumber(name string) int {
	if name == "commander" || name == "cmdr-fleet" {
		return 0
	}
	if rest, ok := strings.CutPrefix(name, "#"); ok {
		if i := strings.IndexByte(rest, ' '); i > 0 {
			if n, err := strconv.Atoi(rest[:i]); err == nil {
				return n
			}
		}
	}
	return -1
}

type sidebarWindow struct {
	id     string
	number int
	active bool
}

func (d *Director) sidebarWindows(ctx context.Context) ([]sidebarWindow, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-windows", "-t", CommanderSessionName, "-F", "#{window_id}|#{window_active}|#{window_name}")
	if err != nil {
		return nil, fmt.Errorf("tmux list-windows: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var out []sidebarWindow
	for _, line := range nonEmptyLines(string(stdout)) {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		out = append(out, sidebarWindow{id: parts[0], active: parts[1] == "1", number: windowNumber(parts[2])})
	}
	return out, nil
}

// SidebarKey is C-a <n>: the first press expands workstream n in every sidebar
// (collapsing the one that was open) and leaves keyboard focus where it was; a
// press on the row that is already open goes to that window. fromPane, when
// set (a click in a sidebar), gets keyboard focus handed back to its chat.
func (d *Director) SidebarKey(ctx context.Context, n int, fromPane string) (SidebarKeyResult, error) {
	windows, err := d.sidebarWindows(ctx)
	if err != nil {
		return SidebarKeyResult{}, err
	}
	target, current := "", -1
	for _, w := range windows {
		if w.number == n {
			target = w.id
		}
		if w.active {
			current = w.number
		}
	}
	if target == "" {
		return SidebarKeyResult{}, fmt.Errorf("no window for %d", n)
	}
	stdout, _, _ := d.r.Run(ctx, "show-options", "-t", CommanderSessionName, "-qv", ExpandedOption)
	expanded := current
	if v, err := strconv.Atoi(strings.TrimSpace(string(stdout))); err == nil {
		expanded = v
	}
	if expanded == n {
		if _, stderr, err := d.r.Run(ctx, "select-window", "-t", target); err != nil {
			return SidebarKeyResult{}, fmt.Errorf("tmux select-window: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
		d.focusChat(ctx, target)
		return SidebarKeyResult{Action: "go", N: n}, nil
	}
	if _, stderr, err := d.r.Run(ctx, "set-option", "-t", CommanderSessionName, ExpandedOption, strconv.Itoa(n)); err != nil {
		return SidebarKeyResult{}, fmt.Errorf("tmux set-option: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	if fromPane != "" {
		d.focusChat(ctx, fromPane)
	}
	return SidebarKeyResult{Action: "expand", N: n}, nil
}

// SyncExpanded points the shared state at the visible window (after-select-window
// hook), so a window reached any other way shows its own row open.
func (d *Director) SyncExpanded(ctx context.Context) error {
	windows, err := d.sidebarWindows(ctx)
	if err != nil {
		return err
	}
	for _, w := range windows {
		if w.active && w.number >= 0 {
			if _, _, err := d.r.Run(ctx, "set-option", "-t", CommanderSessionName, ExpandedOption, strconv.Itoa(w.number)); err != nil {
				return err
			}
		}
	}
	return nil
}

// focusChat gives keyboard focus to the chat pane of the window holding target
// (a window id or a pane id), so typing always reaches the agent.
func (d *Director) focusChat(ctx context.Context, target string) {
	stdout, _, err := d.r.Run(ctx, "list-panes", "-t", target, "-F", "#{pane_id}|#{"+ChatPaneOption+"}")
	if err != nil {
		return
	}
	for _, line := range nonEmptyLines(string(stdout)) {
		if id, tag, _ := strings.Cut(line, "|"); tag == "1" {
			_, _, _ = d.r.Run(ctx, "select-pane", "-t", id)
			return
		}
	}
}

// ensureSidebarBindings makes prefix 0-9 the expand/go keys and keeps the
// shared state following the visible window.
func (d *Director) ensureSidebarBindings(ctx context.Context) error {
	bin := d.cmdrBinary
	if bin == "" {
		bin = "cmdr"
	}
	for n := 0; n <= 9; n++ {
		args := []string{"bind-key", strconv.Itoa(n), "run-shell", "-b", fmt.Sprintf("%s __sidebar-key %d", bin, n)}
		if _, stderr, err := d.r.Run(ctx, args...); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
		}
	}
	// With window-size latest a window is resized to the client only when it is
	// selected, and tmux rescales its panes proportionally: re-apply the absolute
	// column widths on every select and every window resize (__relayout is
	// idempotent and takes a lock, so overlapping runs are skipped).
	hooks := [][]string{
		{"set-hook", "-t", CommanderSessionName + ":", "after-select-window[0]", fmt.Sprintf("run-shell -b '%s __sidebar-key --sync'", bin)},
		{"set-hook", "-t", CommanderSessionName + ":", "after-select-window[1]", fmt.Sprintf("run-shell -b '%s __relayout'", bin)},
		{"set-hook", "-gw", "window-resized[0]", fmt.Sprintf("run-shell -b '%s __relayout'", bin)},
	}
	for _, args := range hooks {
		if _, stderr, err := d.r.Run(ctx, args...); err != nil {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
		}
	}
	return nil
}
