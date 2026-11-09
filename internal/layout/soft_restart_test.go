package layout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

func TestLaunchArgvChanged(t *testing.T) {
	base := "claude -n manager-x --append-system-prompt-file '/p/manager-x.md' --model sonnet --disallowedTools 'mcp__a'"
	cases := []struct {
		name            string
		current, wanted string
		changed         bool
	}{
		{"identical", base + " --session-id abc", base + " --session-id abc", false},
		{"fresh id becomes resume", base + " --session-id abc", base + " --resume abc", false},
		{"new flag", base + " --resume abc", base + " --newflag --resume abc", true},
		// Adding the mail-hook --settings flag changes the launch argv, so the
		// first soft restart after this upgrade restarts every Claude chat once
		// (resuming its session). SetKeepChats defers that to the next restart.
		{"mail hook settings added", base + " --resume abc", base + ` --settings '{"hooks":{}}' --resume abc`, true},
		{"mail hook settings unchanged", base + ` --settings '{"hooks":{}}' --session-id abc`, base + ` --settings '{"hooks":{}}' --resume abc`, false},
		{"different session", base + " --resume abc", base + " --resume xyz", true},
		{"different model", base + " --resume abc", strings.Replace(base, "sonnet", "opus", 1) + " --resume abc", true},
		{"opencode unchanged", "opencode --session ses_1 --agent commander", "opencode --session ses_1 --agent commander", false},
		{"opencode session changed", "opencode --session ses_1 --agent commander", "opencode --session ses_2 --agent commander", true},
	}
	for _, c := range cases {
		if got := launchArgvChanged(c.current, c.wanted); got != c.changed {
			t.Errorf("%s: changed = %v, want %v", c.name, got, c.changed)
		}
	}
}

func softFixture(t *testing.T, panes string) (*Director, *MockRunner, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, CommanderSessionFileName), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mr := &MockRunner{}
	mr.SetSessionExists(CommanderSessionName, true)
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			return nil, nil, nil
		case "list-panes":
			return []byte(panes), nil, nil
		case "list-windows":
			return []byte(""), nil, nil
		}
		return []byte("%99\n"), nil, nil
	})
	d := NewDirectorWith(mr)
	var buf bytes.Buffer
	d.SetProgressWriter(&buf)
	return d, mr, &buf
}

// window_id|window_name|@cmdr_ws|pane_id|@cmdr_chat|pane_start_command
const softPanes = "@1|commander||%1|||0|cmdr __column cmdr-objectives\n" +
	"@1|commander||%2|1||0|opencode --session ses_old --agent commander\n" +
	"@1|commander||%3|||0|cmdr __column cmdr-fleet\n" +
	"@2|#2 Observability|ws-b|%4|||0|cmdr __column cmdr-objectives\n" +
	"@2|#2 Observability|ws-b|%5|1||0|claude -n manager-ws-b --append-system-prompt-file '/p/m.md' --resume abc\n" +
	"@2|#2 Observability|ws-b|%6|||0|cmdr __column lead-fleet --project p\n"

func TestSoftRestart_ReloadsColumnsAndKeepsRunningChats(t *testing.T) {
	d, mr, buf := softFixture(t, softPanes+"@1|commander||%7|||0|cmdr __column chat-title\n@2|#2 Observability|ws-b|%8|||0|cmdr __column chat-title\n")
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	calls := mr.CallStrings()
	for _, pane := range []string{"%1", "%4", "%6", "%7", "%8"} {
		mustContainCall(t, calls, "tmux respawn-pane -k -t "+pane)
	}
	// The Commander window's legacy fleet column becomes the Inspector.
	mustContainCall(t, calls, "tmux respawn-pane -k -t %3 cmdr __column inspector")
	for _, c := range calls {
		if strings.Contains(c, "kill-session") || strings.Contains(c, "kill-window") {
			t.Fatalf("a soft restart never kills the session or a window: %s", c)
		}
		if strings.HasPrefix(c, "tmux respawn-pane -k -t %2") || strings.HasPrefix(c, "tmux respawn-pane -k -t %5") {
			t.Fatalf("a chat whose launch command is unchanged must keep running: %s", c)
		}
	}
	if !strings.Contains(buf.String(), "Reloaded 6 column panes; restarted 0 chat panes.") {
		t.Fatalf("progress = %s", buf.String())
	}
}

