package tmuxctl

import (
	"context"
	"errors"
	"os"
	osx "os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

func TestNewSessionUsesOpsSocket(t *testing.T) {
	t.Setenv("COMMANDER_TMUX_SOCKET", "")
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}
	if err := c.NewSessionCommand(context.Background(), "s", "/x", "cat"); err != nil {
		t.Fatal(err)
	}
	if !argvHasFlag(r.args, "-L", OpsSocket) {
		t.Errorf("want -L %s: %v", OpsSocket, r.args)
	}
}

func TestDefaultSocketOverride(t *testing.T) {
	t.Setenv("COMMANDER_TMUX_SOCKET", "default")
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}
	if err := c.NewSession(context.Background(), "s", "/x"); err != nil {
		t.Fatal(err)
	}
	if argvHasToken(r.args, "-L") {
		t.Errorf("default socket must not pass -L: %v", r.args)
	}
}

// A session that is not on the ops socket (an Operator started before the
// move) is found on the default server.
func TestSessionLookupFallsBackToDefaultServer(t *testing.T) {
	t.Setenv("COMMANDER_TMUX_SOCKET", "")
	var calls [][]string
	c := &cmdClient{runner: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, args)
		if argvHasFlag(args, "-L", OpsSocket) {
			return nil, []byte("can't find session: old"), errors.New("exit 1")
		}
		return nil, nil, nil
	}}
	ok, err := c.SessionExists(context.Background(), "old")
	if err != nil || !ok {
		t.Fatalf("SessionExists = %v, %v", ok, err)
	}
	if len(calls) != 2 || argvHasToken(calls[1], "-L") {
		t.Errorf("want ops try then default retry: %v", calls)
	}
	if err := c.KillPane(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
}

// The point of the ops socket: a keeper owns the server, so killing the
// process group of whatever created the session (cmdrd) leaves it running.
func TestOperatorSessionSurvivesParentGroupKill(t *testing.T) {
	if _, err := osx.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "tk.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("COMMANDER_TMUX_SOCKET", "ops-test")

	keeper := osx.Command("tmux", "-L", "ops-test", "-D")
	keeper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := keeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = osx.Command("tmux", "-L", "ops-test", "kill-server").Run()
		_ = keeper.Wait()
	})
	waitFor(t, func() bool { return osx.Command("tmux", "-L", "ops-test", "show-options", "-s").Run() == nil })

	// The "cmdrd": its own process group, creating the session then living on.
	parent := osx.Command("sh", "-c",
		"tmux -L ops-test new-session -d -s cmdr-op cat; sleep 60")
	parent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ok, _ := New().SessionExists(context.Background(), "cmdr-op")
		return ok
	})

	if err := syscall.Kill(-parent.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	time.Sleep(300 * time.Millisecond)

	ok, err := New().SessionExists(context.Background(), "cmdr-op")
	if err != nil || !ok {
		t.Fatalf("operator session died with its parent: ok=%v err=%v", ok, err)
	}
	out, err := ListPanes(context.Background(), "#{session_name}")
	if err != nil || !strings.Contains(out, "cmdr-op") {
		t.Errorf("ListPanes = %q, %v", out, err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// The launchd units must keep their children alive across a reload, and the
// keeper must run the same socket the code addresses.
func TestInstallUnitsMatchOpsSocket(t *testing.T) {
	for _, name := range []string{"org.commander.plist", "org.commander-tmux.plist"} {
		b, err := os.ReadFile("../../install/launchd/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "<key>AbandonProcessGroup</key>") {
			t.Errorf("%s: missing AbandonProcessGroup", name)
		}
	}
	keeper, _ := os.ReadFile("../../install/launchd/org.commander-tmux.plist")
	if !strings.Contains(string(keeper), "tmux -L "+OpsSocket+" new-session -d -s keeper") {
		t.Errorf("keeper plist does not start tmux -L %s with a keeper session", OpsSocket)
	}
	unit, _ := os.ReadFile("../../install/systemd/commander-tmux.service")
	if !strings.Contains(string(unit), "tmux -L "+OpsSocket+" new-session -d -s keeper") {
		t.Errorf("systemd keeper does not start tmux -L %s with a keeper session", OpsSocket)
	}
}

// An Operator on the ops socket and the user's cockpit on the default server, both
// private: RunOnSession reaches the Operator (send-keys, capture), ListPanes sees
// both, KillPane ends only the Operator.
func TestOperatorSessionOnOpsSocketIsReachable(t *testing.T) {
	if _, err := osx.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "tk.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("COMMANDER_TMUX_SOCKET", "ops-test")
	t.Cleanup(func() {
		testguard.KillTmuxServer(dir, "ops-test")
		testguard.KillTmuxServer(dir, "default")
	})

	ctx := context.Background()
	c := New()
	if err := c.NewSessionCommand(ctx, "op-session", dir, "cat"); err != nil {
		t.Fatal(err)
	}
	if out, err := osx.Command("tmux", "new-session", "-d", "-s", "commander", "cat").CombinedOutput(); err != nil {
		t.Fatalf("default server session: %v: %s", err, out)
	}
	// The Operator is on the ops socket only.
	if osx.Command("tmux", "has-session", "-t", "op-session").Run() == nil {
		t.Fatal("operator session leaked onto the default server")
	}

	if err := c.SendKeys(ctx, "op-session", "wake-line"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		out, _, err := RunOnSession(ctx, "capture-pane", "-p", "-t", "op-session")
		return err == nil && strings.Contains(string(out), "wake-line")
	})

	panes, err := ListPanes(ctx, "#{session_name}")
	if err != nil || !strings.Contains(panes, "op-session") || !strings.Contains(panes, "commander") {
		t.Fatalf("ListPanes = %q, %v", panes, err)
	}

	if err := c.KillPane(ctx, "op-session"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.SessionExists(ctx, "op-session"); ok {
		t.Error("operator session survived KillPane")
	}
	if osx.Command("tmux", "has-session", "-t", "commander").Run() != nil {
		t.Error("KillPane touched the default server's cockpit session")
	}
}
