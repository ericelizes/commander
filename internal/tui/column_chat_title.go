package tui

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// ChatTitleColumn is the full-width header bar above the chat in the middle
// column (spec §2.3). Row 1 is the identity band: a pill naming the kind
// (◆ COMMANDER / ◇ MANAGER / ▸ OPERATOR), the title, a ╱ fill and right-aligned
// meta. Row 2 is a live status line. The kind, title, workstream and meta come
// from the window's @cmdr_kind / @cmdr_title / @cmdr_ws / @cmdr_meta tmux
// options, which layout keeps current, so the bar never needs respawning when a
// workstream is renamed.
type ChatTitleColumn struct {
	frame0        frameKeeper
	width, height int
	title, kind   string
	ws, meta      string

	svc        *Services
	live       CommanderLiveData
	liveLoaded bool

	// unread counts mail this window's agent has not been shown (the ✉ badge).
	unread   unreadFunc
	unreadN  int
	unreadOK bool
}

// titleRows is how tall the bar is, in terminal rows.
const titleRows = 4 // pad, title, status, pad

// narrowTitleWidth is the width below which the bar shows only pill + title
// and a compact status line.
const narrowTitleWidth = 100

// minTitleFill is the fewest ╱ cells the fill ever shrinks to.
const minTitleFill = 3

func NewChatTitleColumn() *ChatTitleColumn { return &ChatTitleColumn{} }

// RunChatTitleColumn is the `cmdr __column chat-title` entry point.
func RunChatTitleColumn() error {
	svc, _ := openColumnServices() // best-effort: without a store row 2 stays empty
	c := NewChatTitleColumn()
	c.svc = svc
	c.unread = newUnreadFunc(svc.Store)
	_, err := tea.NewProgram(c, tea.WithAltScreen()).Run()
	return err
}

type chatTitleMsg struct{ title, kind, ws, meta string }

func (c *ChatTitleColumn) Init() tea.Cmd {
	return tea.Batch(c.fetch(), c.loadLive(), columnTick())
}

// fetch reads the title, kind, workstream and meta of the window this pane is in.
// The title goes last because it may itself contain "|" ("Manager | X").
func (c *ChatTitleColumn) fetch() tea.Cmd {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return nil
	}
	return func() tea.Msg {
		out, err := execCommand("tmux", "display-message", "-p", "-t", pane,
			"#{@cmdr_kind}|#{@cmdr_ws}|#{@cmdr_meta}|#{@cmdr_title}").Output()
		if err != nil {
			return chatTitleMsg{}
		}
		parts := strings.SplitN(strings.TrimRight(string(out), "\n"), "|", 4)
		if len(parts) < 4 {
			return chatTitleMsg{}
		}
		return chatTitleMsg{kind: parts[0], ws: parts[1], meta: parts[2], title: parts[3]}
	}
}

// loadLive reloads the cross-fleet snapshot the status line summarizes.
func (c *ChatTitleColumn) loadLive() tea.Cmd {
	if c.svc == nil || c.svc.Store == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return commanderLiveDataMsg{Data: LoadCommanderData(ctx, CommanderDataDeps{Store: c.svc.Store, Beads: c.svc.Beads})}
	}
}

func (c *ChatTitleColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = m.Width-2*panelPadX, m.Height
	case columnTickMsg:
		return c, tea.Batch(c.fetch(), c.loadLive(), c.loadUnread(), columnTick())
	case chatTitleMsg:
		if m.title != "" {
			role := mailRoleFor(c.kind, c.ws)
			c.title, c.kind, c.ws, c.meta = m.title, m.kind, m.ws, m.meta
			if mailRoleFor(c.kind, c.ws) != role {
				c.unreadOK = false // a different agent: forget the old count
				return c, c.loadUnread()
			}
		}
	case chatUnreadMsg:
		c.unreadN, c.unreadOK = m.n, m.ok
	case commanderLiveDataMsg:
		c.live, c.liveLoaded = m.Data, true
	}
	return c, nil // no keys: this pane only displays
}

func accentFor(kind string) lipgloss.Color {
	switch kind {
	case "commander":
		return widgets.T.KindCommander // Mauve: Yellow is reserved for "needs the user"
	case "operator":
		return widgets.T.KindOperator
	default:
		return widgets.T.KindManager
	}
}

