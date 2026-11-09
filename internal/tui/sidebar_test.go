package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/example-user/commander/internal/beads"
)

func sidebar(t *testing.T) *ObjectivesColumn {
	t.Helper()
	return panelColumn(t, []beads.Bead{
		{ID: "w1", Title: "First", Status: "open", CreatedAt: "1"},
		{ID: "w1.1", Title: "First step", Status: "open"},
		{ID: "w2", Title: "Second", Status: "open", CreatedAt: "2"},
		{ID: "w3", Title: "Third", Status: "open", CreatedAt: "3"},
	}, 60)
}

func selectedTitle(c *ObjectivesColumn) string {
	if sel := c.tree.Selected(); sel != nil {
		return sel.Title
	}
	return ""
}

func current(c *ObjectivesColumn) string {
	for _, n := range c.tree.Nodes() {
		if n.Current {
			return n.Title
		}
	}
	return ""
}

func TestSidebar_RowZeroIsCommanderThenRankedWorkstreams(t *testing.T) {
	view := stripANSI(sidebar(t).View())
	i0, i1, i2 := strings.Index(view, "0  Commander"), strings.Index(view, "1  First"), strings.Index(view, "2  Second")
	if i0 < 0 || i1 < i0 || i2 < i1 {
		t.Fatalf("expected 0 Commander, then 1.., in order:\n%s", view)
	}
	if got := selectedTitle(sidebar(t)); !strings.HasPrefix(got, "First") {
		t.Fatalf("with no window identity the cursor starts on the first workstream, got %q", got)
	}
}

func TestSidebar_HighlightAndCursorFollowTheWindowYouAreIn(t *testing.T) {
	c := sidebar(t)
	c.applyOwnWindow(ownWindowMsg{ok: true, name: "#2 Second", workstream: "w2", windowActive: true})
	if current(c) != "Second" || !strings.HasPrefix(selectedTitle(c), "Second") {
		t.Fatalf("window #2: current=%q selected=%q", current(c), selectedTitle(c))
	}

	// Browse away, then switch back to this window: the cursor returns to it.
	c = press(c, "j")
	if !strings.HasPrefix(selectedTitle(c), "Third") {
		t.Fatalf("browsing moved the cursor, got %q", selectedTitle(c))
	}
	updated, _ := c.Update(tea.FocusMsg{})
	c = updated.(*ObjectivesColumn)
	if !strings.HasPrefix(selectedTitle(c), "Second") || current(c) != "Second" {
		t.Fatalf("focus should put the cursor back on the current window: selected=%q current=%q", selectedTitle(c), current(c))
	}
}

func TestSidebar_CommanderWindowHighlightsRowZero(t *testing.T) {
	c := sidebar(t)
	c.applyOwnWindow(ownWindowMsg{ok: true, name: "commander", windowActive: true})
	if current(c) != "Commander" || selectedTitle(c) != "Commander" {
		t.Fatalf("Commander window: current=%q selected=%q", current(c), selectedTitle(c))
	}
	// Enter on row 0 goes to the Commander window.
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	// J/K and the step keys do nothing on row 0.
	before := stripANSI(c.View())
	c = press(c, "J", "K", "x")
	if after := stripANSI(c.View()); strings.Contains(after, "mark") || c.prompt != "" || !strings.Contains(before, "0  Commander") {
		t.Fatalf("row 0 is not a workstream; prompt=%q", c.prompt)
	}
}

func TestSidebar_OperatorWindowsHaveNoHighlight(t *testing.T) {
	c := sidebar(t)
	c.applyOwnWindow(ownWindowMsg{ok: true, name: "op-fix-bug", windowActive: true})
	if got := current(c); got != "" {
		t.Fatalf("an operator window is not a row, got current=%q", got)
	}
}

func TestSidebar_QDoesNotCloseIt(t *testing.T) {
	c := sidebar(t)
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q must not close the navigator")
		}
	}
}

