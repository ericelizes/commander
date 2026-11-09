package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/kittyimg"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// InspectorColumn is the right-hand column of every Commander / workstream
// window (`cmdr __column inspector [--ws BEAD --project P]`): a strip of tabs
// (PRs, issues, specs, previews, commands) over one card. Each window owns its
// own process, so switching windows switches tabs. Managers add tabs with
// `cmdr tab add`; the column re-reads the table every columnTick.
type InspectorColumn struct {
	width, height int

	svc         *Services
	projectName string
	wsBead      string
	projectID   int64
	resolved    bool

	tabs    []store.InspectorTab
	active  int64 // active tab id; 0 = the first tab
	cards   map[int64]inspector.Card
	loading map[int64]bool

	focused  bool
	hover    int // tab index under the pointer, -1 none; addHover for the "+"
	addHover bool
	hoverSeq int
	segs     []tabSeg
	showHelp bool
	status   string

	vp         viewport.Model
	contentFor string // what the viewport currently shows (tab id + fetch time + width)

	// prompt is "add" (URL input) or "close" (y/n); keys go to it while set.
	prompt string
	input  textinput.Model

	fetcher inspector.Fetcher
	now     func() time.Time

	// Kitty-graphics pictures (browser renderer). graphics is false unless the
	// terminal supports it; imgOff is the first image row shown (j/k scroll);
	// sent remembers which picture each tab last uploaded to the terminal.
	graphics  bool
	inTmux    bool
	kittyFile bool // CMDR_KITTY_MEDIUM=file: the terminal reads the PNG itself
	imgHi     uint8
	imgOff    int
	sent      map[int64]string

	// Decision cards (kind "decision"): the user's answer in progress per tab, and
	// the clickable rows of the card last drawn.
	dec     map[int64]*decisionState
	decHits []decisionHit

	frame0 frameKeeper

	emptyKnown, wasEmpty bool // last value told to tmux (@cmdr_inspector_empty)
}

// Test seams for the two things the column does outside the process.
var (
	inspectorOpenURL = func(url string) error {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		return exec.Command(opener, url).Run()
	}
	inspectorCopy = func(text string) error {
		cmd := exec.Command("xclip", "-selection", "clipboard")
		if runtime.GOOS == "darwin" {
			cmd = exec.Command("pbcopy")
		}
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	// inspectorWriteRaw sends escape sequences straight to the terminal, beside
	// Bubble Tea's frames (os.File writes are serialized, so they never interleave).
	inspectorWriteRaw = func(s string) error {
		_, err := os.Stdout.WriteString(s)
		return err
	}
	inspectorLookPath = exec.LookPath
	// inspectorPopup runs a shell command full-window in a tmux popup.
	inspectorPopup = func(shellCmd string) error {
		return exec.Command("tmux", "display-popup", "-E", "-w", "100%", "-h", "100%", shellCmd).Run()
	}
)

// tmuxOutput runs tmux and returns its trimmed stdout ("" on error).
func tmuxOutput(args ...string) string {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// enableGraphics turns on kitty pictures for web/browser tabs when the outer
// terminal supports them (Ghostty, kitty). Without them those tabs stay link
// cards with a hint.
func (c *InspectorColumn) enableGraphics() {
	c.graphics = kittyimg.Supported(os.Getenv, tmuxOutput)
	c.inTmux = os.Getenv("TMUX") != ""
	c.kittyFile = os.Getenv("CMDR_KITTY_MEDIUM") == "file"
	cellW, cellH := kittyimg.CellSize(os.Getenv, tmuxOutput)
	dir, profile := "", ""
	if db, err := stateDBPath(); err == nil {
		dir = filepath.Join(filepath.Dir(db), "inspector")
		profile = inspector.ProfileDir(filepath.Dir(db))
	}
	c.fetcher.Browser = inspector.BrowserConfig{Enabled: c.graphics && dir != "", Dir: dir, Profile: profile, CellW: cellW, CellH: cellH}
}

// NewInspectorColumn builds the column. projectName and wsBead are empty for
// the Commander window.
func NewInspectorColumn(svc *Services, projectName, wsBead string) *InspectorColumn {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "https://… (PR, issue, spec, preview)"
	c := &InspectorColumn{
		svc: svc, projectName: projectName, wsBead: wsBead,
		cards: map[int64]inspector.Card{}, loading: map[int64]bool{}, hover: -1,
		vp: viewport.New(0, 0), input: in,
		fetcher: inspector.Fetcher{}, now: time.Now,
		imgHi: uint8(1 + os.Getpid()%255), sent: map[int64]string{},
	}
	c.resolve()
	return c
}

// RunInspectorColumn is the `cmdr __column inspector` entry point.
func RunInspectorColumn(projectName, wsBead string) error {
	svc, _ := openColumnServices()
	m := NewInspectorColumn(svc, projectName, wsBead)
	m.enableGraphics()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithReportFocus()).Run()
	return err
}

func (c *InspectorColumn) store() *store.Store {
	if c.svc == nil {
		return nil
	}
	return c.svc.Store
}

// resolve finds this window's (project id, workstream) key; it is retried on
// every tick until a workstream's Manager row exists.
func (c *InspectorColumn) resolve() {
	st := c.store()
	if st == nil || c.resolved {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id, ws, err := st.InspectorWindow(ctx, c.projectName, c.wsBead)
	if err != nil {
		c.status = err.Error()
		return
	}
	c.projectID, c.wsBead, c.resolved = id, ws, true
	c.status = ""
}

// ---- messages --------------------------------------------------------------

type inspectorLoadedMsg struct {
	decisions map[int64]decisionRecord // by tab id, for decision tabs
	tabs      []store.InspectorTab
	active    int64
	cacheJSON string
	cachedAt  time.Time
	status    string
}

type inspectorCardMsg struct {
	tabID int64
	card  inspector.Card
}

type inspectorActionMsg struct{ status string }

// ---- commands (all run off the UI goroutine) --------------------------------

func (c *InspectorColumn) loadCmd() tea.Cmd {
	st := c.store()
	if st == nil {
		return nil
	}
	c.resolve()
	if !c.resolved {
		return nil
	}
	pid, ws := c.projectID, c.wsBead
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tabs, err := st.ListTabs(ctx, pid, ws)
		if err != nil {
			return inspectorLoadedMsg{status: err.Error()}
		}
		active, _ := st.GetActiveTab(ctx, pid, ws)
		js, at, _ := st.GetTabCache(ctx, pid, ws)
		return inspectorLoadedMsg{tabs: tabs, active: active, cacheJSON: js, cachedAt: at, decisions: loadDecisions(ctx, st, pid, tabs)}
	}
}

// mutate runs a store write, then reloads the table.
func (c *InspectorColumn) mutate(fn func(ctx context.Context, st *store.Store) error) tea.Cmd {
	st := c.store()
	if st == nil {
		return nil
	}
	load := c.loadCmd()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fn(ctx, st); err != nil {
			return inspectorActionMsg{status: err.Error()}
		}
		if load == nil {
			return nil
		}
		return load()
	}
}

