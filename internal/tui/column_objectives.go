package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

var execCommand = exec.Command

// ObjectivesColumn is the Bubble Tea program rendered in the Commander
// view's middle pane (`cmdr __column cmdr-objectives`). It shows the active
// project list first, then objectives scoped to the selected project.
//
// The pane is a passive renderer: it doesn't drill or dispatch (drilling
// happens via the opencode Commander chat in the left pane, which shells out
// to `cmdr --project <name>`). Tab moves focus between rows for visibility; q or
// Ctrl-C quits the program (which closes the pane).
type ObjectivesColumn struct {
	width, height int

	fixtureProjects   []FixtureProject
	fixtureObjectives []FixtureObjective
	tree              *widgets.Tree
	nodeMeta          map[string]objectiveNodeMeta

	// The panel lives in one tmux window and is the same navigator in every
	// window. ownWS / ownCommander say which row is "the window I'm in";
	// ownActive is whether that window is currently the visible one.
	ownWS        string
	frame0       frameKeeper
	reconnecting bool   // the last live-data read failed; the previous frame is on screen
	sharedOpen   string // shared @cmdr_expanded ("" = unset)
	ownCommander bool
	ownKnown     bool
	ownActive    bool
	showHelp     bool
	lastFocus    string // wsKey whose steps were shown at the last rebuild
	rebuilding   bool

	// Restrained motion (spec §4): off unless RunObjectivesColumn turns it on.
	motion    bool
	frame     int
	pulse     map[string]int  // bead id -> pulse frames left
	seenNeeds map[string]bool // ⚑ beads already seen, so only NEW ones pulse
	seenInit  bool

	// grouped (key "g") nests the same priority-ranked list under project
	// headers; the choice is remembered per window in workstreams-ui.json.
	grouped bool
	// order is the global priority order from the last rebuild, and nodeWS maps
	// each workstream/step row to its index in order (for J/K reordering).
	order    []workstream
	nodeWS   map[string]int
	followID string
	// expanded holds workstreams whose collapsed lines ("+N more", "✓ N done",
	// "N more need you") are shown in full.
	expanded map[string]bool

	svc        *Services
	live       CommanderLiveData
	liveLoaded bool
	status     string

	// prompt is the modal line for n/r/x: "child", "rename", or "close".
	// While set, keys go to the input instead of the tree.
	prompt       string
	promptTarget objectiveNodeMeta
	input        textinput.Model
}

type objectiveNodeMeta struct {
	kind      string
	project   store.Project
	objective ObjectiveRow
}

type objectiveActionResultMsg struct {
	err       error
	status    string
	created   *ObjectiveRow
	projectID int64
}

type createObjectiveResult struct {
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	Title   string `json:"title"`
}

// NewObjectivesColumn constructs the column with fixture data seeded so the
// first paint isn't blank, then attempts to swap in live data if the store
// is available.
func NewObjectivesColumn(svc *Services) *ObjectivesColumn {
	c := &ObjectivesColumn{
		svc:      svc,
		tree:     widgets.NewTree(),
		nodeMeta: make(map[string]objectiveNodeMeta),
		input:    textinput.New(),
	}
	if svc == nil {
		c.loadFixtureSeed()
	} else {
		c.preloadLiveData()
	}
	return c
}

// RunObjectivesColumn is the cmd/cmdr __column cmdr-objectives entry point.
// It opens the services bundle (best-effort), constructs the model, and
// drives a Bubble Tea program in alt-screen mode until the user quits.
func RunObjectivesColumn() error {
	svc, warn := openColumnServices()
	m := NewObjectivesColumn(svc)
	m.motion = os.Getenv("CMDR_MOTION") != "off"
	m.grouped = loadGroupPref()
	m.rebuildTree()
	applyCommanderStartupWarning(warn, svc, m.applyLiveData)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithReportFocus()).Run()
	return err
}

// loadFixtureSeed populates the pane with fixtures so the first paint
// renders an obvious Commander hierarchy even before live data returns.
func (c *ObjectivesColumn) loadFixtureSeed() {
	c.fixtureProjects = FixtureProjects()
	c.fixtureObjectives = FixtureObjectives()
	c.rebuildTree()
}

// Init schedules the periodic refresh tick + the first live load if the
// store is wired.
func (c *ObjectivesColumn) Init() tea.Cmd {
	cmds := []tea.Cmd{columnTick(), expandedTick(), c.ownWindowCmd()}
	if c.motion {
		cmds = append(cmds, animTick(animInterval))
	}
	if c.svc != nil && c.svc.Store != nil {
		cmds = append(cmds, c.loadLiveCmd())
	}
	return tea.Batch(cmds...)
}

// loadLiveCmd produces a tea.Cmd that runs LoadCommanderData and emits the
// result on completion.
func (c *ObjectivesColumn) loadLiveCmd() tea.Cmd {
	if c.svc == nil || c.svc.Store == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return commanderLiveDataMsg{Data: LoadCommanderData(ctx, CommanderDataDeps{Store: c.svc.Store, Beads: c.svc.Beads})}
	}
}

func (c *ObjectivesColumn) preloadLiveData() {
	if c.svc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c.applyLiveData(LoadCommanderData(ctx, CommanderDataDeps{Store: c.svc.Store, Beads: c.svc.Beads}))
}

// commanderLiveDataMsg is shared with the fleet column — both consume the
// cross-fleet snapshot on the same cadence.
type commanderLiveDataMsg struct {
	Data CommanderLiveData
}

