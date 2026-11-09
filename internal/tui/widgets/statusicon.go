package widgets

import (
	"github.com/charmbracelet/lipgloss"
)

// StatusIcon returns a single colored unicode glyph representing a commander
// status or Task phase. It is a pure function — no model state — so any view
// can call it cheaply for table rows, footers, tree decorations, etc.
//
// Color sourcing: the foreground comes from theme.StatusColor so the badge
// matches every other status-tinted element (metrics counts, bottom bar
// segments, ceilings tier). Bold weight is added for high-signal states
// (blocked, working, done, executing) so the eye lands on them first; idle
// is rendered in the muted tier so a quiet fleet recedes.
//
// Supported inputs (case-sensitive, kept narrow on purpose):
//
//	Operational bead states (cmdr= dimension):
//	  "working"      → ⟳ (blue, bold)
//	  "blocked"      → ✗ (red, bold)
//	  "needs-input"  → ? (peach, bold)
//	  "done"         → ✓ (green, bold)
//	  "idle"         → — (overlay1, faint)
//
//	Task phase states (cmdr= dimension on kind:task beads):
//	  "brainstorming" → ◇ (mauve)
//	  "planning"      → ◆ (mauve)
//	  "executing"     → ⏵ (blue, bold)
//	  "reviewing"     → ◎ (green)
//
// Unknown statuses fall back to a dim "?" so renderers degrade gracefully
// rather than printing the raw string and breaking column widths.
func StatusIcon(status string) string {
	glyph := StatusGlyph(status)
	style := lipgloss.NewStyle().Foreground(StatusColor(status))
	switch status {
	case "working", "blocked", "done", "executing", "needs-input":
		style = style.Bold(true)
	}
	return style.Render(glyph)
}

// StatusGlyph returns just the unprintable-free glyph for a status, with no
// styling applied. Useful when callers want to embed the symbol inside an
// already-styled lipgloss block (lipgloss escape composition is fragile).
func StatusGlyph(status string) string {
	switch status {
	case "working":
		return "⟳"
	case "blocked":
		return "✗"
	case "needs-input":
		return "?"
	case "done":
		return "✓"
	case "idle":
		return "—"
	case "brainstorming":
		return "◇"
	case "planning":
		return "◆"
	case "executing":
		return "⏵"
	case "reviewing":
		return "◎"
	default:
		return "?"
	}
}