// fetchCmd refreshes one tab's card. The result is cached in the store when the
// tab is still the window's active one.
func (c *InspectorColumn) fetchCmd(tab store.InspectorTab) tea.Cmd {
	if tab.Kind == inspector.KindDecision || c.loading[tab.ID] {
		return nil
	}
	c.loading[tab.ID] = true
	st, pid, ws := c.store(), c.projectID, c.wsBead
	fetcher := c.fetcher
	firstID := c.tabs[0].ID
	cols, rows := c.pictureBox()
	return func() tea.Msg {
		card, _ := fetcher.Fetch(context.Background(), inspector.Target{
			Kind: tab.Kind, Target: tab.Target, Renderer: tab.Renderer, Title: tab.Title,
			Cols: cols, Rows: rows,
		})
		if st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			active, _ := st.GetActiveTab(ctx, pid, ws)
			if active == 0 {
				active = firstID
			}
			if active == tab.ID && card.Err == "" {
				if js, err := inspector.Marshal(card); err == nil {
					_ = st.SaveTabCache(ctx, pid, ws, js, card.FetchedAt)
				}
			}
		}
		return inspectorCardMsg{tabID: tab.ID, card: card}
	}
}

// refreshActive fetches the active tab if its card is missing or stale (or
// force is set).
func (c *InspectorColumn) refreshActive(force bool) tea.Cmd {
	tab := c.activeTab()
	if tab == nil {
		return nil
	}
	if tab.Kind == inspector.KindDecision {
		return nil // a form, not a fetched page
	}
	card, have := c.cards[tab.ID]
	resized := card.Image != "" && card.ImageCols != c.width
	if !force && have && !resized && !inspector.Stale(card.FetchedAt, tab.RefreshSecs, c.now()) {
		return c.transmitCmd()
	}
	return c.fetchCmd(*tab)
}

// pictureBox is the cell box a page picture is shot for: the column's width and
// twice the body height, so j/k can scroll a page and a half below the fold.
func (c *InspectorColumn) pictureBox() (cols, rows int) {
	body := max(4, c.height-8) // label, strip, rule, header, blank, footer
	return min(c.width, kittyimg.MaxCells), min(2*body, kittyimg.MaxCells)
}

// imageID is the kitty image id of a tab's picture: the tab in the low byte,
// this process in the high byte (each window's inspector is its own process
// but they all draw into the same terminal).
func (c *InspectorColumn) imageID(tabID int64) uint32 {
	return kittyimg.ID(c.imgHi, uint8(1+tabID%255))
}

// showsPicture reports whether the active tab is drawn as a kitty picture.
func (c *InspectorColumn) showsPicture(card inspector.Card) bool {
	return c.graphics && card.Image != "" && card.ImageRows > 0
}

// transmitCmd uploads the active tab's picture to the terminal once per fetch.
func (c *InspectorColumn) transmitCmd() tea.Cmd {
	tab := c.activeTab()
	if tab == nil {
		return nil
	}
	card := c.cards[tab.ID]
	if !c.showsPicture(card) {
		return nil
	}
	key := card.Image + "|" + card.FetchedAt.String()
	if c.sent[tab.ID] == key {
		return nil
	}
	c.sent[tab.ID] = key
	id, tmux, file := c.imageID(tab.ID), c.inTmux, c.kittyFile
	return func() tea.Msg {
		var seq string
		if file {
			seq = kittyimg.TransmitFile(id, card.Image, card.ImageCols, card.ImageRows, tmux)
		} else {
			png, err := os.ReadFile(card.Image)
			if err != nil {
				return inspectorActionMsg{status: "picture: " + err.Error()}
			}
			seq = kittyimg.Transmit(id, png, card.ImageCols, card.ImageRows, tmux)
		}
		if err := inspectorWriteRaw(seq); err != nil {
			return inspectorActionMsg{status: "picture: " + err.Error()}
		}
		return nil
	}
}