// pillLabel is the text inside the identity pill.
func pillLabel(kind string) string {
	switch kind {
	case "commander":
		return "◆ COMMANDER"
	case "operator":
		return "▸ OPERATOR"
	default:
		return "◇ MANAGER"
	}
}

// displayTitle drops the "Manager | " / "Operator | " prefix: the pill carries the kind.
func displayTitle(title string) string {
	for _, prefix := range []string{"Manager | ", "Operator | "} {
		if rest, ok := strings.CutPrefix(title, prefix); ok {
			return rest
		}
	}
	return title
}

// truncate cuts s to at most max cells, ending in "…" when it cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > max {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func (c *ChatTitleColumn) View() string {
	return c.frame0.keep(c.view())
}

func (c *ChatTitleColumn) view() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}
	rows := []string{""} // top pad
	rows = append(rows, c.sidePad(c.identityRow(), widgets.T.BgRaised))
	if c.height > 2 {
		rows = append(rows, c.sidePad(c.statusRow(), widgets.T.BgChrome))
	}
	for len(rows) < c.height && len(rows) < titleRows {
		rows = append(rows, "") // bottom pad
	}
	return strings.Join(rows[:min(len(rows), c.height)], "\n")
}

func (c *ChatTitleColumn) identityRow() string {
	bg := widgets.T.BgRaised
	on := func(fg lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Background(bg).Foreground(fg) }

	pill := c.pill()
	rest := c.width - lipgloss.Width(pill)
	title := displayTitle(c.title)
	if title == "" {
		title = "…"
	}
	meta := c.meta
	// Meta drops before the title truncates, and never shows when narrow.
	if c.width < narrowTitleWidth || 1+lipgloss.Width(title)+1+minTitleFill+1+lipgloss.Width(meta)+1 > rest {
		meta = ""
	}
	metaW := 0
	if meta != "" {
		metaW = 1 + lipgloss.Width(meta) + 1
	}
	title = truncate(title, rest-2-minTitleFill-metaW)
	fill := rest - (1 + lipgloss.Width(title) + 1) - metaW

	var b strings.Builder
	b.WriteString(pill)
	b.WriteString(on(widgets.T.TextPrimary).Bold(true).Render(" " + title + " "))
	b.WriteString(on(widgets.T.Rule).Render(strings.Repeat("╱", max(fill, 0))))
	if meta != "" {
		b.WriteString(on(widgets.T.TextMuted).Render(" " + meta + " "))
	}
	return lipgloss.NewStyle().Background(bg).MaxWidth(c.width).Render(b.String())
}

// pill renders " ◆ COMMANDER " bold on the kind color. Commander's gets a
// Mauve -> Pink gradient (spec §4) so window 0 is unmistakable.
func (c *ChatTitleColumn) pill() string {
	text := " " + pillLabel(c.kind) + " "
	base := lipgloss.NewStyle().Bold(true).Foreground(widgets.T.OnKind)
	if c.kind == "commander" && widgets.T.KindCommander == widgets.Palette.Mauve {
		return gradientBackground(text, base, string(widgets.Palette.Mauve), string(widgets.Palette.Pink))
	}
	return base.Background(accentFor(c.kind)).Render(text)
}

// gradientBackground paints text one rune at a time with the background
// interpolated linearly from hex color `from` to `to`.
func gradientBackground(text string, base lipgloss.Style, from, to string) string {
	runes := []rune(text)
	var b strings.Builder
	for i, r := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(i) / float64(len(runes)-1)
		}
		b.WriteString(base.Background(lerpHex(from, to, t)).Render(string(r)))
	}
	return b.String()
}