// Update handles ticks, live data swaps, navigation keys, and quit.
func (c *ObjectivesColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = innerSize(m.Width, m.Height)
		c.tree.SetSize(c.width-2, c.height-4)
		return c, nil

	case animMsg:
		c.frame++
		for k, left := range c.pulse {
			if left <= 1 {
				delete(c.pulse, k)
			} else {
				c.pulse[k] = left - 1
			}
		}
		if c.wantsAnim() {
			c.followID = ""
			if sel := c.tree.Selected(); sel != nil {
				c.followID = sel.ID
			}
			c.rebuildTree()
			return c, animTick(animInterval)
		}
		return c, animTick(750 * time.Millisecond) // idle: just check again later

	case ownWindowMsg:
		c.applyOwnWindow(m)
		return c, nil

	case tea.BlurMsg:
		c.tree.SetFocused(false) // drop the cursor band; "▌" still marks the current window
		c.refocus()
		return c, nil

	case tea.FocusMsg:
		// Switching windows by any means (Enter, C-a <n>, C-g) focuses this pane:
		// put the cursor back on the row for the window we're in.
		c.tree.SetFocused(true)
		c.ownActive = true
		c.followID = c.currentNodeID()
		c.rebuildTree()
		return c, c.ownWindowCmd()

	case expandedTickMsg:
		return c, tea.Batch(expandedTick(), c.ownWindowCmd()) // follow C-a <n> / clicks in other windows

	case columnTickMsg:
		cmds := []tea.Cmd{columnTick(), c.ownWindowCmd()}
		if cmd := c.loadLiveCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return c, tea.Batch(cmds...)

	case commanderLiveDataMsg:
		c.applyLiveData(m.Data)
		return c, nil

	case objectiveActionResultMsg:
		if m.err != nil {
			c.status = m.err.Error()
		} else {
			c.status = m.status
			if m.created != nil {
				c.upsertCreatedObjective(m.projectID, *m.created)
			}
		}
		cmds := []tea.Cmd{}
		if cmd := c.loadLiveCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return c, tea.Batch(cmds...)

	case widgets.TreeNodeSelectedMsg:
		meta, ok := c.nodeMeta[m.ID]
		if ok && meta.kind == "commander" {
			return c, sidebarKeyCmd(0)
		}
		if ok && meta.kind == "needsyou" {
			// Open that workstream's Manager with the ask pre-loaded as the first
			// inspector tab (full note, commands, links) and the Manager pointed at it.
			o := meta.objective
			root := c.wsmgrRoot(o.BeadID)
			ask := o.Ask
			if ask == "" {
				ask = o.Title
			}
			return c, tea.Sequence(
				runCmdrCommand("tab", "add", "--ws", root, "--project", o.ProjectName,
					"--decision", o.BeadID, "--title", truncate(ask, 24), "--focus"),
				runCmdrCommand("internal-open-workstream", "--project", o.ProjectName, "--bead", root, "--focus",
					"__status:Opened "+strconv.Quote(ask)+"."),
			)
		}
		if !ok || meta.kind != "objective" || meta.objective.BeadID == "" {
			return c, nil
		}
		if idx, inOrder := c.nodeWS[m.ID]; inOrder {
			return c, sidebarKeyCmd(idx + 1) // first press expands, the next one goes there
		}
		return c, runCmdrCommand("internal-open-workstream", "--project", meta.objective.ProjectName, "--bead", meta.objective.BeadID, "--focus",
			"__status:Opened the Manager for "+strconv.Quote(meta.objective.Title)+".")

	case tea.MouseMsg:
		m.X, m.Y = m.X-panelPadX, m.Y-panelPadY
		switch m.Button {
		case tea.MouseButtonWheelUp:
			c.tree.Move(-1)
			c.refocus()
			return c, nil
		case tea.MouseButtonWheelDown:
			c.tree.Move(1)
			c.refocus()
			return c, nil
		}
		idx, over := c.tree.RowAt(m.Y)
		if !over || !c.tree.Selectable(idx) {
			return c, nil
		}
		// The pointer moves the one highlighted row, which then stays where it
		// was left (no hover state of its own, nothing expires).
		c.tree.SelectIndex(idx)
		c.refocus()
		if m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft {
			// Click = go, like Enter on that row.
			updated, cmd := c.tree.Update(tea.KeyMsg{Type: tea.KeyEnter})
			*c.tree = updated
			return c, cmd
		}
		return c, nil

	case tea.KeyMsg:
		if c.prompt != "" {
			return c.updatePrompt(m)
		}
		switch m.String() {
		// No quit key: the sidebar is the navigator and must not be closable by accident.
		case "n", "r", "x", "m", "o":
			c.startPrompt(m.String())
			return c, nil
		case "s", "w":
			return c, c.toggleSelected(m.String())
		case "J", "shift+down":
			return c, c.moveSelected(+1)
		case "K", "shift+up":
			return c, c.moveSelected(-1)
		case "a", "d":
			if cmd := c.resolveSelectedAsk(m.String()); cmd != nil {
				return c, cmd
			}
			return c, nil
		case "e":
			c.toggleExpanded()
			return c, nil
		case "?":
			c.showHelp = !c.showHelp
			return c, nil
		case "g":
			c.grouped = !c.grouped
			saveGroupPref(c.grouped)
			c.rebuildTree()
			return c, nil
		}
		if forward := c.forwardTyping(m); forward != nil {
			return c, forward
		}
		updated, cmd := c.tree.Update(m)
		*c.tree = updated
		c.refocus()
		return c, cmd
	}
	return c, nil
}

// forwardTyping hands printable keys the sidebar has no use for to the chat
// pane (focus included), so typing to the agent always just works, even when the
// sidebar somehow kept the keyboard.
func (c *ObjectivesColumn) forwardTyping(m tea.KeyMsg) tea.Cmd {
	// Only text the user typed: never function keys, escape sequences, Alt
	// chords or pastes (those are not for the chat from here).
	if (m.Type != tea.KeyRunes && m.Type != tea.KeySpace) || m.Alt || m.Paste {
		return nil
	}
	text := string(m.Runes)
	if m.Type == tea.KeySpace {
		text = " "
	}
	for _, r := range text {
		if !unicode.IsPrint(r) {
			return nil
		}
	}
	switch text {
	case "", "j", "k", "h", "l":
		return nil // the tree's own navigation
	case "q":
		return nil // people hit q expecting to quit the sidebar: swallow it, never leak it into a chat
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return nil
	}
	return func() tea.Msg {
		out, err := execCommand("tmux", "list-panes", "-t", pane, "-F", "#{pane_id}|#{@cmdr_chat}").Output()
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if id, tag, _ := strings.Cut(line, "|"); tag == "1" {
				_ = execCommand("tmux", "select-pane", "-t", id).Run()
				_ = execCommand("tmux", "send-keys", "-t", id, "-l", text).Run()
				break
			}
		}
		return nil
	}
}

// resolveSelectedAsk answers the highlighted ⚑ ask in one key: a = approve (the
// step is marked done), d = defer (parked). Only NEEDS YOU rows respond.
// onAskRow reports whether the highlighted row is a NEEDS YOU ask.
// wsmgrRoot is the workstream whose Manager owns an objective id, by the
// loaded objectives (a nested one with its own Manager, else the root).
func (c *ObjectivesColumn) wsmgrRoot(id string) string {
	return c.live.OwnerOf(id)
}

func (c *ObjectivesColumn) onAskRow() bool {
	if sel := c.tree.Selected(); sel != nil {
		return c.nodeMeta[sel.ID].kind == "needsyou"
	}
	return false
}

func (c *ObjectivesColumn) resolveSelectedAsk(key string) tea.Cmd {
	sel := c.tree.Selected()
	if sel == nil {
		return nil
	}
	meta, ok := c.nodeMeta[sel.ID]
	if !ok || meta.kind != "needsyou" {
		return nil
	}
	action, done := "approve", "Approved"
	if key == "d" {
		action, done = "defer", "Deferred"
	}
	o := meta.objective
	ask := o.Ask
	if ask == "" {
		ask = o.Title
	}
	return runCmdrCommand("internal-resolve-needs", "--project", o.ProjectName, "--bead", o.BeadID, "--action", action,
		"__status:"+done+" "+strconv.Quote(ask)+".")
}

// toggleSelected flips the star ("s") or started state ("w") of the
// highlighted objective. Project rows and closed objectives are ignored.
func (c *ObjectivesColumn) toggleSelected(key string) tea.Cmd {
	sel := c.tree.Selected()
	if sel == nil {
		return nil
	}
	meta, ok := c.nodeMeta[sel.ID]
	if !ok || meta.kind != "objective" || meta.objective.BeadID == "" || meta.objective.Status == "closed" {
		return nil
	}
	o := meta.objective
	if key == "s" {
		star, msg := "true", "Starred "
		if o.Starred {
			star, msg = "false", "Unstarred "
		}
		return runCmdrCommand("internal-set-objective", "--project", o.ProjectName, "--bead", o.BeadID, "--star", star,
			"__status:"+msg+strconv.Quote(o.Title)+".")
	}
	status, msg := "in_progress", "Started "
	if o.Status == "in_progress" {
		status, msg = "open", "Stopped "
	}
	return runCmdrCommand("internal-set-objective", "--project", o.ProjectName, "--bead", o.BeadID, "--status", status,
		"__status:"+msg+strconv.Quote(o.Title)+".")
}

