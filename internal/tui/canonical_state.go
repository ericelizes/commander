package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

type canonicalLine struct {
	kind, text string
	working    int
}
type canonicalState struct {
	lines       map[store.WorkstreamKey]canonicalLine
	working     map[store.WorkstreamKey]bool
	commander   canonicalLine
	unavailable bool
}

func newCanonicalState(s *livedata.Snapshot) *canonicalState {
	if s == nil {
		return nil
	}
	out := &canonicalState{lines: map[store.WorkstreamKey]canonicalLine{}, working: map[store.WorkstreamKey]bool{}, unavailable: s.Degraded != nil}
	if s.Display == nil {
		return out
	}
	c := s.Display.Commander
	out.commander = canonicalLine{kind: c.Kind, text: c.Text}
	if c.Kind == "working" {
		out.commander.working = 1
	}
	projects := map[string]int64{}
	for _, p := range s.Projects {
		projects[p.Name] = p.ID
	}
	var tracks func(int64, []livedata.BoardTrack)
	tracks = func(pid int64, ts []livedata.BoardTrack) {
		for _, t := range ts {
			out.lines[store.WorkstreamKey{ProjectID: pid, BeadID: t.BeadID}] = canonicalLine{kind: t.Kind, text: t.Text, working: t.Working}
			tracks(pid, t.Children)
		}
	}
	for _, row := range s.Display.Workstreams {
		pid := projects[row.Project]
		out.lines[store.WorkstreamKey{ProjectID: pid, BeadID: row.BeadID}] = canonicalLine{kind: row.Kind, text: row.Text, working: row.Working}
		tracks(pid, row.Tracks)
	}
	var steps func(int64, []livedata.SnapStep)
	steps = func(pid int64, ss []livedata.SnapStep) {
		for _, s := range ss {
			out.working[store.WorkstreamKey{ProjectID: pid, BeadID: s.BeadID}] = s.Working
			steps(pid, s.Children)
		}
	}
	for _, ws := range s.Workstreams {
		steps(ws.ProjectID, ws.Steps)
	}
	return out
}
func (c *canonicalState) scope(key store.WorkstreamKey) canonicalLine {
	if c.unavailable {
		return canonicalLine{kind: "waiting", text: "Current state is unavailable"}
	}
	if line, ok := c.lines[key]; ok {
		return line
	}
	return canonicalLine{kind: "waiting", text: "Checking current state"}
}
func canonicalNow(line canonicalLine, frame int, motion bool) (string, bool) {
	glyph := "◔"
	color := widgets.T.TextMuted
	if line.text == "" {
		line.text = "Checking current state"
	}
	switch line.kind {
	case "working":
		if line.working > 0 {
			glyph = "●"
			color = widgets.T.Run
			if motion {
				glyph = widgets.SpinnerFrames[frame%len(widgets.SpinnerFrames)]
			}
		}
	case "needs":
		glyph = "⚑"
		color = widgets.T.Attn
	case "stuck", "stalled":
		glyph = "!"
		color = widgets.T.Attn
	}
	return lipgloss.NewStyle().Foreground(color).Render(glyph + " " + line.text), line.working > 0
}