func TestSidebar_OneDimHintLineAndQuestionMarkShowsEveryKey(t *testing.T) {
	c := sidebar(t)
	view := stripANSI(c.View())
	if strings.Count(view, "?") != 1 || !strings.Contains(view, "? keys") || strings.Contains(view, "s star") {
		t.Fatalf("default footer is one short hint line:\n%s", view)
	}
	c = press(c, "?")
	if view := stripANSI(c.View()); !strings.Contains(view, "s star") || !strings.Contains(view, "e expand") {
		t.Fatalf("? should list every key:\n%s", view)
	}
	c = press(c, "?")
	if view := stripANSI(c.View()); strings.Contains(view, "s star") {
		t.Fatalf("? toggles back:\n%s", view)
	}
}

func TestSidebar_CommanderIsItsOwnSectionAboveTheWorkstreamsHeader(t *testing.T) {
	view := stripANSI(sidebar(t).View())
	iCmdr, iHeader, iFirst := strings.Index(view, "0  Commander"), strings.Index(view, "WORKSTREAMS"), strings.Index(view, "1  First")
	if iCmdr < 0 || iHeader < iCmdr || iFirst < iHeader {
		t.Fatalf("order must be 0 Commander, then the WORKSTREAMS header, then the rows:\n%s", view)
	}
	// The header is a divider, never a stop for the cursor.
	for _, n := range sidebar(t).tree.Nodes() {
		if n.Header && !n.Skip {
			t.Fatal("the WORKSTREAMS header must not be selectable")
		}
	}
}

func accordionColumn(t *testing.T) *ObjectivesColumn {
	t.Helper()
	return panelColumn(t, []beads.Bead{
		{ID: "w1", Title: "First", Status: "open", CreatedAt: "1"},
		{ID: "w1.1", Title: "first step", Status: "open", Labels: []string{"kind:objective", "needs-user"}},
		{ID: "w1.2", Title: "other first step", Status: "in_progress"},
		{ID: "w2", Title: "Second", Status: "open", CreatedAt: "2"},
		{ID: "w2.1", Title: "second step", Status: "in_progress"},
		{ID: "w3", Title: "Third", Status: "open", CreatedAt: "3"},
		{ID: "w3.1", Title: "third step", Status: "open"},
	}, 60)
}

func TestSidebar_AccordionOnlyTheCursorWorkstreamShowsItsSteps(t *testing.T) {
	c := accordionColumn(t)
	view := stripANSI(c.View())
	if !strings.Contains(view, "other first step") || strings.Contains(view, "second step") || strings.Contains(view, "third step") {
		t.Fatalf("only the highlighted workstream (1) shows steps:\n%s", view)
	}
	c = press(c, "j")
	view = stripANSI(c.View())
	// (the ⚑ ask stays listed under NEEDS YOU; only the accordion rows move)
	if strings.Contains(view, "other first step") || !strings.Contains(view, "second step") || strings.Contains(view, "third step") {
		t.Fatalf("after j the accordion follows the cursor to workstream 2:\n%s", view)
	}
	// The expanded workstream carries a quiet meta line: project and progress.
	if !strings.Contains(view, "example-app · 0 of 1 done") {
		t.Fatalf("expected the meta line under the open workstream:\n%s", view)
	}
}

func TestSidebar_UnfocusedShowsTheCurrentWindowsWorkstreamNotTheCursors(t *testing.T) {
	c := accordionColumn(t)
	c.applyOwnWindow(ownWindowMsg{ok: true, name: "#3 Third", workstream: "w3", windowActive: true, paneProbe: true, paneActive: false})
	view := stripANSI(c.View())
	if !strings.Contains(view, "third step") || strings.Contains(view, "other first step") {
		t.Fatalf("an unfocused sidebar expands the workstream of its own window:\n%s", view)
	}
	if strings.Contains(view, "↑↓ move") {
		t.Fatalf("key hints show only while the column has focus:\n%s", view)
	}
}