func TestSoftRestart_RestartsOnlyAChatWhoseLaunchCommandChanged(t *testing.T) {
	// The Commander chat was started with an older session id than the file says now.
	panes := strings.Replace(softPanes, "ses_old", "ses_stale", 1)
	d, mr, buf := softFixture(t, panes)
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux respawn-pane -k -t %2 opencode --session ses_old --agent commander")
	for _, c := range calls {
		if strings.HasPrefix(c, "tmux respawn-pane -k -t %5 ") {
			t.Fatalf("the Manager chat is unchanged and must keep running: %s", c)
		}
	}
	if !strings.Contains(buf.String(), "restarted 1 chat panes") {
		t.Fatalf("progress = %s", buf.String())
	}
}

func TestSoftRestart_LeavesANativeChatPlaceholderAlone(t *testing.T) {
	// The Commander chat was handed over to cmdrd's native engine: its pane runs
	// the placeholder. Restarting the TUI there would fork the session.
	panes := strings.Replace(softPanes, "opencode --session ses_old --agent commander",
		"CMDR_NATIVE_CHAT=commander sh -c 'printf x; exec tail -f /dev/null'", 1)
	d, mr, _ := softFixture(t, panes)
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	for _, c := range mr.CallStrings() {
		if strings.HasPrefix(c, "tmux respawn-pane -k -t %2") {
			t.Fatalf("the native chat placeholder must not be respawned: %s", c)
		}
	}
}

func TestSoftRestart_RestoresAMissingSidebarAndTitleBar(t *testing.T) {
	// Commander window with only chat + context (the sidebar pane was closed) and no title bar.
	panes := "@1|commander||%2|1||0|opencode --session ses_old --agent commander\n" +
		"@1|commander||%3|||0|cmdr __column cmdr-fleet\n"
	d, mr, _ := softFixture(t, panes)
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"split-window -h -b -d -P -F #{pane_id} -t %2 -p 25",
		"cmdr __column cmdr-objectives",
		"split-window -v -b -d -l 4 -P -F #{pane_id} -t %2",
		"cmdr __column chat-title",
		"set-option -w -t %2 @cmdr_title Commander",
		"set-option -w -t %2 @cmdr_kind commander",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestSoftRestart_WithNoSessionIsAnOrdinaryLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, CommanderSessionFileName), []byte("ses_old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mr := &MockRunner{}
	hasSession := 0
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "has-session":
			hasSession++
			return nil, []byte("can't find session: commander"), errExit1
		case "new-session":
			return []byte("%1\n"), nil, nil
		}
		return []byte("%2\n"), nil, nil
	})
	if err := NewDirectorWith(mr).SoftRestart(context.Background(), home, false); err != nil {
		t.Fatal(err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+os.Getenv("COMMANDER_WORKSPACE_ROOT")+" opencode --session ses_old --agent commander")
}

func TestSoftRestart_KeepChatsNeverRespawnsAChatEvenIfItsLaunchCommandChanged(t *testing.T) {
	panes := strings.Replace(softPanes, "ses_old", "ses_stale", 1)
	d, mr, buf := softFixture(t, panes)
	d.SetKeepChats(true)
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	for _, c := range mr.CallStrings() {
		if strings.HasPrefix(c, "tmux respawn-pane -k -t %2 ") || strings.HasPrefix(c, "tmux respawn-pane -k -t %5 ") {
			t.Fatalf("--keep-chats must leave chats alone: %s", c)
		}
	}
	if !strings.Contains(buf.String(), "Leaving the chat in") || !strings.Contains(buf.String(), "restarted 0 chat panes") {
		t.Fatalf("progress = %s", buf.String())
	}
	// Chrome is still reloaded.
	mustContainCall(t, mr.CallStrings(), "tmux respawn-pane -k -t %1")
}

