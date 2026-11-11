package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// maxVisible is how many open steps one level shows before "+N more".
const maxVisible = 6

// stepNode is a step with its children (phase > unit); see livedata.StepNode.
type stepNode = livedata.StepNode

// buildStepTree turns a workstream's flat nested steps into trees by depth.
func buildStepTree(steps []nestedObjective) []*stepNode {
	live := make([]livedata.NestedObjective, len(steps))
	for i, st := range steps {
		live[i] = livedata.NestedObjective{Row: st.row, Depth: st.depth}
	}
	return livedata.BuildStepTree(live)
}

// stepRows lays the open workstream's steps out as a tree in plan order: the
// active phase expanded, other phases one line with their state, done steps a
// "✓ n done" under their parent, and "+N more · U2, U4" naming what is hidden.
func stepRows(ws workstream, expanded bool, anim animState, working bool) []panelRow {
	root := ws.root
	roots := buildStepTree(ws.steps)
	now := livedata.NowStep(roots)

	row := func(n *stepNode, indent int, collapsedNote string) panelRow {
		o := n.Row
		text := o.Title
		if o.State == beads.StateNeedsUser && o.Ask != "" {
			text = o.Ask // verb first: what the user must do
		}
		if o.Starred {
			text = "★ " + text
		}
		if o.Review != "" && !n.Done() {
			text += "  #" + strings.TrimPrefix(o.Review, "?")
			text = strings.TrimSuffix(text, "  #")
		}
		if o.Owner != "" && o.Owner != root.Owner {
			text += "  → " + o.Owner
		}
		if collapsedNote != "" {
			text += lipgloss.NewStyle().Foreground(widgets.T.TextMuted).Render("  · " + collapsedNote)
		}
		style := &dimStyle
		isNow := o.BeadID == now
		if isNow {
			style = &brightStyle
		}
		spin := anim
		spin.on = isNow && working && !n.Done() // the spinner marks the one step an agent is on
		if anim.truth != nil {
			spin.on = anim.motion && anim.truth.working[store.WorkstreamKey{ProjectID: ws.project.ID, BeadID: o.BeadID}] && !n.Done()
		}
		return panelRow{kind: "objective", row: o, node: widgets.TreeNode{
			ID: objectiveNodeID(o), Title: text, Columnar: true,
			Marker: stepMarker(o, spin), Indent: indent + 2, Inline: true, TitleStyle: style,
		}}
	}
	summary := func(kind, mark, text string, indent int) panelRow {
		return panelRow{kind: "summary", row: root, node: widgets.TreeNode{
			ID: "summary:" + kind + ":" + root.ProjectName + ":" + root.BeadID + ":" + strconv.Itoa(indent), Title: text,
			Columnar: true, Marker: mark, Indent: indent + 2, Inline: true, TitleStyle: &dimStyle,
		}}
	}

	// withGates is a step's row followed by its gates checklist, one quiet line each.
	withGates := func(rows []panelRow, r panelRow, n *stepNode, indent int) []panelRow {
		rows = append(rows, r)
		for _, g := range n.Row.Gates {
			rows = append(rows, gateRow(g, n.Row, indent+2))
		}
		return rows
	}

	var level func(nodes []*stepNode, indent int) []panelRow
	level = func(nodes []*stepNode, indent int) []panelRow {
		var open, done []*stepNode
		for _, n := range nodes {
			if n.Done() {
				done = append(done, n)
			} else {
				open = append(open, n)
			}
		}
		// The active phase is the one with something moving; with none, the first
		// open phase, so the workstream always shows where to look next.
		activePhase := -1
		for i, n := range open {
			if len(n.Children) > 0 && n.Active() {
				activePhase = i
				break
			}
		}
		if activePhase < 0 {
			for i, n := range open {
				if len(n.Children) > 0 {
					activePhase = i
					break
				}
			}
		}
		visible := make([]bool, len(open))
		shown := 0
		for i, n := range open {
			if n.Active() || n.Row.BeadID == now || i == activePhase {
				visible[i] = true
				shown++
			}
		}
		for i := range open {
			if !visible[i] && (expanded || shown < maxVisible) {
				visible[i] = true
				shown++
			}
		}
		var rows []panelRow
		var hidden []*stepNode
		for i, n := range open {
			if !visible[i] {
				hidden = append(hidden, n)
				continue
			}
			if len(n.Children) == 0 {
				rows = withGates(rows, row(n, indent, ""), n, indent)
				continue
			}
			if i == activePhase || expanded {
				rows = withGates(rows, row(n, indent, ""), n, indent)
				rows = append(rows, level(n.Children, indent+2)...)
			} else {
				rows = withGates(rows, row(n, indent, livedata.PhaseNote(n)), n, indent)
			}
		}
		if len(hidden) > 0 {
			rows = append(rows, summary("more", "", fmt.Sprintf("+%d more · %s", len(hidden), hiddenLabels(hidden)), indent))
		}
		if len(done) > 0 {
			if expanded {
				for _, n := range done {
					rows = withGates(rows, row(n, indent, ""), n, indent)
				}
			} else {
				rows = append(rows, summary("done", "", fmt.Sprintf("✓ %d done", len(done)), indent))
			}
		}
		return rows
	}
	return level(roots, 0)
}

