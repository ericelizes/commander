package widgets

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TreeNode is one row in a flat-with-depth tree. The widget itself is
// agnostic about the node's domain — it can render Objectives or Tasks
// equally well — so the contract is small: an ID for messages, a title
// for display, a status string fed to StatusIcon, a Depth to indent by.
//
// Children is unused by the widget today; it's stored so callers parsing
// hierarchical data (Objectives → Tasks) can keep the structural reference
// without a parallel map. Expansion state for now is just whether the row
// should be rendered (parent's Expanded controls visibility of its kids,
// computed by the caller before SetNodes).
type TreeNode struct {
	ID       string
	Title    string
	Status   string
	Depth    int
	Icon     string
	Emphasis bool
	Muted    bool
	Expanded bool
	Children []string
	// Outline renders the row indented by spaces only (no ↳ arrow).
	Outline bool
	// GapBefore inserts a blank line above the row to separate groups.
	GapBefore bool
	// TitleStyle overrides the default title styling when set.
	TitleStyle *lipgloss.Style
	// Current marks the row for the window the viewer is in (the cursor is a
	// separate thing: it is where Enter would go).
	Current bool
	// Header renders the row as a section header ("▎ WORKSTREAMS"), not an item.
	Header bool
	// Skip makes the row display-only: the cursor never lands on it.
	Skip bool
	// Prefix is rendered dim before the icon (e.g. a row number "1 ").
	Prefix string
	// Tag is rendered dim after the title (e.g. the owning project).
	Tag string

	// Columnar switches the row to a fixed status-column layout: Marker (or
	// Prefix when Marker is empty) is right-aligned in a 2-cell column so every
	// marker lines up, then Indent spaces, then the title, with RightTag
	// right-aligned and dim at the end of the line.
	Columnar bool
	// Inline draws the Marker in the title area (after Indent) instead of the
	// number column, so a step sits to the right of the workstream title it
	// belongs to: glyph at the title column + 2, text two cells after it.
	Inline   bool
	Marker   string
	Indent   int
	RightTag string
	// TagParts, when set, replaces RightTag with separately coloured pieces
	// (e.g. "⚑ 2" in Attn next to "● 3" in Run), joined by two spaces.
	TagParts []TagPart
	// NoRule makes a Header a bare label (no rule line, no count).
	NoRule bool
}

// TagPart is one coloured piece of a row's right-aligned meta.
type TagPart struct {
	Text  string
	Color lipgloss.Color
	Bold  bool
}

// TreeNodeSelectedMsg fires when the user presses Enter on a node. The
// parent view (Commander or Lead) uses the ID to drill down or focus.
type TreeNodeSelectedMsg struct {
	ID string
}

// TreeNodeToggledMsg fires when the user presses left/right to collapse or
// expand a row. The widget does not own expansion state — it's a row-level
// boolean caller-managed — so this message is the seam through which the
// caller updates its own model and re-supplies SetNodes.
type TreeNodeToggledMsg struct {
	ID       string
	Expanded bool
}

// Tree is a navigable list with depth-aware indenting and a status icon
// per row. Cursor moves with up/down (and j/k); enter emits a select msg.
// Designed for the leftmost columns of Commander and Lead views.
type Tree struct {
	width    int
	height   int
	nodes    []TreeNode
	selected int
	// focused: the column holds keyboard focus. The cursor (› and its band) is
	// drawn only then, so an unfocused sidebar reads as a map, not a form.
	focused bool
	// visible maps each line of the last View to its node index (-1 for the
	// "↑ N more" / "↓ N more" indicator lines), so mouse rows resolve correctly
	// through gaps and scrolling.
	visible []int
	// offset is the first visible line when the tree is taller than height; it
	// only moves to keep the selected row on screen, so a fresh tree opens at
	// the top.
	offset int
}

// NewTree returns an empty tree. SetNodes populates it.
func NewTree() *Tree {
	return &Tree{focused: true}
}

// RowAt returns the node drawn on line y of the last View, if any.
func (t *Tree) RowAt(y int) (int, bool) {
	if y < 0 || y >= len(t.visible) || t.visible[y] < 0 {
		return 0, false
	}
	return t.visible[y], true
}

// SelectIndex moves the cursor to node i if it is selectable.
func (t *Tree) SelectIndex(i int) bool {
	if i < 0 || i >= len(t.nodes) || t.nodes[i].Skip {
		return false
	}
	t.selected = i
	return true
}

// Selectable reports whether node i can be selected or entered.
func (t *Tree) Selectable(i int) bool { return i >= 0 && i < len(t.nodes) && !t.nodes[i].Skip }

// SetFocused tells the tree whether its column has focus (see Tree.focused).
func (t *Tree) SetFocused(f bool) { t.focused = f }

// Focused reports whether the cursor is being drawn.
func (t *Tree) Focused() bool { return t.focused }

