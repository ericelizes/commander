package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceColorProfile pins lipgloss to ANSI so styling-dependent tests see
// escape codes even when running without a TTY (CI, `go test`, etc.).
// Lipgloss otherwise auto-detects "no terminal" and strips colors, which
// would cause the BottomBar style assertions to fail spuriously.
func forceColorProfile(t *testing.T) {
	t.Helper()
	lipgloss.SetColorProfile(termenv.ANSI)
}

// TestBottomBar_RendersAllProjects: at a comfortable width every project name
// and at least one status icon shows up in the rendered output.
func TestBottomBar_RendersAllProjects(t *testing.T) {
	bar := NewBottomBar()
	bar.SetSize(140)
	bar.SetFleet([]ProjectFleetSummary{
		{Name: "alpha", Working: 2},
		{Name: "beta", Blocked: 1, HasBlockedAgent: true},
		{Name: "gamma", Idle: 3},
	})

	view := bar.View()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(view, name) {
			t.Errorf("expected view to contain %q, got %q", name, view)
		}
	}
	// Verify icon glyphs land in the output for non-zero statuses.
	for _, glyph := range []string{"⟳", "✗", "—"} {
		if !strings.Contains(view, glyph) {
			t.Errorf("expected view to contain glyph %q, got %q", glyph, view)
		}
	}
}

// TestBottomBar_TruncatesAtNarrowWidth: at 40 cols the bar can't fit 6
// projects, so the suffix `+N more` must appear.
func TestBottomBar_TruncatesAtNarrowWidth(t *testing.T) {
	bar := NewBottomBar()
	bar.SetSize(40)
	bar.SetFleet([]ProjectFleetSummary{
		{Name: "alpha", Working: 1},
		{Name: "bravo", Working: 1},
		{Name: "charlie", Working: 1},
		{Name: "delta", Working: 1},
		{Name: "echo", Working: 1},
		{Name: "foxtrot", Working: 1},
	})

	view := bar.View()
	if !strings.Contains(view, "more") {
		t.Errorf("expected view to contain `more` suffix, got %q", view)
	}
	if !strings.Contains(view, "+") {
		t.Errorf("expected view to contain `+` in `+N more`, got %q", view)
	}
	if !strings.Contains(view, "…") {
		t.Errorf("expected view to contain ellipsis `…`, got %q", view)
	}
}

// TestBottomBar_EmptyFleetRendersIdle: nil fleet → `fleet idle` (the
// substring; styling adds escape codes around it).
func TestBottomBar_EmptyFleetRendersIdle(t *testing.T) {
	forceColorProfile(t)
	bar := NewBottomBar()
	bar.SetSize(120)
	bar.SetFleet(nil)

	view := bar.View()
	if !strings.Contains(view, "fleet idle") {
		t.Errorf("expected view to contain `fleet idle`, got %q", view)
	}
	// The empty state must be dim-styled — assert ANSI faint code.
	if !strings.Contains(view, "\x1b[") {
		t.Errorf("expected view to contain ANSI escape codes (dim styling), got %q", view)
	}
}

// TestBottomBar_BlockedAgentPrefix: HasBlockedAgent=true causes the segment
// to start with the ⚠ glyph.
func TestBottomBar_BlockedAgentPrefix(t *testing.T) {
	bar := NewBottomBar()
	bar.SetSize(140)
	bar.SetFleet([]ProjectFleetSummary{
		{Name: "alpha", Working: 1},
		{Name: "beta", Blocked: 1, HasBlockedAgent: true},
	})

	view := bar.View()
	if !strings.Contains(view, "⚠") {
		t.Errorf("expected view to contain warning glyph ⚠, got %q", view)
	}
	// ⚠ should appear before "beta" not before "alpha".
	warnIdx := strings.Index(view, "⚠")
	alphaIdx := strings.Index(view, "alpha")
	betaIdx := strings.Index(view, "beta")
	if warnIdx < 0 || alphaIdx < 0 || betaIdx < 0 {
		t.Fatalf("missing required substrings in %q", view)
	}
	if warnIdx < alphaIdx {
		t.Errorf("⚠ appeared before alpha — should be only on beta segment: %q", view)
	}
	if warnIdx > betaIdx {
		t.Errorf("⚠ appeared after beta — should prefix beta, got %q", view)
	}
}

// TestBottomBar_AllZeroStatusRendersDash: a project with every count zero
// renders as `<name> —` rather than disappearing.
func TestBottomBar_AllZeroStatusRendersDash(t *testing.T) {
	bar := NewBottomBar()
	bar.SetSize(140)
	bar.SetFleet([]ProjectFleetSummary{
		{Name: "sandbox"},
	})

	view := bar.View()
	if !strings.Contains(view, "sandbox") {
		t.Errorf("expected view to contain `sandbox`, got %q", view)
	}
	if !strings.Contains(view, "—") {
		t.Errorf("expected dash `—` for zero-status project, got %q", view)
	}
}

// TestBottomBar_BuildFleetSummariesFromFixtures: rollup matches the U6 plan
// mockup — frontend (1 blocked, 1 working, 2 idle), survey (1 working, 1
// idle), sandbox (0 of everything).
func TestBottomBar_BuildFleetSummariesFromFixtures(t *testing.T) {
	got := BuildFleetSummariesFromFixtures()
	if len(got) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(got))
	}

	byName := make(map[string]ProjectFleetSummary, len(got))
	for _, s := range got {
		byName[s.Name] = s
	}

	fe, ok := byName["frontend"]
	if !ok {
		t.Fatalf("missing frontend in summaries: %+v", got)
	}
	if fe.Blocked != 1 || fe.Working != 1 || fe.Idle != 2 {
		t.Errorf("frontend counts wrong: blocked=%d working=%d idle=%d (want 1/1/2)",
			fe.Blocked, fe.Working, fe.Idle)
	}
	if !fe.HasBlockedAgent {
		t.Errorf("frontend should have HasBlockedAgent=true")
	}

	sv, ok := byName["survey"]
	if !ok {
		t.Fatalf("missing survey in summaries: %+v", got)
	}
	if sv.Working != 1 || sv.Idle != 1 {
		t.Errorf("survey counts wrong: working=%d idle=%d (want 1/1)",
			sv.Working, sv.Idle)
	}
	if sv.Blocked != 0 || sv.HasBlockedAgent {
		t.Errorf("survey should have zero blocked: blocked=%d hasBlocked=%v",
			sv.Blocked, sv.HasBlockedAgent)
	}

	sbx, ok := byName["sandbox"]
	if !ok {
		t.Fatalf("missing sandbox in summaries: %+v", got)
	}
	if sbx.Blocked+sbx.Working+sbx.NeedsInput+sbx.Done+sbx.Idle != 0 {
		t.Errorf("sandbox should have zero counts, got %+v", sbx)
	}
}

// TestBottomBar_StylingPreserved: rendered output must contain ANSI escape
// codes — the bar is colored, not plain text.
func TestBottomBar_StylingPreserved(t *testing.T) {
	forceColorProfile(t)
	bar := NewBottomBar()
	bar.SetSize(140)
	bar.SetFleet([]ProjectFleetSummary{
		{Name: "alpha", Working: 1, Blocked: 1, HasBlockedAgent: true},
	})

	view := bar.View()
	if !strings.Contains(view, "\x1b[") {
		t.Errorf("expected ANSI escape codes in view, got %q", view)
	}
}
