package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

// nineManagers is the live shape: 4 Managers switched to Codex by hand
// (runtime files with no auto marker: pinned), 5 on Claude by default, and the
// Commander, which a rebalance never touches.
func nineManagers(t *testing.T, home string) []chat.Status {
	t.Helper()
	chats := []chat.Status{{Role: "commander", Running: true, Runtime: chat.RuntimeClaude}}
	for _, ws := range []string{"example-app-t02", "example-app-t06", "example-app-t05", "example-automation-t08"} {
		if err := os.MkdirAll(home+"/chat", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(chat.RuntimeFile(home, "manager-"+ws), []byte("codex\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		chats = append(chats, chat.Status{Role: "manager-" + ws, Running: true, Runtime: chat.RuntimeCodex, State: store.AgentIdle})
	}
	for _, ws := range []string{"commander-il9", "example-app-t11", "example-app-t01", "example-automation-t10", "example-app-t04"} {
		chats = append(chats, chat.Status{Role: "manager-" + ws, Running: true, Runtime: chat.RuntimeClaude, State: store.AgentIdle})
	}
	return append(chats, chat.Status{Role: "manager-stopped", Runtime: chat.RuntimeClaude})
}

type fakeSwitch struct {
	calls []string
	err   map[string]error
}

func (f *fakeSwitch) set(_ context.Context, role, rt string) error {
	f.calls = append(f.calls, role+"->"+rt)
	if err := f.err[role]; err != nil {
		return err
	}
	return chat.WriteRuntime(os.Getenv("COMMANDER_HOME"), role, rt) // what cmdrd's SetRuntime does
}

func runRebalance(t *testing.T, home string, chats []chat.Status, share float64, sw *fakeSwitch, dry bool) string {
	t.Helper()
	var out bytes.Buffer
	d := rebalanceDeps{home: home, share: share, hooksReady: true, set: sw.set,
		list: func(context.Context) ([]chat.Status, error) { return chats, nil }}
	if err := rebalance(context.Background(), d, dry, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestRebalanceDryRunListsMovesAndChangesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	sw := &fakeSwitch{}
	out := runRebalance(t, home, nineManagers(t, home), 0.5, sw, true)
	if len(sw.calls) != 0 {
		t.Fatalf("dry run switched: %v", sw.calls)
	}
	// round(0.5 * 9) = 5 on Codex: one Claude Manager moves.
	if !strings.Contains(out, "Managers on Codex: 4 of 9 live, target 5") || strings.Count(out, "would move") != 1 {
		t.Errorf("dry run:\n%s", out)
	}
}

func TestRebalanceMovesIdleUnpinnedSkipsMidTurnNeverPinnedOrCommander(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	chats := nineManagers(t, home)
	for i := range chats {
		if chats[i].Role == "manager-commander-il9" {
			chats[i].State = store.AgentWorking // mid-turn
		}
	}
	sw := &fakeSwitch{err: map[string]error{"manager-example-app-t11": errors.New("cmdrd runtime manager-example-app-t11: busy: wait for the turn to end")}}
	out := runRebalance(t, home, chats, 0.5, sw, false)
	for _, c := range sw.calls {
		if strings.HasPrefix(c, "manager-commander-il9") || strings.HasPrefix(c, "commander") || strings.Contains(c, "->claude") {
			t.Errorf("switched %s", c)
		}
	}
	moved := ""
	for _, c := range sw.calls {
		role := strings.TrimSuffix(c, "->codex")
		if sw.err[role] == nil {
			moved = role
		}
	}
	if moved == "" || !strings.Contains(out, "moved "+moved+": claude -> codex") {
		t.Fatalf("nothing moved: %v\n%s", sw.calls, out)
	}
	if chat.ReadRuntime(home, moved) != chat.RuntimeCodex || chat.RuntimePinned(home, moved) {
		t.Errorf("a rebalanced Manager must stay unpinned on codex")
	}

	// Share 0: only unpinned Codex Managers go back; the 4 hand-set ones stay.
	for i := range chats {
		if chats[i].Role == moved {
			chats[i].Runtime = chat.RuntimeCodex
		}
	}
	sw = &fakeSwitch{}
	out = runRebalance(t, home, chats, 0, sw, false)
	if len(sw.calls) != 1 || sw.calls[0] != moved+"->claude" {
		t.Errorf("share 0 switched %v:\n%s", sw.calls, out)
	}
}

func TestRebalanceNeverMovesToCodexWithoutHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	chats := nineManagers(t, home)
	var out bytes.Buffer
	sw := &fakeSwitch{}
	d := rebalanceDeps{home: home, share: 1, set: sw.set, list: func(context.Context) ([]chat.Status, error) { return chats, nil }}
	if err := rebalance(context.Background(), d, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(sw.calls) != 0 || !strings.Contains(out.String(), "Codex runs no hooks") {
		t.Errorf("moved without hooks: %v\n%s", sw.calls, out.String())
	}
}

// An explicit choice pins a role; a share's choice does not, until a person
// switches it again.
func TestRuntimePinning(t *testing.T) {
	home := t.TempDir()
	if chat.RuntimePinned(home, "manager-a") {
		t.Fatal("no runtime file is not a pin")
	}
	if err := chat.WriteAutoRuntime(home, "manager-a", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if chat.RuntimePinned(home, "manager-a") {
		t.Error("a share's choice pinned the role")
	}
	if err := chat.WriteRuntime(home, "manager-a", chat.RuntimeClaude); err != nil { // cmdr runtime / the menu
		t.Fatal(err)
	}
	if !chat.RuntimePinned(home, "manager-a") {
		t.Error("an explicit switch did not pin the role")
	}
}

func TestRuntimeReportShowsSharesAndLiveSplit(t *testing.T) {
	t.Setenv("COMMANDER_CODEX_MANAGER_SHARE", "0.4")
	t.Setenv("COMMANDER_CODEX_OPERATOR_SHARE", "0.6")
	var out bytes.Buffer
	writeShares(&out, split{managersCodex: 4, managers: 9, operatorsCodex: 2, operators: 5})
	for _, want := range []string{"Managers 0.40 (codex_manager_share), Operators 0.60", "Live on Codex: Managers 4/9, Operators 2/5"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	writeShares(&out, split{managers: -1})
	if !strings.Contains(out.String(), "Managers unknown (cmdrd not running)") {
		t.Errorf("cmdrd down:\n%s", out.String())
	}
}

// When every movable Manager is mid-turn, none is switched and the shortfall is said.
func TestRebalanceAllMidTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	chats := nineManagers(t, home)
	for i := range chats {
		if chats[i].Runtime == chat.RuntimeClaude && chats[i].Role != "commander" {
			chats[i].State = store.AgentWorking
		}
	}
	sw := &fakeSwitch{}
	out := runRebalance(t, home, chats, 0.5, sw, false)
	if len(sw.calls) != 0 || !strings.Contains(out, "mid-turn") || !strings.Contains(out, "1 short of the target") {
		t.Errorf("switched %v:\n%s", sw.calls, out)
	}
}

// Share 0 (the default): nobody moves to Codex, hooks ready or not; a drawn
// Manager on Codex comes back to Claude; a pinned one and the Commander stay.
func TestRebalanceAtShareZeroNeverMovesToCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if err := chat.WriteAutoRuntime(home, "manager-drawn", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := chat.WriteRuntime(home, "manager-pinned", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	chats := []chat.Status{
		{Role: "commander", Running: true, Runtime: chat.RuntimeClaude},
		{Role: "manager-drawn", Running: true, Runtime: chat.RuntimeCodex, State: store.AgentIdle},
		{Role: "manager-pinned", Running: true, Runtime: chat.RuntimeCodex, State: store.AgentIdle},
		{Role: "manager-claude", Running: true, Runtime: chat.RuntimeClaude, State: store.AgentIdle},
	}
	sw := &fakeSwitch{}
	runRebalance(t, home, chats, 0, sw, false)
	if len(sw.calls) != 1 || sw.calls[0] != "manager-drawn->claude" {
		t.Errorf("moves at share 0: %v", sw.calls)
	}
	if chat.ReadRuntime(home, "manager-pinned") != chat.RuntimeCodex {
		t.Error("a pinned Manager was moved")
	}
	if chat.ReadRuntime(home, "commander") != chat.RuntimeClaude {
		t.Error("the Commander was moved")
	}
}

// An authorized rebalance reaches cmdrd under either agent role variable.
func TestRebalanceAgentChoiceReachesCmdrd(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(home+"/api-url", []byte("http://127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := cmdrdChats{home: home}
	for _, env := range [][2]string{{"manager-ws1", ""}, {"", "manager-ws1"}} {
		t.Setenv("COMMANDER_ROLE", env[0])
		t.Setenv("COMMANDER_MANAGER_ROLE", env[1])
		err := c.setRuntime(context.Background(), "manager-a", chat.RuntimeCodex)
		if err == nil || strings.Contains(err.Error(), "d11") {
			t.Errorf("%v: codex: %v", env, err)
		}
		if err := c.setRuntime(context.Background(), "manager-a", chat.RuntimeClaude); err == nil || strings.Contains(err.Error(), "d11") {
			t.Errorf("%v: claude: %v, want it sent to cmdrd (not running)", env, err)
		}
	}
}