// startPrompt opens the child/rename/close prompt for the highlighted
// objective. Project rows and objectives without a bead id are ignored.
func (c *ObjectivesColumn) startPrompt(key string) {
	sel := c.tree.Selected()
	if sel == nil {
		return
	}
	meta, ok := c.nodeMeta[sel.ID]
	if !ok || meta.kind != "objective" || meta.objective.BeadID == "" || meta.objective.Status == "closed" {
		return
	}
	c.promptTarget = meta
	c.input.SetValue("")
	switch key {
	case "n":
		c.prompt = "child"
		c.input.Placeholder = "step title"
		c.input.Focus()
	case "r":
		c.prompt = "rename"
		c.input.SetValue(meta.objective.Title)
		c.input.CursorEnd()
		c.input.Focus()
	case "x":
		c.prompt = "close"
	case "m":
		c.prompt = "state"
	case "o":
		c.prompt = "owner"
		c.input.Placeholder = "operator-x, user, … (empty clears)"
		c.input.SetValue(meta.objective.Owner)
		c.input.CursorEnd()
		c.input.Focus()
	}
}

func (c *ObjectivesColumn) updatePrompt(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	target := c.promptTarget.objective
	project := target.ProjectName
	if k.String() == "esc" || k.String() == "ctrl+c" {
		c.prompt = ""
		return c, nil
	}
	if c.prompt == "state" {
		states := map[string]string{
			"n": beads.StateNotStarted, "w": beads.StateInProgress,
			"b": beads.StateBlocked, "e": beads.StateNeedsUser,
		}
		state, ok := states[k.String()]
		if !ok {
			return c, nil
		}
		c.prompt = ""
		return c, runCmdrCommand("internal-set-objective", "--project", project, "--bead", target.BeadID, "--state", state, "--no-notify",
			"__status:"+strconv.Quote(target.Title)+" → "+state+".")
	}
	if c.prompt == "close" {
		switch k.String() {
		case "y", "enter":
			c.prompt = ""
			return c, runCmdrCommand("internal-close-objective", "--project", project, "--bead", target.BeadID,
				"__status:Done: "+strconv.Quote(target.Title)+".")
		case "n":
			c.prompt = ""
		}
		return c, nil
	}
	if k.String() == "enter" {
		title := strings.TrimSpace(c.input.Value())
		mode := c.prompt
		c.prompt = ""
		if mode == "owner" {
			if title == "" {
				title = "none"
			}
			return c, runCmdrCommand("internal-set-objective", "--project", project, "--bead", target.BeadID, "--owner", title,
				"__status:Owner of "+strconv.Quote(target.Title)+": "+title+".")
		}
		if title == "" {
			return c, nil
		}
		if mode == "rename" {
			return c, runCmdrCommand("internal-rename-objective", "--project", project, "--bead", target.BeadID, "--title", title,
				"__status:Renamed to "+strconv.Quote(title)+".")
		}
		return c, runCmdrCommand("internal-create-objective", "--project", project, "--parent", target.BeadID, "--title", title,
			"__status:Added step "+strconv.Quote(title)+".")
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	return c, cmd
}

func (c *ObjectivesColumn) renderPrompt() string {
	switch c.prompt {
	case "child":
		return "new step under " + strconv.Quote(c.promptTarget.objective.Title) + ": " + c.input.View()
	case "rename":
		return "rename: " + c.input.View()
	case "close":
		return "mark " + strconv.Quote(c.promptTarget.objective.Title) + " done? y/n"
	case "state":
		return "mark " + strconv.Quote(c.promptTarget.objective.Title) + ": n not started · w working · b blocked · e needs the user"
	case "owner":
		return "owner: " + c.input.View()
	}
	return ""
}

func (c *ObjectivesColumn) upsertCreatedObjective(projectID int64, created ObjectiveRow) {
	if !c.liveLoaded {
		c.liveLoaded = true
	}
	foundProject := false
	for _, project := range c.live.Projects {
		if project.ID == projectID {
			foundProject = true
			break
		}
	}
	if !foundProject {
		return
	}
	for _, objective := range c.live.Objectives {
		if objective.BeadID == created.BeadID {
			return
		}
	}
	c.live.Objectives = append(c.live.Objectives, created)
	c.rebuildTree()
}

// applyLiveData swaps the fixture seed for live project/objective state.
func (c *ObjectivesColumn) applyLiveData(d CommanderLiveData) {
	// A failed read (db or beads busy, daemon restarting) must not wipe the
	// panel: keep the last good data and say so.
	if d.LoadErr != nil && c.liveLoaded && len(c.live.Projects) > 0 &&
		(len(d.Projects) == 0 || (len(d.Objectives) == 0 && len(c.live.Objectives) > 0)) {
		log.Printf("sidebar: live data load failed, keeping the last good frame: %v", d.LoadErr)
		c.reconnecting = true
		return
	}
	c.reconnecting = false
	c.live = d
	c.liveLoaded = true
	c.rebuildTree()
}

// View renders one Commander-scoped project hierarchy: each linked project is
// shown once with its objectives nested beneath it.
func (c *ObjectivesColumn) View() string {
	return c.frame0.keep(insetView(c.innerView()))
}

func (c *ObjectivesColumn) innerView() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}

	// The tree carries COMMANDER, "0 Commander" and its own WORKSTREAMS header.
	// Only with no tree at all (no projects yet) do we draw the header by hand.
	var head []string
	if len(c.tree.Nodes()) == 0 {
		head = append(head, widgets.SectionLabel("WORKSTREAMS", true, "", c.width))
	}

	var tail []string
	if len(c.projectsWithoutObjectives()) > 0 {
		tail = append(tail,
			"",
			widgets.SectionLabel("NO WORKSTREAMS YET", true, "", c.width),
			c.renderNoObjectives(),
		)
	}
	if c.reconnecting {
		tail = append(tail, lipgloss.NewStyle().Foreground(widgets.T.Attn).Render("↻ reconnecting…"))
	}
	for _, line := range []string{c.renderStatus(), c.renderPrompt()} {
		if line != "" { // don't spend pane height on empty status/prompt lines
			tail = append(tail, line)
		}
	}
	// FLEET block, pinned to the bottom of the column.
	if c.liveLoaded {
		m := c.live.Metrics
		tail = append(tail, widgets.SectionLabel("FLEET", true, "", c.width))
		tail = append(tail, widgets.MetricsData{
			TotalCostUSD24h: m.TotalCostUSD24h, TotalCostUSD7d: m.TotalCostUSD7d,
			AgentCount: m.AgentCount, ActiveCount: m.ActiveCount, BlockedCount: m.BlockedCount,
		}.Compact(c.width, m.CostSeries24h)...)
	}
	// Key hints appear only while this column has focus.
	if c.tree.Focused() {
		tail = append(tail, hintLines(c.width, c.showHelp, c.onAskRow())...)
	}

	// The tree gets whatever height the header and footer leave over, so the
	// whole view always fits the pane and the top is never pushed off-screen.
	fixed := lineCount(head) + lineCount(tail)
	treeH := max(3, c.height-fixed)
	c.tree.SetSize(c.width-2, treeH)
	body := c.renderProjectHierarchy()
	// Pad the tree area so the footer sits at the bottom of the pane.
	if n := strings.Count(body, "\n") + 1; n < treeH && len(c.tree.Nodes()) > 0 {
		body += strings.Repeat("\n", treeH-n)
	}

	parts := append(append(head, body), tail...)
	view := lipgloss.NewStyle().MaxWidth(c.width).MaxHeight(c.height).Render(strings.Join(parts, "\n"))
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ") // Render pads short lines to the widest
	}
	return strings.Join(lines, "\n")
}

