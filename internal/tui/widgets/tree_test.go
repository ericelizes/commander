package widgets

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// keyMsg builds a tea.KeyMsg from a single rune key or special string
// (matched by Update's switch on .String()). Mirrors how teatest pumps keys.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// fixtureNodes returns a small Objective→Task tree the tests can share.
func fixtureNodes() []TreeNode {
	return []TreeNode{
		{ID: "obj-1", Title: "Onboard frontend", Status: "executing", Depth: 0, Expanded: true},
		{ID: "task-1", Title: "audit auth flow", Status: "working", Depth: 1, Expanded: false},
		{ID: "task-2", Title: "rewrite OpenAPI types", Status: "blocked", Depth: 1, Expanded: false},
		{ID: "obj-2", Title: "Refactor sandbox", Status: "planning", Depth: 0, Expanded: false},
	}
}

// TestTree_SetAndViewShowsAllTitles confirms every node's title makes it
// into the rendered output (no accidental clipping at typical widths).
func TestTree_SetAndViewShowsAllTitles(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	out := tr.View()
	for _, want := range []string{
		"Onboard frontend",
		"audit auth flow",
		"rewrite OpenAPI types",
		"Refactor sandbox",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing title %q\n%s", want, out)
		}
	}
}

// TestTree_DepthIndents verifies child rows render with an explicit branch
// marker while top-level rows do not.
func TestTree_DepthIndents(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	out := tr.View()
	lines := strings.Split(out, "\n")
	if strings.Contains(lines[0], "↳") {
		t.Errorf("depth-0 row should not include child branch marker: %q", lines[0])
	}
	if !strings.Contains(lines[1], "↳") {
		t.Errorf("depth-1 row should include child branch marker: %q", lines[1])
	}
}

func TestTree_SelectionKeepsIndentationStable(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())

	initial := strings.Split(tr.View(), "\n")
	if !strings.Contains(initial[1], "↳") {
		t.Fatalf("expected initial child marker: %q", initial[1])
	}

	updated, _ := tr.Update(keyMsg("down"))
	tr = &updated
	after := strings.Split(tr.View(), "\n")
	if !strings.Contains(after[1], "↳") {
		t.Fatalf("selected child row should still include child marker: %q", after[1])
	}
	if !strings.HasPrefix(after[0], "  ") {
		t.Fatalf("unselected parent row should keep fixed-width gutter: %q", after[0])
	}
}

func TestTree_MutedChildDoesNotRenderStatusIcon(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes([]TreeNode{
		{ID: "project", Title: "manifest (code)", Emphasis: true},
		{ID: "empty", Title: "(no objectives yet)", Depth: 1, Muted: true},
	})

	out := tr.View()
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected two lines, got:\n%s", out)
	}
	if strings.Contains(lines[1], "◆") || strings.Contains(lines[1], "◇") || strings.Contains(lines[1], "⏵") {
		t.Fatalf("muted placeholder should not render a status icon: %q", lines[1])
	}
	if !strings.Contains(lines[1], "(no objectives yet)") {
		t.Fatalf("expected muted placeholder title, got: %q", lines[1])
	}
}

// TestTree_DownMovesSelectionAndSelectedReturnsCorrect drives the cursor
// down and confirms Selected() reports the new node.
func TestTree_DownMovesSelectionAndSelectedReturnsCorrect(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())

	if tr.Selected().ID != "obj-1" {
		t.Fatalf("initial selection = %q, want obj-1", tr.Selected().ID)
	}
	updated, _ := tr.Update(keyMsg("down"))
	tr = &updated
	if tr.Selected().ID != "task-1" {
		t.Errorf("after down, selection = %q, want task-1", tr.Selected().ID)
	}
}

// TestTree_UpAtTopClamps confirms pressing up at index 0 doesn't underflow.
func TestTree_UpAtTopClamps(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	updated, _ := tr.Update(keyMsg("up"))
	tr = &updated
	if tr.Selected().ID != "obj-1" {
		t.Errorf("up at top should clamp to obj-1, got %q", tr.Selected().ID)
	}
}

// TestTree_DownAtBottomClamps confirms walking past the last row doesn't
// overflow.
func TestTree_DownAtBottomClamps(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	for i := 0; i < 10; i++ {
		updated, _ := tr.Update(keyMsg("down"))
		tr = &updated
	}
	if tr.Selected().ID != "obj-2" {
		t.Errorf("down past bottom should clamp to obj-2, got %q", tr.Selected().ID)
	}
}

// TestTree_EnterEmitsSelectedMsg fires Enter and inspects the returned cmd.
// Bubble Tea cmds are funcs that return a Msg — we invoke and assert.
func TestTree_EnterEmitsSelectedMsg(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	updated, cmd := tr.Update(keyMsg("enter"))
	tr = &updated
	if cmd == nil {
		t.Fatal("enter on a node should return a non-nil cmd")
	}
	msg := cmd()
	sel, ok := msg.(TreeNodeSelectedMsg)
	if !ok {
		t.Fatalf("expected TreeNodeSelectedMsg, got %T (%+v)", msg, msg)
	}
	if sel.ID != "obj-1" {
		t.Errorf("selected msg ID = %q, want obj-1", sel.ID)
	}
}

