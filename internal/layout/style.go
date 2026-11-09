package layout

import (
	"context"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/tui/widgets"
)

// applyWindowStyle gives the window of chatPane solid, explicit backgrounds and
// visible pane framing (spec docs/plans/2026-10-20-tui-redesign.md §2.1, §2.6).
//
// Why: Ghostty's background-opacity only applies to cells that use the DEFAULT
// background. Cells with an explicit background (Claude Code's input box, any
// lipgloss Background) are drawn opaque, so a pane with a default bg is a patchwork
// of glass and solid. Setting window-style gives every unstyled cell an
// explicit background too: chrome panes sit on Mantle, the chat on Base.
// Pane-level options are not inherited by panes created later by a split, so
// the chat override cannot leak into the columns.
func (d *Director) applyWindowStyle(ctx context.Context, chatPane string) error {
	t := widgets.T
	chrome, chat := "bg="+string(t.BgChrome), "bg="+string(t.BgChat)
	for _, o := range [][]string{
		// No border lines at all ("spaces") in the chrome colour (fg == bg): plain
		// dark gaps between panes, never translucent. The chrome/chat boundary is
		// the colour change itself.
		{"-w", "pane-border-style", "fg=" + string(t.BgChrome) + ",bg=" + string(t.BgChrome)},
		{"-w", "pane-active-border-style", "fg=" + string(t.BgChrome) + ",bg=" + string(t.BgChrome)},
		// Copy-mode and selection highlights otherwise fall back to default colors.
		{"-w", "mode-style", "fg=" + string(t.OnKind) + ",bg=" + string(t.Accent)},
		{"-w", "copy-mode-match-style", "fg=" + string(t.OnKind) + ",bg=" + string(t.Attn)},
		{"-w", "copy-mode-current-match-style", "fg=" + string(t.OnKind) + ",bg=" + string(t.Accent)},
		{"-w", "copy-mode-mark-style", "fg=" + string(t.OnKind) + ",bg=" + string(t.Accent)},
		{"-w", "pane-border-lines", "spaces"},
		{"-w", "window-style", chrome},
		{"-w", "window-active-style", chrome},
		{"-p", "window-style", chat},
		{"-p", "window-active-style", chat},
	} {
		_, stderr, err := d.r.Run(ctx, "set-option", o[0], "-t", chatPane, o[1], o[2])
		if err != nil && o[1] == "pane-border-lines" && o[2] == "spaces" {
			// "spaces" needs tmux 3.5+; the border colour equals the background,
			// so a plain line is invisible too.
			_, stderr, err = d.r.Run(ctx, "set-option", o[0], "-t", chatPane, o[1], "simple")
		}
		if err != nil {
			return fmt.Errorf("tmux set-option %s: %w: %s", o[1], err, strings.TrimSpace(string(stderr)))
		}
	}
	// A window sized with resize-window -x/-y is stuck at that size ("manual") and
	// never follows the terminal again. Never size windows by hand; heal any that
	// were. (Unsetting an unset option is a harmless no-op.)
	_, _, _ = d.r.Run(ctx, "set-option", "-u", "-w", "-t", chatPane, "window-size")
	return nil
}

// sessionStyles are the commander session's remaining default-background
// surfaces: the (disabled) status line, command/message prompts, popups and
// menus. They are session-scoped so the user's other tmux sessions keep their look.
func sessionStyles() [][]string {
	t := widgets.T
	chrome := "bg=" + string(t.BgChrome)
	text := "fg=" + string(t.TextPrimary) + "," + chrome
	raised := "fg=" + string(t.TextPrimary) + ",bg=" + string(t.BgRaised)
	return [][]string{
		{"status-style", "fg=" + string(t.TextSecondary) + "," + chrome},
		{"message-style", raised},
		{"message-command-style", raised},
		{"popup-style", text},
		{"popup-border-style", "fg=" + string(t.Rule) + "," + chrome},
		{"menu-style", text},
		{"menu-border-style", "fg=" + string(t.Rule) + "," + chrome},
		{"menu-selected-style", "fg=" + string(t.OnKind) + ",bg=" + string(t.Accent)},
	}
}