// browseActive opens the active URL in carbonyl (a Chromium in the terminal)
// in a full-window tmux popup, when carbonyl is installed.
func (c *InspectorColumn) browseActive() tea.Cmd {
	tab := c.activeTab()
	if tab == nil || tab.Kind != inspector.KindURL {
		return nil
	}
	bin, err := inspectorLookPath("carbonyl")
	if err != nil {
		c.status = "install carbonyl for an interactive browser (npm i -g carbonyl)"
		return nil
	}
	shellCmd := shellQuote(bin) + " " + shellQuote(tab.Target)
	return func() tea.Msg {
		if err := inspectorPopup(shellCmd); err != nil {
			return inspectorActionMsg{status: "carbonyl: " + err.Error()}
		}
		return nil
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---- state ------------------------------------------------------------------

func (c *InspectorColumn) activeTab() *store.InspectorTab {
	if len(c.tabs) == 0 {
		return nil
	}
	for i := range c.tabs {
		if c.tabs[i].ID == c.active {
			return &c.tabs[i]
		}
	}
	return &c.tabs[0]
}

func (c *InspectorColumn) activeIndex() int {
	if t := c.activeTab(); t != nil {
		for i := range c.tabs {
			if c.tabs[i].ID == t.ID {
				return i
			}
		}
	}
	return 0
}

// selectTab makes tab idx the active one (persisted) and fetches it if needed.
func (c *InspectorColumn) selectTab(idx int) tea.Cmd {
	if idx < 0 || idx >= len(c.tabs) {
		return nil
	}
	tab := c.tabs[idx]
	changed := tab.ID != c.activeTab().ID
	c.active = tab.ID
	if changed {
		c.vp.GotoTop()
		c.imgOff = 0
	}
	pid, ws := c.projectID, c.wsBead
	persist := c.mutate(func(ctx context.Context, st *store.Store) error {
		return st.SetActiveTab(ctx, pid, ws, tab.ID)
	})
	return tea.Batch(persist, c.refreshActive(false))
}

func (c *InspectorColumn) applyLoaded(m inspectorLoadedMsg) {
	if m.status != "" {
		c.status = m.status
		return
	}
	c.tabs = m.tabs
	c.active = m.active
	c.noteEmpty(len(m.tabs) == 0)
	c.applyDecisions(m.decisions)
	if c.hover >= len(c.tabs) {
		c.hover = -1
	}
	// Seed the active tab's card from the store cache so a window switch paints
	// instantly, before any fetch.
	if tab := c.activeTab(); tab != nil && m.cacheJSON != "" {
		if _, have := c.cards[tab.ID]; !have {
			if card, err := inspector.Unmarshal(m.cacheJSON); err == nil {
				if card.FetchedAt.IsZero() {
					card.FetchedAt = m.cachedAt
				}
				c.cards[tab.ID] = card
			}
		}
	}
	live := map[int64]bool{}
	for _, t := range c.tabs {
		live[t.ID] = true
	}
	for id := range c.cards {
		if !live[id] {
			delete(c.cards, id)
		}
	}
}

// ---- Bubble Tea ---------------------------------------------------------------

func (c *InspectorColumn) Init() tea.Cmd {
	return tea.Batch(c.loadCmd(), columnTick())
}

func (c *InspectorColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = innerSize(m.Width, m.Height)
		// A resize often means a (re)attached terminal, which has none of our
		// pictures: upload again, and reshoot if the width changed.
		clear(c.sent)
		return c, c.refreshActive(false)

	case tea.FocusMsg:
		c.focused = true
		return c, c.loadCmd()
	case tea.BlurMsg:
		c.focused = false
		c.hover, c.addHover = -1, false
		return c, nil

	case columnTickMsg:
		return c, tea.Batch(columnTick(), c.loadCmd())

	case hoverExpireMsg:
		if m.seq == c.hoverSeq {
			c.hover, c.addHover = -1, false
		}
		return c, nil

	case inspectorLoadedMsg:
		c.applyLoaded(m)
		return c, c.refreshActive(false)

	case inspectorCardMsg:
		delete(c.loading, m.tabID)
		// A failed refresh keeps the last good card (plus the error line) rather
		// than flashing to a bare link card. FetchedAt moves forward so the retry
		// waits a full refresh interval.
		if prev, ok := c.cards[m.tabID]; ok && m.card.Err != "" && prev.Err == "" && prev.Renderer != inspector.RendererLink {
			prev.Err, prev.FetchedAt = m.card.Err, m.card.FetchedAt
			m.card = prev
		}
		c.cards[m.tabID] = m.card
		return c, c.transmitCmd()

	case inspectorActionMsg:
		c.status = m.status
		return c, nil

	case decisionSentMsg:
		return c, c.decisionSent(m)

	case tea.MouseMsg:
		return c.updateMouse(m)

	case tea.KeyMsg:
		if c.prompt != "" {
			return c.updatePrompt(m)
		}
		return c.updateKey(m)
	}
	return c, nil
}

func (c *InspectorColumn) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c.status = ""
	key := k.String()
	if cmd, handled := c.decisionKey(key); handled {
		return c, cmd
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return c, c.selectTab(int(key[0] - '1'))
	}
	n := len(c.tabs)
	switch key {
	case "ctrl+c":
		return c, tea.Quit
	case "]", "tab":
		if n > 0 {
			return c, c.selectTab((c.activeIndex() + 1) % n)
		}
	case "[", "shift+tab":
		if n > 0 {
			return c, c.selectTab((c.activeIndex() + n - 1) % n)
		}
	case "?":
		c.showHelp = !c.showHelp
	case "r":
		c.status = "refreshing…"
		return c, c.refreshActive(true)
	case "o":
		return c, c.openActive()
	case "y":
		return c, c.copyActive()
	case "a":
		c.startAdd()
	case "x":
		if c.activeTab() != nil {
			c.prompt = "close"
		}
	case "J":
		return c, c.moveActive(+1)
	case "K":
		return c, c.moveActive(-1)
	case "B":
		return c, c.browseActive()
	case "up", "k":
		c.scroll(-1)
	case "down", "j":
		c.scroll(1)
	case "pgup", "ctrl+u":
		c.scroll(-max(1, c.vp.Height/2))
	case "pgdown", "ctrl+d", " ":
		c.scroll(max(1, c.vp.Height/2))
	case "g", "home":
		c.scroll(-1 << 20)
	case "G", "end":
		c.scroll(1 << 20)
	}
	return c, nil
}