// stepMarker is the state glyph of a step row: ⎇ when in review, else the
// usual state marker (the spinner only when anim.on, i.e. an agent is on it).
func stepMarker(o ObjectiveRow, anim animState) string {
	state := o.State
	if o.Status == "closed" {
		state = beads.StateDone
	}
	if o.Review != "" && state == beads.StateInProgress {
		return lipgloss.NewStyle().Foreground(widgets.T.Accent).Bold(true).Render("⎇")
	}
	if state == beads.StateInProgress && !anim.on {
		return lipgloss.NewStyle().Foreground(widgets.T.Run).Bold(true).Render("●") // static: nobody is on it right now
	}
	return markerFor(state, o.BeadID, anim)
}

var stepLabelRe = regexp.MustCompile(`^([A-Za-z]+)(\d+)\b`)

// hiddenLabels names the hidden steps: "U2, U4, U6–U9" when their titles lead
// with a code, else their (shortened) titles.
func hiddenLabels(nodes []*stepNode) string {
	type code struct {
		prefix string
		n      int
	}
	var codes []code
	var names []string
	for _, n := range nodes {
		if m := stepLabelRe.FindStringSubmatch(strings.TrimSpace(n.Row.Title)); m != nil {
			v, _ := strconv.Atoi(m[2])
			codes = append(codes, code{m[1], v})
			continue
		}
		names = append(names, truncate(n.Row.Title, 14))
	}
	var parts []string
	for i := 0; i < len(codes); {
		j := i
		for j+1 < len(codes) && codes[j+1].prefix == codes[i].prefix && codes[j+1].n == codes[j].n+1 {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, fmt.Sprintf("%s%d–%s%d", codes[i].prefix, codes[i].n, codes[j].prefix, codes[j].n))
		} else {
			for k := i; k <= j; k++ {
				parts = append(parts, fmt.Sprintf("%s%d", codes[k].prefix, codes[k].n))
			}
		}
		i = j + 1
	}
	parts = append(parts, names...)
	if len(parts) > 6 {
		parts = append(parts[:6], "…")
	}
	return strings.Join(parts, ", ")
}

// gateRow is one check of a stage: a mark (passed ✓, failed ✗, open ○), its
// name, and its verifier, small. A failed gate adds why.
func gateRow(g store.Gate, stage ObjectiveRow, indent int) panelRow {
	mark, color := "○", widgets.T.TextMuted
	switch g.Status {
	case store.GatePassed:
		mark, color = "✓", widgets.T.Ok
	case store.GateFailed:
		mark, color = "✗", widgets.T.Err
	}
	muted := lipgloss.NewStyle().Foreground(widgets.T.TextMuted)
	title := g.Name + muted.Render("  "+g.Verifier)
	if g.Status == store.GateFailed && g.Evidence != "" {
		title += muted.Render(" · " + truncate(g.Evidence, 30))
	}
	return panelRow{kind: "gate", row: stage, node: widgets.TreeNode{
		ID: "gate:" + stage.ProjectName + ":" + stage.BeadID + ":" + g.Name, Title: title, Columnar: true,
		Marker: lipgloss.NewStyle().Foreground(color).Render(mark), Indent: indent + 2, Inline: true, Skip: true, TitleStyle: &dimStyle,
	}}
}