// needsYouCount is how many open steps (anywhere in the fleet) wait on the user.
func (c *ObjectivesColumn) needsYouCount() int {
	n := 0
	for _, ws := range c.order {
		if ws.root.State == beads.StateNeedsUser {
			n++
		}
		for _, step := range ws.steps {
			if step.row.State == beads.StateNeedsUser {
				n++
			}
		}
	}
	return n
}

func lineCount(parts []string) int {
	n := 0
	for _, p := range parts {
		n += strings.Count(p, "\n") + 1
	}
	return n
}

var hintPairs = [][2]string{
	{"↑↓", "move"}, {"⏎", "go"}, {"s", "star"}, {"w", "start"}, {"m", "mark"}, {"o", "owner"}, {"x", "done"},
	{"a", "approve ⚑"}, {"d", "defer ⚑"}, {"n", "add step"}, {"r", "rename"}, {"J/K", "reorder"}, {"g", "group"}, {"e", "expand"}, {"?", "close help"},
}

// hintLines is one short line by default ("key label  key label"); "?" shows
// every key, wrapped to the pane width on pair boundaries.
func hintLines(width int, full, onAsk bool) []string {
	if !full && onAsk {
		return []string{widgets.HintLine(width, [2]string{"a", "approve"}, [2]string{"d", "defer"}, [2]string{"⏎", "open it"}, [2]string{"?", "keys"})}
	}
	if !full {
		return []string{widgets.HintLine(width, [2]string{"↑↓", "move"}, [2]string{"⏎", "go"}, [2]string{"J/K", "reorder"}, [2]string{"?", "keys"})}
	}
	return widgets.HintLines(width, hintPairs...)
}

func (c *ObjectivesColumn) renderProjectHierarchy() string {
	projects := c.projectsForRender()
	if len(projects) == 0 {
		if c.liveLoaded && c.live.LoadErr != nil {
			return widgets.ErrorCard("Commander state unavailable", c.live.LoadErr.Error(), "retrying every 5s · cmdr doctor", c.width)
		}
		return strings.Join([]string{
			widgets.FaintText.Render("(no projects yet)"),
			widgets.FaintText.Render("Tell Commander to add a project"),
		}, "\n")
	}

	return c.tree.View()
}

func (c *ObjectivesColumn) renderNoObjectives() string {
	names := c.projectsWithoutObjectives()
	if len(names) == 0 {
		return widgets.FaintText.Render("(none)")
	}
	return widgets.FaintText.Render(strings.Join(names, ", "))
}

func (c *ObjectivesColumn) projectsForRender() []store.Project {
	if c.liveLoaded {
		return c.live.Projects
	}
	projects := make([]store.Project, 0, len(c.fixtureProjects))
	for _, p := range c.fixtureProjects {
		projects = append(projects, store.Project{
			ID:     p.ID,
			Name:   p.Name,
			Type:   p.Type,
			Path:   p.Path,
			Status: p.Status,
		})
	}
	return projects
}

func (c *ObjectivesColumn) objectivesByProjectID() map[int64][]string {
	if c.liveLoaded {
		return buildObjectiveRowsFromLive(c.live.Objectives)
	}
	return buildObjectiveRowsFromFixtures(c.fixtureObjectives)
}

func (c *ObjectivesColumn) renderStatus() string {
	if strings.TrimSpace(c.status) == "" {
		return ""
	}
	return widgets.FaintText.Render(c.status)
}

// workstream is one top-level objective plus its nested steps.
type workstream struct {
	project store.Project
	root    ObjectiveRow
	steps   []nestedObjective
}

// workstreamsInOrder flattens every project's open workstreams into one list
// in the user's priority order (see livedata.WorkstreamsInOrder).
func workstreamsInOrder(projects []store.Project, objectivesFor func(projectID int64) []ObjectiveRow, ranks map[store.WorkstreamKey]int) []workstream {
	live := livedata.WorkstreamsInOrder(projects, objectivesFor, ranks)
	out := make([]workstream, len(live))
	for i, ws := range live {
		out[i] = workstream{project: ws.Project, root: ws.Root, steps: fromLiveNested(ws.Steps)}
	}
	return out
}

const (
	commanderRowID      = "commander:0"
	workstreamsHeaderID = "header:workstreams"
	needsHeaderID       = "header:needs"
)

var currentTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(widgets.T.TextPrimary)

// ownWindowMsg reports which window this panel lives in.
type ownWindowMsg struct {
	ok           bool
	name         string
	workstream   string // @cmdr_ws, "" for non-workstream windows
	windowActive bool
	// paneProbe is set when paneActive was actually measured (tmux answered).
	paneProbe  bool
	paneActive bool
	// sharedOpen is the shared @cmdr_expanded: which row every sidebar has open
	// ("" = unset, open this window's own).
	sharedOpen string
}

// ownWindowCmd asks tmux which window this pane is in ($TMUX_PANE) and whether
// it is the visible one, so the highlight tracks the window we're in.
func (c *ObjectivesColumn) ownWindowCmd() tea.Cmd {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return nil
	}
	return func() tea.Msg {
		out, err := execCommand("tmux", "display-message", "-p", "-t", pane,
			"#{window_name}|#{@cmdr_ws}|#{window_active}|#{pane_active}|#{@cmdr_expanded}").Output()
		if err != nil {
			return ownWindowMsg{}
		}
		parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 5)
		if len(parts) < 5 {
			return ownWindowMsg{}
		}
		return ownWindowMsg{ok: true, name: parts[0], workstream: parts[1], windowActive: parts[2] == "1",
			paneProbe: true, paneActive: parts[3] == "1", sharedOpen: parts[4]}
	}
}

// applyOwnWindow records the window identity. The cursor jumps to the current
// row the first time we learn it and whenever the window becomes the visible one.
func (c *ObjectivesColumn) applyOwnWindow(m ownWindowMsg) {
	if !m.ok {
		return
	}
	commander := m.workstream == "" && (m.name == "commander" || m.name == "cmdr-fleet")
	changed := !c.ownKnown || commander != c.ownCommander || m.workstream != c.ownWS
	becameActive := m.windowActive && !c.ownActive
	if m.paneProbe {
		c.tree.SetFocused(m.paneActive && m.windowActive)
	}
	c.ownKnown, c.ownCommander, c.ownWS, c.ownActive = true, commander, m.workstream, m.windowActive
	if m.sharedOpen != c.sharedOpen {
		c.sharedOpen = m.sharedOpen
		if sel := c.tree.Selected(); sel != nil {
			c.followID = sel.ID
		}
		c.rebuildTree()
	}
	if changed || becameActive {
		c.followID = c.currentNodeID()
		c.rebuildTree()
	}
}