// scroll moves the body by delta rows: the picture's crop window when the tab
// is a picture (clamped in the view), else the text viewport.
func (c *InspectorColumn) scroll(delta int) {
	if tab := c.activeTab(); tab != nil && c.showsPicture(c.cards[tab.ID]) {
		c.imgOff = max(0, min(c.imgOff+delta, c.cards[tab.ID].ImageRows-max(1, c.vp.Height)))
		return
	}
	switch {
	case delta <= -1<<20:
		c.vp.GotoTop()
	case delta >= 1<<20:
		c.vp.GotoBottom()
	case delta < 0:
		c.vp.ScrollUp(-delta)
	default:
		c.vp.ScrollDown(delta)
	}
}

func (c *InspectorColumn) startAdd() {
	c.prompt = "add"
	c.input.SetValue("")
	c.input.Width = max(10, c.width-6)
	c.input.Focus()
}

func (c *InspectorColumn) updatePrompt(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "esc" || k.String() == "ctrl+c" {
		c.prompt = ""
		return c, nil
	}
	if c.prompt == "note" {
		return c, c.noteKey(k)
	}
	if c.prompt == "close" {
		switch k.String() {
		case "y", "enter":
			c.prompt = ""
			if tab := c.activeTab(); tab != nil {
				id := tab.ID
				return c, c.mutate(func(ctx context.Context, st *store.Store) error { return st.RemoveTab(ctx, id) })
			}
		case "n":
			c.prompt = ""
		}
		return c, nil
	}
	if k.String() == "enter" {
		raw := c.input.Value()
		c.prompt = ""
		target, ok := normalizeInspectorURL(raw)
		if !ok {
			if strings.TrimSpace(raw) != "" {
				c.status = "not a URL: " + strings.TrimSpace(raw)
			}
			return c, nil
		}
		pid, ws := c.projectID, c.wsBead
		return c, c.mutate(func(ctx context.Context, st *store.Store) error {
			id, _, err := st.AddTab(ctx, store.InspectorTab{
				ProjectID: pid, WorkstreamBeadID: ws, Kind: inspector.KindURL, Target: target,
				Title: inspector.DefaultTitle(inspector.KindURL, target), AddedBy: "user",
			})
			if err != nil {
				return err
			}
			return st.SetActiveTab(ctx, pid, ws, id)
		})
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	return c, cmd
}

// normalizeInspectorURL accepts "https://…", "http://…", or a bare host
// ("localhost:5173", "example.com/x") and returns a full URL.
func normalizeInspectorURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t") {
		return "", false
	}
	if strings.Contains(s, "://") {
		return s, true
	}
	if strings.HasPrefix(s, "localhost") || strings.HasPrefix(s, "127.0.0.1") {
		return "http://" + s, true
	}
	if strings.Contains(s, ".") {
		return "https://" + s, true
	}
	return "", false
}

func (c *InspectorColumn) moveActive(delta int) tea.Cmd {
	tab := c.activeTab()
	if tab == nil {
		return nil
	}
	id, to := tab.ID, tab.Position+delta
	if to < 1 || to > len(c.tabs) {
		return nil
	}
	return c.mutate(func(ctx context.Context, st *store.Store) error { return st.MoveTab(ctx, id, to) })
}

func (c *InspectorColumn) activeURL() string {
	tab := c.activeTab()
	if tab == nil {
		return ""
	}
	return tab.Target
}

func (c *InspectorColumn) openActive() tea.Cmd {
	tab := c.activeTab()
	if tab == nil {
		return nil
	}
	if tab.Kind != inspector.KindURL {
		c.status = "a command tab has no URL to open"
		return nil
	}
	url := tab.Target
	c.status = "opened in browser"
	return func() tea.Msg {
		if err := inspectorOpenURL(url); err != nil {
			return inspectorActionMsg{status: "open failed: " + err.Error()}
		}
		return nil
	}
}

func (c *InspectorColumn) copyActive() tea.Cmd {
	text := c.activeURL()
	if text == "" {
		return nil
	}
	c.status = "copied " + midTruncate(text, max(10, c.width-12))
	return func() tea.Msg {
		if err := inspectorCopy(text); err != nil {
			return inspectorActionMsg{status: "copy failed: " + err.Error()}
		}
		return nil
	}
}

