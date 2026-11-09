package wsmgr

import (
	"context"
	"github.com/example-user/commander/internal/testguard"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
)

func TestMain(m *testing.M) {
	os.Setenv("COMMANDER_RUNTIME", "claude")
	os.Setenv("COMMANDER_CODEX_MANAGER_SHARE", "0") // no Manager goes to Codex unless a test asks
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

type fixture struct {
	m    *Manager
	st   *store.Store
	proj store.Project
	reg  *agentmail.MockRegistrar
	mr   *layout.MockRunner
	bc   *beads.MockClient
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("COMMANDER_HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	path := t.TempDir()
	id, err := st.InsertProject(context.Background(), store.Project{Name: "example-app", Type: "code", Path: path, ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	proj := store.Project{ID: id, Name: "example-app", Type: "code", Path: path}

	mr := &layout.MockRunner{}
	mr.SetSessionExists(layout.CommanderSessionName, true)
	created := false
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "list-windows":
			if created { // the new window carries its @cmdr_ws tag
				return []byte("@1 \n@2 example-app-t11\n"), nil, nil
			}
			return []byte("@1 \n"), nil, nil
		case "new-window":
			created = true
			return []byte("%30\n"), nil, nil
		case "list-panes":
			return []byte("%30 1\n"), nil, nil
		}
		return []byte("%31\n"), nil, nil
	})
	reg := &agentmail.MockRegistrar{NextName: "BoldFox"}
	bc := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "example-app-t11.1", Title: "Get new spec review", Status: "open", Labels: []string{"kind:objective", "needs-user", "owner:user"}},
	}}
	bc.ShowResults = map[string]*beads.Bead{}
	for _, id := range []string{"example-app-t11", "example-app-t01"} {
		bc.ShowResults[id] = &beads.Bead{ID: id, Status: "open", Labels: []string{"kind:objective"}}
	}
	m := &Manager{
		Store: st, Beads: bc, Reg: reg, AmKey: "/repo", Dir: layout.NewDirectorWith(mr),
		Numbered: func(context.Context) ([]tui.NumberedWorkstream, error) {
			return []tui.NumberedWorkstream{
				{N: 1, Project: "example-app", BeadID: "example-app-t11", Title: "Import pipeline with retries (TEAM-101)"},
				{N: 2, Project: "example-app", BeadID: "example-app-t01", Title: "Sample shelf overview"},
			}, nil
		},
	}
	return &fixture{m: m, st: st, proj: proj, reg: reg, mr: mr, bc: bc}
}

// promotedBeads is the live shape: example-app-t02.8 was unhooked from
// example-app-t02 (no parent edge) and is a workstream of its own.
func promotedBeads() *beads.MemClient {
	m := &beads.MemClient{EdgeParentsOnly: true}
	for _, id := range []string{"example-app-t02", "example-app-t02.2", "example-app-t02.8", "example-app-t02.8.4", "example-app-t02.8.4.5"} {
		m.Seed(beads.Bead{ID: id, Title: id, Labels: []string{"kind:objective"}})
	}
	m.Edges = []beads.Dep{
		{IssueID: "example-app-t02.2", DependsOnID: "example-app-t02", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4", DependsOnID: "example-app-t02.8", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4.5", DependsOnID: "example-app-t02.8.4", Type: "parent-child"},
	}
	return m
}