func TestSoftRestart_RestylesEveryWindowWithSolidBackgrounds(t *testing.T) {
	d, mr, _ := softFixture(t, softPanes)
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"set-option -w -t %2 window-style bg=#181825",
		"set-option -p -t %2 window-style bg=#1e1e2e",
		"set-option -w -t %5 window-active-style bg=#181825",
		"set-option -p -t %5 window-active-style bg=#1e1e2e",
		"set-option -w -t %2 pane-border-style fg=#181825,bg=#181825", // invisible borders
		"set-option -w -t %2 pane-active-border-style fg=#181825,bg=#181825",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestSoftRestart_InsideTmuxDoesNotChangeTheViewersWindow(t *testing.T) {
	d, mr, _ := softFixture(t, softPanes)
	d.insideTmuxFn = func() bool { return true }
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), true); err != nil {
		t.Fatal(err)
	}
	for _, c := range mr.CallStrings() {
		if strings.HasPrefix(c, "tmux select-window") || strings.Contains(c, "switch-client") {
			t.Fatalf("a soft restart run inside the cockpit must not move the viewer: %s", c)
		}
	}
}

func TestSoftRestart_RemovesAStraySpacerAboveTheChatAndKeepsTheRest(t *testing.T) {
	// window_id|name|ws|pane|chat|pad|top|cmd
	panes := "@1|commander||%1|||0|cmdr __column cmdr-objectives\n" +
		"@1|commander||%9||v|3|cmdr __column pad\n" + // above the chat: stray
		"@1|commander||%2|1||5|opencode --session ses_old --agent commander\n" +
		"@1|commander||%8||v|74|cmdr __column pad\n" + // below: keep
		"@1|commander||%3|||0|cmdr __column cmdr-fleet\n"
	d, mr, _ := softFixture(t, panes)
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	calls := mr.CallStrings()
	mustContainCall(t, calls, "tmux kill-pane -t %9")
	for _, c := range calls {
		if strings.HasPrefix(c, "tmux kill-pane -t %8") || strings.HasPrefix(c, "tmux kill-pane -t %2") {
			t.Fatalf("only the stray top spacer goes: %s", c)
		}
	}
}