// lerpHex interpolates between two "#rrggbb" colors; t is 0..1.
func lerpHex(from, to string, t float64) lipgloss.Color {
	var r1, g1, b1, r2, g2, b2 int
	if _, err := fmt.Sscanf(from, "#%02x%02x%02x", &r1, &g1, &b1); err != nil {
		return lipgloss.Color(from)
	}
	if _, err := fmt.Sscanf(to, "#%02x%02x%02x", &r2, &g2, &b2); err != nil {
		return lipgloss.Color(from)
	}
	mix := func(a, b int) int { return a + int(math.Round(float64(b-a)*t)) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", mix(r1, r2), mix(g1, g2), mix(b1, b2)))
}

// statusPart is one piece of the status line, with a wide and a compact form.
type statusPart struct {
	wide, compact string
	color         lipgloss.Color
	bold          bool
}

func (c *ChatTitleColumn) statusRow() string {
	bg := widgets.T.BgChrome
	row := lipgloss.NewStyle().Background(bg).Width(c.width).MaxWidth(c.width)
	parts := c.statusParts()
	if badge, ok := c.badgePart(); ok {
		parts = append([]statusPart{badge}, parts...) // first, so a narrow bar cuts it last
	}
	if len(parts) == 0 {
		return row.Render("")
	}
	compact := c.width < narrowTitleWidth
	sep := "   "
	if compact {
		sep = "  "
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Background(bg).Render(" "))
	for i, p := range parts {
		if i > 0 {
			b.WriteString(lipgloss.NewStyle().Background(bg).Render(sep))
		}
		text := p.wide
		if compact {
			text = p.compact
		}
		b.WriteString(lipgloss.NewStyle().Background(bg).Foreground(p.color).Bold(p.bold).Render(text))
	}
	return row.Render(b.String())
}

// statusParts builds the live status line for this window's kind. Empty until
// the first snapshot arrives, and for operator windows.
func (c *ChatTitleColumn) statusParts() []statusPart {
	if !c.liveLoaded {
		return nil
	}
	t := widgets.T
	count := func(n int, color lipgloss.Color) lipgloss.Color {
		if n == 0 {
			return t.TextMuted // zero is quiet; yellow only ever means "needs you"
		}
		return color
	}
	switch c.kind {
	case "commander":
		needs := len(needsYou(c.live))
		// Live agent state, not bead state: Managers actually mid-turn vs waiting.
		running, idle := 0, 0
		for _, ws := range workstreamsInOrder(c.live.Projects, func(id int64) []ObjectiveRow { return objectivesOf(c.live, id) }, c.live.Ranks) {
			if _, working := agentNow(store.WorkstreamManagerRole(ws.root.BeadID), animState{agents: c.live.Agents}); working {
				running++
			} else {
				idle++
			}
		}
		ops := c.live.Metrics.ActiveCount
		cost := fmt.Sprintf("$%.2f", c.live.Metrics.TotalCostUSD24h)
		return []statusPart{
			{fmt.Sprintf("⚑ %d need you", needs), fmt.Sprintf("⚑ %d", needs), count(needs, t.Attn), needs > 0},
			{fmt.Sprintf("● %d working · %d idle", running, idle), fmt.Sprintf("● %d", running), count(running, t.Run), false},
			{fmt.Sprintf("%d operators running", ops), fmt.Sprintf("▸ %d", ops), t.TextSecondary, false},
			{fmt.Sprintf("%s today", cost), cost, t.TextSecondary, false},
		}
	case "manager":
		ws, ok := c.workstream()
		if !ok {
			return nil
		}
		needs, running, _, _, done := stepCounts(ws)
		if ws.root.State == beads.StateNeedsUser {
			needs++
		}
		next := nextStepTitle(ws)
		return []statusPart{
			{"★ next " + next, "★ " + truncate(next, 14), t.Accent, true},
			{fmt.Sprintf("⚑ %d need you", needs), fmt.Sprintf("⚑ %d", needs), count(needs, t.Attn), needs > 0},
			{fmt.Sprintf("● %d in progress", running), fmt.Sprintf("● %d", running), count(running, t.Run), false},
			{fmt.Sprintf("✓ %d/%d", done, len(ws.steps)), fmt.Sprintf("✓ %d/%d", done, len(ws.steps)), t.Ok, false},
		}
	}
	return nil
}

// workstream finds this Manager window's workstream by its @cmdr_ws bead id.
func (c *ChatTitleColumn) workstream() (workstream, bool) {
	for _, ws := range workstreamsInOrder(c.live.Projects, func(id int64) []ObjectiveRow { return objectivesOf(c.live, id) }, c.live.Ranks) {
		if ws.root.BeadID == c.ws {
			return ws, true
		}
	}
	return workstream{}, false
}

// nextStepTitle is the first step that is not done yet.
func nextStepTitle(ws workstream) string {
	for _, st := range ws.steps {
		if st.row.State != beads.StateDone && st.row.Status != "closed" {
			return st.row.Title
		}
	}
	return "all done"
}

// sidePad insets a bar's content panelPadX cells from each pane edge; the
// padding carries the bar's background so the bar stays full-bleed.
func (c *ChatTitleColumn) sidePad(row string, bg lipgloss.Color) string {
	pad := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", panelPadX))
	return pad + row + pad
}