func TestRoot_FollowsRealParents(t *testing.T) {
	bc := promotedBeads()
	for in, want := range map[string]string{
		"example-app-t02":       "example-app-t02",
		"example-app-t02.2":     "example-app-t02",
		"example-app-t02.8":     "example-app-t02.8",
		"example-app-t02.8.4.5": "example-app-t02.8",
	} {
		if got := Owner(context.Background(), bc, "/p", in); got != want {
			t.Errorf("Root(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Role(Owner(context.Background(), bc, "/p", "example-app-t02.8.4.5")); got != "manager-example-app-t02.8" {
		t.Errorf("Manager = %q, want manager-example-app-t02.8", got)
	}
}

func TestOpen_PromotedStepOpensItsOwnWorkstream(t *testing.T) {
	f := newFixture(t)
	f.m.Beads = promotedBeads()
	f.m.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) {
		return []tui.NumberedWorkstream{
			{N: 1, Project: "example-app", BeadID: "example-app-t02", Title: "Parent"},
			{N: 2, Project: "example-app", BeadID: "example-app-t02.8", Title: "Promoted"},
		}, nil
	}
	res, err := f.m.Open(context.Background(), "example-app", "example-app-t02.8.4.5", OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.BeadID != "example-app-t02.8" || res.Role != "manager-example-app-t02.8" {
		t.Fatalf("result = %+v", res)
	}
}

func TestPrepare_CreatesSessionPromptMailRoleAndRowOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	target := Target{Project: f.proj, BeadID: "example-app-t11", Title: "Import pipeline", Rank: 1}

	w, err := f.m.Prepare(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if w.AgentName != "manager-example-app-t11" || w.SessionID == "" || w.ProjectPath != f.proj.Path {
		t.Fatalf("window = %+v", w)
	}
	sessionFile := filepath.Join(f.proj.Path, ".commander", "workstreams", "example-app-t11.claude.session-id")
	if data, err := os.ReadFile(sessionFile); err != nil || strings.TrimSpace(string(data)) != w.SessionID {
		t.Fatalf("session file: %q %v", data, err)
	}
	prompt, err := os.ReadFile(agentrt.ClaudePromptPath("manager-example-app-t11"))
	if err != nil || !strings.Contains(string(prompt), "Manager of ONE workstream") || !strings.Contains(string(prompt), "Manager agent") {
		t.Fatalf("prompt should be preamble + lead.md: %v\n%.200s", err, prompt)
	}
	if name, _ := f.st.GetAmAgent(ctx, "/repo", "manager-example-app-t11"); name != "BoldFox" {
		t.Fatalf("mail role not registered, got %q", name)
	}
	if row, _ := f.st.GetWorkstreamManager(ctx, f.proj.ID, "example-app-t11"); row == nil || row.SessionFile != sessionFile {
		t.Fatalf("row = %+v", row)
	}

	// Idempotent: same session, no second registration.
	w2, err := f.m.Prepare(ctx, target)
	if err != nil || w2.SessionID != w.SessionID || len(f.reg.RegisterCalls) != 1 {
		t.Fatalf("second prepare: %+v %v registrations=%d", w2, err, len(f.reg.RegisterCalls))
	}
}

func TestPrepare_AdoptedManagerKeepsItsOriginalSessionFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orig := filepath.Join(f.proj.Path, ".commander", "manager.claude.session-id")
	if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orig, []byte("22222222-2222-4222-8222-222222222222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: f.proj.ID, WorkstreamBeadID: "example-app-t01", SessionFile: orig}); err != nil {
		t.Fatal(err)
	}
	w, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: "example-app-t01", Title: "Observability", Rank: 2})
	if err != nil || w.SessionID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("adopted session must be reused: %+v %v", w, err)
	}
}