func TestSidebar_CollapsedRowsCarryCountsNotTheProjectName(t *testing.T) {
	c := accordionColumn(t)
	var first, third string
	for _, n := range c.tree.Nodes() {
		if n.Title == "First" {
			for _, p := range n.TagParts {
				first += p.Text + ";"
			}
		}
		if n.Title == "Third" {
			for _, p := range n.TagParts {
				third += p.Text + ";"
			}
		}
		if n.RightTag == "example-app" {
			t.Fatalf("the project name no longer rides on every row: %q", n.Title)
		}
	}
	if first != "⚑ 1;● 1;" { // 1 needs the user, 1 in progress
		t.Fatalf("First counts = %q", first)
	}
	if third != "○ 1;" {
		t.Fatalf("Third counts = %q", third)
	}
}

func TestSidebar_FleetBlockSitsAtTheBottomAboveTheHints(t *testing.T) {
	c := accordionColumn(t)
	lines := strings.Split(stripANSI(c.innerView()), "\n")
	last := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "FLEET") {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("no FLEET block:\n%s", strings.Join(lines, "\n"))
	}
	if len(lines)-last > 6 {
		t.Fatalf("the FLEET block must be pinned to the bottom, but it starts %d lines from the end", len(lines)-last)
	}
	if !strings.Contains(strings.Join(lines[last:], "\n"), "today") {
		t.Fatalf("FLEET shows spend:\n%s", strings.Join(lines[last:], "\n"))
	}
}

func rowOf(view, needle string) int {
	for i, l := range strings.Split(view, "\n") {
		if strings.Contains(l, needle) {
			return i
		}
	}
	return -1
}

func TestSidebar_PointerMovesTheOneHighlightAndItStaysWhereItWasLeft(t *testing.T) {
	c := accordionColumn(t)
	c.tree.SetFocused(false)
	y := rowOf(stripANSI(c.View()), "2  Second")
	if y < 0 {
		t.Fatal("row 2 not found")
	}
	updated, cmd := c.Update(tea.MouseMsg{X: 8, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	c = updated.(*ObjectivesColumn)
	if cmd != nil {
		t.Fatal("moving the pointer is not a command (no expiry timers)")
	}
	if got := selectedTitle(c); !strings.HasPrefix(got, "Second") {
		t.Fatalf("the pointer moves the highlight, got %q", got)
	}
	// Moving over a display-only step row leaves the highlight where it was.
	stepY := rowOf(stripANSI(c.View()), "second step")
	if stepY >= 0 {
		updated, _ = c.Update(tea.MouseMsg{X: 8, Y: stepY, Action: tea.MouseActionMotion})
		if got := selectedTitle(updated.(*ObjectivesColumn)); !strings.HasPrefix(got, "Second") {
			t.Fatalf("step rows are not selectable; highlight must stay, got %q", got)
		}
	}
}

func TestSidebar_ExactlyOneRowIsAlwaysHighlightedEvenUnfocused(t *testing.T) {
	// Force colors on so the escape codes exist, then look for the cursor band.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	band := "48;2;69;71;" // BgCursor #45475a (the profile rounds the last channel; SGR params may be combined)
	for _, focused := range []bool{true, false} {
		c := accordionColumn(t)
		c.tree.SetFocused(focused)
		view := c.View()
		rows := 0
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, band) {
				rows++
			}
		}
		if rows != 1 {
			t.Fatalf("focused=%v: expected exactly one highlighted row, found %d:\n%s", focused, rows, stripANSI(view))
		}
		hasArrow := strings.Contains(stripANSI(view), "›")
		if hasArrow != focused {
			t.Fatalf("focused=%v: the › marks focus only (got %v)", focused, hasArrow)
		}
	}
}

