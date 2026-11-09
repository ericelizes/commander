package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func titleBar(width int, msg chatTitleMsg) *ChatTitleColumn {
	c := NewChatTitleColumn()
	m, _ := c.Update(tea.WindowSizeMsg{Width: width, Height: 4})
	m, _ = m.(*ChatTitleColumn).Update(msg)
	return m.(*ChatTitleColumn)
}

func titleRowsOf(c *ChatTitleColumn) []string {
	rows := strings.Split(stripANSI(c.View()), "\n")
	return rows[1:3] // drop the pad rows above and below the bar
}

func liveFixture() CommanderLiveData {
	rows := []ObjectiveRow{
		{BeadID: "ws-1", ProjectID: 1, Title: "Sample shelf overview", State: beads.StateInProgress},
		{BeadID: "s1", ParentBeadID: "ws-1", ProjectID: 1, Title: "Design", State: beads.StateDone},
		{BeadID: "s2", ParentBeadID: "ws-1", ProjectID: 1, Title: "Build the worker", State: beads.StateInProgress},
		{BeadID: "s3", ParentBeadID: "ws-1", ProjectID: 1, Title: "Review", State: beads.StateNeedsUser, Note: "Review the build."},
		{BeadID: "s4", ParentBeadID: "ws-1", ProjectID: 1, Title: "Ship", State: beads.StateNotStarted},
	}
	return CommanderLiveData{
		Projects:   []store.Project{{ID: 1, Name: "example-app"}},
		Objectives: rows,
		Metrics:    FleetMetricsLive{ActiveCount: 3, TotalCostUSD24h: 4.125},
	}
}

func withLive(c *ChatTitleColumn) *ChatTitleColumn {
	m, _ := c.Update(commanderLiveDataMsg{Data: liveFixture()})
	return m.(*ChatTitleColumn)
}

func TestChatTitle_PillNamesTheKindAndTheTitleLosesItsPrefix(t *testing.T) {
	cmdr := titleRowsOf(titleBar(120, chatTitleMsg{kind: "commander", title: "Commander"}))[0]
	if !strings.HasPrefix(cmdr, "   ◆ COMMANDER  Commander ") {
		t.Fatalf("Commander row = %q", cmdr)
	}
	mgr := titleRowsOf(titleBar(120, chatTitleMsg{kind: "manager", title: "Manager | Per-App Observability"}))[0]
	if !strings.HasPrefix(mgr, "   ◇ MANAGER  Per-App Observability ") || strings.Contains(mgr, "Manager |") {
		t.Fatalf("Manager row = %q", mgr)
	}
	op := titleRowsOf(titleBar(120, chatTitleMsg{kind: "operator", title: "Operator | TEAM-1"}))[0]
	if !strings.HasPrefix(op, "   ▸ OPERATOR  TEAM-1 ") {
		t.Fatalf("Operator row = %q", op)
	}
}

func TestChatTitle_IsAFullWidthTwoRowBarWithADistinctAccentPerKind(t *testing.T) {
	for _, width := range []int{40, 99, 100, 160} {
		c := withLive(titleBar(width, chatTitleMsg{kind: "commander", title: "Commander", meta: "~/code · opus"}))
		rows := titleRowsOf(c)
		if len(rows) != 2 {
			t.Fatalf("the bar is two rows, got %d", len(rows))
		}
		for i, r := range rows {
			if w := lipgloss.Width(r); w != width {
				t.Errorf("width %d: row %d is %d wide: %q", width, i, w, r)
			}
		}
	}
	if accentFor("commander") == accentFor("manager") || accentFor("operator") == accentFor("manager") {
		t.Fatal("Commander, Manager and Operator windows need different accents")
	}
}

func TestChatTitle_FillAndRightAlignedMeta(t *testing.T) {
	row := titleRowsOf(titleBar(120, chatTitleMsg{kind: "manager", title: "Manager | X", meta: "~/code/app · opus"}))[0]
	if !strings.HasSuffix(row, " ~/code/app · opus   ") {
		t.Fatalf("meta is not right-aligned: %q", row)
	}
	if strings.Count(row, "╱") < minTitleFill {
		t.Fatalf("fill too short: %q", row)
	}
}

