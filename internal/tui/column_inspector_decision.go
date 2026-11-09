package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// A decision tab is the form the user answers a ⚑ with: the Manager's question,
// context, options (with what each implies), commands to copy, and a note field.
// Submitting mails the Manager (never types into its chat) and clears the flag.

// decisionRecord is a decision tab's card as stored; Present is false once the
// flag has been answered or withdrawn.
type decisionRecord struct {
	Decision *store.Decision
	Present  bool
}

// decisionState is the user's in-progress answer to one card.
type decisionState struct {
	rec     decisionRecord
	sel     int // chosen option, -1 none
	note    string
	sending bool
	sent    string // confirmation shown after a submit
	err     string
}

type decisionHit struct {
	from, to int // inner-view rows (inclusive)
	kind     string
	idx      int
}

type decisionSentMsg struct {
	tabID  int64
	choice string
	err    error
}

func loadDecisions(ctx context.Context, st *store.Store, pid int64, tabs []store.InspectorTab) map[int64]decisionRecord {
	var out map[int64]decisionRecord
	for _, t := range tabs {
		if t.Kind != inspector.KindDecision {
			continue
		}
		if out == nil {
			out = map[int64]decisionRecord{}
		}
		n, err := st.GetNeedsYou(ctx, pid, t.Target)
		if err != nil || n == nil || n.Decision == "" {
			out[t.ID] = decisionRecord{}
			continue
		}
		d, err := store.ParseDecision(n.Decision)
		out[t.ID] = decisionRecord{Decision: d, Present: err == nil}
	}
	return out
}

func (c *InspectorColumn) applyDecisions(recs map[int64]decisionRecord) {
	if c.dec == nil {
		c.dec = map[int64]*decisionState{}
	}
	for id, rec := range recs {
		st := c.dec[id]
		if st == nil {
			st = &decisionState{sel: -1}
			c.dec[id] = st
		}
		st.rec = rec
		if rec.Present && st.sel >= len(rec.Decision.Options) {
			st.sel = -1
		}
	}
	live := map[int64]bool{}
	for _, t := range c.tabs {
		live[t.ID] = true
	}
	for id := range c.dec {
		if !live[id] {
			delete(c.dec, id)
		}
	}
}

func (c *InspectorColumn) activeDecision() (*store.InspectorTab, *decisionState) {
	tab := c.activeTab()
	if tab == nil || tab.Kind != inspector.KindDecision {
		return nil, nil
	}
	st := c.dec[tab.ID]
	if st == nil {
		if c.dec == nil {
			c.dec = map[int64]*decisionState{}
		}
		st = &decisionState{sel: -1}
		c.dec[tab.ID] = st
	}
	return tab, st
}

// ready reports whether Submit would do something, and the choice it sends.
func (s *decisionState) ready() (choice string, ok bool) {
	d := s.rec.Decision
	if !s.rec.Present || s.sending || s.sent != "" {
		return "", false
	}
	if len(d.Options) > 0 {
		if s.sel < 0 {
			return "", false
		}
		return d.Options[s.sel].Label, true
	}
	if len(d.Steps) > 0 {
		return "done", true
	}
	return "answered", strings.TrimSpace(s.note) != ""
}

func (c *InspectorColumn) decisionKey(key string) (tea.Cmd, bool) {
	tab, st := c.activeDecision()
	if tab == nil || !st.rec.Present || st.sent != "" {
		return nil, false
	}
	d := st.rec.Decision
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' && len(d.Options) > 0 {
		if i := int(key[0] - '1'); i < len(d.Options) {
			st.sel = i
		}
		return nil, true
	}
	switch key {
	case "i", "n":
		c.startNote(st)
		return nil, true
	case "c":
		return c.copyStep(-1), true
	case "enter":
		return c.submitDecision(tab, st), true
	case "up", "k":
		if len(d.Options) > 0 {
			st.sel = max(0, st.sel-1)
			return nil, true
		}
	case "down", "j":
		if len(d.Options) > 0 {
			st.sel = min(len(d.Options)-1, st.sel+1)
			return nil, true
		}
	}
	return nil, false
}

func (c *InspectorColumn) startNote(st *decisionState) {
	c.prompt = "note"
	c.input.SetValue(st.note)
	c.input.Width = max(10, c.width-8)
	c.input.Focus()
}