// currentNodeID is the row for the window we're in ("" when it has none, e.g. an
// operator window).
func (c *ObjectivesColumn) currentNodeID() string {
	if c.ownCommander {
		return commanderRowID
	}
	if c.ownWS == "" {
		return ""
	}
	for _, ws := range c.order {
		if ws.root.BeadID == c.ownWS {
			return objectiveNodeID(ws.root)
		}
	}
	return ""
}

// focusWS is the workstream whose steps are shown (the accordion): the shared
// @cmdr_expanded row when set (C-a <n>, clicks), else the current window's.
func (c *ObjectivesColumn) focusWS() string {
	// One dropdown open at a time, shared by every window's sidebar: C-a <n>.
	if n, err := strconv.Atoi(c.sharedOpen); err == nil {
		if n >= 1 && n <= len(c.order) {
			return wsKey(c.order[n-1])
		}
		return "" // 0 = Commander: no workstream open
	}
	if c.ownWS != "" {
		for _, ws := range c.order {
			if ws.root.BeadID == c.ownWS {
				return wsKey(ws)
			}
		}
	}
	// A window with no row of its own (an operator window): follow the cursor.
	if !c.ownCommander && c.ownWS == "" && c.tree.Focused() {
		if sel := c.tree.Selected(); sel != nil {
			if idx, ok := c.nodeWS[sel.ID]; ok && idx < len(c.order) {
				return wsKey(c.order[idx])
			}
		}
	}
	return ""
}

// refocus rebuilds when the accordion's open workstream has changed (the cursor
// moved, focus changed). The cursor stays where it is.
func (c *ObjectivesColumn) refocus() {
	if c.focusWS() != c.lastFocus {
		if sel := c.tree.Selected(); sel != nil {
			c.followID = sel.ID
		}
		c.rebuildTree()
	}
}

// notePulses starts a short pulse for every step that has newly become ⚑ since
// the last rebuild. The first rebuild only records what already needs the user.
func (c *ObjectivesColumn) notePulses(order []workstream) {
	if c.seenNeeds == nil {
		c.seenNeeds = map[string]bool{}
		c.pulse = map[string]int{}
	}
	for _, ws := range order {
		for _, st := range ws.steps {
			if st.row.State != beads.StateNeedsUser || c.seenNeeds[st.row.BeadID] {
				continue
			}
			c.seenNeeds[st.row.BeadID] = true
			if c.seenInit && c.motion {
				c.pulse[st.row.BeadID] = 14 // ~2s at 150ms
			}
		}
	}
	if len(order) > 0 {
		c.seenInit = true
	}
}

// animMsg advances the spinner / pulse one frame.
type animMsg struct{}

// expandedTick re-reads the shared open row (@cmdr_expanded) often enough that a
// C-a <n> in one window shows in the others at once. State is polled, never
// pushed: no keystroke or signal is used to wake a sidebar.
type expandedTickMsg struct{}

func expandedTick() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return expandedTickMsg{} })
}

const animInterval = 110 * time.Millisecond

func animTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return animMsg{} })
}

// wantsAnim: something visible is moving (a spinner on the open workstream's
// in-progress steps, or a pulsing ⚑).
func (c *ObjectivesColumn) wantsAnim() bool {
	if !c.motion {
		return false
	}
	if len(c.pulse) > 0 {
		return true
	}
	// Any Manager mid-turn has a spinning "now" line.
	for _, ws := range c.order {
		if _, working := agentNow(store.WorkstreamManagerRole(ws.root.BeadID), animState{agents: c.liveAgents(), nowTime: time.Now()}); working {
			return true
		}
	}
	key := c.lastFocus
	for _, ws := range c.order {
		if wsKey(ws) != key {
			continue
		}
		for _, st := range ws.steps {
			if st.row.State == beads.StateInProgress {
				return true
			}
		}
	}
	return false
}

func wsKey(ws workstream) string {
	return fmt.Sprintf("%d:%s", ws.project.ID, ws.root.BeadID)
}

// toggleExpanded expands or collapses the workstream owning the highlighted row.
func (c *ObjectivesColumn) toggleExpanded() {
	sel := c.tree.Selected()
	if sel == nil {
		return
	}
	idx, ok := c.nodeWS[sel.ID]
	if !ok {
		return
	}
	if c.expanded == nil {
		c.expanded = map[string]bool{}
	}
	key := wsKey(c.order[idx])
	c.expanded[key] = !c.expanded[key]
	c.followID = sel.ID
	c.rebuildTree()
}

// allObjectives is every objective row the sidebar is built from.
func (c *ObjectivesColumn) allObjectives() []ObjectiveRow {
	if c.liveLoaded {
		return c.live.Objectives
	}
	var out []ObjectiveRow
	for _, p := range c.projectsForRender() {
		out = append(out, c.objectivesForProject(p.ID)...)
	}
	return out
}

func (c *ObjectivesColumn) liveAgents() map[string]store.AgentState {
	if c.liveLoaded {
		return c.live.Agents
	}
	return nil
}

func (c *ObjectivesColumn) liveWaits() map[string]livedata.ManagerWait {
	if c.liveLoaded {
		return livedata.ManagerWaits(livedata.Workstreams(c.live))
	}
	return nil
}

func (c *ObjectivesColumn) liveRanks() map[store.WorkstreamKey]int {
	if c.liveLoaded {
		return c.live.Ranks
	}
	return nil
}