func TestChatTitle_NarrowDropsMetaAndKeepsAtLeastThreeFillCells(t *testing.T) {
	row := titleRowsOf(titleBar(60, chatTitleMsg{kind: "manager", title: "Manager | X", meta: "~/code/app · opus"}))[0]
	if strings.Contains(row, "opus") {
		t.Fatalf("meta must drop below %d cols: %q", narrowTitleWidth, row)
	}
	long := "Manager | " + strings.Repeat("very long workstream ", 6)
	row = titleRowsOf(titleBar(50, chatTitleMsg{kind: "manager", title: long}))[0]
	if strings.Count(row, "╱") < minTitleFill || !strings.Contains(row, "…") || lipgloss.Width(row) != 50 {
		t.Fatalf("long title must truncate, keeping the fill: %q", row)
	}
}

func TestChatTitle_MetaDropsBeforeTheTitleTruncates(t *testing.T) {
	title := "Manager | A fairly long workstream title"
	row := titleRowsOf(titleBar(105, chatTitleMsg{kind: "manager", title: title, meta: strings.Repeat("m", 70)}))[0]
	if !strings.Contains(row, "A fairly long workstream title") || strings.Contains(row, "mmm") {
		t.Fatalf("meta should drop, title stay whole: %q", row)
	}
}

func TestChatTitle_CommanderStatusLine(t *testing.T) {
	c := withLive(titleBar(140, chatTitleMsg{kind: "commander", title: "Commander"}))
	got := titleRowsOf(c)[1]
	want := "⚑ 1 need you   ● 0 working · 1 idle   3 operators running   $4.12 today"
	if !strings.Contains(got, want) {
		t.Fatalf("status = %q, want it to contain %q", got, want)
	}
}

func TestChatTitle_ManagerStatusLineWideAndCompact(t *testing.T) {
	msg := chatTitleMsg{kind: "manager", title: "Manager | Per-app", ws: "ws-1"}
	wide := titleRowsOf(withLive(titleBar(140, msg)))[1]
	want := "★ next Build the worker   ⚑ 1 need you   ● 1 in progress   ✓ 1/4"
	if !strings.Contains(wide, want) {
		t.Fatalf("wide status = %q, want %q", wide, want)
	}
	compact := titleRowsOf(withLive(titleBar(80, msg)))[1]
	if want := "★ Build the wor…  ⚑ 1  ● 1  ✓ 1/4"; !strings.Contains(compact, want) {
		t.Fatalf("compact status = %q, want %q", compact, want)
	}
}

func TestChatTitle_StatusLineIsBlankUntilDataArrivesOrWorkstreamUnknown(t *testing.T) {
	c := titleBar(120, chatTitleMsg{kind: "manager", title: "Manager | X", ws: "ws-1"})
	if strings.TrimSpace(titleRowsOf(c)[1]) != "" {
		t.Fatalf("no data yet, row 2 = %q", titleRowsOf(c)[1])
	}
	c = withLive(titleBar(120, chatTitleMsg{kind: "manager", title: "Manager | X", ws: "nope"}))
	if strings.TrimSpace(titleRowsOf(c)[1]) != "" {
		t.Fatalf("unknown workstream, row 2 = %q", titleRowsOf(c)[1])
	}
}

func TestChatTitle_StylesUseRolesNeverFaintOrReverse(t *testing.T) {
	c := withLive(titleBar(120, chatTitleMsg{kind: "commander", title: "Commander", meta: "m"}))
	// lipgloss strips color without a TTY, so assert on the SGR attributes only.
	for _, sgr := range []string{"\x1b[2m", "\x1b[7m", ";2m", ";7m"} {
		if strings.Contains(c.View(), sgr) {
			t.Fatalf("bar uses SGR %q (faint/reverse)", sgr)
		}
	}
}

func TestChatTitle_GradientEndpointsAreMauveAndPink(t *testing.T) {
	if got := lerpHex("#cba6f7", "#f5c2e7", 0); got != "#cba6f7" {
		t.Fatalf("t=0 = %s", got)
	}
	if got := lerpHex("#cba6f7", "#f5c2e7", 1); got != "#f5c2e7" {
		t.Fatalf("t=1 = %s", got)
	}
	if got := lerpHex("#000000", "#ffffff", 0.5); got != "#808080" {
		t.Fatalf("midpoint = %s", got)
	}
}

func TestChatTitle_HasNoKeysAndKeepsItsTextWhenTheLookupFails(t *testing.T) {
	c := titleBar(40, chatTitleMsg{kind: "manager", title: "Manager | X"})
	m, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Fatal("the bar ignores keys")
	}
	m, _ = m.(*ChatTitleColumn).Update(chatTitleMsg{}) // a failed tmux lookup
	if !strings.Contains(stripANSI(m.(*ChatTitleColumn).View()), "X") {
		t.Fatal("a failed lookup must not blank the bar")
	}
}
