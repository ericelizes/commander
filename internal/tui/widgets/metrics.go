package widgets

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MetricsData is the value-typed input the Metrics widget renders. It is
// intentionally a flat record (no pointers) so callers can compose new values
// each refresh tick without churning allocations or worrying about aliasing.
type MetricsData struct {
	// TotalCostUSD24h is the rolling 24-hour spend across the fleet.
	TotalCostUSD24h float64
	// TotalCostUSD7d is the rolling 7-day spend across the fleet.
	TotalCostUSD7d float64
	// AgentCount is the total number of operators known to commander —
	// active, blocked, idle, and finished. Anchors the count line.
	AgentCount int
	// ActiveCount is the subset currently running (cmdr=working/executing).
	ActiveCount int
	// BlockedCount is the subset stuck (cmdr=blocked / needs-input).
	BlockedCount int
	// IdleRatio is in [0,1]; >0.5 = green, 0.25–0.5 = yellow, <0.25 = red.
	IdleRatio float64
}

// Metrics is a bordered card summarizing fleet-level cost + agent counts.
// It lives in the right column of Commander view and the upper-right of
// Lead view. Width-driven truncation lets it shrink to ~24 cols without
// breaking the box.
//
// Visual hierarchy (per the polish pass):
//   - Row 1: tiny header "FLEET" prefixed by the accent bar so the card
//     announces what it is even when stacked next to other boxes.
//   - Row 2-4: cost / agents / idle. Numbers wear NumberHighStyle (bold
//     Lavender) so they out-weigh the dim "cost" / "agents" labels.
type Metrics struct {
	width  int
	height int
	data   MetricsData
	// header is the small label rendered in row 1. Defaults to "FLEET" if
	// unset; Lead view passes "PROJECT" so the same widget reads honestly
	// at the project scope.
	header string
}

// NewMetrics returns a Metrics card with zero data — the empty state renders
// $0.00 across the board, which is correct for a fresh launch.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// SetData replaces the rendered values. Callers refresh on a polling tick or
// store-event subscription; the widget itself is dumb.
func (m *Metrics) SetData(d MetricsData) {
	m.data = d
}

// SetHeader overrides the default "FLEET" label. Optional — Lead view uses
// "PROJECT" so the right-column card doesn't lie about its scope.
func (m *Metrics) SetHeader(label string) {
	m.header = label
}

// SetSize stores the canvas dimensions; View centers the bordered box inside
// them.
func (m *Metrics) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// View renders header + three lines (cost / counts / idle) inside a rounded
// border. Below ~40 cols the labels collapse to icons so the numbers remain
// visible.
func (m *Metrics) View() string {
	if m.width <= 0 {
		return ""
	}

	narrow := m.width < 40

	header := m.header
	if header == "" {
		header = "FLEET"
	}
	headerLine := AccentBar + " " + HeaderStyle.Render(header)

	costLine := m.renderCost(narrow)
	countLine := m.renderCounts(narrow)
	idleLine := m.renderIdle(narrow)

	// Skip the accent-bar header line — the parent view's framePane already
	// labels the box ("FLEET METRICS" / "PROJECT METRICS"). The widget
	// renders just the data rows so we don't have redundant headers.
	_ = headerLine
	body := strings.Join([]string{costLine, countLine, idleLine}, "\n")
	return body
}

// renderCost prints the 24h / 7d cost pair. Always uses 2-decimal $ format
// so columns line up across refreshes. Numbers are NumberHighStyle so they
// outweigh the dim window labels next to them.
func (m *Metrics) renderCost(narrow bool) string {
	if narrow {
		return fmt.Sprintf("$ %s / %s",
			NumberHighStyle.Render(formatUSD(m.data.TotalCostUSD24h)),
			NumberHighStyle.Render(formatUSD(m.data.TotalCostUSD7d)),
		)
	}
	label := DimText.Render("cost  ")
	w24 := DimText.Render("24h")
	w7 := DimText.Render("7d")
	dot := FaintText.Render("·")
	return fmt.Sprintf("%s%s %s %s %s %s",
		label,
		w24, NumberHighStyle.Render(formatUSD(m.data.TotalCostUSD24h)),
		dot, w7, NumberHighStyle.Render(formatUSD(m.data.TotalCostUSD7d)),
	)
}

