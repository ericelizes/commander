package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/tui/widgets"
)

// BottomBar is the always-visible cross-fleet signal strip rendered at the
// foot of Lead and Operator views (NOT Commander — Commander's column 3 is
// already a fleet view, so a bar there would duplicate). Format per project:
//
//	[⚠ ]<name> <icon><n>·<icon><n>...
//
// Projects are joined by ` │ `. When the rendered width exceeds the canvas,
// trailing projects collapse to ` … +M more`.
type BottomBar struct {
	width int
	fleet []ProjectFleetSummary
}

// ProjectFleetSummary is the per-project rollup the bar renders. Counts are
// what BottomBar displays as `<icon><n>` segments; HasBlockedAgent flips on
// the ⚠ prefix so a glance flags projects that need attention.
type ProjectFleetSummary struct {
	Name            string
	Blocked         int  // ✗
	Working         int  // ⟳
	NeedsInput      int  // ?
	Done            int  // ✓
	Idle            int  // —
	HasBlockedAgent bool // for ⚠ prefix
}

// truncateBudget is the safety margin reserved for the ` … +M more` suffix
// plus a couple of cells of breathing room before the right edge. Subtracted
// from b.width when deciding whether to truncate.
const truncateBudget = 12

// statusOrder fixes the left-to-right order of icon segments per project so
// the bar reads consistently across refreshes. Blocked comes first because
// it's the highest-signal status — the user's eye lands on ✗ instantly.
var statusOrder = []string{"blocked", "working", "needs-input", "done", "idle"}

// NewBottomBar returns a zero-state bar. The caller wires width via SetSize
// on tea.WindowSizeMsg and fleet via SetFleet on each refresh tick.
func NewBottomBar() *BottomBar {
	return &BottomBar{}
}

// SetSize stores the canvas width. The bar is always one line tall, so no
// height parameter — the parent view appends it via lipgloss.JoinVertical.
func (b *BottomBar) SetSize(width int) {
	b.width = width
}

// SetFleet replaces the rendered project summaries.
func (b *BottomBar) SetFleet(summaries []ProjectFleetSummary) {
	b.fleet = summaries
}

// View renders a single line. Empty fleet → dim "◇ fleet idle" (italic)
// for a quiet "tracked but quiet" affordance; otherwise each project segment
// is built then joined by a faint vertical-bar separator with truncation
// if needed.
func (b *BottomBar) View() string {
	if len(b.fleet) == 0 {
		return lipgloss.NewStyle().
			Italic(true).
			Foreground(widgets.T.TextMuted).
			Render(" ◇ fleet idle")
	}

	segments := make([]string, len(b.fleet))
	for i, p := range b.fleet {
		segments[i] = renderProjectSegment(p)
	}

	separator := lipgloss.NewStyle().Foreground(widgets.Palette.Surface2).Render(" │ ")
	full := strings.Join(segments, separator)

	// Truncation budget: only trim if width is set and the line overflows.
	if b.width > 0 {
		limit := b.width - truncateBudget
		if limit > 0 && lipgloss.Width(full) > limit {
			return truncateSegments(segments, separator, limit)
		}
	}

	return full
}

// renderProjectSegment turns one ProjectFleetSummary into its visible chunk:
// `[⚠ ]<chip-name> <icon-segments>` where icon-segments is `<icon><n>`
// joined by a faint `·`. If every status count is zero, the project renders
// as `<name> —` (Surface2 dash) to convey "tracked but quiet" rather than
// disappearing.
func renderProjectSegment(p ProjectFleetSummary) string {
	name := widgets.ProjectChip(p.Name)

	prefix := ""
	if p.HasBlockedAgent {
		prefix = lipgloss.NewStyle().
			Bold(true).
			Foreground(widgets.Palette.Red).
			Render("⚠") + " "
	}

	icons := renderStatusIcons(p)
	if icons == "" {
		dash := lipgloss.NewStyle().
			Foreground(widgets.Palette.Surface2).
			Render("—")
		return prefix + name + " " + dash
	}
	return prefix + name + " " + icons
}

// renderStatusIcons assembles the `<icon><n>·...` chunk in statusOrder.
// Counts inherit the status color from theme.StatusColor so the digit reads
// at the same hue as its glyph. Zero counts are skipped so the bar stays
// compact. Returns "" if every count is zero — the caller renders a dash
// in that case.
func renderStatusIcons(p ProjectFleetSummary) string {
	counts := map[string]int{
		"blocked":     p.Blocked,
		"working":     p.Working,
		"needs-input": p.NeedsInput,
		"done":        p.Done,
		"idle":        p.Idle,
	}

	separator := widgets.FaintText.Render("·")

	parts := make([]string, 0, len(statusOrder))
	for _, status := range statusOrder {
		n := counts[status]
		if n <= 0 {
			continue
		}
		count := lipgloss.NewStyle().
			Bold(true).
			Foreground(widgets.StatusColor(status)).
			Render(fmt.Sprintf("%d", n))
		parts = append(parts, fmt.Sprintf("%s%s", widgets.StatusIcon(status), count))
	}
	return strings.Join(parts, separator)
}

// truncateSegments greedily appends segments until the next one would push
// us past `limit` (accounting for the suffix). The remainder is reported as
// `+M more`. We always emit the suffix even when zero segments fit, so the
// user sees a hint rather than a blank bar at very narrow widths.
func truncateSegments(segments []string, separator string, limit int) string {
	suffix := lipgloss.NewStyle().Foreground(widgets.T.TextMuted)

	var b strings.Builder
	kept := 0
	for i, seg := range segments {
		// Tentatively add the segment + a separator (if not first) + worst-case suffix.
		candidate := b.String()
		if i > 0 {
			candidate += separator
		}
		candidate += seg
		// We need room for ` … +<remaining> more` after this segment.
		remaining := len(segments) - i - 1
		tentativeSuffix := fmt.Sprintf(" … +%d more", remaining)
		if lipgloss.Width(candidate)+lipgloss.Width(tentativeSuffix) > limit {
			break
		}
		if i > 0 {
			b.WriteString(separator)
		}
		b.WriteString(seg)
		kept++
	}

	dropped := len(segments) - kept
	return b.String() + suffix.Render(fmt.Sprintf(" … +%d more", dropped))
}

// BuildFleetSummariesFromFixtures rolls FixtureOperators up by ProjectID so
// Lead/Operator views can wire the bar before U13 swaps in real metrics. The
// returned slice has one entry per FixtureProject in fixture order; projects
// with zero operators still appear (rendered as `<name> —`) so the user sees
// the full fleet rather than mistakenly thinking sandbox vanished.
func BuildFleetSummariesFromFixtures() []ProjectFleetSummary {
	projects := FixtureProjects()
	operators := FixtureOperators()

	summaries := make([]ProjectFleetSummary, len(projects))
	indexByID := make(map[int64]int, len(projects))
	for i, p := range projects {
		summaries[i] = ProjectFleetSummary{Name: p.Name}
		indexByID[p.ID] = i
	}

	for _, op := range operators {
		idx, ok := indexByID[op.ProjectID]
		if !ok {
			continue
		}
		switch op.Status {
		case "blocked":
			summaries[idx].Blocked++
			summaries[idx].HasBlockedAgent = true
		case "working":
			summaries[idx].Working++
		case "needs-input":
			summaries[idx].NeedsInput++
		case "done":
			summaries[idx].Done++
		case "idle":
			summaries[idx].Idle++
		}
	}

	return summaries
}
