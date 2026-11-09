package layout

import (
	"context"
	"strings"
	"testing"
)

func TestWorkstreamWindowName(t *testing.T) {
	cases := []struct {
		rank  int
		title string
		want  string
	}{
		{1, "Import pipeline with retries (TEAM-101)", "#1 Import pipeline with…"},
		{2, "Sample shelf overview", "#2 Sample shelf overview"},
		{8, "Commander", "#8 Commander"},
		{0, "Unranked", "Unranked"},
	}
	for _, c := range cases {
		if got := WorkstreamWindowName(c.rank, c.title); got != c.want {
			t.Errorf("WorkstreamWindowName(%d, %q) = %q, want %q", c.rank, c.title, got, c.want)
		}
	}
}

// An agent gets this error from internal-open-workstream, and bare cmdr refuses it.
func TestEnsureWorkstreamWindow_NoSessionSaysWhoCanStartIt(t *testing.T) {
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, false)
	_, _, err := NewDirectorWith(mr).EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{BeadID: "demo-1"})
	if err == nil {
		t.Fatal("no session must be an error")
	}
	for _, want := range []string{"session not running", "the user", "own terminal", "an agent is refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

func TestEnsureWorkstreamWindow_CreatesTaggedThreeColumnWindow(t *testing.T) {
	t.Setenv("COMMANDER_RUNTIME", "claude")
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			return nil, nil, nil
		case "list-windows":
			return []byte("@1|\n"), nil, nil
		case "new-window":
			return []byte("%30\n"), nil, nil
		}
		return []byte("%31\n"), nil, nil
	})
	d := NewDirectorWith(mr)
	pane, created, err := d.EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{
		ProjectName: "example-app", ProjectPath: "/p", BeadID: "example-app-t11", Title: "Import pipeline", Rank: 1,
		AgentName: "manager-example-app-t11", SessionID: "11111111-1111-4111-8111-111111111111",
	})
	if err != nil || !created || pane != "%30" {
		t.Fatalf("pane=%q created=%v err=%v", pane, created, err)
	}
	calls := mr.CallStrings()
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"tmux new-window -d -P -F #{pane_id} -t commander: -n #1 Import pipeline -c /p env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 claude -n manager-example-app-t11",
		"tmux set-option -w -t %30 @cmdr_ws example-app-t11",
		"tmux set-option -p -t %30 @cmdr_chat 1",
		"cmdr __column cmdr-objectives",
		"cmdr __column inspector --ws example-app-t11 --project example-app",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestEnsureWorkstreamWindow_ReusesWindowFoundByOption(t *testing.T) {
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			return nil, nil, nil
		case "list-windows":
			return []byte("@1 \n@7 example-app-t11\n"), nil, nil
		case "list-panes":
			return []byte("%40 \n%41 1\n"), nil, nil
		}
		t.Errorf("an open window must not be recreated: %v", args)
		return nil, nil, nil
	})
	pane, created, err := NewDirectorWith(mr).EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{BeadID: "example-app-t11"})
	if err != nil || created || pane != "%41" {
		t.Fatalf("pane=%q created=%v err=%v", pane, created, err)
	}
}

