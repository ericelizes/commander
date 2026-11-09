package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// accountHome is a COMMANDER_HOME with Claude accounts (logged in through a
// fake Keychain) and a fake HOME.
func accountHome(t *testing.T, loggedIn []string, loggedOut ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("HOME", t.TempDir())
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	held := map[string]bool{claudeacct.KeychainService(""): true}
	for _, n := range append(append([]string{}, loggedIn...), loggedOut...) {
		if _, err := claudeacct.Create(home, n); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range loggedIn {
		held[claudeacct.KeychainService(claudeacct.Dir(home, n))] = true
	}
	prev := claudeacct.KeychainHas
	claudeacct.KeychainHas = func(s string) bool { return held[s] }
	claudeacct.ForgetProbes()
	t.Cleanup(func() { claudeacct.KeychainHas = prev; claudeacct.ForgetProbes() })
	return home
}

// fakeCmdrd answers chat posts with status/body and records them.
func fakeCmdrd(t *testing.T, home string, status int, reply string) *[]string {
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		seen = append(seen, r.URL.Path+" "+string(raw))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(ts.Close)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(ts.URL+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte("tok\n"), 0o600)
	return &seen
}

// Commander switches a Manager: through cmdrd, which restarts the chat there.
func TestAccountUseSwitchesAManagerThroughCmdrd(t *testing.T) {
	home := accountHome(t, []string{"work"})
	seen := fakeCmdrd(t, home, 200, `{"ok":true}`)
	var out, errOut bytes.Buffer
	if code := runAccount([]string{"use", "manager-bd-1", "work"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if len(*seen) != 1 || (*seen)[0] != `/api/chat/manager-bd-1/account {"account":"work"}` {
		t.Fatalf("posts = %v", *seen)
	}
}

func TestAccountUseRefusesMidTurnLoggedOutAndOperators(t *testing.T) {
	home := accountHome(t, []string{"work"}, "spare")
	fakeCmdrd(t, home, 409, `{"ok":false,"error":"chat: a turn is in progress: wait for the turn to end before switching account"}`)
	var out, errOut bytes.Buffer
	if code := runAccount([]string{"use", "manager-bd-1", "work"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "turn is in progress") {
		t.Fatalf("mid-turn: %d %s", code, errOut.String())
	}
	if chat.ReadAccount(home, "manager-bd-1") != claudeacct.Default {
		t.Error("a refused switch was recorded")
	}
	errOut.Reset()
	if code := runAccount([]string{"use", "manager-bd-1", "spare"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "claude auth login") {
		t.Fatalf("logged out: %d %s", code, errOut.String())
	}
	errOut.Reset()
	if code := runAccount([]string{"use", "operator-x", "work"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "internal-operator-provider") {
		t.Fatalf("operator: %d %s", code, errOut.String())
	}
}

func TestAccountAddPrintsTheLoginLineAndListShowsUsage(t *testing.T) {
	home := accountHome(t, nil)
	var out, errOut bytes.Buffer
	if code := runAccount([]string{"add", "second"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if want := "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude-accounts", "second") + " claude auth login"; !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
	s := chat.OpenUsage(home)
	s.Record(chat.RuntimeClaude, "", "commander", json.RawMessage(`{"status":"allowed_warning","unifiedWindows":{"five_hour":{"utilization":0.99,"resetsAt":4102444800},"seven_day":{"utilization":0.39,"resetsAt":4102444800}}}`), time.Now())
	s.Record(chat.RuntimeCodex, "", "operator-x", json.RawMessage(`{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.1}}}`), time.Now())
	out.Reset()
	if code := runAccount([]string{"list"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	got := out.String()
	for _, want := range []string{"claude:default", "logged in", "warning", "99% (resets", "39%", "claude:second", "not logged in", "no report yet", "codex", "10%"} {
		if !strings.Contains(got, want) {
			t.Errorf("list lacks %q:\n%s", want, got)
		}
	}
}

// fakeProviderRuns is the store side of internal-operator-provider.
type fakeProviderRuns struct {
	runs   map[string]*store.AgentRun
	closed map[int64]string
}

func (f *fakeProviderRuns) GetAgentRunByAgentName(_ context.Context, n string) (*store.AgentRun, error) {
	return f.runs[n], nil
}

func (f *fakeProviderRuns) GetProjectByID(context.Context, int64) (*store.Project, error) {
	return &store.Project{ID: 3, Name: "demo", Type: "code", Path: "/p"}, nil
}

func (f *fakeProviderRuns) MarkClosed(_ context.Context, id int64, status string) error {
	f.closed[id] = status
	return nil
}

func stubProviderDeps(t *testing.T) (*fakeProviderRuns, *fakeOperatorSpawner, *[]string) {
	runs := &fakeProviderRuns{closed: map[int64]string{}, runs: map[string]*store.AgentRun{
		"operator-op-a-ws1-1": {ID: 11, ProjectID: 3, TaskBeadID: "ws1.1.1", AgentName: "operator-op-a-ws1-1", Status: "running", TmuxPaneID: sql.NullString{String: "cmdr-op-demo-op-a", Valid: true}},
		"operator-op-b-ws2-1": {ID: 12, ProjectID: 3, TaskBeadID: "ws2.1.1", AgentName: "operator-op-b-ws2-1", Status: "running", TmuxPaneID: sql.NullString{String: "cmdr-op-demo-op-b", Valid: true}},
	}}
	sp := &fakeOperatorSpawner{}
	var ended []string
	mock := &beads.MockClient{ParentsResult: map[string]string{"ws1.1.1": "ws1.1", "ws1.1": "ws1", "ws1": "", "ws2.1.1": "ws2.1", "ws2.1": "ws2", "ws2": ""}}
	orig := operatorProviderDepsFn
	operatorProviderDepsFn = func() (*operatorProviderDeps, error) {
		return &operatorProviderDeps{Runs: runs, Spawner: sp, Beads: mock,
			EndSession: func(_ context.Context, name string) error { ended = append(ended, name); return nil },
			BranchOf:   func(string) (string, error) { return "op/a", nil }, DefaultModel: "sonnet"}, nil
	}
	t.Cleanup(func() { operatorProviderDepsFn = orig })
	return runs, sp, &ended
}

// A detached worktree has no branch to respawn on: refused with the reason while
// the Operator still runs, its run open and its session alive.
func TestOperatorProviderRefusesADetachedWorktree(t *testing.T) {
	accountHome(t, []string{"work"})
	runs, sp, ended := stubProviderDeps(t)
	orig := operatorProviderDepsFn
	operatorProviderDepsFn = func() (*operatorProviderDeps, error) {
		d, err := orig()
		d.BranchOf = func(string) (string, error) { return spawn.DetachedHEAD, nil }
		return d, err
	}
	var out, errOut bytes.Buffer
	if code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--account", "work"}, &out, &errOut); code == 0 {
		t.Fatal("respawned on a detached HEAD")
	}
	if !strings.Contains(errOut.String(), "detached HEAD") {
		t.Errorf("stderr: %s", errOut.String())
	}
	if len(runs.closed) != 0 || len(*ended) != 0 || len(sp.reqs) != 0 {
		t.Fatalf("closed %v ended %v spawns %v", runs.closed, *ended, sp.reqs)
	}
}

// A Manager switches its own Operator: the old run closes, its session ends,
// and it respawns on the same bead, branch and worktree, resumed, on the account.
func TestOperatorProviderRespawnsAManagersOwnOperator(t *testing.T) {
	accountHome(t, []string{"work"})
	runs, sp, ended := stubProviderDeps(t)
	t.Setenv("COMMANDER_ROLE", "manager-ws1")
	var out, errOut bytes.Buffer
	if code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--account", "work"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if runs.closed[11] != switchedStatus || len(*ended) != 1 || (*ended)[0] != "cmdr-op-demo-op-a" {
		t.Fatalf("closed %v ended %v", runs.closed, *ended)
	}
	if len(sp.reqs) != 1 {
		t.Fatalf("spawns = %v", sp.reqs)
	}
	r := sp.reqs[0]
	if !r.Resume || r.TaskBeadID != "ws1.1.1" || r.Branch != "op/a" || r.Name != "op-a-ws1-1" || r.Account != "work" || r.Runtime != "" {
		t.Errorf("respawn request = %+v", r)
	}

	// Codex: a person, with no role in the environment.
	t.Setenv("COMMANDER_ROLE", "")
	if code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--runtime", "codex"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if r := sp.reqs[1]; r.Runtime != chat.RuntimeCodex || r.Account != "" {
		t.Errorf("codex request = %+v", r)
	}
}

// A provider switch closes the row itself, so on a stopped run it would lose the
// settle and bring the run back: it is refused before the Operator is touched.
func TestOperatorProviderRefusesARunStoppedButNotSettled(t *testing.T) {
	accountHome(t, []string{"work"})
	runs, sp, ended := stubProviderDeps(t)
	t.Setenv("COMMANDER_ROLE", "manager-ws1")
	session := runs.runs["operator-op-a-ws1-1"].TmuxPaneID.String
	if err := os.MkdirAll(spawn.StopMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spawn.StopMarker(session), []byte("manager-ws1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--account", "work"}, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "'Operator stopped' mail") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if len(runs.closed) != 0 || len(*ended) != 0 || len(sp.reqs) != 0 {
		t.Fatalf("a refused switch touched the Operator: %v %v %v", runs.closed, *ended, sp.reqs)
	}
}

// Requested runtime changes work from each supported agent role.
func TestOperatorProviderAllowsRequestedCodexFromAnAgent(t *testing.T) {
	accountHome(t, []string{"work"})
	for _, role := range []string{"commander", "manager-ws1", "operator-op-a-ws1-1"} {
		runs, sp, ended := stubProviderDeps(t)
		t.Setenv("COMMANDER_ROLE", role)
		var out, errOut bytes.Buffer
		code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--runtime", "codex"}, &out, &errOut)
		if code != 0 {
			t.Errorf("%s: exit %d: %s", role, code, errOut.String())
		}
		if len(runs.closed) != 1 || len(*ended) != 1 || len(sp.reqs) != 1 || sp.reqs[0].Runtime != chat.RuntimeCodex {
			t.Errorf("%s: requested Codex switch did not preserve and respawn the Operator", role)
		}
	}
	_, sp, _ := stubProviderDeps(t)
	t.Setenv("COMMANDER_ROLE", "manager-ws1")
	var out, errOut bytes.Buffer
	if code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--runtime", "claude"}, &out, &errOut); code != 0 || sp.reqs[0].Runtime != chat.RuntimeClaude {
		t.Errorf("claude from a Manager: exit %d: %s", code, errOut.String())
	}
}

// A host Operator's launcher sets only COMMANDER_MANAGER_ROLE.
func TestOperatorProviderAllowsRequestedCodexFromAHostOperator(t *testing.T) {
	accountHome(t, []string{"work"})
	runs, sp, ended := stubProviderDeps(t)
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-ws1")
	var out, errOut bytes.Buffer
	code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--runtime", "codex"}, &out, &errOut)
	if code != 0 {
		t.Errorf("exit %d: %s", code, errOut.String())
	}
	if len(runs.closed) != 1 || len(*ended) != 1 || len(sp.reqs) != 1 || sp.reqs[0].Runtime != chat.RuntimeCodex {
		t.Error("requested Codex switch did not preserve and respawn the Operator")
	}
}

func TestOperatorProviderRefusesAnotherWorkstreamsOperator(t *testing.T) {
	accountHome(t, []string{"work"})
	runs, sp, ended := stubProviderDeps(t)
	t.Setenv("COMMANDER_ROLE", "manager-ws1")
	var out, errOut bytes.Buffer
	if code := runInternalOperatorProvider([]string{"operator-op-b-ws2-1", "--account", "work"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "belongs to manager-ws2") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if len(runs.closed) != 0 || len(*ended) != 0 || len(sp.reqs) != 0 {
		t.Fatal("a refused switch touched the Operator")
	}
	for _, bad := range [][]string{{"manager-ws1", "--account", "work"}, {"operator-op-a-ws1-1"}, {"operator-op-a-ws1-1", "--runtime", "gemini"}} {
		if code := runInternalOperatorProvider(bad, &out, &errOut); code == 0 {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestOfflineAccountUsePreservesRuntimeIntent(t *testing.T) {
	home := accountHome(t, []string{"work"})
	role := "manager-sample"
	if err := chat.WriteRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "api-url"), []byte("http://127.0.0.1:1"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runAccount([]string{"use", role, "work"}, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	if chat.ReadRuntime(home, role) != chat.RuntimeClaude || chat.ReadRuntimePin(home, role) != chat.RuntimeCodex || chat.ReadAccount(home, role) != "work" {
		t.Fatal("account switch changed runtime intent")
	}
}

// A parent Manager may respawn an Operator under its nested workstream (the nested
// Manager may be down); a sibling may not; with nested Managers off nobody above.
func TestOperatorProvider_ParentMayRespawnNested(t *testing.T) {
	ctx := context.Background()
	tree := beads.NestedTree()
	dir := t.TempDir()
	if ownerAbove(ctx, tree, dir, beads.NestedStep, "manager-"+beads.NestedRoot) {
		t.Fatal("switch off: an owner above was accepted")
	}
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	for caller, want := range map[string]bool{
		"manager-" + beads.NestedA:    true,
		"manager-" + beads.NestedRoot: true,
		"manager-" + beads.NestedB:    false,
		"manager-" + beads.NestedA1:   false, // the owner itself is not above
	} {
		if got := ownerAbove(ctx, tree, dir, beads.NestedStep, caller); got != want {
			t.Errorf("%s: %v, want %v", caller, got, want)
		}
	}
}

// A respawn onto a runtime or account with no usage left is refused while the
// Operator still runs (run open, session alive, nothing spawned), naming an
// account that has usage.
func TestOperatorProviderRefusesAnOutOfUsageProvider(t *testing.T) {
	home := accountHome(t, []string{"work"})
	now := time.Now()
	u := chat.OpenUsage(home)
	u.MarkOut(chat.RuntimeCodex, "", "manager-x", now.Add(time.Hour).Unix(), now)
	u.MarkOut(chat.RuntimeClaude, "work", "manager-x", now.Add(time.Hour).Unix(), now)
	for _, c := range []struct{ args, want []string }{
		{[]string{"--runtime", "codex"}, []string{"Codex is out of usage until", "--runtime claude", "nothing was changed"}},
		{[]string{"--account", "work"}, []string{"Claude account work is out of usage until", "--account default", "nothing was changed"}},
	} {
		runs, sp, ended := stubProviderDeps(t)
		var out, errOut bytes.Buffer
		if code := runInternalOperatorProvider(append([]string{"operator-op-a-ws1-1"}, c.args...), &out, &errOut); code == 0 {
			t.Fatalf("%v: respawned onto an out provider", c.args)
		}
		for _, w := range c.want {
			if !strings.Contains(errOut.String(), w) {
				t.Errorf("%v: stderr lacks %q: %s", c.args, w, errOut.String())
			}
		}
		if len(runs.closed) != 0 || len(*ended) != 0 || len(sp.reqs) != 0 {
			t.Fatalf("%v: a refused switch touched the Operator: %v %v %v", c.args, runs.closed, *ended, sp.reqs)
		}
	}
}

// A respawn that names no account (or names Codex while its hooks are not
// ready, which runs Claude) is checked against the account it will really run
// on, the role's or else its Manager's: with that account and every other out,
// it is refused while the Operator still runs (run open, session alive,
// nothing spawned).
func TestOperatorProviderChecksTheAccountItResolvesTo(t *testing.T) {
	t.Setenv("COMMANDER_CODEX_BIN", "/bin/false") // Codex hooks not ready
	for _, c := range []struct {
		name string
		args []string
		set  func(home string)
	}{
		{"role account, --runtime claude", []string{"--runtime", "claude"}, func(home string) { _ = chat.WriteAccount(home, "operator-op-a-ws1-1", "work") }},
		{"Manager account, --runtime claude", []string{"--runtime", "claude"}, func(home string) { _ = chat.WriteAccount(home, "manager-ws1", "work") }},
		{"role account, --runtime codex without hooks", []string{"--runtime", "codex"}, func(home string) { _ = chat.WriteAccount(home, "operator-op-a-ws1-1", "work") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := accountHome(t, []string{"work"})
			now := time.Now()
			u := chat.OpenUsage(home)
			u.MarkOut(chat.RuntimeClaude, "work", "manager-x", now.Add(time.Hour).Unix(), now)
			u.MarkOut(chat.RuntimeClaude, "default", "manager-x", now.Add(time.Hour).Unix(), now)
			c.set(home)
			runs, sp, ended := stubProviderDeps(t)
			var out, errOut bytes.Buffer
			if code := runInternalOperatorProvider(append([]string{"operator-op-a-ws1-1"}, c.args...), &out, &errOut); code == 0 {
				t.Fatal("respawned with no account that has usage")
			}
			for _, w := range []string{"Claude account work is out of usage", "nothing was changed"} {
				if !strings.Contains(errOut.String(), w) {
					t.Errorf("stderr lacks %q: %s", w, errOut.String())
				}
			}
			if strings.Contains(errOut.String(), "--runtime codex") {
				t.Errorf("offered Codex, whose hooks are not ready: %s", errOut.String())
			}
			if len(runs.closed) != 0 || len(*ended) != 0 || len(sp.reqs) != 0 {
				t.Fatalf("a refused switch touched the Operator: %v %v %v", runs.closed, *ended, sp.reqs)
			}
		})
	}
	// The same switch with an account that has usage goes ahead (the spawner moves it).
	home := accountHome(t, []string{"work"})
	now := time.Now()
	chat.OpenUsage(home).MarkOut(chat.RuntimeClaude, "work", "manager-x", now.Add(time.Hour).Unix(), now)
	_ = chat.WriteAccount(home, "operator-op-a-ws1-1", "work")
	runs, sp, _ := stubProviderDeps(t)
	var out, errOut bytes.Buffer
	if code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--runtime", "claude"}, &out, &errOut); code != 0 || len(runs.closed) != 1 || len(sp.reqs) != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}