func TestSoftRestart_ReplacesTheLegacyFleetColumnWithTheInspector(t *testing.T) {
	// window_id|name|ws|pane|chat|pad|top|cmd
	panes := "@1|commander||%1|||0|cmdr __column cmdr-objectives\n" +
		"@1|commander||%2|1||3|opencode --session ses_old --agent commander\n" +
		"@1|commander||%3|||0|cmdr __column cmdr-fleet\n" +
		"@2|#2 Obs|ws-b|%4|||0|cmdr __column cmdr-objectives\n" +
		"@2|#2 Obs|ws-b|%5|1||3|claude -n manager-ws-b --resume abc\n" +
		"@2|#2 Obs|ws-b|%6|||0|cmdr __column lead-fleet --project p\n" +
		"@3|op-x||%7|||0|cmdr __column operator-side --task t\n"
	d, mr, _ := softFixture(t, panes)
	SetWorkstreamWindowsFn(func(context.Context) ([]WorkstreamWindow, error) {
		return []WorkstreamWindow{{ProjectName: "example-app", BeadID: "ws-b", Title: "Obs", AgentName: "manager-ws-b", SessionID: "abc"}}, nil
	})
	t.Cleanup(func() { SetWorkstreamWindowsFn(nil) })
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(mr.CallStrings(), "\n")
	for _, want := range []string{
		"respawn-pane -k -t %3 cmdr __column inspector",
		"respawn-pane -k -t %6 cmdr __column inspector --ws ws-b --project example-app",
		"respawn-pane -k -t %7\n", // operator windows keep their side column untouched
	} {
		if !strings.Contains(joined+"\n", want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// One skipped workstream (a nested display row, a busy read) must not leave
// every other Manager's chat as it is: the windows that did come back are used.
func TestSoftRestart_SkippedWorkstreamKeepsTheOthers(t *testing.T) {
	d, mr, buf := softFixture(t, softPanes)
	old := workstreamWindowsFn
	t.Cleanup(func() { workstreamWindowsFn = old })
	SetWorkstreamWindowsFn(func(context.Context) ([]WorkstreamWindow, error) {
		return []WorkstreamWindow{{BeadID: "ws-b", AgentName: "manager-ws-b", SessionID: "new-session", Title: "Observability"}},
			errors.New("skipped ws-b.3: ws-b.3 is nested under ws-b, not a workstream")
	})
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	restarted := false
	for _, c := range mr.CallStrings() {
		if strings.HasPrefix(c, "tmux respawn-pane -k -t %5 ") && strings.Contains(c, "new-session") {
			restarted = true
		}
	}
	if !restarted {
		t.Fatalf("the valid Manager's chat was left alone:\n%s", strings.Join(mr.CallStrings(), "\n"))
	}
	if !strings.Contains(buf.String(), "Some workstream Managers were skipped") || strings.Contains(buf.String(), "Could not list workstreams") {
		t.Fatalf("progress = %s", buf.String())
	}
}

const carriedSession = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"

// carryFixture is a cockpit whose Commander pane runs the old launch form (no
// clearing of CLAUDE_CONFIG_DIR), so SoftRestart restarts it.
func carryFixture(t *testing.T, tmuxEnv string) (d *Director, mr *MockRunner, claudeDir string) {
	t.Helper()
	claudeDir = useClaudeRuntime(t)
	panes := "@1|commander||%1|||0|cmdr __column cmdr-objectives\n" +
		"@1|commander||%2|1||0|DISABLE_AUTOUPDATER=1 claude -n commander --resume " + carriedSession + "\n" +
		"@1|commander||%3|||0|cmdr __column inspector\n"
	d, mr, _ = softFixture(t, panes)
	base := mr.runFunc
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "show-environment" {
			return []byte(tmuxEnv), nil, nil
		}
		return base(args)
	})
	home := os.Getenv("COMMANDER_HOME")
	if err := os.WriteFile(filepath.Join(home, claudeCommanderSessionFileName), []byte(carriedSession+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d, mr, claudeDir
}

func writeSessionFile(t *testing.T, configDir, text string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(configDir, "projects", "-work", carriedSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func respawnOf(mr *MockRunner, pane string) string {
	for _, c := range mr.CallStrings() {
		if strings.Contains(c, "respawn-pane -k -t "+pane+" ") {
			return c
		}
	}
	return ""
}

func TestSoftRestartCarriesTheTranscriptOfARestartedPane(t *testing.T) {
	inherited := t.TempDir()
	for _, tc := range []struct {
		name        string
		viaTmux     bool // the tmux server has the variable; cmdr itself does not
		defaultCopy time.Duration
		inherited   time.Duration
		want        string
	}{
		{name: "only in the inherited dir", inherited: time.Hour, want: "inherited"},
		{name: "only in the tmux server's dir", viaTmux: true, inherited: time.Hour, want: "inherited"},
		{name: "a newer inherited copy wins", defaultCopy: 2 * time.Hour, inherited: time.Hour, want: "inherited"},
		{name: "an older inherited copy never rewinds", defaultCopy: time.Hour, inherited: 2 * time.Hour, want: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := ""
			if tc.viaTmux {
				env = "CLAUDE_CONFIG_DIR=" + inherited + "\n"
			}
			d, mr, claudeDir := carryFixture(t, env)
			if !tc.viaTmux {
				t.Setenv("CLAUDE_CONFIG_DIR", inherited)
			}
			writeSessionFile(t, inherited, "inherited\n", tc.inherited)
			if tc.defaultCopy > 0 {
				writeSessionFile(t, claudeDir, "default\n", tc.defaultCopy)
			} else {
				os.RemoveAll(filepath.Join(claudeDir, "projects"))
			}

			if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
				t.Fatal(err)
			}

			call := respawnOf(mr, "%2")
			if !strings.HasSuffix(call, " --resume "+carriedSession) || !strings.Contains(call, "env -u CLAUDE_CONFIG_DIR ") {
				t.Fatalf("restart does not resume on default: %q", call)
			}
			got, err := os.ReadFile(filepath.Join(claudeDir, "projects", "-work", carriedSession+".jsonl"))
			if err != nil || string(got) != tc.want+"\n" {
				t.Fatalf("~/.claude holds %q (%v), want %s", got, err, tc.want)
			}
		})
	}
}

// A chat SoftRestart leaves running keeps writing where it is, so nothing is
// carried for it: not with --keep-chats, not when its command is unchanged.
func TestSoftRestartCarriesNothingForAPaneLeftRunning(t *testing.T) {
	check := func(t *testing.T, d *Director, mr *MockRunner, claudeDir string) {
		t.Helper()
		inherited := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", inherited)
		writeSessionFile(t, inherited, "inherited\n", time.Hour)
		if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
			t.Fatal(err)
		}
		if call := respawnOf(mr, "%2"); call != "" {
			t.Errorf("pane restarted: %s", call)
		}
		if _, err := os.Stat(filepath.Join(claudeDir, "projects", "-work", carriedSession+".jsonl")); err == nil {
			t.Errorf("carried a transcript for a pane left running")
		}
	}
	t.Run("keep chats", func(t *testing.T) {
		d, mr, claudeDir := carryFixture(t, "")
		d.SetKeepChats(true)
		check(t, d, mr, claudeDir)
	})
	t.Run("command unchanged", func(t *testing.T) {
		d, mr, claudeDir := carryFixture(t, "")
		current := d.claudeChatCmd(claudeChat{"commander", agentrt.Model(agentrt.TierCommander), carriedSession})
		base := mr.runFunc
		mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
			if args[0] == "list-panes" {
				return []byte("@1|commander||%1|||0|cmdr __column cmdr-objectives\n@1|commander||%2|1||0|" + current + "\n@1|commander||%3|||0|cmdr __column inspector\n"), nil, nil
			}
			return base(args)
		})
		check(t, d, mr, claudeDir)
	})
}

