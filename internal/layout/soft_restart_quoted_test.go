package layout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/testguard"
)

func TestPlainStartCommand(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"raw from old tmux", "claude -n x --resume abc", "claude -n x --resume abc"},
		{"quoted", `"claude -n x --resume abc"`, "claude -n x --resume abc"},
		{"inner quotes", `"sleep \"6\""`, `sleep "6"`},
		{"json settings", `"c --settings '{\"a\":\"b\"}'"`, `c --settings '{"a":"b"}'`},
		{"backslash stays one", `"printf \"a\\nb\""`, `printf "a\nb"`},
		{"dollar", `"c \$HOME \${X} $(true) $1"`, `c $HOME ${X} $(true) $1`},
		{"dollar, ubuntu 3.4", `"c \\$HOME \\${X} \\$_y $1"`, `c $HOME ${X} $_y $1`},
		{"backslash before dollar name", `"c \\\\$H \\\$H"`, `c \$H \$H`},
		{"two backslashes before dollar name", `"c \\\\\\$H"`, `c \\$H`},
		{"backslash before dollar digit", `"c \\$1"`, `c \$1`},
		{"leading tilde", `"\~/bin/c --x"`, "~/bin/c --x"},
		{"tab newline", `"a b\tc\nd"`, "a b\tc\nd"},
		{"other control byte and non-ascii", `"a \033 é"`, "a \x1b é"},
		{"empty quotes", `""`, ""},
		{"two raw quoted words", `"a" "b"`, `"a" "b"`},
		{"unknown escape is raw", `"a\qb c"`, `"a\qb c"`},
		{"short octal is raw", `"a \03"`, `"a \03"`},
		{"octal above a byte is raw", `"a \477"`, `"a \477"`},
		{"dangling backslash is raw", `"a b\"`, `"a b\"`},
		{"lone quote", `"`, `"`},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := plainStartCommand(c.in); got != c.want {
			t.Errorf("%s: plainStartCommand(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// quotedPanes is softPanes as tmux 3.2+ prints it.
func quotedPanes() string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(softPanes), "\n") {
		i := strings.LastIndex(line, "|") + 1
		out = append(out, line[:i]+`"`+line[i:]+`"`)
	}
	return strings.Join(out, "\n") + "\n"
}

func TestSoftRestart_QuotedStartCommandIsNotAChange(t *testing.T) {
	d, mr, buf := softFixture(t, quotedPanes())
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	for _, c := range mr.CallStrings() {
		if strings.HasPrefix(c, "tmux respawn-pane -k -t %2") || strings.HasPrefix(c, "tmux respawn-pane -k -t %5") {
			t.Fatalf("a chat whose quoted command is unchanged must keep running: %s", c)
		}
	}
	if !strings.Contains(buf.String(), "restarted 0 chat panes") {
		t.Fatalf("progress = %s", buf.String())
	}
}

func TestSoftRestart_QuotedStartCommandThatChangedStillRestarts(t *testing.T) {
	d, mr, _ := softFixture(t, strings.Replace(quotedPanes(), "ses_old", "ses_stale", 1))
	if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
		t.Fatal(err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux respawn-pane -k -t %2 opencode --session ses_old --agent commander")
}

func TestSoftRestart_LeavesAQuotedNativeChatPlaceholderAlone(t *testing.T) {
	panes := strings.Replace(quotedPanes(), "opencode --session ses_old --agent commander",
		`CMDR_NATIVE_CHAT=commander sh -c 'printf \"x\\n\"; exec tail -f /dev/null'`, 1)
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

// startPrivateCockpit starts a real tmux server on a private socket whose
// "commander" session runs command in its only pane and keeps the pane once the
// command has exited. PATH is an empty dir, so no real agent can start. It
// returns a runner for that socket and nothing else.
func startPrivateCockpit(t *testing.T, command string) func(args ...string) ([]byte, error) {
	t.Helper()
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "sr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	testguard.NoLeakedProcesses(t, dir)
	socket := testguard.TmuxSocket(dir, "s")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "conf")
	if err := os.WriteFile(conf, []byte("set -g remain-on-exit on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(tmuxBin, append([]string{"-S", socket, "-f", conf}, args...)...)
		cmd.Env = []string{"PATH=" + dir, "SHELL=/bin/sh", "HOME=" + dir, "LC_ALL=C.UTF-8"}
		return cmd.Output()
	}
	if _, err := run("new-session", "-d", "-s", CommanderSessionName, "-n", FleetWindowName, command); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testguard.KillTmuxServer(dir, "s") })
	return run
}

// Real tmux, whatever its version, on a private socket: what it prints for a
// command must read back as that command.
func TestPlainStartCommand_RoundTripsThroughRealTmux(t *testing.T) {
	commands := []string{
		agentrt.ClaudeInteractiveCmd("claude", "manager-x", "sonnet", "abc"),
		`CMDR_NATIVE_CHAT=manager-x sh -c 'printf "line\nnext\n"; exec tail -f /dev/null'`,
		"opencode --session ses_1 --agent commander",
		`false 'a\b' "c" $HOME ${X} $(true) #y %z é ; false`,
		`false \$H \\$H \$1 $1 $_x "$HOME" \ ; false`,
	}
	for _, want := range commands {
		run := startPrivateCockpit(t, want)
		out, err := run("list-panes", "-t", CommanderSessionName, "-F", "#{pane_start_command}")
		if err != nil {
			t.Fatal(err)
		}
		raw := strings.TrimSuffix(string(out), "\n")
		if got := plainStartCommand(raw); got != want {
			t.Errorf("tmux printed %q\n read back %q\n      want %q", raw, got, want)
		}
	}
}

// The whole soft restart against what a real tmux lists. Only list-panes reaches
// tmux; every other call, respawns included, is recorded and not executed.
func TestSoftRestart_AgainstRealTmuxStartCommands(t *testing.T) {
	cases := []struct {
		name, command string
		respawn       string
	}{
		{"unchanged chat stays", "opencode --session ses_old --agent commander", ""},
		{"native placeholder stays", `CMDR_NATIVE_CHAT=commander sh -c 'printf "x\n"; exec tail -f /dev/null'`, ""},
		{"changed chat restarts", "opencode --session ses_stale --agent commander", "tmux respawn-pane -k -t %0 opencode --session ses_old --agent commander"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := startPrivateCockpit(t, c.command)
			d, mr, _ := softFixture(t, "")
			mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
				if args[0] == "list-panes" {
					out, err := run(args...)
					return out, nil, err
				}
				return []byte("%99\n"), nil, nil
			})
			if err := d.SoftRestart(context.Background(), os.Getenv("COMMANDER_HOME"), false); err != nil {
				t.Fatal(err)
			}
			var respawns []string
			for _, call := range mr.CallStrings() {
				if strings.HasPrefix(call, "tmux respawn-pane") {
					respawns = append(respawns, call)
				}
			}
			if c.respawn == "" && len(respawns) != 0 {
				t.Fatalf("the pane must keep running: %v", respawns)
			}
			if c.respawn != "" && (len(respawns) != 1 || respawns[0] != c.respawn) {
				t.Fatalf("respawns = %v, want %q", respawns, c.respawn)
			}
		})
	}
}
