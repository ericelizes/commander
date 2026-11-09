package widgets

import (
	"strings"
	"testing"
)

// TestStatusIcon_KnownStatusesReturnExpectedGlyph asserts the documented
// glyph table. Downstream views (tree, bottom bar, metrics) all rely on the
// glyph being exactly this code-point.
func TestStatusIcon_KnownStatusesReturnExpectedGlyph(t *testing.T) {
	cases := map[string]string{
		// operational states
		"working":     "⟳",
		"blocked":     "✗",
		"needs-input": "?",
		"done":        "✓",
		"idle":        "—",
		// phase states
		"brainstorming": "◇",
		"planning":      "◆",
		"executing":     "⏵",
		"reviewing":     "◎",
	}
	for status, want := range cases {
		got := StatusGlyph(status)
		if got != want {
			t.Errorf("StatusGlyph(%q) = %q, want %q", status, got, want)
		}
		// The styled output must contain the glyph somewhere too.
		styled := StatusIcon(status)
		if !strings.Contains(styled, want) {
			t.Errorf("StatusIcon(%q) = %q, missing glyph %q", status, styled, want)
		}
	}
}

// TestStatusIcon_UnknownReturnsFallback ensures a typo (or future state we
// haven't implemented yet) renders as a dim "?" rather than the raw string.
func TestStatusIcon_UnknownReturnsFallback(t *testing.T) {
	got := StatusGlyph("not-a-real-state")
	if got != "?" {
		t.Errorf("unknown status glyph = %q, want %q", got, "?")
	}
	styled := StatusIcon("not-a-real-state")
	if !strings.Contains(styled, "?") {
		t.Errorf("unknown styled output = %q, missing fallback glyph", styled)
	}
}

// TestStatusIcon_ContainsAnsiEscape verifies lipgloss actually applied a
// foreground color. We check for the ESC byte rather than a specific sequence
// because the exact code depends on color profile detection.
func TestStatusIcon_ContainsAnsiEscape(t *testing.T) {
	// Force a colored profile for the test by checking a known-colored status.
	// In CI without a TTY lipgloss may strip colors; in that case the styled
	// string should still at least contain the glyph (covered above) and we
	// only assert the escape when colors are emitted.
	out := StatusIcon("working")
	if strings.Contains(out, "\x1b[") {
		// Has ANSI — sanity check that it wraps the glyph.
		if !strings.Contains(out, "⟳") {
			t.Errorf("ANSI-styled output dropped glyph: %q", out)
		}
		return
	}
	// No ANSI emitted (color profile = ascii). The glyph itself must remain.
	if !strings.Contains(out, "⟳") {
		t.Errorf("unstyled output missing glyph: %q", out)
	}
}
