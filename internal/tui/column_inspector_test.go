package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/store"
)

// drive feeds msg to the column, then runs the returned commands (flattening
// batches) and feeds their results back. Commands that block (ticks, hover
// expiry) are abandoned after a short wait.
func drive(t *testing.T, c *InspectorColumn, msg tea.Msg) {
	t.Helper()
	queue := []tea.Msg{msg}
	for steps := 0; len(queue) > 0 && steps < 50; steps++ {
		m := queue[0]
		queue = queue[1:]
		if m == nil {
			continue
		}
		if b, ok := m.(tea.BatchMsg); ok {
			for _, cmd := range b {
				queue = append(queue, runCmd(cmd))
			}
			continue
		}
		_, cmd := c.Update(m)
		queue = append(queue, runCmd(cmd))
	}
}

func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case m := <-done:
		return m
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(t *testing.T, c *InspectorColumn, s string) {
	t.Helper()
	for _, r := range s {
		c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) // cursor-blink commands would only stall the test
	}
}

func newInspectorForTest(t *testing.T, w, h int) (*InspectorColumn, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := NewInspectorColumn(&Services{Store: st}, "", "")
	c.fetcher = inspector.Fetcher{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			return []byte(`{"title":"ship worker","state":"OPEN","headRefName":"feat/x","baseRefName":"main","additions":4,"deletions":1,"changedFiles":2,"reviewDecision":"REVIEW_REQUIRED","body":"hello body","statusCheckRollup":[{"name":"ci","conclusion":"SUCCESS"}],"reviews":[]}`), nil
		}
		return []byte("cmd output"), nil
	}}
	drive(t, c, tea.WindowSizeMsg{Width: w, Height: h})
	return c, st
}