// TestTree_LeftCollapsesExpanded checks that pressing left on an expanded
// row emits a TreeNodeToggledMsg{Expanded: false}.
func TestTree_LeftCollapsesExpanded(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes()) // obj-1 starts Expanded=true
	updated, cmd := tr.Update(keyMsg("left"))
	tr = &updated
	if cmd == nil {
		t.Fatal("left on expanded node should emit a cmd")
	}
	msg, ok := cmd().(TreeNodeToggledMsg)
	if !ok {
		t.Fatalf("expected TreeNodeToggledMsg, got %T", cmd())
	}
	if msg.ID != "obj-1" || msg.Expanded {
		t.Errorf("toggle msg = %+v, want {obj-1, false}", msg)
	}
}

// TestTree_RightExpandsCollapsed walks down to obj-2 (Expanded=false) and
// presses right; emits {Expanded: true}.
func TestTree_RightExpandsCollapsed(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	for i := 0; i < 3; i++ {
		updated, _ := tr.Update(keyMsg("down"))
		tr = &updated
	}
	if tr.Selected().ID != "obj-2" {
		t.Fatalf("expected to be on obj-2, got %q", tr.Selected().ID)
	}
	updated, cmd := tr.Update(keyMsg("right"))
	tr = &updated
	if cmd == nil {
		t.Fatal("right on collapsed node should emit a cmd")
	}
	msg, ok := cmd().(TreeNodeToggledMsg)
	if !ok {
		t.Fatalf("expected TreeNodeToggledMsg, got %T", cmd())
	}
	if msg.ID != "obj-2" || !msg.Expanded {
		t.Errorf("toggle msg = %+v, want {obj-2, true}", msg)
	}
}

// TestTree_EmptyStateRenders confirms a freshly-built tree (no nodes) shows
// the (no items) placeholder rather than panicking.
func TestTree_EmptyStateRenders(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	out := tr.View()
	if !strings.Contains(out, "(no items)") {
		t.Errorf("empty tree should render placeholder, got %q", out)
	}
	// Selected on empty tree must return nil, not panic.
	if tr.Selected() != nil {
		t.Errorf("Selected on empty tree should be nil")
	}
	// Pressing keys on empty tree must not panic.
	tr.Update(keyMsg("enter"))
	tr.Update(keyMsg("down"))
}

// TestTree_SetNodesClampsSelection re-feeds a shorter list and verifies the
// cursor doesn't dangle past the new end.
func TestTree_SetNodesClampsSelection(t *testing.T) {
	tr := NewTree()
	tr.SetSize(60, 10)
	tr.SetNodes(fixtureNodes())
	// Walk to the last row.
	for i := 0; i < 10; i++ {
		updated, _ := tr.Update(keyMsg("down"))
		tr = &updated
	}
	tr.SetNodes([]TreeNode{
		{ID: "only", Title: "only one", Status: "idle"},
	})
	if tr.Selected() == nil || tr.Selected().ID != "only" {
		t.Errorf("after shrinking, selection should clamp to last row, got %+v", tr.Selected())
	}
}

func TestTree_NoSGRFaintAnywhereInTheSharedStyles(t *testing.T) {
	for name, st := range map[string]lipgloss.Style{"FaintText": FaintText, "DimText": DimText, "HeaderStyle": HeaderStyle} {
		if st.GetFaint() {
			t.Errorf("%s uses SGR faint; use a text tier instead", name)
		}
	}
	if StatusColor("needs-input") != T.Attn || StatusColor("working") != T.Run || StatusColor("done") != T.Ok || StatusColor("blocked") != T.Err {
		t.Fatal("status colors must come from the role palette (Attn / Run / Ok / Err)")
	}
}

func TestTree_HighlightAlwaysShownArrowOnlyWhenFocusedCurrentBarAlways(t *testing.T) {
	tr := NewTree()
	tr.SetSize(40, 10)
	tr.SetNodes([]TreeNode{
		{ID: "a", Title: "Alpha", Columnar: true, Prefix: "1", Current: true},
		{ID: "b", Title: "Beta", Columnar: true, Prefix: "2"},
	})
	focused := stripANSIForTest(tr.View())
	if !strings.Contains(focused, "▌›") {
		t.Fatalf("current row with focus shows the bar and the arrow:\n%q", focused)
	}
	tr.SetFocused(false)
	blurred := stripANSIForTest(tr.View())
	if strings.Contains(blurred, "›") {
		t.Fatalf("the arrow marks focus only:\n%q", blurred)
	}
	if !strings.Contains(blurred, "▌") {
		t.Fatalf("the current-window bar stays when unfocused:\n%q", blurred)
	}
}

func TestTree_CursorBandSpansTheFullWidthWithTheRaisedBackground(t *testing.T) {
	tr := NewTree()
	tr.SetSize(30, 10)
	tr.SetNodes([]TreeNode{{ID: "a", Title: "Alpha", Columnar: true, Prefix: "1"}})
	if w := lipgloss.Width(tr.View()); w != 30 {
		t.Fatalf("the band must fill the column: width %d, want 30", w)
	}
}

var ansiForTest = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSIForTest(s string) string { return ansiForTest.ReplaceAllString(s, "") }
