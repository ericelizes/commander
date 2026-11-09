package tui

import (
	"context"
	"github.com/example-user/commander/internal/livedata"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// rowsOf is workstreamRows without the quiet meta line, so the step-order tests
// read the same as before the redesign.
func rowsOf(rank int, ws workstream, expanded, tagProject, showSteps bool) []panelRow {
	var out []panelRow
	for _, r := range workstreamRows(rank, ws, expanded, tagProject, showSteps, animState{}) {
		if r.kind == "now" {
			continue // the live-state line has its own tests
		}
		if r.kind != "meta" && r.kind != "nogates" {
			out = append(out, r)
		}
	}
	return out
}

func step(id, title, state string) nestedObjective {
	return nestedObjective{row: ObjectiveRow{BeadID: id, Title: title, State: state, ProjectName: "p"}, depth: 1}
}

func panelWS(owner string, steps ...nestedObjective) workstream {
	return workstream{
		project: store.Project{ID: 1, Name: "example-app"},
		root:    ObjectiveRow{BeadID: "w", Title: "Keep pipeline healthy", ProjectName: "p", Owner: owner},
		steps:   steps,
	}
}

func titles(rows []panelRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.node.Title)
	}
	return out
}

func TestWorkstreamRows_OrderAndCollapsing(t *testing.T) {
	ws := panelWS("user",
		step("1", "Done one", beads.StateDone),
		step("2", "Next step", beads.StateInProgress),
		step("3", "the user A", beads.StateNeedsUser),
		step("4", "the user B", beads.StateNeedsUser),
		step("5", "Stuck", beads.StateBlocked),
		step("6", "Working", beads.StateInProgress),
		step("7", "Up 1", beads.StateNotStarted),
		step("8", "Up 2", beads.StateNotStarted),
		step("9", "Up 3", beads.StateNotStarted),
		step("10", "Up 4", beads.StateNotStarted),
	)
	got := strings.Join(titles(rowsOf(2, ws, false, true, true)), "|")
	want := "Keep pipeline healthy  → user|Next step|the user A|the user B|Stuck|Working|Up 1|+3 more · Up 2, Up 3, Up 4|✓ 1 done"
	if got != want {
		t.Fatalf("rows =\n%s\nwant\n%s", got, want)
	}
}

func TestWorkstreamRows_ExpandedShowsEverything(t *testing.T) {
	ws := panelWS("",
		step("1", "Done one", beads.StateDone),
		step("2", "Next step", beads.StateNotStarted),
		step("3", "the user A", beads.StateNeedsUser),
		step("4", "the user B", beads.StateNeedsUser),
		step("5", "Up 1", beads.StateNotStarted),
		step("6", "Up 2", beads.StateNotStarted),
		step("7", "Up 3", beads.StateNotStarted),
	)
	got := strings.Join(titles(rowsOf(1, ws, true, false, true)), "|")
	want := "Keep pipeline healthy|Next step|the user A|the user B|Up 1|Up 2|Up 3|Done one"
	if got != want {
		t.Fatalf("rows =\n%s\nwant\n%s", got, want)
	}
}

func TestWorkstreamRows_OwnerShownOnStepOnlyWhenItDiffers(t *testing.T) {
	same, other := step("1", "Same", beads.StateInProgress), step("2", "Other", beads.StateNotStarted)
	same.row.Owner, other.row.Owner = "user", "operator-x"
	got := titles(rowsOf(1, panelWS("user", same, other), false, false, true))
	if got[1] != "Same" || got[2] != "Other  → operator-x" {
		t.Fatalf("rows = %q", got)
	}
}

func TestWorkstreamRows_BrightnessLevels(t *testing.T) {
	rows := rowsOf(1, panelWS("",
		step("1", "Next", beads.StateInProgress),
		step("2", "Later", beads.StateNotStarted),
	), false, true, true)
	header, next, later := rows[0].node.TitleStyle, rows[1].node.TitleStyle, rows[2].node.TitleStyle
	if next != &brightStyle || header != &normalStyle || later != &dimStyle {
		t.Fatalf("styles: header=%p next=%p later=%p", header, next, later)
	}
	if next.GetReverse() {
		t.Fatal("Next is only inverted when it waits on the user")
	}
}