func (c *ObjectivesColumn) rebuildTree() {
	keepID := c.followID
	if keepID == "" {
		if sel := c.tree.Selected(); sel != nil {
			keepID = sel.ID
		}
	}
	c.followID = ""

	projects := c.projectsForRender()
	order := workstreamsInOrder(projects, c.objectivesForProject, c.liveRanks())
	c.order = order
	c.notePulses(order)
	nodes := make([]widgets.TreeNode, 0, len(order)*3)
	meta := make(map[string]objectiveNodeMeta)
	nodeWS := make(map[string]int)

	focusKey := c.focusWS()
	c.lastFocus = focusKey

	// Row 0 is Commander, the first stop of the navigator, under its own label.
	// The fleet-wide totals live on its row.
	needsTotal, runningTotal := 0, 0
	for _, ws := range order {
		_, r, _, _, _ := stepCounts(ws)
		runningTotal += r
	}
	needsTotal = c.needsYouCount()
	nodes = append(nodes, widgets.TreeNode{ID: "header:commander", Title: "COMMANDER", Header: true, NoRule: true, Skip: true})
	nodes = append(nodes, widgets.TreeNode{
		ID: commanderRowID, Title: "Commander", Columnar: true, Prefix: "0", TitleStyle: &normalStyle,
		Current:  c.ownCommander,
		TagParts: countParts(needsTotal, runningTotal, 0, 0),
	})
	meta[commanderRowID] = objectiveNodeMeta{kind: "commander"}
	// Row 0's own live state: the Commander chat in window 0, nothing else.
	cmdrNow, _ := agentNow("commander", animState{frame: c.frame, on: c.motion, agents: c.liveAgents(), nowTime: time.Now()})
	nodes = append(nodes, widgets.TreeNode{
		ID: "now:commander", Title: cmdrNow, Columnar: true, Indent: 2, Skip: true, TitleStyle: &lipgloss.Style{},
	})
	// NEEDS YOU: the fleet-wide queue of ⚑ asks, so what the user must do is the
	// first thing he sees. Hidden when nothing is flagged.
	if asks := needsYou(CommanderLiveData{Projects: projects, Objectives: c.allObjectives(), Ranks: c.liveRanks()}); len(asks) > 0 {
		nodes = append(nodes, widgets.TreeNode{ID: needsHeaderID, Title: "NEEDS YOU", RightTag: fmt.Sprint(len(asks)), Header: true, Skip: true, GapBefore: true})
		for _, a := range asks {
			text := a.Ask
			if text == "" {
				text = a.Title
			}
			id := "needs:" + a.Project + ":" + a.BeadID
			row := ObjectiveRow{BeadID: a.BeadID, Title: a.Title, ProjectName: a.Project, Ask: a.Ask, Note: a.Note, State: beads.StateNeedsUser}
			nodes = append(nodes, widgets.TreeNode{
				ID: id, Title: text, Columnar: true, Marker: marker(beads.StateNeedsUser), TitleStyle: &brightStyle,
				TagParts: []widgets.TagPart{{Text: fmt.Sprintf("#%d", a.Rank), Color: widgets.T.TextMuted}},
			})
			meta[id] = objectiveNodeMeta{kind: "needsyou", objective: row}
		}
	}
	// "0 Commander" is its own section above the WORKSTREAMS header.
	nodes = append(nodes, widgets.TreeNode{ID: workstreamsHeaderID, Title: "WORKSTREAMS", RightTag: fmt.Sprint(len(order)), Header: true, Skip: true, GapBefore: true})

	waits := c.liveWaits()
	// emit adds workstream i's rows; the panel layout lives in workstreamRows.
	emit := func(i int, tagProject bool) {
		ws := order[i]
		expanded := c.expanded[wsKey(ws)]
		for n, r := range workstreamRows(i+1, ws, expanded, tagProject, wsKey(ws) == focusKey, animState{frame: c.frame, on: c.motion, pulse: c.pulse, agents: c.liveAgents(), waits: waits, nowTime: time.Now()}) {
			node := r.node
			node.Skip = n != 0 // only the workstream header is selectable
			if n == 0 && !c.ownCommander && c.ownWS != "" && ws.root.BeadID == c.ownWS {
				node.Current = true
				node.TitleStyle = &currentTitleStyle
			}
			nodes = append(nodes, node)
			meta[node.ID] = objectiveNodeMeta{kind: r.kind, project: ws.project, objective: r.row}
			nodeWS[node.ID] = i
		}
	}

	if !c.grouped {
		for i := range order {
			emit(i, false)
		}
	} else {
		// Projects ordered by their highest-priority workstream; ranks stay global.
		seen := map[int64]bool{}
		for _, first := range order {
			if seen[first.project.ID] {
				continue
			}
			seen[first.project.ID] = true
			headerID := "project:" + first.project.Name
			nodes = append(nodes, widgets.TreeNode{
				ID:         headerID,
				Title:      strings.ToUpper(first.project.Name),
				Emphasis:   true,
				Outline:    true,
				GapBefore:  len(nodes) > 0,
				Skip:       true,
				TitleStyle: &projectTitleStyle,
			})
			meta[headerID] = objectiveNodeMeta{kind: "project", project: first.project}
			for i, ws := range order {
				if ws.project.ID == first.project.ID {
					emit(i, false)
				}
			}
		}
	}
	c.nodeMeta = meta
	c.nodeWS = nodeWS
	c.tree.SetNodes(nodes)
	if keepID != "" {
		c.tree.SelectID(keepID)
	} else if !c.ownCommander {
		// nothing selected yet: start on the first workstream, not on row 0 or an ask
		for i, n := range nodes {
			if m, ok := meta[n.ID]; ok && !n.Skip && m.kind != "commander" && m.kind != "needsyou" {
				c.tree.SelectIndex(i)
				break
			}
		}
	}
	// The accordion follows the cursor, which only now has its final position.
	if !c.rebuilding && c.focusWS() != c.lastFocus {
		c.rebuilding = true
		if sel := c.tree.Selected(); sel != nil {
			c.followID = sel.ID
		}
		c.rebuildTree()
		c.rebuilding = false
	}
}

// reorderKeys returns the workstream keys with order[from] moved to index to.
func reorderKeys(order []workstream, from, to int) []store.WorkstreamKey {
	keys := make([]store.WorkstreamKey, 0, len(order))
	for _, ws := range order {
		keys = append(keys, store.WorkstreamKey{ProjectID: ws.project.ID, BeadID: ws.root.BeadID})
	}
	moved := keys[from]
	keys = append(keys[:from], keys[from+1:]...)
	keys = append(keys[:to], append([]store.WorkstreamKey{moved}, keys[to:]...)...)
	return keys
}

func ranksFromKeys(keys []store.WorkstreamKey) map[store.WorkstreamKey]int {
	ranks := make(map[store.WorkstreamKey]int, len(keys))
	for i, k := range keys {
		ranks[k] = i + 1
	}
	return ranks
}

// moveSelected moves the highlighted workstream (or the workstream owning the
// highlighted step) one slot down (+1) or up (-1) in priority. When grouped it
// jumps to the neighbouring workstream of the same project so the move is
// visible.
func (c *ObjectivesColumn) moveSelected(delta int) tea.Cmd {
	sel := c.tree.Selected()
	if sel == nil {
		return nil
	}
	idx, ok := c.nodeWS[sel.ID]
	if !ok {
		return nil
	}
	if c.svc == nil || c.svc.Store == nil || !c.liveLoaded {
		c.status = "Reordering needs live data."
		return nil
	}
	target := idx + delta
	if c.grouped {
		for target >= 0 && target < len(c.order) && c.order[target].project.ID != c.order[idx].project.ID {
			target += delta
		}
	}
	if target < 0 || target >= len(c.order) {
		return nil
	}
	present := make([]store.WorkstreamKey, 0, len(c.order))
	for _, ws := range c.order {
		present = append(present, store.WorkstreamKey{ProjectID: ws.project.ID, BeadID: ws.root.BeadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keys, err := c.svc.Store.MoveWorkstream(ctx, present, present[idx], target+1)
	if err != nil {
		c.status = err.Error()
		return nil
	}
	c.live.Ranks = ranksFromKeys(keys)
	c.followID = sel.ID
	c.status = fmt.Sprintf("Moved to priority %d.", target+1)
	c.rebuildTree()
	// Manager windows are named "#<rank> <title>"; bring them in line.
	return runCmdrCommand("internal-sync-windows", "__status:"+c.status)
}

// --- per-window group preference -------------------------------------------

func groupPrefPath() string {
	statePath, err := stateDBPath()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(statePath), "workstreams-ui.json")
}

func readGroupPrefs() map[string]bool {
	prefs := map[string]bool{}
	if path := groupPrefPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &prefs)
		}
	}
	return prefs
}