// SetNodes replaces the rendered list. If the previously-selected index is
// now out of range we clamp to the new last row so navigation never lands
// on a phantom slot.
func (t *Tree) SetNodes(nodes []TreeNode) {
	t.nodes = nodes
	if t.selected >= len(nodes) {
		t.selected = len(nodes) - 1
	}
	if t.selected < 0 {
		t.selected = 0
	}
	t.settle()
}

// settle moves the cursor off a display-only (Skip) row, preferring the next
// selectable row and falling back to the previous one.
func (t *Tree) settle() {
	if len(t.nodes) == 0 || !t.nodes[t.selected].Skip {
		return
	}
	for i := t.selected; i < len(t.nodes); i++ {
		if !t.nodes[i].Skip {
			t.selected = i
			return
		}
	}
	for i := t.selected; i >= 0; i-- {
		if !t.nodes[i].Skip {
			t.selected = i
			return
		}
	}
}

// step moves the cursor n selectable rows (negative = up), skipping Skip rows.
func (t *Tree) step(n int) {
	dir := 1
	if n < 0 {
		dir, n = -1, -n
	}
	for ; n > 0; n-- {
		next := t.selected + dir
		for next >= 0 && next < len(t.nodes) && t.nodes[next].Skip {
			next += dir
		}
		if next < 0 || next >= len(t.nodes) {
			return
		}
		t.selected = next
	}
}

// SetSize records canvas dimensions; View truncates rows to width-1 so a
// trailing reverse-video cell doesn't bleed past the column edge.
func (t *Tree) SetSize(w, h int) {
	t.width = w
	t.height = h
}

// Nodes returns the rendered rows in order.
func (t *Tree) Nodes() []TreeNode {
	return t.nodes
}

// Move shifts the cursor by delta selectable rows, clamped to the list.
func (t *Tree) Move(delta int) { t.step(delta) }

// SelectID moves the cursor to the node with id; it reports whether it exists.
func (t *Tree) SelectID(id string) bool {
	for i, n := range t.nodes {
		if n.ID == id {
			t.selected = i
			return true
		}
	}
	return false
}

// Selected returns a pointer to the currently-highlighted node, or nil if
// the tree is empty. Callers should not retain the pointer across SetNodes.
func (t *Tree) Selected() *TreeNode {
	if len(t.nodes) == 0 {
		return nil
	}
	if t.selected < 0 || t.selected >= len(t.nodes) {
		return nil
	}
	return &t.nodes[t.selected]
}

// Update handles navigation keys and emits domain messages on enter /
// left / right. The caller embeds the Tree in a parent model and forwards
// tea.KeyMsg here.
func (t *Tree) Update(msg tea.Msg) (Tree, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "up", "k":
			t.step(-1)
		case "down", "j":
			t.step(1)
		case "enter":
			if sel := t.Selected(); sel != nil {
				return *t, func() tea.Msg {
					return TreeNodeSelectedMsg{ID: sel.ID}
				}
			}
		case "left", "h":
			if sel := t.Selected(); sel != nil && sel.Expanded {
				id := sel.ID
				return *t, func() tea.Msg {
					return TreeNodeToggledMsg{ID: id, Expanded: false}
				}
			}
		case "right", "l":
			if sel := t.Selected(); sel != nil && !sel.Expanded {
				id := sel.ID
				return *t, func() tea.Msg {
					return TreeNodeToggledMsg{ID: id, Expanded: true}
				}
			}
		}
	}
	return *t, nil
}

// knownProjects is the set of project names we color-chip in tree titles.
// Stored as a slice so iteration order is stable; small enough that O(n)
// substring scan dominates a map lookup anyway. Add to this list as new
// fixture / live projects come online.
var knownProjects = []string{"frontend", "survey", "sandbox", "commander", "dotfiles"}

