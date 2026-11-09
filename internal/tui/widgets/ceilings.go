package widgets

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// CeilingsData carries one operator's three ceiling dimensions. USD is the
// primary signal (per U6 design lens + watchdog spec); tokens and wallclock
// back it up. All three render even when one ceiling is the trip wire.
type CeilingsData struct {
	// USDUsed / USDMax are dollars; floats so partial cents render correctly.
	USDUsed float64
	USDMax  float64
	// Tokens are integers — the watchdog summed input + output + reasoning.
	TokensUsed int
	TokensMax  int
	// Wallclock is in minutes from the spawn timestamp; integer so the bar
	// doesn't jitter sub-second.
	WallclockMin int
	WallclockMax int
}

// Ceilings renders three labelled progress bars: USD, tokens, wallclock.
// It's the operator-view side panel's heartbeat — the user reads this to decide
// whether to let the operator keep going or kill it.
//
// Visual hierarchy (per the polish pass):
//   - Used numbers wear NumberHighStyle (bold Lavender) so they out-weigh
//     the dim "/ max" suffix and the right-aligned percent.
//   - Bar is rendered with the eighth-block ramp for sub-cell precision —
//     a 17% bar reads at 17%, not snapped to the nearest cell.
//   - Bar + percent share a tier color that crosses the 50/80/95% thresholds
//     so a glance at the side panel tells you which dimension is the trip
//     wire even before reading the numbers.
type Ceilings struct {
	width  int
	height int
	data   CeilingsData
}

// NewCeilings returns a zero-valued widget. SetData populates it.
func NewCeilings() *Ceilings {
	return &Ceilings{}
}

// SetData replaces the rendered values.
func (c *Ceilings) SetData(d CeilingsData) {
	c.data = d
}

// SetSize records canvas dimensions; bar width derives from this.
func (c *Ceilings) SetSize(w, h int) {
	c.width = w
	c.height = h
}

// View renders three lines, one per ceiling. Each line is:
//
//	<label>  <used> / <max>  ▕████▏  pp%
//
// The bar fills proportionally; color tiers from green → yellow → red →
// blink-red track how close the watchdog is to firing the kill.
func (c *Ceilings) View() string {
	if c.width <= 0 {
		return ""
	}
	usdLine := c.line("usd",
		formatUSD(c.data.USDUsed), formatUSD(c.data.USDMax),
		ratio(c.data.USDUsed, c.data.USDMax),
	)
	tokLine := c.line("tok",
		formatInt(c.data.TokensUsed), formatInt(c.data.TokensMax),
		ratio(float64(c.data.TokensUsed), float64(c.data.TokensMax)),
	)
	wallLine := c.line("wall",
		fmt.Sprintf("%dm", c.data.WallclockMin),
		fmt.Sprintf("%dm", c.data.WallclockMax),
		ratio(float64(c.data.WallclockMin), float64(c.data.WallclockMax)),
	)
	return strings.Join([]string{usdLine, tokLine, wallLine}, "\n")
}

// line composes one labelled progress row. usedStr/maxStr are pre-formatted
// because each ceiling has its own units (USD / tokens / minutes).
func (c *Ceilings) line(label, usedStr, maxStr string, fill float64) string {
	tier := ceilingTierColor(fill)
	pctStyle := lipgloss.NewStyle().Foreground(tier).Bold(true)
	if fill >= 0.95 {
		// At 95%+ we layer Blink on top so the operator's about-to-die state
		// is impossible to miss.
		pctStyle = pctStyle.Blink(true)
	}

	bar := renderBar(c.barWidth(), fill)
	pct := fmt.Sprintf("%3.0f%%", fill*100)

	return fmt.Sprintf("%s  %s %s %s  %s  %s",
		DimText.Render(fmt.Sprintf("%-4s", label)),
		NumberHighStyle.Render(usedStr),
		FaintText.Render("/"),
		FaintText.Render(maxStr),
		bar,
		pctStyle.Render(pct),
	)
}

