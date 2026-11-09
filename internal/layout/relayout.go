package layout

import (
	"context"
	"fmt"
	"math"
	"strings"
)

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ColumnWidths gives the sidebar and right-column widths for a window W cells
// wide (spec §2.9), so every window is identical and the chat keeps >= 80
// columns whenever the terminal allows it.
//
//	left  = clamp(round(W*0.13), 36, 56)
//	right = min(clamp(round(W*0.36), 42, 160), W-left-2-80)
func ColumnWidths(w int) (left, right int) {
	left = clampInt(int(math.Round(float64(w)*0.13)), 36, 56)
	right = clampInt(int(math.Round(float64(w)*0.36)), 42, 160)
	if maxRight := w - left - 2 - 80; right > maxRight {
		right = maxRight
	}
	if right < 20 { // a very narrow terminal: keep something visible
		right = 20
	}
	if left+right+2+20 > w { // and never starve the chat completely
		left = clampInt(w/4, 12, left)
		right = clampInt(w/4, 12, right)
	}
	return left, right
}

// InspectorEmptyOption is the window option an inspector with no tabs sets, so
// Relayout can give its width to the chat instead.
const InspectorEmptyOption = "@cmdr_inspector_empty"

// EmptyInspectorWidth is the right column's width while it has nothing to show.
func EmptyInspectorWidth(w int) int {
	return clampInt(int(math.Round(float64(w)*0.14)), 28, 40)
}

// TitleBarRows is the height of the chat-title pane: pad, title, status, pad.
const TitleBarRows = 4

type relayoutPane struct {
	windowID, id, cmd, pad string
	windowWidth            int
	emptyInspector         bool // the window's inspector has no tabs: it shrinks
}

// Relayout re-applies the absolute column widths and title-bar height in every
// window. tmux rescales panes proportionally when a window changes size (an
// 80x24 window attached to a 426x73 terminal ends up with a 117-wide sidebar and
// a 26-row title bar), so this runs on client-resized / client-attached and
// after any soft restart. It is idempotent and never touches chat panes' content.
func (d *Director) Relayout(ctx context.Context) error {
	// A window left on window-size manual keeps the size of whatever monitor it was
	// made on (everything then looks cropped on a smaller one): let it follow the
	// client again before reading widths.
	if out, _, err := d.r.Run(ctx, "list-windows", "-t", CommanderSessionName, "-F", "#{window_id}"); err == nil {
		for _, id := range nonEmptyLines(string(out)) {
			_, _, _ = d.r.Run(ctx, "set-option", "-u", "-w", "-t", id, "window-size")
		}
	}
	stdout, stderr, err := d.r.Run(ctx, "list-panes", "-s", "-t", CommanderSessionName, "-F",
		"#{window_id}|#{window_width}|#{pane_id}|#{"+PadOption+"}:#{"+InspectorEmptyOption+"}|#{pane_start_command}")
	if err != nil {
		s := strings.TrimSpace(string(stderr))
		if strings.Contains(s, "can't find session") || strings.Contains(s, "no server running") {
			return nil
		}
		return fmt.Errorf("tmux list-panes: %w: %s", err, s)
	}
	var panes []relayoutPane
	for _, line := range nonEmptyLines(string(stdout)) {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}
		var w int
		if _, err := fmt.Sscanf(parts[1], "%d", &w); err != nil || w <= 0 {
			continue
		}
		pad, empty, _ := strings.Cut(parts[3], ":")
		panes = append(panes, relayoutPane{windowID: parts[0], windowWidth: w, id: parts[2], pad: pad, cmd: parts[4], emptyInspector: empty == "1"})
	}
	for _, p := range panes {
		left, right := ColumnWidths(p.windowWidth)
		var args []string
		switch {
		case p.pad == "h":
			args = []string{"resize-pane", "-t", p.id, "-x", "1"}
		case p.pad == "v":
			args = []string{"resize-pane", "-t", p.id, "-y", "1"}
		case strings.Contains(p.cmd, "cmdr __column cmdr-objectives"):
			args = []string{"resize-pane", "-t", p.id, "-x", fmt.Sprint(left)}
		case strings.Contains(p.cmd, "cmdr __column chat-title"):
			args = []string{"resize-pane", "-t", p.id, "-y", fmt.Sprint(TitleBarRows)}
		case isRightColumn(p.cmd):
			if p.emptyInspector {
				right = EmptyInspectorWidth(p.windowWidth)
			}
			args = []string{"resize-pane", "-t", p.id, "-x", fmt.Sprint(right)}
		default:
			continue
		}
		if _, stderr, err := d.r.Run(ctx, args...); err != nil {
			return fmt.Errorf("tmux resize-pane %s: %w: %s", p.id, err, strings.TrimSpace(string(stderr)))
		}
	}
	return nil
}

func isRightColumn(cmd string) bool {
	for _, k := range []string{"cmdr-fleet", "lead-fleet", "inspector", "operator-side"} {
		if strings.Contains(cmd, "cmdr __column "+k) {
			return true
		}
	}
	return false
}