func (c *InspectorColumn) updateMouse(m tea.MouseMsg) (tea.Model, tea.Cmd) {
	m.X, m.Y = m.X-panelPadX, m.Y-panelPadY
	if cmd, handled := c.decisionMouse(m); handled {
		return c, cmd
	}
	switch m.Button {
	case tea.MouseButtonWheelUp:
		c.scroll(-3)
		return c, nil
	case tea.MouseButtonWheelDown:
		c.scroll(3)
		return c, nil
	}
	if m.Y != inspectorStripRow {
		c.hover, c.addHover = -1, false
		return c, nil
	}
	idx, add := -1, false
	for _, s := range c.segs {
		if m.X >= s.x0 && m.X < s.x1 {
			if s.idx < 0 {
				add = s.idx == segAdd
			} else {
				idx = s.idx
			}
		}
	}
	c.hover, c.addHover = idx, add
	if idx < 0 && !add {
		return c, nil
	}
	c.hoverSeq++
	expire := hoverExpire(c.hoverSeq)
	if m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft {
		if add {
			c.startAdd()
			return c, expire
		}
		return c, tea.Batch(c.selectTab(idx), expire)
	}
	return c, expire
}

// ---- rendering ------------------------------------------------------------------

const (
	inspectorStripRow = 1 // row 0 is the section label
	segAdd            = -1
	segLeft           = -2 // "‹+k" overflow marker
	segRight          = -3 // "+k›" overflow marker
)

// tabSeg is one clickable piece of the tab strip.
type tabSeg struct {
	idx    int // tab index, or segAdd / segLeft / segRight
	x0, x1 int // [x0, x1) columns in the pane
	text   string
}

func iconMode() string {
	if os.Getenv("CMDR_ICONS") == "ascii" {
		return "ascii"
	}
	return "unicode"
}

// tabIcon is the kind glyph of a tab: PR / issue / doc / dashboard / local /
// cmd / web (spec 2.7). CMDR_ICONS=nerd uses the unicode set too.
func tabIcon(tab store.InspectorTab) string {
	if tab.Kind == inspector.KindDecision {
		return "⚑"
	}
	kind := "web"
	switch r := firstNonEmptyStr(tab.Renderer, inspector.Detect(tab.Kind, tab.Target)); r {
	case inspector.RendererGitHubPR:
		kind = "pr"
	case inspector.RendererLinear:
		kind = "issue"
	case inspector.RendererNotion:
		kind = "doc"
	case inspector.RendererCmd:
		kind = "cmd"
	case inspector.RendererLink, inspector.RendererWeb:
		t := strings.ToLower(tab.Target)
		switch {
		case strings.Contains(t, "datadog") || strings.Contains(t, "grafana"):
			kind = "dashboard"
		case strings.Contains(t, "localhost") || strings.Contains(t, "127.0.0.1"):
			kind = "local"
		}
	}
	unicode := map[string]string{"pr": "⎇", "issue": "◇", "doc": "▤", "dashboard": "◷", "local": "◎", "cmd": "$", "web": "◌"}
	ascii := map[string]string{"pr": "PR", "issue": "IS", "doc": "DOC", "dashboard": "DB", "local": "LH", "cmd": "$", "web": "WWW"}
	if iconMode() == "ascii" {
		return ascii[kind]
	}
	return unicode[kind]
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// stripLayout lays out the tab strip: full labels, then shorter labels, then
// icons only, then a window of tabs around the active one with "‹+k" / "+k›".
// It returns the segments (with their columns) and the active tab's columns.
func (c *InspectorColumn) stripLayout() (segs []tabSeg, activeX0, activeX1 int) {
	n := len(c.tabs)
	if n == 0 {
		return nil, 0, 0
	}
	avail := c.width - 1
	active := c.activeIndex()

	parts := func(i int, labelMax int) (plain string, styled string) {
		tab := c.tabs[i]
		num := fmt.Sprint(i + 1)
		icon := tabIcon(tab)
		label := ""
		if labelMax > 0 {
			label = " " + inspector.TrimTitle(tab.Title, labelMax)
		}
		attn := ""
		if card, ok := c.cards[tab.ID]; ok && card.Attn {
			attn = "•"
		}
		plain = num + " " + icon + label + attn
		numStyle := lipgloss.NewStyle().Foreground(widgets.T.TextMuted)
		body := lipgloss.NewStyle().Foreground(widgets.T.TextSecondary)
		if i == active {
			numStyle = lipgloss.NewStyle().Bold(true).Foreground(widgets.T.Accent)
			body = lipgloss.NewStyle().Bold(true).Foreground(widgets.T.TextPrimary)
		}
		if i == c.hover && i != active {
			numStyle = numStyle.Background(widgets.T.BgHover)
			body = body.Background(widgets.T.BgHover)
		}
		styled = numStyle.Render(num) + body.Render(" "+icon+label)
		if attn != "" {
			styled += lipgloss.NewStyle().Foreground(widgets.T.Attn).Render(attn)
		}
		return plain, styled
	}
	addStyle := lipgloss.NewStyle().Foreground(widgets.T.TextMuted)
	if c.addHover {
		addStyle = lipgloss.NewStyle().Bold(true).Foreground(widgets.T.Accent).Background(widgets.T.BgHover)
	}

	build := func(from, to, labelMax, gap int, leftMore, rightMore int) ([]tabSeg, int, int, int) {
		x := 1
		var out []tabSeg
		ax0, ax1 := 0, 0
		if leftMore > 0 {
			t := fmt.Sprintf("‹+%d", leftMore)
			out = append(out, tabSeg{idx: segLeft, x0: x, x1: x + ansi.StringWidth(t), text: addStyle.Render(t)})
			x += ansi.StringWidth(t) + gap
		}
		for i := from; i < to; i++ {
			plain, styled := parts(i, labelMax)
			w := ansi.StringWidth(plain)
			out = append(out, tabSeg{idx: i, x0: x, x1: x + w, text: styled})
			if i == active {
				ax0, ax1 = x, x+w
			}
			x += w + gap
		}
		if rightMore > 0 {
			t := fmt.Sprintf("+%d›", rightMore)
			out = append(out, tabSeg{idx: segRight, x0: x, x1: x + ansi.StringWidth(t), text: addStyle.Render(t)})
			x += ansi.StringWidth(t) + gap
		}
		out = append(out, tabSeg{idx: segAdd, x0: x, x1: x + 1, text: addStyle.Render("+")})
		return out, ax0, ax1, x + 1
	}

	// Labels shrink before they disappear; below 50 columns they are gone.
	modes := []struct{ label, gap int }{{16, 3}, {9, 2}, {0, 2}}
	if c.width < 50 {
		modes = modes[2:]
	}
	for _, md := range modes {
		out, a0, a1, total := build(0, n, md.label, md.gap, 0, 0)
		if total <= avail+1 {
			return out, a0, a1
		}
	}
	// Icons only is still too wide: slide a window of tabs around the active one.
	for from := 0; from <= active; from++ {
		for to := n; to > active; to-- {
			left, right := from, n-to
			out, a0, a1, total := build(from, to, 0, 2, left, right)
			if total <= avail+1 {
				return out, a0, a1
			}
		}
	}
	out, a0, a1, _ := build(active, active+1, 0, 2, active, n-active-1)
	return out, a0, a1
}

func (c *InspectorColumn) renderStrip() (strip, underline string) {
	segs, ax0, ax1 := c.stripLayout()
	c.segs = segs
	var b strings.Builder
	x := 0
	for _, s := range segs {
		b.WriteString(strings.Repeat(" ", max(0, s.x0-x)))
		b.WriteString(s.text)
		x = s.x1
	}
	rule := lipgloss.NewStyle().Foreground(widgets.T.Rule)
	accent := lipgloss.NewStyle().Foreground(widgets.T.Accent)
	if ax1 <= ax0 {
		return b.String(), rule.Render(strings.Repeat("─", max(0, c.width)))
	}
	ax1 = min(ax1, c.width)
	underline = rule.Render(strings.Repeat("─", ax0)) + accent.Render(strings.Repeat("━", ax1-ax0)) +
		rule.Render(strings.Repeat("─", max(0, c.width-ax1)))
	return b.String(), underline
}

// midTruncate shortens s to w cells with an ellipsis in the middle.
func midTruncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w <= 3 {
		return ansi.Truncate(s, w, "")
	}
	keepRight := (w - 1) / 2
	keepLeft := w - 1 - keepRight
	return ansi.Truncate(s, keepLeft, "") + "…" + ansi.TruncateLeft(s, ansi.StringWidth(s)-keepRight, "")
}