func TestWorkstreamRows_NextOnUserIsToldApartByMarkerNotReverseVideo(t *testing.T) {
	rows := rowsOf(1, panelWS("", step("1", "Review spec", beads.StateNeedsUser)), false, true, true)
	next := rows[1].node
	if next.TitleStyle.GetReverse() || next.TitleStyle.GetFaint() {
		t.Fatal("no reverse video or SGR faint: both turn to mush on a translucent terminal")
	}
	if !next.TitleStyle.GetBold() {
		t.Fatal("the Next step stays bold")
	}
	if !strings.Contains(stripANSI(next.Marker), "⚑") {
		t.Fatalf("a needs-the user Next carries the ⚑ marker, got %q", stripANSI(next.Marker))
	}
}

func TestWorkstreamRows_DoneOnlyAndNoInlineStrikethrough(t *testing.T) {
	rows := rowsOf(1, panelWS("", step("1", "A", beads.StateDone), step("2", "B", beads.StateDone)), false, true, true)
	if got := strings.Join(titles(rows), "|"); got != "Keep pipeline healthy|✓ 2 done" {
		t.Fatalf("rows = %s", got)
	}
	for _, r := range rows {
		if r.node.TitleStyle != nil && r.node.TitleStyle.GetStrikethrough() {
			t.Fatal("no inline strikethrough")
		}
	}
}