func TestSidebar_ClickSelectsAndGoes(t *testing.T) {
	c := accordionColumn(t)
	y := rowOf(stripANSI(c.View()), "3  Third")
	updated, cmd := c.Update(tea.MouseMsg{X: 8, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	c = updated.(*ObjectivesColumn)
	if cmd == nil {
		t.Fatal("a click must run the same open command as Enter")
	}
	if got := selectedTitle(c); !strings.HasPrefix(got, "Third") {
		t.Fatalf("click selects the row, got %q", got)
	}
}

func TestSidebar_NeedsYouSectionListsEveryFlagAndHidesWhenEmpty(t *testing.T) {
	view := stripANSI(accordionColumn(t).View())
	iNeeds, iWS := strings.Index(view, "NEEDS YOU"), strings.Index(view, "WORKSTREAMS")
	if iNeeds < 0 || iNeeds > iWS || !strings.Contains(view, "first step") {
		t.Fatalf("NEEDS YOU lists the flagged step above WORKSTREAMS:\n%s", view)
	}
	c := panelColumn(t, []beads.Bead{{ID: "w1", Title: "First", Status: "open", CreatedAt: "1"}}, 60)
	if strings.Contains(stripANSI(c.View()), "NEEDS YOU") {
		t.Fatal("NEEDS YOU must be hidden when nothing is flagged")
	}
}

func TestSidebar_TypeThroughForwardsOnlyTypedText(t *testing.T) {
	t.Setenv("TMUX_PANE", "%9")
	c := sidebar(t)
	for name, k := range map[string]tea.KeyMsg{
		"F20":        {Type: tea.KeyF20},
		"alt chord":  {Type: tea.KeyRunes, Runes: []rune("a"), Alt: true},
		"paste":      {Type: tea.KeyRunes, Runes: []rune("hello"), Paste: true},
		"escape seq": {Type: tea.KeyRunes, Runes: []rune{0x1b, '[', '3', '4', '~'}},
		"tree nav j": {Type: tea.KeyRunes, Runes: []rune("j")},
	} {
		if cmd := c.forwardTyping(k); cmd != nil {
			t.Errorf("%s must not be forwarded to the chat", name)
		}
	}
	if cmd := c.forwardTyping(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")}); cmd == nil {
		t.Error("a typed letter the sidebar does not use is forwarded")
	}
}

func TestSidebar_SectionTitlesKeepTheirColorWhenFocusMoves(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	c := sidebar(t)
	c.tree.SetFocused(true)
	focused := c.View()
	c.tree.SetFocused(false)
	c.refocus()
	unfocused := c.View()
	for _, label := range []string{"WORKSTREAMS", "FLEET"} {
		colorOf := func(view string) string {
			i := strings.Index(view, label)
			if i < 0 {
				return "missing"
			}
			j := strings.LastIndex(view[:i], "\x1b[")
			return view[j:i]
		}
		if a, b := colorOf(focused), colorOf(unfocused); a != b {
			t.Errorf("%s changes color with focus: %q vs %q", label, a, b)
		}
	}
}

func TestSidebar_QIsSwallowedNotForwardedToTheChat(t *testing.T) {
	t.Setenv("TMUX_PANE", "%9")
	if cmd := sidebar(t).forwardTyping(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd != nil {
		t.Fatal("q (a habitual quit key) must not be typed into the chat")
	}
}

func TestSidebar_FailedLiveReadKeepsTheLastFrameAndSaysReconnecting(t *testing.T) {
	c := accordionColumn(t)
	before := stripANSI(c.View())
	if !strings.Contains(before, "Second") {
		t.Fatalf("fixture should render workstreams:\n%s", before)
	}
	c.applyLiveData(CommanderLiveData{LoadErr: errors.New("database is busy")})
	after := stripANSI(c.View())
	if !strings.Contains(after, "Second") || !strings.Contains(after, "reconnecting") {
		t.Fatalf("a failed read must keep the data and show a marker:\n%s", after)
	}
	// An empty render never blanks the pane.
	var k frameKeeper
	if k.keep("hello") != "hello" || k.keep("  \n ") != "hello" {
		t.Fatal("frameKeeper must hold the last good frame")
	}
}
