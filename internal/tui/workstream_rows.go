package tui

import (
	"fmt"
	"github.com/example-user/commander/internal/livedata"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// Text tiers on the solid chrome background (spec §2.1): the Next step is bold
// primary, workstream headers and step words are readable secondary text, and
// meta is the muted tier. No SGR faint and no reverse video anywhere: both turn
// to mush on a translucent terminal. Colour lives in the marker column.
var (
	brightStyle = lipgloss.NewStyle().Bold(true).Foreground(widgets.T.TextPrimary)
	normalStyle = lipgloss.NewStyle().Foreground(widgets.T.TextPrimary)
	dimStyle    = lipgloss.NewStyle().Foreground(widgets.T.TextSecondary)

	projectTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(widgets.T.Accent)
)

// animState drives the two restrained animations: a spinner on in-progress steps
// and a short pulse on a ⚑ the first time it appears. The zero value is "still".
type animState struct {
	frame int
	on    bool           // motion enabled (CMDR_MOTION != off)
	pulse map[string]int // bead id -> frames of pulse left

	// agents is the live hook state per Manager role; nowTime dates it. Without
	// an entry the workstream's agent is shown as not started.
	agents  map[string]store.AgentState
	nowTime time.Time
	// waits is what each Manager's turn ended on (CI, a review, the user), by role.
	waits map[string]livedata.ManagerWait
}

// staleWorking is how long a "working" agent may stay silent before it is shown
// idle: a Stop hook that never arrived (crash, killed pane) must not spin forever.
const staleWorking = 10 * time.Minute

// agentNow is the one-line live state of a workstream's Manager, and whether it
// is genuinely working right now.
func agentNow(role string, a animState) (text string, working bool) {
	muted := lipgloss.NewStyle().Foreground(widgets.T.TextMuted)
	st, ok := a.agents[role]
	_, waiting := a.waits[role]
	if !ok && !waiting {
		return muted.Render("– no agent yet"), false
	}
	now := a.nowTime
	if now.IsZero() {
		now = time.Now()
	}
	age := fmtAge(now.Unix() - st.Since)
	if st.State == store.AgentWorking && now.Unix()-st.Updated <= int64(staleWorking.Seconds()) {
		glyph := "●"
		if a.on {
			glyph = widgets.SpinnerFrames[a.frame%len(widgets.SpinnerFrames)]
		}
		detail := st.Detail
		if detail == "" {
			detail = "working"
		}
		return lipgloss.NewStyle().Foreground(widgets.T.Run).Render(glyph+" "+detail) + muted.Render(" · "+age), true
	}
	if w, ok := a.waits[role]; ok && (st.State == "" || st.State == store.AgentIdle || st.State == store.AgentWaiting) {
		return lipgloss.NewStyle().Foreground(widgets.T.Run).Render("◔ waiting · "+w.Text) + muted.Render(" · "+fmtAge(now.Unix()-w.Since.Unix())), true
	}
	if st.State == store.AgentStuck {
		// Mail is waiting but the agent's input box has text, so it was never woken.
		return lipgloss.NewStyle().Foreground(widgets.T.Attn).Bold(true).Render("✉ stuck: "+st.Detail) + muted.Render(" · "+age), false
	}
	if st.State == store.AgentWaiting && now.Unix()-st.Updated <= int64(staleWorking.Seconds()) {
		// Not idle: blocked on sub-agents it launched. Calm glyph, no spinner.
		return lipgloss.NewStyle().Foreground(widgets.T.Run).Render("◔ waiting on "+st.Detail) + muted.Render(" · "+age), true
	}
	return muted.Render("idle " + age), false
}

// fmtAge is a compact duration: 40s, 14m, 3h, 2d.
func fmtAge(secs int64) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", max(secs, 0))
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%dh", secs/3600)
	}
	return fmt.Sprintf("%dd", secs/86400)
}

func marker(state string) string { return markerFor(state, "", animState{}) }