func panelColumn(t *testing.T, objectives []beads.Bead, width int) *ObjectivesColumn {
	t.Helper()
	st := openTestStore(t)
	path := filepath.Join(t.TempDir(), "example-app")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	pid, err := st.InsertProject(context.Background(), store.Project{
		Name: "example-app", Type: "code", Path: path, ActivatedAt: 1, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A flagged step always carries its card (the CLI refuses one without).
	for _, b := range objectives {
		if slices.Contains(b.Labels, "needs-user") {
			if err := st.UpsertNeedsYou(context.Background(), store.NeedsYou{ProjectID: pid, BeadID: b.ID, Ask: "Decide", Note: "Decide " + b.Title}); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := NewObjectivesColumn(&Services{Store: st, Beads: selectiveBeadsClient{objectives: objectives}})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	return updated.(*ObjectivesColumn)
}

func TestPanel_MarkersShareOneColumnAndProjectIsRightAligned(t *testing.T) {
	const width = 60
	c := panelColumn(t, []beads.Bead{
		{ID: "w", Title: "Keep pipeline healthy", Status: "open", CreatedAt: "1"},
		{ID: "w.1", Title: "Stuck import job", Status: "in_progress", Labels: []string{"kind:objective", "star"}},
		{ID: "w.2", Title: "Slow query load", Status: "open"},
		{ID: "w.3", Title: "Ask the user", Status: "open", Labels: []string{"kind:objective", "needs-user"}},
		{ID: "w.4", Title: "Old", Status: "closed"},
	}, width)
	var markerCols []int
	for _, line := range strings.Split(stripANSI(c.View()), "\n") {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "›") {
			continue // headers and the key hints
		}
		for _, m := range []string{"●", "⚑", "○", "✗"} {
			if i := strings.Index(line, m); i >= 8 && i < 14 { // steps only: right of the workstream title
				markerCols = append(markerCols, len([]rune(line[:i])))
			}
		}
		if strings.Contains(line, "Keep pipeline healthy") {
			if !strings.HasSuffix(line, "⚑ 1  ● 1") { // right-aligned counts, not the project name
				t.Errorf("counts should be right-aligned at the end: %q", line)
			}
			if w := len([]rune(line)); w != width-4 { // the tree is 2 cells narrower than the pane, plus 2+2 of panel padding
				t.Errorf("header line width = %d, want %d: %q", w, width-2, line)
			}
		}
	}
	if len(markerCols) < 3 {
		t.Fatalf("expected several markers, got %v", markerCols)
	}
	for _, col := range markerCols {
		if col != markerCols[0] {
			t.Fatalf("markers not in one column: %v", markerCols)
		}
	}
}

func TestPanel_EKeyExpandsHiddenSteps(t *testing.T) {
	var objs = []beads.Bead{{ID: "w", Title: "WS", Status: "open", CreatedAt: "1"}}
	for _, id := range []string{"w.1", "w.2", "w.3", "w.4", "w.5", "w.6", "w.7", "w.8"} {
		objs = append(objs, beads.Bead{ID: id, Title: "step " + id, Status: "open"})
	}
	c := panelColumn(t, objs, 80)
	if view := stripANSI(c.View()); !strings.Contains(view, "+2 more") || strings.Contains(view, "○  step w.8") {
		t.Fatalf("collapsed view:\n%s", view)
	}
	c = press(c, "e")
	if view := stripANSI(c.View()); strings.Contains(view, "+2 more") || !strings.Contains(view, "○  step w.8") {
		t.Fatalf("expanded view:\n%s", view)
	}
	c = press(c, "e")
	if view := stripANSI(c.View()); !strings.Contains(view, "+2 more") {
		t.Fatalf("collapsed again:\n%s", view)
	}
}

func manyWorkstreams(n, steps int) []beads.Bead {
	var objs []beads.Bead
	for i := 1; i <= n; i++ {
		id := string(rune('a' + i - 1))
		objs = append(objs, beads.Bead{ID: id, Title: "Workstream " + id, Status: "open", CreatedAt: string(rune('0' + i))})
		for j := 1; j <= steps; j++ {
			objs = append(objs, beads.Bead{ID: id + "." + string(rune('0'+j)), Title: "step " + id + string(rune('0'+j)), Status: "open"})
		}
	}
	return objs
}

func TestPanel_OpensAtTopAndFitsThePane(t *testing.T) {
	c := panelColumn(t, manyWorkstreams(8, 4), 60)
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Fatalf("view is %d lines, pane is 24:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "1  Workstream a") {
		t.Fatalf("workstream 1 must be visible on open:\n%s", view)
	}
	if !strings.Contains(view, "↓ ") || !strings.Contains(view, " more") || strings.Contains(view, "↑ ") {
		t.Fatalf("expected only a '↓ N more' marker at the top of the list:\n%s", view)
	}
}

func TestPanel_ScrollsWithKeysAndWheelKeepingSelectionVisible(t *testing.T) {
	c := panelColumn(t, manyWorkstreams(8, 4), 60)
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	c = updated.(*ObjectivesColumn)

	for i := 0; i < 40; i++ {
		c = press(c, "j")
	}
	view := stripANSI(c.View())
	if strings.Contains(view, "1  Workstream a") || !strings.Contains(view, "↑ ") {
		t.Fatalf("scrolled to the bottom: expected '↑ N more' and workstream 1 out of view:\n%s", view)
	}
	sel := c.tree.Selected()
	if sel == nil || !strings.Contains(view, strings.TrimSpace(sel.Title)) {
		t.Fatalf("selected row %+v must stay visible:\n%s", sel, view)
	}
	if n := len(strings.Split(view, "\n")); n > 24 {
		t.Fatalf("view is %d lines, pane is 24", n)
	}

	for i := 0; i < 20; i++ {
		updated, _ := c.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		c = updated.(*ObjectivesColumn)
	}
	if view := stripANSI(c.innerView()); !strings.Contains(view, "1  Workstream a") || strings.Contains(view, "↑ ") {
		t.Fatalf("wheel up should return to the top:\n%s", view)
	}
}

func TestPanel_CommanderRowCarriesTheFleetWideNeedsYouTotal(t *testing.T) {
	c := panelColumn(t, []beads.Bead{
		{ID: "a", Title: "A", Status: "open", CreatedAt: "1"},
		{ID: "a.1", Title: "one", Status: "open", Labels: []string{"kind:objective", "needs-user"}},
		{ID: "a.2", Title: "two", Status: "open", Labels: []string{"kind:objective", "needs-user"}},
		{ID: "b", Title: "B", Status: "open", CreatedAt: "2"},
		{ID: "b.1", Title: "three", Status: "open", Labels: []string{"kind:objective", "needs-user"}},
		{ID: "b.2", Title: "done one", Status: "closed", Labels: []string{"kind:objective", "needs-user"}},
	}, 60)
	view := stripANSI(c.View())
	var cmdrLine string
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "Commander") && strings.Contains(l, "0") {
			cmdrLine = l
		}
	}
	if !strings.Contains(cmdrLine, "⚑ 3") {
		t.Fatalf("the 0 Commander row shows '⚑ 3' (closed steps don't count), got %q in:\n%s", cmdrLine, view)
	}
	if strings.Contains(view, "need you") && strings.Contains(view, "⚑ 3 need") {
		t.Fatalf("the old '⚑ N need you' footer is gone:\n%s", view)
	}
	if v := stripANSI(panelColumn(t, []beads.Bead{{ID: "a", Title: "A", Status: "open"}}, 60).View()); strings.Contains(v, "⚑") {
		t.Fatalf("no ⚑ anywhere when nothing needs the user:\n%s", v)
	}
}

func TestNeedsYou_ListsOpenNeedsUserStepsInPriorityOrder(t *testing.T) {
	data := CommanderLiveData{
		Projects: []store.Project{{ID: 1, Name: "p"}},
		Objectives: []ObjectiveRow{
			{BeadID: "a", Title: "WS A", ProjectID: 1, CreatedAt: "1"},
			{BeadID: "a.1", ParentBeadID: "a", Title: "verify it", ProjectID: 1, State: beads.StateNeedsUser, Note: "Verify it works."},
			{BeadID: "a.2", ParentBeadID: "a", Title: "other", ProjectID: 1, State: beads.StateInProgress},
		},
	}
	got := needsYou(data)
	if len(got) != 1 || got[0].BeadID != "a.1" || got[0].Workstream != "WS A" || got[0].Project != "p" {
		t.Fatalf("got %+v", got)
	}
}

func TestNumberWorkstreams_CarriesTheManagerRole(t *testing.T) {
	data := CommanderLiveData{
		Projects:   []store.Project{{ID: 1, Name: "example-app"}},
		Objectives: []ObjectiveRow{{BeadID: "app-a", Title: "A", ProjectID: 1}},
	}
	if got := numberWorkstreams(data); len(got) != 1 || got[0].ManagerRole != "manager-app-a" {
		t.Fatalf("got %+v", got)
	}
}

func TestPanel_EnterOpensTheHighlightedWorkstreamsManager(t *testing.T) {
	c := panelColumn(t, []beads.Bead{
		{ID: "a", Title: "A", Status: "open", CreatedAt: "1"},
		{ID: "a.1", Title: "step", Status: "open"},
	}, 60)
	sel := c.tree.Selected()
	if sel == nil {
		t.Fatal("expected a selection")
	}
	_, cmd := c.Update(widgets.TreeNodeSelectedMsg{ID: sel.ID})
	if cmd == nil {
		t.Fatal("Enter on a workstream should run internal-open-workstream")
	}
	// A display-only step row never reaches Enter.
	for _, n := range c.tree.Nodes() {
		if n.Skip && c.tree.Selected().ID == n.ID {
			t.Fatalf("cursor on a display-only row %q", n.Title)
		}
	}
}

func TestAgentNow_WorkingIdleStaleAndMissing(t *testing.T) {
	now := time.Unix(10_000, 0)
	role := "manager-ws"
	agents := map[string]store.AgentState{
		role: {Role: role, State: store.AgentWorking, Detail: "Bash: go test ./...", Since: now.Unix() - 120, Updated: now.Unix() - 5},
	}
	a := animState{agents: agents, nowTime: now}
	if text, working := agentNow(role, a); !working || !strings.Contains(stripANSI(text), "Bash: go test ./... · 2m") {
		t.Errorf("working = %v %q", working, stripANSI(text))
	}
	agents[role] = store.AgentState{Role: role, State: store.AgentWorking, Since: now.Unix() - 3600, Updated: now.Unix() - 3000}
	if text, working := agentNow(role, a); working || !strings.Contains(stripANSI(text), "idle 1h") {
		t.Errorf("a silent working agent shows idle: %v %q", working, stripANSI(text))
	}
	agents[role] = store.AgentState{Role: role, State: store.AgentIdle, Since: now.Unix() - 840, Updated: now.Unix() - 840}
	if text, _ := agentNow(role, a); !strings.Contains(stripANSI(text), "idle 14m") {
		t.Errorf("idle = %q", stripANSI(text))
	}
	if text, working := agentNow("manager-none", a); working || !strings.Contains(stripANSI(text), "no agent yet") {
		t.Errorf("missing = %v %q", working, stripANSI(text))
	}
}

func TestMergeAgentStates_HooksWinUnlessStale(t *testing.T) {
	now := time.Unix(10_000, 0)
	hooks := map[string]store.AgentState{
		"fresh": {Role: "fresh", State: store.AgentWorking, Detail: "Edit: x", Updated: now.Unix() - 3},
		"stale": {Role: "stale", State: store.AgentIdle, Updated: now.Unix() - 9000},
	}
	scraped := map[string]store.AgentState{
		"fresh":    {Role: "fresh", State: store.AgentIdle},
		"stale":    {Role: "stale", State: store.AgentWorking},
		"hookless": {Role: "hookless", State: store.AgentIdle},
	}
	got := mergeAgentStates(hooks, scraped, now)
	if got["fresh"].Detail != "Edit: x" || got["stale"].State != store.AgentWorking || got["hookless"].State != store.AgentIdle {
		t.Errorf("merge = %+v", got)
	}
}

func TestAgentNow_WaitingOnAWait(t *testing.T) {
	now := time.Unix(10_000, 0)
	role := "manager-ws"
	a := animState{
		agents:  map[string]store.AgentState{role: {Role: role, State: store.AgentIdle, Since: now.Unix() - 900, Updated: now.Unix() - 900}},
		waits:   map[string]livedata.ManagerWait{role: {Text: "CI #4101", Since: now.Add(-4 * time.Minute)}},
		nowTime: now,
	}
	if text, active := agentNow(role, a); !active || !strings.Contains(stripANSI(text), "waiting · CI #4101 · 4m") {
		t.Errorf("idle with a wait = %v %q", active, stripANSI(text))
	}
	a.agents[role] = store.AgentState{Role: role, State: store.AgentWorking, Detail: "Bash", Since: now.Unix(), Updated: now.Unix()}
	if text, _ := agentNow(role, a); strings.Contains(stripANSI(text), "waiting") {
		t.Errorf("a working Manager reads %q", stripANSI(text))
	}
}

func TestWorkstreamRows_GatesChecklistUnderTheirStage(t *testing.T) {
	stage := step("1", "Build", beads.StateInProgress)
	stage.row.Gates = []store.Gate{
		{Name: "spec", Verifier: "reviewer:mf", Status: store.GatePassed, Evidence: "approved"},
		{Name: "tests", Verifier: "auto", Status: store.GateFailed, Evidence: "2 failures"},
		{Name: "signoff", Verifier: "user", Status: store.GateOpen},
	}
	rows := workstreamRows(1, panelWS("", stage), false, false, true, animState{})
	var got []string
	for _, r := range rows {
		switch r.kind {
		case "gate":
			got = append(got, ansi.Strip(r.node.Marker)+" "+ansi.Strip(r.node.Title))
		case "nogates":
			t.Fatal("a workstream with a gated stage must not say no gates")
		}
	}
	want := []string{"✓ spec  reviewer:mf", "✗ tests  auto · 2 failures", "○ signoff  user"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("gate rows = %q, want %q", got, want)
	}
}

func TestWorkstreamRows_NoGatesWarning(t *testing.T) {
	has := func(ws workstream) bool {
		for _, r := range workstreamRows(1, ws, false, false, false, animState{}) {
			if r.kind == "nogates" && r.node.Title == "⚠ no gates" {
				return true
			}
		}
		return false
	}
	open := panelWS("", step("1", "Build", beads.StateInProgress))
	if !has(open) {
		t.Fatal("an open workstream without gates should be flagged")
	}
	open.root.State = beads.StateDone
	if has(open) {
		t.Fatal("a done workstream is not flagged")
	}
}