func stateStyle(state string) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	switch state {
	case "OPEN":
		return s.Foreground(widgets.T.Ok)
	case "MERGED":
		return s.Foreground(widgets.T.Run)
	case "FAILED":
		return s.Foreground(widgets.T.Err)
	case "":
		return s.Foreground(widgets.T.TextSecondary)
	}
	return s.Foreground(widgets.T.TextMuted) // DONE, CLOSED, ...
}

// cardFor is the card to draw for a tab: the fetched one, or a placeholder.
func (c *InspectorColumn) cardFor(tab store.InspectorTab) inspector.Card {
	if card, ok := c.cards[tab.ID]; ok {
		return card
	}
	card := inspector.LinkCard(inspector.Target{Kind: tab.Kind, Target: tab.Target, Title: tab.Title})
	card.Markdown = inspector.FetchingLabel(inspector.Detect(tab.Kind, tab.Target), nil)
	return card
}

func (c *InspectorColumn) renderHeader(tab store.InspectorTab, card inspector.Card) []string {
	w := c.width
	icon := lipgloss.NewStyle().Foreground(widgets.T.TextSecondary).Render(tabIcon(tab))
	state := ""
	if card.State != "" {
		state = stateStyle(card.State).Render(card.State)
	}
	titleW := w - 2 - ansi.StringWidth(card.State) - 2
	title := lipgloss.NewStyle().Bold(true).Foreground(widgets.T.TextPrimary).Render(ansi.Truncate(card.Title, max(1, titleW), "…"))
	line1 := icon + " " + title
	if state != "" {
		line1 += "  " + state
	}

	updated := ""
	if !card.UpdatedAt.IsZero() {
		updated = "updated " + inspector.Ago(card.UpdatedAt, c.now())
	} else if (card.Renderer == inspector.RendererCmd || card.Image != "") && !card.FetchedAt.IsZero() {
		updated = "fetched " + inspector.Ago(card.FetchedAt, c.now())
	}
	line2 := ""
	if card.URL != "" {
		u := strings.TrimPrefix(strings.TrimPrefix(card.URL, "https://"), "http://")
		room := w
		if updated != "" {
			room = w - ansi.StringWidth(updated) - 2
		}
		if room >= 12 {
			line2 = widgets.FaintText.Render(midTruncate(u, room))
			if updated != "" {
				line2 += "  " + widgets.FaintText.Render(updated)
			}
		} else {
			line2 = widgets.FaintText.Render(midTruncate(u, w))
		}
	} else if updated != "" {
		line2 = widgets.FaintText.Render(updated)
	}

	lines := []string{line1}
	if line2 != "" {
		lines = append(lines, line2)
	}
	if len(card.Meta) > 0 {
		meta := ansi.Truncate(strings.Join(card.Meta, " · "), w, "…")
		lines = append(lines, lipgloss.NewStyle().Foreground(widgets.T.TextSecondary).Render(meta))
	}
	if card.Err != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(widgets.T.Err).Render(ansi.Truncate("✗ "+card.Err, w, "…")))
	}
	return lines
}