func addTabs(t *testing.T, st *store.Store, urls ...string) {
	t.Helper()
	for _, u := range urls {
		if _, _, err := st.AddTab(context.Background(), store.InspectorTab{
			Title: inspector.DefaultTitle(inspector.KindURL, u), Kind: inspector.KindURL, Target: u,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("view has %d lines, want %d:\n%s", len(lines), h, view)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got > w {
			t.Errorf("line %d is %d wide (> %d): %q", i, got, w, ansi.Strip(l))
		}
	}
}

func TestInspector_EmptyStateTeachesTabAdd(t *testing.T) {
	c, _ := newInspectorForTest(t, 60, 20)
	drive(t, c, c.loadCmd()())
	v := ansi.Strip(c.innerView())
	for _, want := range []string{"INSPECTOR", "No tabs yet.", "a  add a URL", "cmdr tab add --url <url>"} {
		if !strings.Contains(v, want) {
			t.Errorf("empty view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "1-9") {
		t.Error("hints must only show while focused")
	}
	assertFits(t, c.innerView(), 60, 18)
}

func TestInspector_TabStripCardAndHints(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://github.com/o/r/pull/4109", "https://linear.app/f/issue/TEAM-102/x", "https://example.com/spec")
	drive(t, c, c.loadCmd()())
	v := ansi.Strip(c.innerView())
	for _, want := range []string{"3 tabs", "1 ⎇ PR #4109", "2 ◇ TEAM-102", "3 ◌ spec", "PR #4109  ship worker", "OPEN", "github.com/o/r/pull/4109", "feat/x → main", "CHECKS", "hello body"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "1-9") {
		t.Error("unfocused column must not show hints")
	}
	drive(t, c, tea.FocusMsg{})
	v = ansi.Strip(c.innerView())
	if !strings.Contains(v, "1-9 tab  [ ] cycle  o open  y copy url  r refresh  x close") {
		t.Errorf("focused view missing hint footer:\n%s", v)
	}
	lines := strings.Split(v, "\n")
	if !strings.Contains(lines[len(lines)-1], "1-9 tab") {
		t.Errorf("hints should be the last line, got %q", lines[len(lines)-1])
	}
	assertFits(t, c.innerView(), 100, 22)
	drive(t, c, tea.BlurMsg{})
	if strings.Contains(ansi.Strip(c.innerView()), "1-9") {
		t.Error("hints should disappear on blur")
	}
}

func TestInspector_ActiveTabIsBoldPrimaryWithAccentUnderline(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })

	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://github.com/o/r/pull/1", "https://example.com/two")
	drive(t, c, c.loadCmd()())
	lines := strings.Split(c.innerView(), "\n")
	strip, under := lines[1], lines[2]
	// Mocha: TextPrimary #cdd6f4 = 205;214;243, Accent #cba6f7 = 203;166;247, TextSecondary #a6adc8 = 166;173;200.
	if !strings.Contains(strip, "1;38;2;205;214;243") {
		t.Errorf("active tab not TextPrimary: %q", strip)
	}
	if !strings.Contains(strip, "38;2;166;173;200") {
		t.Errorf("inactive tab not TextSecondary: %q", strip)
	}
	if !strings.Contains(under, "38;2;203;166;247") || !strings.Contains(under, "━") {
		t.Errorf("underline not Accent ━: %q", under)
	}
	if strings.Contains(c.innerView(), "\x1b[2m") || strings.Contains(c.innerView(), "\x1b[7m") {
		t.Error("faint or reverse video must never be used")
	}
	// The ━ run sits exactly under the active tab.
	runes := []rune(ansi.Strip(under))
	start, end := -1, -1
	for i, r := range runes {
		if r == '━' {
			if start < 0 {
				start = i
			}
			end = i
		}
	}
	segs, a0, a1 := c.stripLayout()
	_ = segs
	if start != a0 || end != a1-1 {
		t.Errorf("underline cols %d..%d, active tab cols %d..%d", start, end, a0, a1-1)
	}
}

func TestInspector_KeysSelectCycleAndPersist(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://example.com/a", "https://example.com/b", "https://example.com/c")
	drive(t, c, c.loadCmd()())
	ctx := context.Background()

	drive(t, c, key("2"))
	if tab := c.activeTab(); tab.Target != "https://example.com/b" {
		t.Fatalf("after 2: active = %s", tab.Target)
	}
	if id, _ := st.GetActiveTab(ctx, 0, ""); id != c.activeTab().ID {
		t.Errorf("active tab not persisted: %d", id)
	}
	drive(t, c, key("]"))
	if c.activeTab().Target != "https://example.com/c" {
		t.Errorf("] -> %s", c.activeTab().Target)
	}
	drive(t, c, key("]"))
	if c.activeTab().Target != "https://example.com/a" {
		t.Errorf("] should wrap to a, got %s", c.activeTab().Target)
	}
	drive(t, c, key("["))
	if c.activeTab().Target != "https://example.com/c" {
		t.Errorf("[ should wrap back to c, got %s", c.activeTab().Target)
	}
	drive(t, c, key("9")) // out of range: ignored
	if c.activeTab().Target != "https://example.com/c" {
		t.Error("9 with 3 tabs must not change the selection")
	}
}

func TestInspector_RestoresActiveTabAndCacheInstantly(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://example.com/a", "https://github.com/o/r/pull/9")
	ctx := context.Background()
	tabs, _ := st.ListTabs(ctx, 0, "")
	_ = st.SetActiveTab(ctx, 0, "", tabs[1].ID)
	card := inspector.Card{Renderer: inspector.RendererGitHubPR, Title: "PR #9  cached title", State: "MERGED", URL: tabs[1].Target, FetchedAt: time.Now()}
	js, _ := inspector.Marshal(card)
	_ = st.SaveTabCache(ctx, 0, "", js, card.FetchedAt)

	// A fresh process (new column) paints the cached card from the first load,
	// without a fetch: the fetcher would answer "ship worker".
	c2 := NewInspectorColumn(&Services{Store: st}, "", "")
	c2.fetcher = c.fetcher
	c2.width, c2.height = 100, 24
	msg := c2.loadCmd()()
	c2.applyLoaded(msg.(inspectorLoadedMsg))
	v := ansi.Strip(c2.innerView())
	if !strings.Contains(v, "cached title") || !strings.Contains(v, "MERGED") {
		t.Errorf("cache not shown:\n%s", v)
	}
	if cmd := c2.refreshActive(false); cmd != nil {
		t.Error("a fresh cache must not trigger a fetch")
	}
}

func TestInspector_FetchCachesCardAndRefreshForces(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://github.com/o/r/pull/4109")
	drive(t, c, c.loadCmd()())
	js, _, _ := st.GetTabCache(context.Background(), 0, "")
	if !strings.Contains(js, "ship worker") {
		t.Fatalf("fetched card not cached: %q", js)
	}
	calls := 0
	c.fetcher.Exec = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte(`{"title":"second","state":"OPEN"}`), nil
	}
	drive(t, c, key("r"))
	if calls != 1 || !strings.Contains(ansi.Strip(c.innerView()), "second") {
		t.Errorf("r should refetch once (calls=%d):\n%s", calls, ansi.Strip(c.innerView()))
	}
	// Not stale yet: a tick must not fetch.
	drive(t, c, columnTickMsg{})
	if calls != 1 {
		t.Errorf("fresh card refetched on tick (calls=%d)", calls)
	}
	// Stale: a tick refetches.
	c.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	drive(t, c, columnTickMsg{})
	if calls != 2 {
		t.Errorf("stale card not refetched on tick (calls=%d)", calls)
	}
}

func TestInspector_AddPromptCloseAndReorder(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	ctx := context.Background()
	drive(t, c, key("a"))
	if !strings.Contains(ansi.Strip(c.innerView()), "add ›") {
		t.Fatal("a should open the URL prompt")
	}
	typeText(t, c, "example.com/spec")
	drive(t, c, key("enter"))
	tabs, _ := st.ListTabs(ctx, 0, "")
	if len(tabs) != 1 || tabs[0].Target != "https://example.com/spec" || tabs[0].AddedBy != "user" {
		t.Fatalf("tabs after add = %+v", tabs)
	}
	if c.activeTab() == nil || c.activeTab().ID != tabs[0].ID {
		t.Error("a new tab should become active")
	}
	drive(t, c, key("a"))
	typeText(t, c, "not a url")
	drive(t, c, key("enter"))
	if tabs, _ := st.ListTabs(ctx, 0, ""); len(tabs) != 1 {
		t.Error("garbage must not become a tab")
	}
	addTabs(t, st, "https://example.com/two")
	drive(t, c, c.loadCmd()())
	drive(t, c, key("J")) // active (tab 1) moves down
	tabs, _ = st.ListTabs(ctx, 0, "")
	if tabs[1].Target != "https://example.com/spec" {
		t.Errorf("J did not reorder: %+v", tabs)
	}
	drive(t, c, key("x"))
	if !strings.Contains(ansi.Strip(c.innerView()), "Close") {
		t.Fatal("x should ask for confirmation")
	}
	drive(t, c, key("n"))
	if tabs, _ := st.ListTabs(ctx, 0, ""); len(tabs) != 2 {
		t.Error("n must keep the tab")
	}
	drive(t, c, key("x"))
	drive(t, c, key("y"))
	if tabs, _ := st.ListTabs(ctx, 0, ""); len(tabs) != 1 {
		t.Errorf("y should close the tab, have %d", len(tabs))
	}
}

func TestInspector_OpenAndCopyUseActiveURL(t *testing.T) {
	var opened, copied string
	oldOpen, oldCopy := inspectorOpenURL, inspectorCopy
	inspectorOpenURL = func(u string) error { opened = u; return nil }
	inspectorCopy = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { inspectorOpenURL, inspectorCopy = oldOpen, oldCopy })

	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://example.com/a")
	drive(t, c, c.loadCmd()())
	drive(t, c, key("o"))
	drive(t, c, key("y"))
	if opened != "https://example.com/a" || copied != "https://example.com/a" {
		t.Errorf("opened=%q copied=%q", opened, copied)
	}
}