// A carry that fails must not trade a working pane for a lost conversation.
func TestSoftRestartLeavesThePaneRunningWhenTheCarryFails(t *testing.T) {
	d, mr, claudeDir := carryFixture(t, "")
	inherited := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	writeSessionFile(t, inherited, "inherited\n", time.Hour)
	// A file where the projects dir belongs: the copy cannot be made.
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "projects"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	if call := respawnOf(mr, "%2"); call != "" {
		t.Errorf("pane restarted without its transcript: %s", call)
	}
}

// A new window is a real launch too.
func TestLaunchCommanderCarriesTheTranscriptBeforeTheNewSession(t *testing.T) {
	claudeDir := useClaudeRuntime(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, claudeCommanderSessionFileName), []byte(carriedSession+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inherited := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	writeSessionFile(t, inherited, "inherited\n", time.Hour)
	mr := paneRunner("%1")
	if err := NewDirectorWith(mr).LaunchCommander(context.Background(), home, false); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range mr.CallStrings() {
		if strings.HasPrefix(c, "tmux new-session") && strings.HasSuffix(c, " --resume "+carriedSession) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no new-session resuming the carried session: %v", mr.CallStrings())
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "projects", "-work", carriedSession+".jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureWorkstreamWindowCarriesTheTranscript(t *testing.T) {
	claudeDir := useClaudeRuntime(t)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	inherited := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	writeSessionFile(t, inherited, "inherited\n", time.Hour)
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if args[0] == "new-window" || args[0] == "split-window" {
			return []byte("%30\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)
	_, _, err := d.EnsureWorkstreamWindow(context.Background(), WorkstreamWindow{BeadID: "ws-1", Title: "Thing", AgentName: "manager-ws-1", SessionID: carriedSession, ProjectPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(mr.CallStrings(), "\n"), " --resume "+carriedSession) {
		t.Errorf("window did not resume: %v", mr.CallStrings())
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "projects", "-work", carriedSession+".jsonl")); err != nil {
		t.Fatal(err)
	}
}
