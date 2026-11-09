package widgets

import (
	"hash/fnv"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette is the project's design-token color set, sourced from
// Catppuccin Mocha. Every styled element in the TUI must read its
// foreground/background from here so theming stays consistent and a future
// flavor swap is a single struct rewrite.
//
// Naming follows the upstream spec: Base/Mantle/Crust are background
// surfaces (lightest to darkest); Surface0..2 and Overlay0..2 are
// progressively brighter mid-tones useful for dividers, chips, and inactive
// borders. Text/Subtext1/Subtext0 are foreground tiers. The named accents
// (Mauve, Sky, Peach, ...) are used semantically — see StatusColor and
// ProjectChip below — never picked at random.
var Palette = struct {
	Base, Mantle, Crust                        lipgloss.Color
	Surface0, Surface1, Surface2               lipgloss.Color
	Overlay0, Overlay1, Overlay2               lipgloss.Color
	Text, Subtext1, Subtext0                   lipgloss.Color
	Rosewater, Flamingo, Pink, Mauve           lipgloss.Color
	Red, Maroon, Peach, Yellow                 lipgloss.Color
	Green, Teal, Sky, Sapphire, Blue, Lavender lipgloss.Color
}{
	Base:      lipgloss.Color("#1e1e2e"),
	Mantle:    lipgloss.Color("#181825"),
	Crust:     lipgloss.Color("#11111b"),
	Surface0:  lipgloss.Color("#313244"),
	Surface1:  lipgloss.Color("#45475a"),
	Surface2:  lipgloss.Color("#585b70"),
	Overlay0:  lipgloss.Color("#6c7086"),
	Overlay1:  lipgloss.Color("#7f849c"),
	Overlay2:  lipgloss.Color("#9399b2"),
	Text:      lipgloss.Color("#cdd6f4"),
	Subtext1:  lipgloss.Color("#bac2de"),
	Subtext0:  lipgloss.Color("#a6adc8"),
	Rosewater: lipgloss.Color("#f5e0dc"),
	Flamingo:  lipgloss.Color("#f2cdcd"),
	Pink:      lipgloss.Color("#f5c2e7"),
	Mauve:     lipgloss.Color("#cba6f7"),
	Red:       lipgloss.Color("#f38ba8"),
	Maroon:    lipgloss.Color("#eba0ac"),
	Peach:     lipgloss.Color("#fab387"),
	Yellow:    lipgloss.Color("#f9e2af"),
	Green:     lipgloss.Color("#a6e3a1"),
	Teal:      lipgloss.Color("#94e2d5"),
	Sky:       lipgloss.Color("#89dceb"),
	Sapphire:  lipgloss.Color("#74c7ec"),
	Blue:      lipgloss.Color("#89b4fa"),
	Lavender:  lipgloss.Color("#b4befe"),
}

// Theme is the set of ROLES every renderer reads (spec: docs/plans/2026-10-20-
// tui-redesign.md §2.1). Palette holds the raw swatches; Theme collapses them
// into about fifteen meanings so a color never means two things. Mauve = you /
// focus, Yellow = needs the user, Blue = running, Green = done, Red = broken.
type Theme struct {
	BgChat, BgChrome, BgRaised               lipgloss.Color
	BgCursor, BgHover                        lipgloss.Color
	Rule, BorderIdle                         lipgloss.Color
	TextPrimary, TextSecondary, TextMuted    lipgloss.Color
	Accent, Attn, Run, Ok, Err               lipgloss.Color
	KindCommander, KindManager, KindOperator lipgloss.Color
	OnKind                                   lipgloss.Color // text on a kind-colored pill
}

// Mocha is the default (dark) flavor.
var Mocha = Theme{
	BgChat: Palette.Base, BgChrome: Palette.Mantle, BgRaised: Palette.Surface0,
	BgCursor: Palette.Surface1, BgHover: Palette.Surface0, // the highlighted row must be unmistakable, focused or not; hover (inspector tabs) is quieter
	Rule: Palette.Surface2, BorderIdle: Palette.Surface1,
	TextPrimary: Palette.Text, TextSecondary: Palette.Subtext0, TextMuted: Palette.Overlay1,
	Accent: Palette.Mauve, Attn: Palette.Yellow, Run: Palette.Blue, Ok: Palette.Green, Err: Palette.Red,
	KindCommander: Palette.Mauve, KindManager: Palette.Sapphire, KindOperator: Palette.Teal,
	OnKind: Palette.Crust,
}

// Latte is the light flavor (CMDR_FLAVOR=latte).
var Latte = Theme{
	BgChat: lipgloss.Color("#eff1f5"), BgChrome: lipgloss.Color("#e6e9ef"), BgRaised: lipgloss.Color("#ccd0da"),
	BgCursor: lipgloss.Color("#bcc0cc"), BgHover: lipgloss.Color("#ccd0da"),
	Rule: lipgloss.Color("#acb0be"), BorderIdle: lipgloss.Color("#bcc0cc"),
	TextPrimary: lipgloss.Color("#4c4f69"), TextSecondary: lipgloss.Color("#6c6f85"), TextMuted: lipgloss.Color("#8c8fa1"),
	Accent: lipgloss.Color("#8839ef"), Attn: lipgloss.Color("#df8e1d"), Run: lipgloss.Color("#1e66f5"),
	Ok: lipgloss.Color("#40a02b"), Err: lipgloss.Color("#d20f39"),
	KindCommander: lipgloss.Color("#8839ef"), KindManager: lipgloss.Color("#209fb5"), KindOperator: lipgloss.Color("#179299"),
	OnKind: lipgloss.Color("#eff1f5"),
}

// T is the active theme. Everything below (and every renderer) reads roles from
// it, so a flavor swap is this one assignment.
var T = pickTheme()

func pickTheme() Theme {
	if os.Getenv("CMDR_FLAVOR") == "latte" {
		return Latte
	}
	return Mocha
}

// BorderFocus is the rounded-border foreground for the column the user is
// currently driving. Mauve is the project's accent — it's the same hue used
// by the title accent bar and section headers, so a focused box "echoes"
// the title region and the eye locks on instantly.
var BorderFocus = lipgloss.NewStyle().Foreground(T.Accent)

// BorderIdle is the foreground for unfocused box borders. Surface2 is light
// enough to be visible on Base/Mantle backgrounds (the old `8` ANSI grey
// disappeared on dark terminals) but dim enough to not compete with the
// focused box.
var BorderIdle = lipgloss.NewStyle().Foreground(T.BorderIdle)

// HeaderStyle renders section headers (OPERATORS, FLEET, WORKTREE...) in
// the project's accent color, bold, intended to be uppercased by callers.
// We don't uppercase here so callers can pass mixed-case labels that
// shouldn't be folded.
var HeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(T.TextMuted)

// SectionPill is a chunkier variant of HeaderStyle — same accent but
// reversed onto the Crust background — for high-density labels that need
// to read as "tab-like" rather than "header-like".
var SectionPill = lipgloss.NewStyle().
	Bold(true).
	Foreground(Palette.Crust).
	Background(Palette.Mauve).
	Padding(0, 1)

// AccentBar is the "▎" prefix lazygit uses on every titled box. Rendering
// it through a style ensures the bar picks up the accent color even when
// embedded in an otherwise unstyled string.
var AccentBar = lipgloss.NewStyle().Foreground(T.Accent).Render("▎")

// NumberHighStyle highlights load-bearing numbers — costs, counts, ceiling
// usage. Lavender pops against Text without screaming like Mauve would.
var NumberHighStyle = lipgloss.NewStyle().Bold(true).Foreground(Palette.Lavender)

// DimText is the standard "secondary" foreground — labels next to numbers,
// metadata that supports a primary value. Subtext0 is one tier above
// Overlay so the eye still parses it as content, not chrome.
var DimText = lipgloss.NewStyle().Foreground(T.TextSecondary)

// FaintText is for chrome-level tertiary content — separators, unit
// suffixes, hint labels, meta. It is the TextMuted tier (readable: >= 5:1 on
// the chrome background). It is deliberately NOT SGR "faint": that is rendered
// differently by every terminal and turns to mush over translucent windows.
var FaintText = lipgloss.NewStyle().Foreground(T.TextMuted)

// projectChipPalette is the rotating accent set ProjectChip cycles through
// via fnv hash. Order is curated so adjacent indices contrast (mauve ↔ sky
// ↔ teal...) rather than two pinks landing next to each other.
var projectChipPalette = []lipgloss.Color{
	Palette.Mauve,
	Palette.Sky,
	Palette.Teal,
	Palette.Peach,
	Palette.Pink,
	Palette.Sapphire,
	Palette.Lavender,
}

// ProjectChip returns the project name styled with a stable accent color
// derived from an fnv hash of the name. Stability matters — frontend must
// be the same color in Commander, Lead, Operator, and the BottomBar so the
// user pattern-matches by hue across views.
func ProjectChip(name string) string {
	if name == "" {
		return ""
	}
	color := projectChipPalette[projectChipIndex(name)]
	return lipgloss.NewStyle().Bold(true).Foreground(color).Render(name)
}

// ProjectChipColor exposes the resolved color for a project name so callers
// that need to color sub-pieces (status counts, separators) the same way
// can do so without re-hashing.
func ProjectChipColor(name string) lipgloss.Color {
	if name == "" {
		return Palette.Subtext0
	}
	return projectChipPalette[projectChipIndex(name)]
}

func projectChipIndex(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32()) % len(projectChipPalette)
}