func TestSyncWorkstreamNames_RenamesAndNumbersWindowsByRank(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "list-windows" {
			// id|index|@cmdr_ws|name
			return []byte("@1|1||commander\n@2|2|ws-a|#1 Alpha\n@3|3|ws-b|#9 Beta\n@4|4||op-fix\n"), nil, nil
		}
		return nil, nil, nil
	})
	err := NewDirectorWith(mr).SyncWorkstreamNames(context.Background(), map[string]WorkstreamWindow{
		"ws-a": {Rank: 1, Title: "Alpha"},
		"ws-b": {Rank: 2, Title: "Beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	if strings.Contains(joined, "rename-window -t @2") || !strings.Contains(joined, "rename-window -t @3 #2 Beta") {
		t.Fatalf("expected only @3 renamed:\n%s", joined)
	}
	// Commander -> 0, #1 -> 1, #2 -> 2; the operator window (index 4) is not in the
	// way, so it stays. Two phases so nothing lands on an occupied index.
	for _, want := range []string{
		"move-window -d -s @1 -t commander:1000",
		"move-window -d -s @1 -t commander:0",
		"move-window -d -s @2 -t commander:1",
		"move-window -d -s @3 -t commander:2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-s @4") {
		t.Errorf("an operator window that is not in the way must not move:\n%s", joined)
	}
	if strings.Index(joined, "-t commander:1000") > strings.Index(joined, "-t commander:0") {
		t.Errorf("the temporary moves must come before the final moves:\n%s", joined)
	}
}

func TestSyncWorkstreamNames_LeavesAlreadyNumberedWindowsAlone(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "list-windows" {
			return []byte("@1|0||commander\n@2|1|ws-a|#1 Alpha\n"), nil, nil
		}
		return nil, nil, nil
	})
	if err := NewDirectorWith(mr).SyncWorkstreamNames(context.Background(), map[string]WorkstreamWindow{"ws-a": {Rank: 1, Title: "Alpha"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range mr.CallStrings() {
		if strings.Contains(c, "move-window") || strings.Contains(c, "rename-window") {
			t.Fatalf("nothing to change, got %s", c)
		}
	}
}

func TestWorkstreamWindowNameDropsCharactersTmuxRejects(t *testing.T) {
	for _, title := range []string{"Audit tracker: script audits + run them", "Hands-on development: fix dev flow", "v1.2 release"} {
		if got := WorkstreamWindowName(3, title); strings.ContainsAny(got, ":.") {
			t.Errorf("%q still has ':' or '.': %q", title, got)
		}
	}
}

func TestTagWorkstreamWindow(t *testing.T) {
	mr := &MockRunner{}
	if err := NewDirectorWith(mr).TagWorkstreamWindow(context.Background(), "manager (example-app)", "example-app-t01", 2, "Sample shelf overview"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"tmux set-option -w -t commander:manager (example-app) @cmdr_ws example-app-t01",
		"tmux rename-window -t commander:manager (example-app) #2 Sample shelf overview",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestEnsureWorkstreamWindow_GetsAClearTitleBarAboveTheChat(t *testing.T) {
	t.Setenv("COMMANDER_RUNTIME", "claude")
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			return nil, nil, nil
		case "list-windows":
			return []byte("@1 \n"), nil, nil
		case "new-window":
			return []byte("%30\n"), nil, nil
		}
		return []byte("%31\n"), nil, nil
	})
	_, _, err := NewDirectorWith(mr).EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{
		ProjectName: "p", ProjectPath: "/p", BeadID: "ws-1", Title: "Per-App   Observability", Rank: 2,
		AgentName: "manager-ws-1", SessionID: "11111111-1111-4111-8111-111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"set-option -w -t %30 @cmdr_kind manager",
		"set-option -w -t %30 @cmdr_title Manager | Per-App Observability",
		"set-option -w -t %30 @cmdr_meta /p",
		"split-window -v -b -d -l 4 -P -F #{pane_id} -t %30 -c /p",
		"cmdr __column chat-title",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestSyncWorkstreamNames_KeepsTheTitleBarTextCurrent(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "list-windows" {
			return []byte("@2|1|ws-a|#1 Alpha\n"), nil, nil
		}
		return nil, nil, nil
	})
	if err := NewDirectorWith(mr).SyncWorkstreamNames(context.Background(), map[string]WorkstreamWindow{"ws-a": {Rank: 1, Title: "Alpha renamed"}}); err != nil {
		t.Fatal(err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux set-option -w -t @2 @cmdr_title Manager | Alpha renamed")
}

func TestNewWindowsGetSolidBackgroundsAndVisibleBordersBeforeTheirPanesSplit(t *testing.T) {
	t.Setenv("COMMANDER_RUNTIME", "claude")
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			return nil, nil, nil
		case "list-windows":
			return []byte("@1 \n"), nil, nil
		case "new-window":
			return []byte("%30\n"), nil, nil
		}
		return []byte("%31\n"), nil, nil
	})
	if _, _, err := NewDirectorWith(mr).EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{
		ProjectName: "p", ProjectPath: "/p", BeadID: "ws-1", Title: "T", Rank: 1,
		AgentName: "manager-ws-1", SessionID: "11111111-1111-4111-8111-111111111111",
	}); err != nil {
		t.Fatal(err)
	}
	calls := mr.CallStrings()
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"set-option -w -t %30 window-style bg=#181825",
		"set-option -p -t %30 window-style bg=#1e1e2e",
		"set-option -w -t %30 pane-active-border-style fg=#181825,bg=#181825",
		"set-option -w -t %30 pane-border-lines spaces",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	styled, firstSplit := -1, -1
	for i, c := range calls {
		if strings.Contains(c, "window-style bg=#181825") && styled < 0 {
			styled = i
		}
		if strings.Contains(c, "split-window") && firstSplit < 0 {
			firstSplit = i
		}
	}
	if styled < 0 || firstSplit < 0 || styled > firstSplit {
		t.Fatalf("the window must be styled before its columns are split off (styled=%d split=%d)", styled, firstSplit)
	}
}