func glyphStyle(line string) lipgloss.Style {
	base := lipgloss.NewStyle().Foreground(widgets.T.TextSecondary)
	switch {
	case strings.HasPrefix(line, "✓"):
		return base.Foreground(widgets.T.Ok)
	case strings.HasPrefix(line, "✗"):
		return base.Foreground(widgets.T.Err)
	case strings.HasPrefix(line, "⚑"):
		return base.Foreground(widgets.T.Attn)
	case strings.HasPrefix(line, "○"):
		return base.Foreground(widgets.T.TextMuted)
	}
	return base
}

// bodyContent is what scrolls under the card header: sections, then the body.
func bodyContent(card inspector.Card, w int) string {
	var out []string
	for _, s := range card.Sections {
		head := lipgloss.NewStyle().Bold(true).Foreground(widgets.T.TextMuted).Render(s.Title)
		line := head
		for i, l := range s.Lines {
			if i == 0 {
				line += "  " + styleSectionLine(l)
				out = append(out, ansi.Truncate(line, w, "…"))
				continue
			}
			out = append(out, ansi.Truncate(strings.Repeat(" ", ansi.StringWidth(s.Title)+2)+styleSectionLine(l), w, "…"))
		}
		if len(s.Lines) == 0 {
			out = append(out, head)
		}
	}
	if len(out) > 0 && card.Markdown != "" {
		out = append(out, "")
	}
	if card.Markdown != "" {
		if card.Preformatted {
			for _, l := range strings.Split(card.Markdown, "\n") {
				out = append(out, ansi.Truncate(l, w, ""))
			}
		} else {
			out = append(out, renderMarkdownLite(card.Markdown, w)...)
		}
	}
	return strings.Join(out, "\n")
}

// styleSectionLine colors the glyph-led pieces of a section line ("✓ 11  ✗ 1").
func styleSectionLine(l string) string {
	pieces := strings.Split(l, "  ")
	for i, p := range pieces {
		pieces[i] = glyphStyle(p).Render(p)
	}
	return strings.Join(pieces, "  ")
}

// renderMarkdownLite styles the few markdown shapes PR bodies and specs use:
// headings, bullets, fenced code. It wraps to w and never brings its own colors
// (no glamour: its themes would fight the role palette).
func renderMarkdownLite(md string, w int) []string {
	var out []string
	inCode := false
	text := lipgloss.NewStyle().Foreground(widgets.T.TextSecondary)
	code := lipgloss.NewStyle().Foreground(widgets.T.TextMuted)
	head := lipgloss.NewStyle().Bold(true).Foreground(widgets.T.TextPrimary)
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r", ""), "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			inCode = !inCode
		case inCode:
			out = append(out, code.Render(ansi.Truncate("  "+line, w, "")))
		case strings.HasPrefix(line, "#"):
			out = append(out, "", head.Render(ansi.Truncate(strings.TrimSpace(strings.TrimLeft(line, "#")), w, "…")))
		case strings.HasPrefix(strings.TrimSpace(line), "- ") || strings.HasPrefix(strings.TrimSpace(line), "* "):
			item := strings.TrimSpace(line)[2:]
			wrapped := strings.Split(ansi.Wrap(inlineMarkdown(item, text), max(1, w-2), ""), "\n")
			for i, wl := range wrapped {
				prefix := "  "
				if i == 0 {
					prefix = "• "
				}
				out = append(out, text.Render(prefix)+wl)
			}
		case line == "---" || line == "***":
			out = append(out, code.Render(strings.Repeat("─", max(1, min(w, 40)))))
		case line == "":
			out = append(out, "")
		default:
			for _, wl := range strings.Split(ansi.Wrap(inlineMarkdown(line, text), max(1, w), ""), "\n") {
				out = append(out, wl)
			}
		}
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	return out
}

var inlineMDRe = regexp.MustCompile("\\*\\*([^*]+)\\*\\*|`([^`]+)`|\\[([^\\]]+)\\]\\(([^)]*)\\)")

// inlineMarkdown styles **bold**, `code` and [label](url) (the label is kept,
// the url dropped) so cards do not show raw markers. Everything else renders in
// base.
func inlineMarkdown(s string, base lipgloss.Style) string {
	bold := base.Bold(true).Foreground(widgets.T.TextPrimary)
	code := base.Foreground(widgets.T.Accent)
	link := base.Foreground(widgets.T.Run).Underline(true)
	var b strings.Builder
	last := 0
	for _, m := range inlineMDRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(base.Render(s[last:m[0]]))
		switch {
		case m[2] >= 0:
			b.WriteString(bold.Render(s[m[2]:m[3]]))
		case m[4] >= 0:
			b.WriteString(code.Render(s[m[4]:m[5]]))
		default:
			b.WriteString(link.Render(s[m[6]:m[7]]))
		}
		last = m[1]
	}
	b.WriteString(base.Render(s[last:]))
	return b.String()
}

func (c *InspectorColumn) renderEmpty(rows int) []string {
	msgs := []string{
		"No tabs yet.",
		"a  add a URL",
		"Managers: cmdr tab add --url <url>",
	}
	var lines []string
	for _, m := range msgs {
		for _, wl := range strings.Split(ansi.Wrap(m, max(1, c.width-2), ""), "\n") {
			pad := max(0, (c.width-ansi.StringWidth(wl))/2)
			lines = append(lines, strings.Repeat(" ", pad)+widgets.FaintText.Render(wl))
		}
	}
	top := max(1, (rows-len(lines))/2)
	out := make([]string, 0, rows)
	out = append(out, make([]string, min(top, rows))...)
	out = append(out, lines...)
	return out
}