func TestOpen_FocusesAndPointsAtTheStep(t *testing.T) {
	f := newFixture(t)
	res, err := f.m.Open(context.Background(), "example-app", "example-app-t11.1", OpenOptions{Focus: true, StepID: "example-app-t11.1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.BeadID != "example-app-t11" || res.Role != "manager-example-app-t11" || res.Rank != 1 || !res.Created {
		t.Fatalf("result = %+v", res)
	}
	joined := strings.Join(f.mr.CallStrings(), "\n")
	for _, want := range []string{
		"new-window -d -P -F #{pane_id} -t commander: -n #1 Import pipeline with…",
		"set-option -w -t %30 @cmdr_ws example-app-t11",
		"send-keys -t %30 -l the user opened step example-app-t11.1 \"Get new spec review\" (needs-user, owner user) in \"Import pipeline with retries (TEAM-101)\". Give him status on it and pick it up.",
		"send-keys -t %30 Enter",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "select-window") {
		t.Errorf("--focus should select the window:\n%s", joined)
	}
}

func TestOpen_RejectsNonWorkstreams(t *testing.T) {
	f := newFixture(t)
	if _, err := f.m.Open(context.Background(), "example-app", "example-app-nope.1", OpenOptions{}); err == nil {
		t.Fatal("expected an error for an unknown workstream")
	}
	if _, err := f.m.OpenByRole(context.Background(), "manager-ghost"); err == nil {
		t.Fatal("expected an error for an unknown role")
	}
}

func TestPrepareAll_EveryWorkstreamGetsAManager(t *testing.T) {
	f := newFixture(t)
	ws, err := f.m.PrepareAll(context.Background())
	if err != nil || len(ws) != 2 || ws[0].BeadID != "example-app-t11" || ws[1].Rank != 2 {
		t.Fatalf("windows = %+v err %v", ws, err)
	}
	rows, _ := f.st.ListWorkstreamManagers(context.Background())
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAdopt_KeepsSessionRenamesMailRoleAndTagsTheLiveWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orig := filepath.Join(f.proj.Path, ".commander", "manager.claude.session-id")
	if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orig, []byte("33333333-3333-4333-8333-333333333333\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertAmAgent(ctx, "/repo", "manager-example-app", "GoldTiger"); err != nil {
		t.Fatal(err)
	}

	res, err := f.m.Adopt(ctx, "example-app", "example-app-t01", "manager (example-app)")
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != "manager-example-app-t01" || res.OldRole != "manager-example-app" || res.Rank != 2 || res.SessionFile != orig {
		t.Fatalf("result = %+v", res)
	}
	if name, _ := f.st.GetAmAgent(ctx, "/repo", "manager-example-app-t01"); name != "GoldTiger" {
		t.Fatalf("the Manager keeps its inbox under the new role, got %q", name)
	}
	if name, _ := f.st.GetAmAgent(ctx, "/repo", "manager-example-app"); name != "" {
		t.Fatalf("the old per-project role must stop resolving, got %q", name)
	}
	if len(f.reg.RegisterCalls) != 0 {
		t.Fatalf("adoption must not register a new mail agent: %+v", f.reg.RegisterCalls)
	}
	row, _ := f.st.GetWorkstreamManager(ctx, f.proj.ID, "example-app-t01")
	if row == nil || row.SessionFile != orig {
		t.Fatalf("row = %+v", row)
	}
	joined := strings.Join(f.mr.CallStrings(), "\n")
	for _, want := range []string{
		"set-option -w -t commander:manager (example-app) @cmdr_ws example-app-t01",
		"rename-window -t commander:manager (example-app) #2 Sample shelf overview",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "kill-") || strings.Contains(joined, "new-window") {
		t.Fatalf("a live Manager must not be restarted:\n%s", joined)
	}
	// The next restart resumes the same Claude session under the new name.
	w, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: "example-app-t01", Title: "Sample shelf overview", Rank: 2})
	if err != nil || w.SessionID != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("session after adoption: %+v %v", w, err)
	}
}

func TestAdopt_NeedsAnExistingSession(t *testing.T) {
	f := newFixture(t)
	if _, err := f.m.Adopt(context.Background(), "example-app", "example-app-t01", "x"); err == nil {
		t.Fatal("expected an error without a session to adopt")
	}
}

type fakeNative struct {
	started map[string]bool
	sent    []string
	held    bool  // Start refuses: a terminal owns the session
	down    error // Start fails: cmdrd is not reachable
	fresh   map[string]bool
}

func (n *fakeNative) Start(_ context.Context, role string) error {
	if n.down != nil {
		return n.down
	}
	if n.held {
		return ErrSessionHeld
	}
	if n.started == nil {
		n.started = map[string]bool{}
	}
	n.started[role] = true
	return nil
}