// View renders the tree. Empty state shows a faint "(no items)" so callers
// don't have to special-case it. Selected row avoids a solid background so
// the pane inherits the terminal's own backdrop cleanly inside Ghostty/tmux.
func (t *Tree) View() string {
	if len(t.nodes) == 0 {
		return FaintText.Render("(no items)")
	}

	rows := make([]string, 0, len(t.nodes))

	for i, node := range t.nodes {
		selection := "  "
		cursor := i == t.selected // exactly one row is always highlighted, focused or not
		if cursor {
			selection = lipgloss.NewStyle().Foreground(T.Accent).Render("› ")
		}

		if node.Header {
			w, count := t.width, node.RightTag
			if node.NoRule {
				w, count = 0, ""
			}
			row := SectionLabel(node.Title, true, count, w) // always Accent: focus only adds the › cursor
			if node.GapBefore && i > 0 {
				row = "\n" + row
			}
			rows = append(rows, row)
			continue
		}
		if node.Columnar {
			row := t.columnarRow(node, cursor, t.focused)
			if node.GapBefore && i > 0 {
				row = "\n" + row
			}
			rows = append(rows, row)
			continue
		}

		indent := treeIndent(node.Depth)
		icon := node.Icon
		if icon == "" && !node.Muted && !node.Emphasis {
			icon = StatusIcon(node.Status)
		}

		titleStyle := lipgloss.NewStyle().Foreground(T.TextSecondary)
		if node.Emphasis {
			titleStyle = lipgloss.NewStyle().Bold(true).Foreground(T.TextPrimary)
		}
		if node.Muted {
			titleStyle = FaintText
		}
		title := titleStyle.Render(decorateTitle(node.Title))
		if node.TitleStyle != nil {
			title = node.TitleStyle.Render(node.Title)
		}
		if node.Outline {
			indent = strings.Repeat("  ", node.Depth)
		}

		row := selection + indent
		if node.Prefix != "" {
			row += FaintText.Render(node.Prefix)
		}
		if icon != "" {
			row += icon + " "
		}
		row += title
		if node.Tag != "" {
			row += "  " + FaintText.Render(node.Tag)
		}

		if t.width > 0 && lipgloss.Width(row) > t.width {
			row = truncateAnsi(row, t.width)
		}
		if node.GapBefore && i > 0 {
			row = "\n" + row
		}
		rows = append(rows, row)
	}
	return t.window(rows)
}

// window returns the rows (one entry per node; an entry may start with "\n"
// for a gap line) clipped to t.height lines around the selection. Hidden rows
// are announced with dim "↑ N more" / "↓ N more" lines at the edges.
func (t *Tree) window(rows []string) string {
	var lines []string
	var owner []int                 // node index of each line
	start := make([]int, len(rows)) // first line of each node
	for i, r := range rows {
		start[i] = len(lines)
		for _, l := range strings.Split(r, "\n") {
			lines = append(lines, l)
			owner = append(owner, i)
		}
	}
	total := len(lines)
	if t.height <= 0 || total <= t.height {
		t.offset = 0
		t.visible = owner
		return strings.Join(lines, "\n")
	}

	selStart := start[t.selected]
	// On the first selectable row, display-only rows above it (a section label)
	// belong to it: show them rather than scrolling them off.
	firstSelectable := true
	for i := 0; i < t.selected; i++ {
		if !t.nodes[i].Skip {
			firstSelectable = false
			break
		}
	}
	if firstSelectable {
		selStart = 0
	}
	selEnd := total
	// The selected row owns the display-only rows that follow it (a workstream's
	// steps), so scrolling keeps the whole block in view.
	for i := t.selected + 1; i < len(rows); i++ {
		if !t.nodes[i].Skip {
			selEnd = start[i]
			break
		}
	}
	// A gap line belongs to the row below it, but keep it out of the way: the
	// selected row's own text is what must stay visible.
	if selEnd-selStart > 1 && lines[selStart] == "" {
		selStart++
	}

	area := t.height
	for pass := 0; pass < 4; pass++ {
		area = t.height
		if t.offset > 0 {
			area--
		}
		if t.offset+area < total {
			area--
		}
		if area < 1 {
			area = 1
		}
		if selStart < t.offset {
			t.offset = selStart
		} else if selEnd > t.offset+area {
			t.offset = selEnd - area
		} else {
			break
		}
	}
	if t.offset < 0 {
		t.offset = 0
	}

	end := t.offset + area
	above, below := countNodesBefore(start, t.offset), 0
	for _, st := range start {
		if st >= end {
			below++
		}
	}
	out := make([]string, 0, t.height)
	vis := make([]int, 0, t.height)
	if t.offset > 0 {
		out = append(out, FaintText.Render(fmt.Sprintf("↑ %d more", above)))
		vis = append(vis, -1)
	}
	out = append(out, lines[t.offset:end]...)
	vis = append(vis, owner[t.offset:end]...)
	if end < total {
		out = append(out, FaintText.Render(fmt.Sprintf("↓ %d more", below)))
		vis = append(vis, -1)
	}
	t.visible = vis
	return strings.Join(out, "\n")
}

// countNodesBefore counts nodes that begin above line offset.
func countNodesBefore(start []int, offset int) int {
	n := 0
	for _, st := range start {
		if st < offset {
			n++
		}
	}
	return n
}