func (c *InspectorColumn) footer() []string {
	var out []string
	switch c.prompt {
	case "add":
		out = append(out, lipgloss.NewStyle().Foreground(widgets.T.Accent).Render("add ›")+" "+c.input.View())
	case "note":
		out = append(out, lipgloss.NewStyle().Foreground(widgets.T.Accent).Render("note ›")+" "+c.input.View())
	case "close":
		if tab := c.activeTab(); tab != nil {
			out = append(out, lipgloss.NewStyle().Foreground(widgets.T.Attn).Render(
				ansi.Truncate(fmt.Sprintf("Close %q?  y yes  n no", tab.Title), c.width, "…")))
		}
	}
	if c.status != "" && c.prompt == "" {
		out = append(out, widgets.FaintText.Render(ansi.Truncate(c.status, c.width, "…")))
	}
	if c.focused && c.prompt == "" {
		out = append(out, c.hints()...)
	}
	return out
}

func (c *InspectorColumn) hints() []string {
	if tab, st := c.activeDecision(); tab != nil && st.rec.Present && st.sent == "" {
		return decisionHints(c.width)
	}
	short := [][2]string{{"1-9", "tab"}, {"[ ]", "cycle"}, {"o", "open"}, {"y", "copy url"}, {"r", "refresh"}, {"x", "close"}, {"?", "keys"}}
	if !c.showHelp {
		return []string{widgets.HintLine(c.width, short...)}
	}
	full := [][2]string{{"1-9", "tab"}, {"[ ]", "cycle"}, {"o", "open"}, {"y", "copy url"}, {"r", "refresh"}, {"x", "close"},
		{"a", "add url"}, {"J/K", "reorder"}, {"↑↓", "scroll"}, {"B", "browse"}, {"?", "less"}}
	return widgets.HintLines(c.width, full...)
}

func (c *InspectorColumn) View() string {
	return c.frame0.keep(insetView(c.innerView()))
}

func (c *InspectorColumn) innerView() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}
	count := ""
	if n := len(c.tabs); n > 0 {
		count = fmt.Sprintf("%d tabs", n)
		if n == 1 {
			count = "1 tab"
		}
	}
	lines := []string{widgets.SectionLabel("INSPECTOR", c.focused, count, c.width)}
	footer := c.footer()

	if len(c.tabs) == 0 {
		strip, under := c.renderStrip() // just the "+"
		lines = append(lines, strip, under)
		body := c.renderEmpty(max(0, c.height-len(lines)-len(footer)))
		lines = append(lines, body...)
	} else {
		strip, under := c.renderStrip()
		lines = append(lines, strip, under)
		tab := c.activeTab()
		if tab.Kind == inspector.KindDecision {
			lines = append(lines, c.renderDecision(*tab, len(lines))...)
			return c.finishView(lines, footer)
		}
		card := c.cardFor(*tab)
		lines = append(lines, c.renderHeader(*tab, card)...)
		lines = append(lines, "")
		bodyH := max(1, c.height-len(lines)-len(footer))
		if c.showsPicture(card) {
			c.vp.Height = bodyH
			lines = append(lines, c.pictureLines(tab.ID, card, bodyH)...)
			return c.finishView(lines, footer)
		}
		key := fmt.Sprintf("%d|%d|%d|%s|%d", tab.ID, card.FetchedAt.UnixNano(), c.width, card.Markdown, len(card.Sections))
		c.vp.Width, c.vp.Height = c.width, bodyH
		if key != c.contentFor {
			c.contentFor = key
			c.vp.SetContent(bodyContent(card, c.width))
		}
		lines = append(lines, strings.Split(c.vp.View(), "\n")...)
	}
	return c.finishView(lines, footer)
}

// pictureLines is the body of a picture tab: bodyH rows of placeholder cells
// cropped from the tall screenshot at imgOff.
func (c *InspectorColumn) pictureLines(tabID int64, card inspector.Card, bodyH int) []string {
	rows := min(bodyH, card.ImageRows)
	c.imgOff = max(0, min(c.imgOff, card.ImageRows-rows))
	return kittyimg.Placeholders(c.imageID(tabID), c.imgOff, rows, min(card.ImageCols, c.width))
}

func (c *InspectorColumn) finishView(lines, footer []string) string {
	// Pin the footer to the bottom, and never exceed the pane height.
	if len(footer) >= c.height {
		footer = footer[len(footer)-(c.height-1):]
	}
	for len(lines)+len(footer) < c.height {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	if len(lines) > c.height {
		lines = append(lines[:c.height-len(footer)], footer...)
		lines = lines[len(lines)-c.height:]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Truncate(l, c.width, ""), " ")
	}
	return strings.Join(lines, "\n")
}

// noteEmpty tells tmux when this window's inspector becomes empty (or stops
// being), so the layout can shrink it and give the chat the room.
func (c *InspectorColumn) noteEmpty(empty bool) {
	if c.emptyKnown && c.wasEmpty == empty {
		return
	}
	c.emptyKnown, c.wasEmpty = true, empty
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		return
	}
	go func() {
		args := []string{"set-option", "-w", "-t", pane, "@cmdr_inspector_empty", "1"}
		if !empty {
			args = []string{"set-option", "-w", "-u", "-t", pane, "@cmdr_inspector_empty"}
		}
		_ = exec.Command("tmux", args...).Run()
		if exe, err := selfExe(); err == nil {
			_ = exec.Command(exe, "__relayout").Run()
		}
	}()
}