// noteKey edits the note: Enter keeps it, Esc (handled by the caller) drops the edit.
func (c *InspectorColumn) noteKey(k tea.KeyMsg) tea.Cmd {
	_, st := c.activeDecision()
	if k.String() == "enter" && st != nil {
		st.note = strings.TrimSpace(c.input.Value())
		c.prompt = ""
		return nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	return cmd
}

// copyStep copies one step command, or all of them for idx < 0.
func (c *InspectorColumn) copyStep(idx int) tea.Cmd {
	_, st := c.activeDecision()
	if st == nil || !st.rec.Present {
		return nil
	}
	steps := st.rec.Decision.Steps
	text := strings.Join(steps, "\n")
	if idx >= 0 && idx < len(steps) {
		text = steps[idx]
	}
	if text == "" {
		return nil
	}
	c.status = "copied " + midTruncate(strings.Split(text, "\n")[0], max(10, c.width-12))
	return func() tea.Msg {
		if err := inspectorCopy(text); err != nil {
			return inspectorActionMsg{status: "copy failed: " + err.Error()}
		}
		return nil
	}
}

func (c *InspectorColumn) decisionMouse(m tea.MouseMsg) (tea.Cmd, bool) {
	tab, st := c.activeDecision()
	if tab == nil || !st.rec.Present || st.sent != "" {
		return nil, false
	}
	if m.Action != tea.MouseActionPress || m.Button != tea.MouseButtonLeft {
		return nil, false
	}
	for _, h := range c.decHits {
		if m.Y < h.from || m.Y > h.to {
			continue
		}
		switch h.kind {
		case "opt":
			st.sel = h.idx
		case "step":
			return c.copyStep(h.idx), true
		case "note":
			c.startNote(st)
		case "submit":
			return c.submitDecision(tab, st), true
		}
		return nil, true
	}
	return nil, false
}

// submitDecision mails the Manager the user's answer through internal-resolve-needs
// (which clears the flag and uses the mail path, never the chat pane).
func (c *InspectorColumn) submitDecision(tab *store.InspectorTab, st *decisionState) tea.Cmd {
	choice, ok := st.ready()
	if !ok {
		if len(st.rec.Decision.Options) > 0 {
			c.status = "pick an option first (1-" + fmt.Sprint(len(st.rec.Decision.Options)) + ")"
		} else {
			c.status = "write a note first (i)"
		}
		return nil
	}
	st.sending, st.err = true, ""
	args := []string{"internal-resolve-needs", "--project", c.projectName, "--bead", tab.Target,
		"--action", "decide", "--choice", choice, "--text", st.note}
	id := tab.ID
	return func() tea.Msg {
		exe, err := selfExe()
		if err != nil {
			return decisionSentMsg{tabID: id, choice: choice, err: err}
		}
		cmd := exec.Command(exe, args...)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return decisionSentMsg{tabID: id, choice: choice, err: fmt.Errorf("%s", msg)}
		}
		return decisionSentMsg{tabID: id, choice: choice}
	}
}

func (c *InspectorColumn) decisionSent(m decisionSentMsg) tea.Cmd {
	st := c.dec[m.tabID]
	if st == nil {
		return nil
	}
	st.sending = false
	if m.err != nil {
		st.err = m.err.Error()
		return nil
	}
	st.sent = m.choice
	return c.loadCmd()
}

// ---- rendering --------------------------------------------------------------