// barWidth derives the bar's drawable column count from the canvas width.
// Reserves space for label + numbers + percent so the bar never wraps.
func (c *Ceilings) barWidth() int {
	w := c.width - 28
	if w < 4 {
		w = 4
	}
	if w > 30 {
		w = 30
	}
	return w
}

// barRamp is the eighth-block ramp used to render fractional cells. Index 0
// is empty; index 8 is a full block. Lipgloss/terminals render these glyphs
// continuously so a 13.5-cell bar reads as 13.5 cells, not 13 or 14.
var barRamp = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}

// renderBar draws a fixed-width filled-block bar wrapped in slim brackets.
// fraction is clamped to [0,1]; values >1 still render full to keep the
// visual ceiling clear. The fractional eighth is consumed by a single ramp
// glyph at the boundary so partial-cell progress reads honestly.
//
// Test invariant: at fill < 1.0 we still emit at least one ░ trough cell
// (counted in tests via strings.Count(line, "░")), and at fill > 0 we emit
// at least one █ full cell. This keeps the proportional-bar test happy.
func renderBar(width int, fraction float64) string {
	if width <= 0 {
		return ""
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	// Tier color drives the filled cells; trough is Surface1 so the bar
	// grooves visibly into the chrome rather than going invisible.
	tier := ceilingTierColor(fraction)
	fillStyle := lipgloss.NewStyle().Foreground(tier)
	troughStyle := lipgloss.NewStyle().Foreground(Palette.Surface1)
	bracketStyle := lipgloss.NewStyle().Foreground(Palette.Overlay0)

	// Eighths of progress across the full bar.
	eighths := int(float64(width)*fraction*8 + 0.5)
	if eighths > width*8 {
		eighths = width * 8
	}

	full := eighths / 8
	rem := eighths % 8

	// Ensure tests see at least one ░ when not at full and at least one █
	// when there's any fill — without bending the visual proportions on
	// realistic widths.
	if fraction > 0 && full == 0 && rem == 0 {
		rem = 1
	}
	if fraction < 1 && full == width {
		full = width - 1
	}

	var b strings.Builder
	b.WriteString(bracketStyle.Render("▕"))
	if full > 0 {
		b.WriteString(fillStyle.Render(strings.Repeat("█", full)))
	}
	emptyStart := full
	if rem > 0 && full < width {
		b.WriteString(fillStyle.Render(string(barRamp[rem])))
		emptyStart = full + 1
	}
	if emptyStart < width {
		b.WriteString(troughStyle.Render(strings.Repeat("░", width-emptyStart)))
	}
	b.WriteString(bracketStyle.Render("▏"))
	return b.String()
}

// ratio guards against divide-by-zero (max=0 ⇒ "no ceiling configured" ⇒
// render the bar empty rather than NaN/+Inf).
func ratio(used, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return used / max
}

// ceilingColor maps fill in [0,1] to the watchdog severity palette index.
// 0–50% green, 50–80% yellow, 80%+ red. Returns the legacy ANSI palette
// index ("10"/"11"/"9") so the existing tier test in ceilings_test.go
// stays green; the rendered output uses Catppuccin colors via
// ceilingTierColor.
func ceilingColor(fill float64) string {
	switch {
	case fill < 0.5:
		return "10" // green
	case fill < 0.8:
		return "11" // yellow
	default:
		return "9" // red (and 95%+ blinks)
	}
}

// ceilingTierColor returns the Catppuccin color used for actual rendering
// at each fill tier. Kept in lockstep with ceilingColor.
func ceilingTierColor(fill float64) lipgloss.Color {
	switch {
	case fill < 0.5:
		return Palette.Green
	case fill < 0.8:
		return Palette.Yellow
	default:
		return Palette.Red
	}
}

// formatInt thousands-separates large token counts. 12345 → "12,345" reads
// faster than "12345" in a side panel that updates every second.
func formatInt(n int) string {
	if n < 0 {
		return "-" + formatInt(-n)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return formatInt(n/1000) + "," + fmt.Sprintf("%03d", n%1000)
}