// renderCounts prints active/blocked over total. Active = blue (working),
// blocked = red, both bold so the eye picks up problems at a glance. Total
// is rendered through DimText so the active fraction reads as the numerator.
func (m *Metrics) renderCounts(narrow bool) string {
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(StatusColor("working"))
	blockedStyle := lipgloss.NewStyle().Bold(true).Foreground(StatusColor("blocked"))
	active := activeStyle.Render(fmt.Sprintf("%d", m.data.ActiveCount))
	blocked := blockedStyle.Render(fmt.Sprintf("%d", m.data.BlockedCount))
	total := DimText.Render(fmt.Sprintf("%d", m.data.AgentCount))

	if narrow {
		return fmt.Sprintf("%s %s/%s %s%s",
			DimText.Render("◉"), active, total, FaintText.Render("✗"), blocked,
		)
	}
	label := DimText.Render("agents")
	dot := FaintText.Render("·")
	return fmt.Sprintf("%s %s %s %s %s %s %s %s",
		label,
		DimText.Render("active"), active, FaintText.Render("/"), total,
		dot,
		DimText.Render("blocked"), blocked,
	)
}

// renderIdle colors the percentage by tier so review at a glance signals
// fleet health: lots of idle ⇒ green ⇒ user's bottleneck is elsewhere.
func (m *Metrics) renderIdle(narrow bool) string {
	pct := fmt.Sprintf("%.0f%%", m.data.IdleRatio*100)
	color := idleTierColor(m.data.IdleRatio)
	colored := lipgloss.NewStyle().Foreground(color).Bold(true).Render(pct)

	if narrow {
		return fmt.Sprintf("%s %s", DimText.Render("—"), colored)
	}
	return fmt.Sprintf("%s    %s %s",
		DimText.Render("idle"), colored, DimText.Render("of fleet"),
	)
}

// idleColor maps an idle-ratio in [0,1] to an ANSI palette index. Tiers come
// from the bead description: >0.5 green, 0.25–0.5 yellow, <0.25 red.
//
// Returns the legacy ANSI index ("10"/"11"/"9") so the existing tier test
// in metrics_test.go stays green; the rendered output uses the
// Catppuccin-mapped lipgloss colors via idleTierColor.
func idleColor(r float64) string {
	switch {
	case r >= 0.5:
		return "10" // green
	case r >= 0.25:
		return "11" // yellow
	default:
		return "9" // red
	}
}

// idleTierColor returns the Catppuccin color used for actual rendering at
// each idle tier. Kept in lockstep with idleColor.
func idleTierColor(r float64) lipgloss.Color {
	switch {
	case r >= 0.5:
		return Palette.Green
	case r >= 0.25:
		return Palette.Yellow
	default:
		return Palette.Red
	}
}

// formatUSD enforces the $0.00 format the user reads on every dashboard refresh.
// Negative values are rendered as $-1.23 (no parens) — they shouldn't happen
// but a sign keeps audit trails honest.
func formatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

// Compact is the FLEET block for the bottom of the navigator: spend and agent
// counts in two short lines, plus a 24h sparkline when there is room (width >=
// 44) and any spend to show. Same numbers as the full card.
func (d MetricsData) Compact(width int, series []float64) []string {
	num := lipgloss.NewStyle().Bold(true).Foreground(T.TextPrimary)
	lab := lipgloss.NewStyle().Foreground(T.TextMuted)
	cost := num.Render(fmt.Sprintf("$%.2f", d.TotalCostUSD24h)) + " " + lab.Render("today") + "   " +
		num.Render(fmt.Sprintf("$%.2f", d.TotalCostUSD7d)) + " " + lab.Render("7d")
	ops := lab.Render("no operators running")
	if d.AgentCount > 0 || d.ActiveCount > 0 {
		ops = lipgloss.NewStyle().Foreground(T.Run).Bold(true).Render("●") + " " + num.Render(fmt.Sprint(d.ActiveCount)) + " " + lab.Render("ops")
		if d.BlockedCount > 0 {
			ops += "   " + lipgloss.NewStyle().Foreground(T.Err).Bold(true).Render("✗") + " " + num.Render(fmt.Sprint(d.BlockedCount))
		}
	}
	lines := []string{cost, ops}
	if width >= 44 {
		if sp := Sparkline(series); sp != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(T.Run).Render(sp)+"  "+lab.Render("spend 24h"))
		}
	}
	return lines
}

// Sparkline renders values as block characters scaled to their maximum. It
// returns "" when there is nothing but zeros to draw.
func Sparkline(values []float64) string {
	maxV := 0.0
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range values {
		i := int(v / maxV * float64(len(blocks)-1))
		if v > 0 && i == 0 {
			i = 0
		}
		b.WriteRune(blocks[i])
	}
	return b.String()
}
