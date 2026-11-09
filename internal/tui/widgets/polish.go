package widgets

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// SpinnerFrames is the MiniDot braille spinner (the user prefers its look; the
// earlier "too slow" problem was the tick rate, not the glyphs). Used sparingly: running work in the title
// bar status line, on workstreams' live state and on in-progress steps.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Skeleton returns `rows` placeholder bars (▁▁▁▁) of varied length in the raised
// background colour, with a one-cell shimmer (a brighter cell) travelling along
// them as frame advances. It is what a column draws instead of a blank pane
// while its first data is in flight.
func Skeleton(rows, width, frame int) []string {
	if rows <= 0 || width <= 0 {
		return nil
	}
	base := lipgloss.NewStyle().Foreground(T.BgRaised)
	glint := lipgloss.NewStyle().Foreground(T.BgCursor)
	lengths := []int{70, 90, 55, 80, 65, 85}
	out := make([]string, 0, rows)
	for r := 0; r < rows; r++ {
		n := width * lengths[r%len(lengths)] / 100
		if n < 3 {
			n = min(3, width)
		}
		pos := (frame + r*3) % (n + 6)
		var b strings.Builder
		for i := 0; i < n; i++ {
			if i == pos {
				b.WriteString(glint.Render("▁"))
			} else {
				b.WriteString(base.Render("▁"))
			}
		}
		out = append(out, b.String())
	}
	return out
}

// ErrorCard is a bordered card (Err colour) for a failure the user can act on:
// a one-line cause in bold, the full error muted, and the way out.
func ErrorCard(cause, detail, hint string, width int) string {
	if width < 12 {
		width = 12
	}
	inner := width - 4 // border + padding
	lines := []string{lipgloss.NewStyle().Bold(true).Foreground(T.Err).Render(cause)}
	if detail != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(T.TextMuted).Width(inner).Render(detail))
	}
	if hint != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(T.TextSecondary).Render(hint))
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(T.Err).
		Padding(0, 1).
		Width(width - 2).
		Render(strings.Join(lines, "\n"))
}