func markerFor(state, bead string, a animState) string {
	glyph, color := "○", widgets.T.TextMuted
	switch state {
	case beads.StateInProgress:
		glyph, color = "●", widgets.T.Run
		if a.on {
			glyph = widgets.SpinnerFrames[a.frame%len(widgets.SpinnerFrames)]
		}
	case beads.StateNeedsUser:
		glyph, color = "⚑", widgets.T.Attn
		if a.pulse[bead] > 0 && a.frame%2 == 1 {
			color = widgets.T.TextPrimary // Attn <-> white, a couple of seconds, then it settles
		}
	case beads.StateBlocked:
		glyph, color = "✗", widgets.T.Err
	case beads.StateDone:
		glyph, color = "✓", widgets.T.Ok
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(glyph)
}

// panelRow is one rendered line plus what it stands for.
type panelRow struct {
	node widgets.TreeNode
	// kind is "objective" (a workstream header or step) or "summary" (a
	// collapsed "✓ N done" / "+N more" line that "e" or Enter expands).
	kind string
	row  ObjectiveRow
}

// workstreamRows lays one workstream out top to bottom: header, Next, the
// steps that need the user or are blocked, in-progress steps, up to two upcoming
// steps, and a single collapsed "✓ N done" line. expanded lifts every cap.
func workstreamRows(rank int, ws workstream, expanded, tagProject, showSteps bool, anim animState) []panelRow {
	root := ws.root
	title := root.Title
	if root.Starred {
		title = "★ " + title
	}
	if root.Owner != "" {
		title += "  → " + root.Owner
	}
	header := widgets.TreeNode{
		ID:         objectiveNodeID(root),
		Title:      title,
		Columnar:   true,
		Prefix:     fmt.Sprintf("%d", rank),
		TitleStyle: &normalStyle,
	}
	if tagProject {
		header.RightTag = ws.project.Name
	}
	needs, running, blockedN, open, doneN := stepCounts(ws)
	if !tagProject {
		header.TagParts = countParts(needs, running, blockedN, open)
	}
	rows := []panelRow{{node: header, kind: "objective", row: root}}
	nowText, working := agentNow(store.WorkstreamManagerRole(root.BeadID), anim)
	rows = append(rows, panelRow{kind: "now", row: root, node: widgets.TreeNode{
		ID: "now:" + root.ProjectName + ":" + root.BeadID, Title: nowText, Columnar: true, Indent: 2,
		Skip: true, TitleStyle: &lipgloss.Style{},
	}})
	if noGates(ws) {
		rows = append(rows, panelRow{kind: "nogates", row: root, node: widgets.TreeNode{
			ID: "nogates:" + root.ProjectName + ":" + root.BeadID, Title: "⚠ no gates", Columnar: true, Indent: 2,
			Skip: true, TitleStyle: &noGatesStyle,
		}})
	}
	workingNow := working // only the step the agent is on spins, and only while it works
	anim.on = false
	if !showSteps {
		return rows // collapsed: header, live state, counts at the right
	}
	// Expanded: a quiet meta line (project · progress) above the steps.
	meta := widgets.ProjectChip(ws.project.Name) + lipgloss.NewStyle().Foreground(widgets.T.TextMuted).
		Render(fmt.Sprintf(" · %d of %d done", doneN, len(ws.steps)))
	rows = append(rows, panelRow{kind: "meta", row: root, node: widgets.TreeNode{
		ID: "meta:" + root.ProjectName + ":" + root.BeadID, Title: meta, Columnar: true, Indent: 2,
		Skip: true, TitleStyle: &lipgloss.Style{},
	}})

	return append(rows, stepRows(ws, expanded, anim, workingNow)...)
}

var noGatesStyle = lipgloss.NewStyle().Foreground(widgets.T.Attn)

// noGates is true for an open workstream where neither it nor any stage has a gate.
func noGates(ws workstream) bool {
	if ws.root.State == beads.StateDone || ws.root.Status == "closed" || len(ws.root.Gates) > 0 {
		return false
	}
	for _, st := range ws.steps {
		if len(st.row.Gates) > 0 {
			return false
		}
	}
	return true
}

// stepCounts tallies a workstream's open steps by what matters at a glance.
func stepCounts(ws workstream) (needs, running, blocked, open, done int) {
	for _, st := range ws.steps {
		switch o := st.row; {
		case o.State == beads.StateDone || o.Status == "closed":
			done++
		case o.State == beads.StateNeedsUser:
			needs++
		case o.State == beads.StateBlocked:
			blocked++
		case o.State == beads.StateInProgress:
			running++
		default:
			open++
		}
	}
	return
}

// countParts is the right-aligned meta of a workstream row: ⚑ n (needs the user),
// ✗ n (blocked), then the dominant state, ● n running or else ○ n not started.
func countParts(needs, running, blocked, open int) []widgets.TagPart {
	var parts []widgets.TagPart
	if needs > 0 {
		parts = append(parts, widgets.TagPart{Text: fmt.Sprintf("⚑ %d", needs), Color: widgets.T.Attn, Bold: true})
	}
	if blocked > 0 {
		parts = append(parts, widgets.TagPart{Text: fmt.Sprintf("✗ %d", blocked), Color: widgets.T.Err, Bold: true})
	}
	switch {
	case running > 0:
		parts = append(parts, widgets.TagPart{Text: fmt.Sprintf("● %d", running), Color: widgets.T.Run})
	case open > 0:
		parts = append(parts, widgets.TagPart{Text: fmt.Sprintf("○ %d", open), Color: widgets.T.TextMuted})
	}
	return parts
}

func objectiveNodeID(o ObjectiveRow) string {
	return "objective:" + o.ProjectName + ":" + o.BeadID + ":" + o.Title
}
