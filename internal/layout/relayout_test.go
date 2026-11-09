package layout

import (
	"context"
	"strings"
	"testing"
)

func TestColumnWidths(t *testing.T) {
	cases := []struct{ w, left, right int }{
		{426, 55, 153}, // spec: 426 -> 54 | 216 | 153 (rounding: 55)
		{160, 36, 42},
	}
	for _, c := range cases {
		l, r := ColumnWidths(c.w)
		if l != c.left || r != c.right {
			t.Errorf("ColumnWidths(%d) = %d, %d; want %d, %d", c.w, l, r, c.left, c.right)
		}
		if chat := c.w - l - r - 2; chat < 80 {
			t.Errorf("width %d leaves the chat only %d columns", c.w, chat)
		}
	}
	// A tiny terminal must not produce negative or absurd widths.
	for _, w := range []int{80, 60, 40} {
		l, r := ColumnWidths(w)
		if l < 1 || r < 1 || l+r+2 >= w {
			t.Errorf("ColumnWidths(%d) = %d, %d starves the chat", w, l, r)
		}
	}
}

func TestRelayoutRestoresAbsoluteWidthsAndTitleHeightInEveryWindow(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "list-panes" {
			return []byte("@1|426|%1||cmdr __column cmdr-objectives\n" +
				"@1|426|%2||cmdr __column chat-title\n" +
				"@1|426|%3||claude -n commander\n" +
				"@1|426|%4||cmdr __column cmdr-fleet\n" +
				"@2|160|%5||cmdr __column cmdr-objectives\n" +
				"@2|160|%6||cmdr __column chat-title\n" +
				"@2|160|%7||claude -n manager-x\n" +
				"@2|160|%8||cmdr __column lead-fleet --project p\n"), nil, nil
		}
		return nil, nil, nil
	})
	if err := NewDirectorWith(mr).Relayout(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"resize-pane -t %1 -x 55", "resize-pane -t %2 -y 4", "resize-pane -t %4 -x 153",
		"resize-pane -t %5 -x 36", "resize-pane -t %6 -y 4", "resize-pane -t %8 -x 42",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "resize-pane -t %3") || strings.Contains(joined, "resize-pane -t %7") {
		t.Fatalf("chat panes are never resized directly:\n%s", joined)
	}
}

func TestNeverSizeAWindowByHand(t *testing.T) {
	// resize-window -x/-y pins window-size to "manual": the window then ignores
	// the terminal forever (the 80x24-after-reattach bug).
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
		case "display-message":
			return []byte("426\n"), nil, nil
		}
		return []byte("%31\n"), nil, nil
	})
	t.Setenv("COMMANDER_RUNTIME", "claude")
	if _, _, err := NewDirectorWith(mr).EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{
		ProjectName: "p", ProjectPath: "/p", BeadID: "ws-1", Title: "T", Rank: 1,
		AgentName: "manager-ws-1", SessionID: "11111111-1111-4111-8111-111111111111",
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	if strings.Contains(joined, "resize-window -t") && (strings.Contains(joined, " -x ") && strings.Contains(joined, "resize-window")) {
		t.Fatalf("windows must never be sized by hand:\n%s", joined)
	}
	for _, want := range []string{
		"set-option -u -w -t %30 window-size",              // heal a stale manual flag
		"split-window -h -b -P -F #{pane_id} -t %30 -l 55", // absolute sidebar width from the formula
		"-l 153", // and right column
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestSessionChromeHooksRelayoutOnResizeAndAttach(t *testing.T) {
	mr := &MockRunner{}
	d := NewDirectorWith(mr)
	if err := d.ensureCommanderSessionChrome(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux set-hook -t commander: client-resized run-shell -b 'cmdr __relayout'")
	mustContainCall(t, calls, "tmux set-hook -t commander: client-attached run-shell -b 'cmdr __relayout'")
}

func TestNewWindowsGetThreeSpacerPanesAroundTheChat(t *testing.T) {
	t.Setenv("COMMANDER_RUNTIME", "claude")
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	n := 40
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			return nil, nil, nil
		case "list-windows":
			return []byte("@1 \n"), nil, nil
		case "new-window":
			return []byte("%30\n"), nil, nil
		case "split-window":
			n++
			return []byte("%" + strings.Repeat("1", 1) + string(rune('0'+n%10)) + "\n"), nil, nil
		}
		return nil, nil, nil
	})
	if _, _, err := NewDirectorWith(mr).EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{
		ProjectName: "p", ProjectPath: "/p", BeadID: "ws-1", Title: "T", Rank: 1,
		AgentName: "manager-ws-1", SessionID: "11111111-1111-4111-8111-111111111111",
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	pads := strings.Count(joined, "cmdr __column pad")
	if pads != 3 {
		t.Fatalf("expected three spacer panes (left, right, below), got %d:\n%s", pads, joined)
	}
	for _, want := range []string{
		"split-window -d -l 1 -P -F #{pane_id} -t %30 -v -c /p", // below
		"split-window -d -l 1 -P -F #{pane_id} -t %30 -h -b",    // left
		"split-window -d -l 1 -P -F #{pane_id} -t %30 -h -c /p", // right
		"set-option -p -t", // tagged @cmdr_pad
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "select-pane -d") != 3 || !strings.Contains(joined, PadOption) {
		t.Fatalf("spacers must be tagged and never focusable:\n%s", joined)
	}
}

func TestRelayoutKeepsSpacersOneCellThick(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "list-panes" {
			return []byte("@1|426|%1||cmdr __column cmdr-objectives\n" +
				"@1|426|%5|h|cmdr __column pad\n" +
				"@1|426|%6|v|cmdr __column pad\n"), nil, nil
		}
		return nil, nil, nil
	})
	if err := NewDirectorWith(mr).Relayout(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	if !strings.Contains(joined, "resize-pane -t %5 -x 1") || !strings.Contains(joined, "resize-pane -t %6 -y 1") {
		t.Fatalf("a vertical stripe is 1 wide and a horizontal one 1 tall:\n%s", joined)
	}
}

func TestBordersAreInvisibleSoNoLineSeparatesTheChatFromItsSpacers(t *testing.T) {
	mr := &MockRunner{}
	if err := NewDirectorWith(mr).applyWindowStyle(context.Background(), "%1"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"pane-border-style fg=#181825,bg=#181825",
		"pane-active-border-style fg=#181825,bg=#181825",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestSidebarBindingsInstallRelayoutHooksOnSelectAndResize(t *testing.T) {
	mr := &MockRunner{}
	if err := NewDirectorWith(mr).ensureSidebarBindings(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{"after-select-window[1]", "window-resized[0]", "__relayout", "bind-key 5 run-shell"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}