func TestInspector_MouseClickAndHover(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://example.com/a", "https://example.com/b")
	drive(t, c, c.loadCmd()())
	_ = c.innerView() // lays out the strip
	var second tabSeg
	for _, s := range c.segs {
		if s.idx == 1 {
			second = s
		}
	}
	drive(t, c, tea.MouseMsg{X: second.x0 + 1 + panelPadX, Y: inspectorStripRow + panelPadY, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if c.hover != 1 {
		t.Errorf("hover = %d, want 1", c.hover)
	}
	drive(t, c, tea.MouseMsg{X: second.x0 + 1 + panelPadX, Y: inspectorStripRow + panelPadY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if c.activeTab().Target != "https://example.com/b" {
		t.Errorf("click did not select tab 2: %s", c.activeTab().Target)
	}
	drive(t, c, tea.MouseMsg{X: 5, Y: 10, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if c.hover != -1 {
		t.Error("hover should clear when the pointer leaves the strip")
	}
	// Clicking "+" opens the add prompt.
	for _, s := range c.segs {
		if s.idx == segAdd {
			drive(t, c, tea.MouseMsg{X: s.x0 + panelPadX, Y: inspectorStripRow + panelPadY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		}
	}
	if c.prompt != "add" {
		t.Error("clicking + should open the add prompt")
	}
}

func TestInspector_StripCollapsesThenOverflows(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	for i := 1; i <= 12; i++ {
		addTabs(t, st, fmt.Sprintf("https://example.com/page-number-%d", i))
	}
	drive(t, c, c.loadCmd()())

	// Wide: labels shown.
	if v := ansi.Strip(c.innerView()); !strings.Contains(v, "1 ◌ page-number-1") {
		// 100 cols cannot hold 12 labelled tabs; it must have collapsed already.
		if strings.Contains(strings.Split(v, "\n")[1], "page-number") {
			t.Errorf("labels should have collapsed:\n%s", strings.Split(v, "\n")[1])
		}
	}
	for _, w := range []int{100, 60, 40, 30} {
		drive(t, c, tea.WindowSizeMsg{Width: w, Height: 24})
		for idx := 0; idx < 12; idx++ {
			drive(t, c, key("]"))
			strip := ansi.Strip(strings.Split(c.innerView(), "\n")[1])
			if got := ansi.StringWidth(strip); got > w {
				t.Fatalf("w=%d strip too wide (%d): %q", w, got, strip)
			}
			want := fmt.Sprintf("%d ◌", c.activeIndex()+1)
			if !strings.Contains(strip, want) {
				t.Errorf("w=%d active tab %q not visible in strip %q", w, want, strip)
			}
		}
	}
	drive(t, c, tea.WindowSizeMsg{Width: 30, Height: 24})
	drive(t, c, key("1"))
	strip := ansi.Strip(strings.Split(c.innerView(), "\n")[1])
	if !strings.Contains(strip, "+") || !strings.Contains(strip, "›") {
		t.Errorf("narrow strip should show a +k› overflow marker: %q", strip)
	}
	assertFits(t, c.innerView(), 30, 22)
}

func TestInspector_CmdTabKeepsANSIAndHasNoURL(t *testing.T) {
	c, st := newInspectorForTest(t, 80, 20)
	c.fetcher.Exec = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("\x1b[32mgreen\x1b[0m line"), nil
	}
	if _, _, err := st.AddTab(context.Background(), store.InspectorTab{Title: "health", Kind: inspector.KindCmd, Target: "curl -s localhost:5173/health"}); err != nil {
		t.Fatal(err)
	}
	drive(t, c, c.loadCmd()())
	if v := c.innerView(); !strings.Contains(v, "\x1b[32mgreen") {
		t.Errorf("cmd output should keep its ANSI: %q", v)
	}
	drive(t, c, key("o"))
	if !strings.Contains(c.status, "no URL") {
		t.Errorf("o on a cmd tab: status = %q", c.status)
	}
}

func TestInspector_WorkstreamWindowIsolated(t *testing.T) {
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "example-app", Type: "code", Path: "/p", ActivatedAt: 1, Status: "active"})
	_, _, _ = st.AddTab(ctx, store.InspectorTab{ProjectID: pid, WorkstreamBeadID: "ws-1", Title: "mine", Target: "https://example.com/mine"})
	_, _, _ = st.AddTab(ctx, store.InspectorTab{Title: "root", Target: "https://example.com/root"})

	c := NewInspectorColumn(&Services{Store: st}, "example-app", "ws-1")
	c.width, c.height = 80, 20
	if !c.resolved {
		t.Fatal("window not resolved")
	}
	c.applyLoaded(c.loadCmd()().(inspectorLoadedMsg))
	v := ansi.Strip(c.innerView())
	if !strings.Contains(v, "mine") || strings.Contains(v, "root") {
		t.Errorf("workstream inspector must show only its own tabs:\n%s", v)
	}
}

func TestNormalizeInspectorURL(t *testing.T) {
	good := map[string]string{
		"https://a.com/x":      "https://a.com/x",
		"example.com/spec":     "https://example.com/spec",
		"localhost:5173":       "http://localhost:5173",
		"  http://x.io/y  ":    "http://x.io/y",
		"127.0.0.1:8080/ready": "http://127.0.0.1:8080/ready",
	}
	for in, want := range good {
		if got, ok := normalizeInspectorURL(in); !ok || got != want {
			t.Errorf("normalize(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "not a url", "word"} {
		if _, ok := normalizeInspectorURL(in); ok {
			t.Errorf("normalize(%q) should fail", in)
		}
	}
}

func TestMidTruncate(t *testing.T) {
	got := midTruncate("github.com/example-app/example-app/pull/4109", 20)
	if ansi.StringWidth(got) != 20 || !strings.Contains(got, "…") || !strings.HasPrefix(got, "github.c") || !strings.HasSuffix(got, "/4109") {
		t.Errorf("midTruncate = %q", got)
	}
	if midTruncate("short", 20) != "short" {
		t.Error("short strings are untouched")
	}
}

func TestInlineMarkdown_StylesMarkersAndDropsThem(t *testing.T) {
	got := ansi.Strip(strings.Join(renderMarkdownLite("- **Status:** Draft, see [the spec](http://x.io) and `cmdr tab`\n---", 80), "\n"))
	if strings.Contains(got, "**") || strings.Contains(got, "](") || strings.Contains(got, "`") {
		t.Errorf("raw markers survived: %q", got)
	}
	if !strings.Contains(got, "Status: Draft, see the spec and cmdr tab") {
		t.Errorf("text lost: %q", got)
	}
}