// groupPrefKey: the panel is identical in every window, so one setting covers all.
const groupPrefKey = ""

// loadGroupPref reports whether the panel groups by project. Any failure means flat.
func loadGroupPref() bool { return readGroupPrefs()[groupPrefKey] }

func saveGroupPref(grouped bool) {
	path := groupPrefPath()
	if path == "" {
		return
	}
	prefs := readGroupPrefs()
	prefs[groupPrefKey] = grouped
	if data, err := json.Marshal(prefs); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

type nestedObjective struct {
	row   ObjectiveRow
	depth int
}

// nestObjectives orders objectives parent-first (see livedata.NestObjectives).
func nestObjectives(rows []ObjectiveRow) []nestedObjective {
	return fromLiveNested(livedata.NestObjectives(rows))
}

func fromLiveNested(live []livedata.NestedObjective) []nestedObjective {
	out := make([]nestedObjective, len(live))
	for i, n := range live {
		out[i] = nestedObjective{row: n.Row, depth: n.Depth}
	}
	return out
}

func (c *ObjectivesColumn) projectsWithoutObjectives() []string {
	projects := c.projectsForRender()
	objectivesByProject := c.objectivesByProjectID()
	names := make([]string, 0)
	for _, p := range projects {
		if len(objectivesByProject[p.ID]) == 0 {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (c *ObjectivesColumn) objectivesForProject(projectID int64) []ObjectiveRow {
	if !c.liveLoaded {
		out := make([]ObjectiveRow, 0)
		for _, objective := range c.fixtureObjectives {
			if objective.ProjectID != projectID {
				continue
			}
			out = append(out, ObjectiveRow{
				Title:       objective.Title,
				ProjectID:   objective.ProjectID,
				LinkedTasks: len(objective.TaskBeadIDs),
				Status:      objective.Status,
			})
		}
		return out
	}
	out := make([]ObjectiveRow, 0)
	for _, objective := range c.live.Objectives {
		if objective.ProjectID == projectID {
			out = append(out, objective)
		}
	}
	return out
}

func runCmdrCommand(args ...string) tea.Cmd {
	successStatus := ""
	if len(args) > 0 {
		last := args[len(args)-1]
		if strings.HasPrefix(last, "__status:") {
			successStatus = strings.TrimPrefix(last, "__status:")
			args = args[:len(args)-1]
		}
	}
	return func() tea.Msg {
		exe, err := selfExe()
		if err != nil {
			return objectiveActionResultMsg{err: err}
		}
		cmd := exec.Command(exe, args...)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return objectiveActionResultMsg{err: fmt.Errorf("%s", msg)}
		}
		return objectiveActionResultMsg{status: successStatus}
	}
}

// sidebarKeyCmd is the C-a <n> rule for a click or Enter on row n: expand it,
// or go there if it is already open; keyboard focus goes back to the chat.
func sidebarKeyCmd(n int) tea.Cmd {
	args := []string{"__sidebar-key", strconv.Itoa(n)}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = append(args, "--from-pane", pane)
	}
	return runCmdrCommand(args...)
}

func fastSelectWindowCmd(windowName, successStatus string) tea.Cmd {
	return func() tea.Msg {
		cmd := execCommand("tmux", "select-window", "-t", "commander:"+windowName)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return objectiveActionResultMsg{err: fmt.Errorf("%s", msg)}
		}
		return objectiveActionResultMsg{status: successStatus}
	}
}

func buildObjectiveRowsFromFixtures(objs []FixtureObjective) map[int64][]string {
	rowsByProject := make(map[int64][]string)
	for _, obj := range objs {
		title := obj.Title
		if len(obj.TaskBeadIDs) > 0 {
			title = fmt.Sprintf("%s (%d tasks)", obj.Title, len(obj.TaskBeadIDs))
		}
		rowsByProject[obj.ProjectID] = append(rowsByProject[obj.ProjectID], objectiveRow(title, obj.Status))
	}
	return rowsByProject
}

func buildObjectiveRowsFromLive(objs []ObjectiveRow) map[int64][]string {
	rowsByProject := make(map[int64][]string)
	for _, o := range objs {
		if o.ProjectID == 0 {
			continue
		}
		title := o.Title
		if o.LinkedTasks > 0 {
			title = fmt.Sprintf("%s (%d tasks)", o.Title, o.LinkedTasks)
		}
		rowsByProject[o.ProjectID] = append(rowsByProject[o.ProjectID], objectiveRow(title, o.Status))
	}
	return rowsByProject
}

func objectiveRow(title, status string) string {
	return fmt.Sprintf("%s %s", widgets.StatusIcon(normalizeObjectiveStatus(status)), lipgloss.NewStyle().Foreground(widgets.Palette.Subtext1).Render(title))
}

func normalizeObjectiveStatus(status string) string {
	switch status {
	case "open", "":
		return "planning"
	case "closed":
		return "done"
	default:
		return status
	}
}

// NeedsYou is one open step waiting on the user to verify or decide.
type NeedsYou = livedata.NeedsYou

// ListNeedsYou returns every open needs-the user step in the fleet, in workstream
// priority order, so Commander can keep pending sign-offs in front of the user.
func ListNeedsYou(ctx context.Context) ([]NeedsYou, error) {
	return listNeedsYou(ctx, "", "", LoadCommanderData)
}

// ListNeedsYouFresh is ListNeedsYou read straight from br and state.db, not
// from cmdrd's snapshot, which lags a card stored moments ago.
func ListNeedsYouFresh(ctx context.Context) ([]NeedsYou, error) {
	return listNeedsYou(ctx, "", "", LoadCommanderDataLocal)
}

// ListNeedsYouForBead is ListNeedsYouFresh for one bead: it reads only the
// project given, else the one whose name prefixes the bead id, else all.
func ListNeedsYouForBead(ctx context.Context, project, beadID string) ([]NeedsYou, error) {
	return listNeedsYou(ctx, project, beadID, LoadCommanderDataLocal)
}

func listNeedsYou(ctx context.Context, project, beadID string, load func(context.Context, CommanderDataDeps) CommanderLiveData) ([]NeedsYou, error) {
	svc, warn := openColumnServices()
	if svc.Store == nil {
		return nil, fmt.Errorf("%s", warn)
	}
	defer svc.Store.Close()
	if project == "" && beadID != "" {
		if all, err := svc.Store.ListProjects(ctx); err == nil {
			project = livedata.ProjectOfBead(all, beadID)
		}
	}
	data := load(ctx, CommanderDataDeps{Store: svc.Store, Beads: svc.Beads, OnlyProject: project})
	if data.LoadErr != nil {
		return nil, data.LoadErr
	}
	return needsYou(data), nil
}

func needsYou(data CommanderLiveData) []NeedsYou { return livedata.NeedsYouList(data) }

// NumberedWorkstream is one row of the Workstreams column: the number the user
// sees, so "work on 3" can be resolved to a bead.
type NumberedWorkstream struct {
	N       int    `json:"n"`
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	Title   string `json:"title"`
	State   string `json:"state,omitempty"`
	Owner   string `json:"owner,omitempty"`
	// ManagerRole is the mail role of this workstream's Manager.
	ManagerRole string `json:"manager_role"`
}

// ListNumberedWorkstreams loads live data and returns the workstreams with the
// same numbering the column shows.
func ListNumberedWorkstreams(ctx context.Context) ([]NumberedWorkstream, error) {
	return listNumberedWorkstreams(ctx, LoadCommanderData)
}

// ListNumberedWorkstreamsFresh is ListNumberedWorkstreams read straight from
// br, not from cmdrd's snapshot (which misses a workstream created seconds ago).
func ListNumberedWorkstreamsFresh(ctx context.Context) ([]NumberedWorkstream, error) {
	return listNumberedWorkstreams(ctx, LoadCommanderDataLocal)
}

func listNumberedWorkstreams(ctx context.Context, load func(context.Context, CommanderDataDeps) CommanderLiveData) ([]NumberedWorkstream, error) {
	svc, warn := openColumnServices()
	if svc.Store == nil {
		return nil, fmt.Errorf("%s", warn)
	}
	defer svc.Store.Close()
	data := load(ctx, CommanderDataDeps{Store: svc.Store, Beads: svc.Beads})
	if data.LoadErr != nil && !livedata.OnlyProjectReadErrors(data.LoadErr) {
		return nil, data.LoadErr
	}
	// A failing project does not hide the others: flag it and list the rest.
	if data.LoadErr != nil {
		fmt.Fprintf(os.Stderr, "warning: degraded: %v\n", data.LoadErr)
	}
	return numberWorkstreams(data), nil
}

// MoveBeadTo puts a nested bead at priority rank `to` among its open siblings.
// It reports false when the bead is top-level, which MoveWorkstreamTo moves.
func MoveBeadTo(ctx context.Context, project, beadID string, to int) (bool, error) {
	svc, warn := openColumnServices()
	if svc.Store == nil {
		return false, fmt.Errorf("%s", warn)
	}
	defer svc.Store.Close()
	data := LoadCommanderData(ctx, CommanderDataDeps{Store: svc.Store, Beads: svc.Beads})
	if data.LoadErr != nil {
		return false, data.LoadErr
	}
	for _, o := range data.Objectives {
		if o.BeadID == beadID {
			if o.ParentBeadID == "" {
				return false, nil
			}
			return true, moveAmongSiblings(ctx, svc.Store, data, project, beadID, o.ParentBeadID, "", to)
		}
	}
	return false, fmt.Errorf("no open bead %s in %s", beadID, project)
}

// MoveBeadAfter puts a bead just under parent after a sibling, else at rank `to`.
// parent is passed, not read: a move just written may not show in a load yet.
func MoveBeadAfter(ctx context.Context, project, beadID, parent, after string, to int) error {
	svc, warn := openColumnServices()
	if svc.Store == nil {
		return fmt.Errorf("%s", warn)
	}
	defer svc.Store.Close()
	data := LoadCommanderData(ctx, CommanderDataDeps{Store: svc.Store, Beads: svc.Beads})
	if data.LoadErr != nil {
		return data.LoadErr
	}
	return moveAmongSiblings(ctx, svc.Store, data, project, beadID, parent, after, to)
}

func moveAmongSiblings(ctx context.Context, st *store.Store, data CommanderLiveData, project, beadID, parent, after string, to int) error {
	projectID := int64(-1)
	for _, p := range data.Projects {
		if p.Name == project {
			projectID = p.ID
		}
	}
	if projectID < 0 {
		return fmt.Errorf("no project %s", project)
	}
	key := store.WorkstreamKey{ProjectID: projectID, BeadID: beadID}
	var present []store.WorkstreamKey
	for _, n := range livedata.NestObjectives(objectivesOf(data, projectID)) {
		if n.Row.ParentBeadID == parent && n.Row.BeadID != beadID && n.Row.Status != "closed" && !n.Row.Superseded {
			present = append(present, store.WorkstreamKey{ProjectID: projectID, BeadID: n.Row.BeadID})
		}
	}
	if after != "" {
		for i, k := range present {
			if k.BeadID == after {
				to = i + 2
			}
		}
	}
	present = append(present, key)
	_, err := st.MoveBead(ctx, present, key, to)
	return err
}

// MoveWorkstreamTo puts a workstream at priority rank `to` (1 = top) and
// returns the renumbered list.
func MoveWorkstreamTo(ctx context.Context, project, beadID string, to int) ([]NumberedWorkstream, error) {
	return moveWorkstreamTo(ctx, project, beadID, to, false)
}

// PlaceWorkstreamAt is MoveWorkstreamTo for a bead just moved to the top level.
func PlaceWorkstreamAt(ctx context.Context, project, beadID string, to int) error {
	_, err := moveWorkstreamTo(ctx, project, beadID, to, true)
	return err
}

func moveWorkstreamTo(ctx context.Context, project, beadID string, to int, mayBeNew bool) ([]NumberedWorkstream, error) {
	svc, warn := openColumnServices()
	if svc.Store == nil {
		return nil, fmt.Errorf("%s", warn)
	}
	defer svc.Store.Close()
	data := LoadCommanderData(ctx, CommanderDataDeps{Store: svc.Store, Beads: svc.Beads})
	if data.LoadErr != nil {
		return nil, data.LoadErr
	}
	// Default order (no ranks): the store merges the move into its own current
	// ranks, never into the order of cmdrd's possibly older snapshot.
	var present []store.WorkstreamKey
	var key store.WorkstreamKey
	found := false
	for _, ws := range workstreamsInOrder(data.Projects, func(id int64) []ObjectiveRow { return objectivesOf(data, id) }, nil) {
		k := store.WorkstreamKey{ProjectID: ws.project.ID, BeadID: ws.root.BeadID}
		present = append(present, k)
		if ws.project.Name == project && ws.root.BeadID == beadID {
			key, found = k, true
		}
	}
	if !found && mayBeNew {
		// It only just reached the top: a load can still show it under its old parent.
		for _, p := range data.Projects {
			if p.Name == project {
				key, found = store.WorkstreamKey{ProjectID: p.ID, BeadID: beadID}, true
				present = append(present, key)
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("%s is not an open workstream in %s", beadID, project)
	}
	keys, err := svc.Store.MoveWorkstream(ctx, present, key, to)
	if err != nil {
		return nil, err
	}
	data.Ranks = ranksFromKeys(keys)
	return numberWorkstreams(data), nil
}

func objectivesOf(data CommanderLiveData, projectID int64) []ObjectiveRow {
	return livedata.ObjectivesOf(data, projectID)
}

// NumberWorkstreams is ListNumberedWorkstreams over data already loaded.
func NumberWorkstreams(data CommanderLiveData) []NumberedWorkstream { return numberWorkstreams(data) }

func numberWorkstreams(data CommanderLiveData) []NumberedWorkstream {
	out := []NumberedWorkstream{}
	byProject := func(projectID int64) []ObjectiveRow { return objectivesOf(data, projectID) }
	for i, ws := range workstreamsInOrder(data.Projects, byProject, data.Ranks) {
		out = append(out, NumberedWorkstream{
			N: i + 1, Project: ws.project.Name, BeadID: ws.root.BeadID, Title: ws.root.Title,
			State: ws.root.State, Owner: ws.root.Owner, ManagerRole: store.WorkstreamManagerRole(ws.root.BeadID),
		})
	}
	return out
}