// StatusColor maps an operator/task status string to the palette color the
// rest of the UI uses for that status. Centralized so StatusIcon, the
// metrics counts, the bottom bar, and the ceilings tier all agree.
func StatusColor(status string) lipgloss.Color {
	switch status {
	case "working", "executing":
		return T.Run
	case "blocked":
		return T.Err
	case "needs-input":
		return T.Attn
	case "done", "reviewing":
		return T.Ok
	case "idle", "":
		return T.TextMuted
	case "brainstorming", "planning":
		return T.Accent
	default:
		return T.TextSecondary
	}
}

// RoleBadge renders a chat role tag (system/user/assistant) as a chunky
// pill — Crust foreground on a role-tinted background — so the eye can
// scan the transcript and skip directly to the next role boundary.
func RoleBadge(role string) string {
	var bg lipgloss.Color
	switch role {
	case "system":
		bg = Palette.Sapphire
	case "user":
		bg = Palette.Lavender
	case "assistant":
		bg = Palette.Mauve
	default:
		bg = Palette.Surface2
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(Palette.Crust).
		Background(bg).
		Padding(0, 1).
		Render(role)
}

// Keybind renders one footer hint: the key in bold primary text, the label
// muted. No filled key cap: a cap on every hint is the loudest thing in a quiet
// column.
func Keybind(key, label string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(T.TextPrimary).Render(key) + " " +
		lipgloss.NewStyle().Foreground(T.TextMuted).Render(label)
}

// HintLines wraps key/label pairs onto as many lines as needed (pair
// boundaries only), each at most width wide.
func HintLines(width int, pairs ...[2]string) []string {
	var lines []string
	var cur [][2]string
	flush := func() {
		if len(cur) > 0 {
			lines = append(lines, HintLine(0, cur...))
			cur = nil
		}
	}
	for _, p := range pairs {
		next := append(append([][2]string{}, cur...), p)
		if width > 0 && len(cur) > 0 && lipgloss.Width(HintLine(0, next...)) > width {
			flush()
			next = [][2]string{p}
		}
		cur = next
	}
	flush()
	return lines
}

// SectionLabel is a column's section title: "LABEL ──────── count". The label is
// muted (Accent while the column has focus), the rule is the Rule color and the
// count, if any, sits at the right edge. With width <= 0 it is just the label.
func SectionLabel(label string, focused bool, count string, width int) string {
	color := T.TextMuted
	if focused {
		color = T.Accent
	}
	l := lipgloss.NewStyle().Bold(true).Foreground(color).Render(label)
	cnt := ""
	if count != "" {
		cnt = " " + lipgloss.NewStyle().Foreground(T.TextMuted).Render(count)
	}
	rule := width - lipgloss.Width(l) - lipgloss.Width(cnt) - 2
	if width <= 0 || rule < 3 {
		return l + cnt
	}
	return l + " " + lipgloss.NewStyle().Foreground(T.Rule).Render(strings.Repeat("─", rule)) + cnt
}

// HintLine joins key/label pairs with two spaces ("key label  key label"),
// dropping trailing pairs that do not fit in width, never cutting mid-pair.
func HintLine(width int, pairs ...[2]string) string {
	out := ""
	for _, p := range pairs {
		next := Keybind(p[0], p[1])
		if out != "" {
			next = out + "  " + next
		}
		if width > 0 && lipgloss.Width(next) > width {
			break
		}
		out = next
	}
	return out
}

// ModeChip returns a compact label pill for the operator side panel's
// "Mode: knowledge / code" row. Knowledge mode is green (read-only,
// safe), code mode is blue (writes, code is changing).
func ModeChip(mode string) string {
	var bg lipgloss.Color
	switch mode {
	case "knowledge":
		bg = Palette.Green
	case "code":
		bg = Palette.Blue
	default:
		bg = Palette.Surface2
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(Palette.Crust).
		Background(bg).
		Padding(0, 1).
		Render(mode)
}