func (n *fakeNative) Fresh(role string) bool { return n.fresh == nil || n.fresh[role] }

func (n *fakeNative) Send(_ context.Context, role, text string) error {
	n.sent = append(n.sent, role+": "+text)
	return nil
}

func hasNewWindow(f *fixture) bool {
	for _, c := range f.mr.CallStrings() {
		if strings.Contains(c, "new-window") {
			return true
		}
	}
	return false
}

func TestOpen_NativeManagerGetsNoTerminalWindow(t *testing.T) {
	f := newFixture(t)
	n := &fakeNative{}
	f.m.Native = n
	res, err := f.m.Open(context.Background(), "example-app", "example-app-t11.1", OpenOptions{Focus: true, StepID: "example-app-t11.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Native || res.Pane != "" || !n.started["manager-example-app-t11"] {
		t.Fatalf("res = %+v started=%v", res, n.started)
	}
	if hasNewWindow(f) {
		t.Fatalf("no tmux window for a native Manager:\n%s", strings.Join(f.mr.CallStrings(), "\n"))
	}
	if len(n.sent) != 1 || !strings.Contains(n.sent[0], "Get new spec review") {
		t.Fatalf("step pointer should go to the native chat: %v", n.sent)
	}
}

func TestOpen_FallsBackToTerminalWhenHeldOrDown(t *testing.T) {
	for name, n := range map[string]*fakeNative{"held": {held: true}, "down": {down: os.ErrNotExist}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.m.Native = n
			res, err := f.m.Open(context.Background(), "example-app", "example-app-t11", OpenOptions{})
			if err != nil || res.Native || res.Pane != "%30" || !hasNewWindow(f) {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

func TestEnsureNative_StartsNewManagersAndSkipsStoppedOnes(t *testing.T) {
	f := newFixture(t)
	n := &fakeNative{fresh: map[string]bool{"manager-example-app-t11": true, "manager-example-app-t01": false}}
	f.m.Native = n
	if err := f.m.EnsureNative(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !n.started["manager-example-app-t11"] || n.started["manager-example-app-t01"] {
		t.Fatalf("started = %v", n.started)
	}
	// Prepared too: the engine can resolve the role from the row.
	if row, _ := f.st.GetWorkstreamManagerByRole(context.Background(), "manager-example-app-t11"); row == nil {
		t.Fatal("workstream_managers row missing")
	}
	if hasNewWindow(f) {
		t.Fatal("ensure must not open a terminal window")
	}
}

func TestEnsureRole_UnknownRole(t *testing.T) {
	f := newFixture(t)
	f.m.Native = &fakeNative{}
	if err := f.m.EnsureRole(context.Background(), "manager-nope"); err == nil {
		t.Fatal("want an error for a role without an open workstream")
	}
	if err := f.m.EnsureRole(context.Background(), "manager-example-app-t01"); err != nil {
		t.Fatal(err)
	}
}

// A workstream created seconds ago is in br but not yet in cmdrd's snapshot
// (what the default Numbered reads): it must be found by a fresh read, not
// reported as "not an open workstream".
func TestFind_FallsBackToFreshReadForNewWorkstream(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	t013 := tui.NumberedWorkstream{N: 3, Project: "example-app", BeadID: "example-app-t013", Title: "Installs bounded"}
	fresh := 0
	f.m.Fresh = func(context.Context) ([]tui.NumberedWorkstream, error) {
		fresh++
		return []tui.NumberedWorkstream{t013}, nil
	}

	got, err := f.m.find(ctx, func(t Target) bool { return t.BeadID == "example-app-t013" })
	if err != nil || got == nil || got.Title != t013.Title {
		t.Fatalf("find = %+v, %v; want the fresh workstream", got, err)
	}
	if _, err := f.m.find(ctx, func(t Target) bool { return t.BeadID == "example-app-t11" }); err != nil || fresh != 1 {
		t.Fatalf("a known workstream must not trigger a fresh read (fresh=%d, err=%v)", fresh, err)
	}

	f.m.Fresh = func(context.Context) ([]tui.NumberedWorkstream, error) { return nil, nil }
	if _, err := f.m.Open(ctx, "example-app", "example-app-nope", OpenOptions{}); err == nil || !strings.Contains(err.Error(), "not an open workstream") {
		t.Fatalf("unknown workstream: err = %v", err)
	}
}

// A new Manager starts on the runtime the Manager share draws for its
// workstream (unpinned); one that ran before, or has a runtime, is not touched.
func TestEnsureNative_NewManagerDrawsItsRuntime(t *testing.T) {
	f := newFixture(t)
	home := os.Getenv("COMMANDER_HOME")
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	t.Setenv("COMMANDER_CODEX_MANAGER_SHARE", "1")
	f.m.Native = &fakeNative{fresh: map[string]bool{"manager-example-app-t11": true, "manager-example-app-t01": false}}
	if err := f.m.EnsureNative(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt := chat.ReadRuntime(home, "manager-example-app-t11"); rt != chat.RuntimeCodex || chat.RuntimePinned(home, "manager-example-app-t11") {
		t.Errorf("new Manager: runtime %s, pinned %v", rt, chat.RuntimePinned(home, "manager-example-app-t11"))
	}
	if _, err := os.Stat(chat.RuntimeFile(home, "manager-example-app-t01")); !os.IsNotExist(err) {
		t.Errorf("a Manager that ran before got a runtime: %v", err)
	}
}

// scopeBeads: t07 is a track with its own prefix under ws-t08; ws-other is a
// separate workstream; ws-t08.9 was moved under ws-other.
func scopeBeads() *beads.MemClient {
	m := &beads.MemClient{EdgeParentsOnly: true}
	for _, id := range []string{"ws-t08", "t07-1", "t07-1.2", "ws-other", "ws-t08.9"} {
		m.Seed(beads.Bead{ID: id, Title: id, Labels: []string{"kind:objective"}})
	}
	m.Edges = []beads.Dep{
		{IssueID: "t07-1", DependsOnID: "ws-t08", Type: "parent-child"},
		{IssueID: "t07-1.2", DependsOnID: "t07-1", Type: "parent-child"},
		{IssueID: "ws-t08.9", DependsOnID: "ws-other", Type: "parent-child"},
	}
	return m
}

func TestManagerScope_ByParentageNotPrefix(t *testing.T) {
	bc := scopeBeads()
	for in, want := range map[string]string{
		"t07-1":    "ws-t08", // own prefix, in scope
		"t07-1.2":  "ws-t08",
		"ws-other": "ws-other", // another workstream, not in scope
		"ws-t08.9": "ws-other", // moved bead follows its new parent
	} {
		if got := Owner(context.Background(), bc, "/p", in); got != want {
			t.Errorf("Root(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManagerScope_PreambleSaysParentageNotPrefix(t *testing.T) {
	var tg Target
	tg.BeadID = "ws-t08"
	got := Preamble(tg)
	if strings.Contains(got, "ids start with") {
		t.Errorf("preamble still claims an id prefix:\n%s", got)
	}
	for _, want := range []string{"parent chain leads to ws-t08", "whatever its id prefix", "IN scope"} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble missing %q:\n%s", want, got)
		}
	}
}

func TestOpen_NativeFailureIsNotHiddenByMissingTmux(t *testing.T) {
	f := newFixture(t)
	f.mr.SetSessionExists(layout.CommanderSessionName, false)
	f.m.Native = &fakeNative{down: os.ErrNotExist}
	_, err := f.m.Open(context.Background(), "example-app", "example-app-t11", OpenOptions{})
	if err == nil || !strings.Contains(err.Error(), "native start of") || !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("err = %v", err)
	}
}