// columnarRow renders the fixed-column layout described on TreeNode.Columnar.
//
//	col 0   ▌ when this is the window you are in (Accent)
//	col 1   › when the cursor is here and the column has focus (Accent)
//	col 2-3 status marker, or the right-aligned number
//	then    two spaces, Indent spaces, the title, and RightTag flush right.
//
// With the cursor the whole row sits on a BgRaised band, so every segment is
// rendered with that background (lipgloss resets the bg after each segment).
func (t *Tree) columnarRow(node TreeNode, cursor, focused bool) string {
	sty := func(s lipgloss.Style) lipgloss.Style {
		if cursor {
			return s.Background(T.BgCursor)
		}
		return s
	}
	plain := sty(lipgloss.NewStyle())
	spaces := func(n int) string {
		if n <= 0 {
			return ""
		}
		return plain.Render(strings.Repeat(" ", n))
	}

	bar, arrow := spaces(1), spaces(1)
	if node.Current {
		bar = sty(lipgloss.NewStyle().Bold(true).Foreground(T.Accent)).Render("▌")
	}
	if cursor && focused {
		// Focus makes the highlight stronger: a bold Mauve ›. The band itself is
		// there either way.
		arrow = sty(lipgloss.NewStyle().Bold(true).Foreground(T.Accent)).Render("›")
	}

	cell := node.Marker
	if node.Inline {
		cell = "" // the marker moves into the title area below
	}
	if cell == "" && !node.Inline {
		num := strings.TrimSpace(node.Prefix)
		style := lipgloss.NewStyle().Foreground(T.TextSecondary)
		if node.Current {
			style = lipgloss.NewStyle().Bold(true).Foreground(T.Accent)
		}
		cell = sty(style).Render(num)
	}
	if pad := 2 - lipgloss.Width(cell); pad > 0 {
		cell = spaces(pad) + cell
	}
	left := bar + arrow + cell + spaces(2+node.Indent)
	if node.Inline {
		// Under a workstream: glyph at the title column + Indent (+2), text after it.
		mark := node.Marker
		if mark == "" {
			mark = " "
		}
		left = bar + arrow + spaces(2) + spaces(2+node.Indent) + mark + spaces(2)
	}

	titleStyle := lipgloss.NewStyle().Foreground(T.TextPrimary)
	if node.TitleStyle != nil {
		titleStyle = *node.TitleStyle
	}
	if cursor {
		titleStyle = titleStyle.Bold(true) // the highlighted row reads bold
	}
	title := sty(titleStyle).Render(node.Title)
	tag := node.RightTag
	tagStyle := sty(lipgloss.NewStyle().Foreground(T.TextMuted))
	renderedTag := ""
	if len(node.TagParts) > 0 {
		var pieces []string
		plainTag := make([]string, 0, len(node.TagParts))
		for _, part := range node.TagParts {
			st := lipgloss.NewStyle().Foreground(part.Color).Bold(part.Bold)
			pieces = append(pieces, sty(st).Render(part.Text))
			plainTag = append(plainTag, part.Text)
		}
		renderedTag = strings.Join(pieces, spaces(2))
		tag = strings.Join(plainTag, "  ")
	}
	renderTag := func() string {
		if renderedTag != "" {
			return renderedTag
		}
		return tagStyle.Render(tag)
	}

	if t.width <= 0 {
		row := left + title
		if tag != "" {
			row += spaces(2) + renderTag()
		}
		return row
	}
	room := t.width - lipgloss.Width(left)
	if tag != "" && room-lipgloss.Width(tag)-1 >= 8 {
		room -= lipgloss.Width(tag) + 1
	} else {
		tag = ""
	}
	if lipgloss.Width(title) > room {
		title = truncateAnsi(title, room)
	}
	row := left + title
	if tag != "" {
		row += spaces(max(1, t.width-lipgloss.Width(row)-lipgloss.Width(tag))) + renderTag()
	}
	if cursor {
		row += spaces(t.width - lipgloss.Width(row)) // the band runs the full width
	}
	return row
}

func treeIndent(depth int) string {
	if depth <= 0 {
		return ""
	}
	return strings.Repeat("  ", depth-1) + FaintText.Render("↳ ")
}

// decorateTitle splits the title on a known project name and renders that
// substring as a colored chip. Keeps the rest of the title untouched so
// downstream substring tests still match. If no known project name is
// embedded, returns the title verbatim.
func decorateTitle(title string) string {
	for _, p := range knownProjects {
		idx := strings.Index(strings.ToLower(title), p)
		if idx < 0 {
			continue
		}
		// Re-slice the original title (preserves case) and chip the
		// matched span.
		head := title[:idx]
		match := title[idx : idx+len(p)]
		tail := title[idx+len(p):]
		return head + ProjectChip(match) + tail
	}
	return title
}

// truncateAnsi shortens s so its visible width fits within max, regardless
// of embedded ANSI escape sequences. We reserve one cell for the "…" suffix.
func truncateAnsi(s string, max int) string {
	if max <= 1 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	// Strip-and-rebuild fallback: shorten the underlying runes by one until
	// the visible width fits. ANSI-aware truncation is a minefield, so we
	// settle for a reasonably-fast O(n) walk.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > max {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