func (c *InspectorColumn) renderDecision(tab store.InspectorTab, firstRow int) []string {
	_, st := c.activeDecision()
	c.decHits = c.decHits[:0]
	t := widgets.T
	w := c.width
	muted := lipgloss.NewStyle().Foreground(t.TextMuted)
	text := lipgloss.NewStyle().Foreground(t.TextSecondary)
	bold := lipgloss.NewStyle().Bold(true).Foreground(t.TextPrimary)
	wrap := func(s string, indent int, style lipgloss.Style) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wrap(s, max(8, w-indent), ""), "\n") {
			out = append(out, strings.Repeat(" ", indent)+style.Render(l))
		}
		return out
	}

	head := lipgloss.NewStyle().Foreground(t.Attn).Bold(true).Render("⚑ DECISION")
	if st.sent != "" || !st.rec.Present {
		head = lipgloss.NewStyle().Foreground(t.Ok).Bold(true).Render("✓ ANSWERED")
		if !st.rec.Present && st.sent == "" {
			head = muted.Render("⚑ no longer needed")
		}
	}
	lines := []string{head + muted.Render("  "+strings.TrimPrefix(tab.Title, "⚑ ")), ""}

	if st.sent != "" {
		lines = append(lines, wrap("Sent to the Manager: "+st.sent+".", 0, bold)...)
		lines = append(lines, wrap("The flag is cleared and the step is back in progress; the Manager reads this by mail.", 0, muted)...)
		return lines
	}
	if !st.rec.Present {
		lines = append(lines, wrap("This ask was answered or withdrawn, so there is nothing left to decide. Close this tab with x.", 0, muted)...)
		return lines
	}

	d := st.rec.Decision
	lines = append(lines, wrap(d.Question, 0, lipgloss.NewStyle().Bold(true).Foreground(t.TextPrimary))...)
	if d.Context != "" {
		lines = append(lines, "")
		lines = append(lines, wrap(d.Context, 0, text)...)
	}

	for i, o := range d.Options {
		if i == 0 {
			lines = append(lines, "")
		}
		from := firstRow + len(lines)
		marker, row := "○", lipgloss.NewStyle().Foreground(t.TextSecondary)
		if i == st.sel {
			marker, row = "●", lipgloss.NewStyle().Bold(true).Foreground(t.TextPrimary).Background(t.BgCursor)
		}
		label := fmt.Sprintf("%s %d  %s", marker, i+1, o.Label)
		if o.Recommended {
			label += "  ★ recommended"
		}
		line := row.Render(padRight(ansi.Truncate(label, w, "…"), w))
		if o.Recommended && i != st.sel {
			line = row.Render(ansi.Truncate(fmt.Sprintf("%s %d  %s", marker, i+1, o.Label), w, "…")) +
				lipgloss.NewStyle().Foreground(t.Accent).Render("  ★ recommended")
		}
		lines = append(lines, line)
		if o.Implications != "" {
			lines = append(lines, wrap(o.Implications, 5, muted)...)
		}
		c.decHits = append(c.decHits, decisionHit{from: from, to: firstRow + len(lines) - 1, kind: "opt", idx: i})
	}

	for i, s := range d.Steps {
		if i == 0 {
			lines = append(lines, "", muted.Render("DO THIS  (click a line, or c, to copy)"))
		}
		from := firstRow + len(lines)
		code := lipgloss.NewStyle().Foreground(t.Accent)
		lines = append(lines, ansi.Truncate(muted.Render("⧉ ")+code.Render(s), w, "…"))
		c.decHits = append(c.decHits, decisionHit{from: from, to: from, kind: "step", idx: i})
	}

	if d.Input || len(d.Options) > 0 || len(d.Steps) > 0 {
		lines = append(lines, "")
		from := firstRow + len(lines)
		note := muted.Render("✎ add a note (i)")
		if st.note != "" {
			note = muted.Render("✎ ") + text.Render(st.note)
		}
		if c.prompt == "note" {
			note = muted.Render("✎ editing below…")
		}
		lines = append(lines, ansi.Truncate(note, w, "…"))
		c.decHits = append(c.decHits, decisionHit{from: from, to: from, kind: "note"})
	}

	lines = append(lines, "")
	from := firstRow + len(lines)
	label := " Submit ⏎ "
	if len(d.Options) == 0 && len(d.Steps) > 0 {
		label = " Done ⏎ "
	}
	btn := lipgloss.NewStyle().Foreground(t.TextMuted).Background(t.BgRaised)
	if _, ok := st.ready(); ok {
		btn = lipgloss.NewStyle().Bold(true).Foreground(t.BgChrome).Background(t.Accent)
	}
	lines = append(lines, btn.Render(label))
	if st.sending {
		lines = append(lines, muted.Render("sending…"))
	}
	if st.err != "" {
		lines = append(lines, wrap("✗ "+st.err, 0, lipgloss.NewStyle().Foreground(t.Err))...)
	}
	c.decHits = append(c.decHits, decisionHit{from: from, to: from, kind: "submit"})
	return lines
}

func padRight(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func decisionHints(width int) []string {
	return []string{widgets.HintLine(width, [2]string{"1-9", "pick"}, [2]string{"i", "note"}, [2]string{"c", "copy"}, [2]string{"⏎", "submit"}, [2]string{"[ ]", "tabs"})}
}
