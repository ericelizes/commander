// Package tmuxctl is a thin wrapper around the `tmux` CLI.
//
// Knowledge-type operator spawn (U15) runs opencode inside a detached tmux
// session instead of a Docker container. Commander shells out to `tmux` to
// create the session, wire its pane stdout into a fifo (so the U10 watchdog
// can consume nd-JSON the same way it consumes `docker logs -f` for code-type),
// send the bootstrap command, and tear the pane down on rollback.
//
// Every shape invoked here is documented in share/contracts.md.
package tmuxctl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	osx "os/exec"
	"strings"
)

// OpsSocket is the tmux socket name (tmux -L) every Operator session lives on.
// A keeper launchd/systemd agent (install/) runs `tmux -L commander-ops -D` as
// its own job, so a cmdrd restart can never take the server down with it. the user's
// own cockpit stays on the default socket. $COMMANDER_TMUX_SOCKET overrides the
// name; "default" puts new sessions back on the default server.
const OpsSocket = "commander-ops"

// SocketName is the socket new Operator sessions are created on; "" is the
// default server.
func SocketName() string {
	switch v := os.Getenv("COMMANDER_TMUX_SOCKET"); v {
	case "":
		return OpsSocket
	case "default":
		return ""
	default:
		return v
	}
}

// OpsArgs prefixes tmux args with -L <ops socket> (unchanged when the default
// server is selected).
func OpsArgs(args ...string) []string {
	if sock := SocketName(); sock != "" {
		return append([]string{"-L", sock}, args...)
	}
	return args
}

// missingServerOrSession reports a tmux failure that means "not on this
// server": the lookup may then be retried on the default server, where
// Operators started before the ops socket still live.
func missingServerOrSession(stderr []byte) bool {
	s := string(stderr)
	return strings.Contains(s, "can't find session") ||
		strings.Contains(s, "can't find pane") ||
		strings.Contains(s, "can't find window") ||
		strings.Contains(s, "no server running") ||
		strings.Contains(s, "error connecting to")
}

// runFunc abstracts process invocation so tests can capture argv without
// running a real tmux binary. Same shape as the U5 docker wrapper.
type runFunc func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// Client is the surface commander code depends on. The MockClient in mock.go
// satisfies this for downstream package tests.
type Client interface {
	// NewSession creates a detached tmux session named `name` with cwd `cwd`.
	// Argv: `tmux new-session -d -s <name> -c <cwd>`.
	NewSession(ctx context.Context, name, cwd string) error

	// NewSessionCommand is NewSession running argv directly instead of the
	// user's shell, so no rc file runs. Argv:
	// `tmux new-session -d -s <name> -c <cwd> <argv...>`.
	NewSessionCommand(ctx context.Context, name, cwd string, argv ...string) error

	// RespawnPane replaces the pane's process with argv (exec form, no shell
	// and no rc files). An existing pipe-pane survives. Argv:
	// `tmux respawn-pane -k -t <target> -c <cwd> <argv...>`.
	RespawnPane(ctx context.Context, target, cwd string, argv ...string) error

	// SendKeys types `keys` into pane `target` followed by Enter.
	// Argv: `tmux send-keys -t <target> <keys> Enter`.
	SendKeys(ctx context.Context, target, keys string) error

	// PipePane appends the pane's output to `fifoPath`. Argv:
	// `tmux pipe-pane -t <target> -O "exec cat >> '<fifoPath>'"`.
	// tmux never kills the writer: it ends on EOF when the pane closes. A
	// writer opening a real fifo blocks until a reader opens it and never sees
	// that EOF, so it outlives the pane; host Operators pipe to a regular file.
	PipePane(ctx context.Context, target, fifoPath string) error

	// KillPane kills the pane (and the session if it's the last pane). Argv:
	// `tmux kill-pane -t <target>`. Idempotent: a missing session is treated
	// as success so cleanup stacks can blindly call it.
	KillPane(ctx context.Context, target string) error

	// SessionExists returns true iff `tmux has-session -t <name>` exits 0.
	SessionExists(ctx context.Context, name string) (bool, error)
}

// cmdClient is the real Client backed by the tmux binary on PATH.
type cmdClient struct {
	runner runFunc
}

// New returns a Client that shells out to `tmux`.
func New() Client {
	return &cmdClient{runner: defaultRun}
}

// NewOnSocket returns a Client whose every call goes to the tmux server on the
// socket path (tmux -S), with no config file: the ops socket and the default
// server are never reached. For tests on a private server.
func NewOnSocket(path string) Client {
	return &cmdClient{runner: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if len(args) >= 2 && args[0] == "-L" {
			args = args[2:]
		}
		return defaultRun(ctx, name, append([]string{"-S", path, "-f", "/dev/null"}, args...)...)
	}}
}

// ExactSession is a target naming exactly the session called name. A bare
// name also matches by prefix (cmdr-a-1 finds cmdr-a-11 once cmdr-a-1 is
// gone), so anything that ends or types into a session must use this.
func ExactSession(name string) string { return "=" + name }

// ExactPane is the pane target (kill-pane, send-keys, pipe-pane) for the active
// pane of exactly the session called name: "=name" alone is not resolved as a
// session there.
func ExactPane(name string) string { return "=" + name + ":" }

// defaultRun runs name+args via os/exec, capturing stdout and stderr separately.
func defaultRun(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := osx.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// create runs a session-creating tmux command on the ops socket only.
func (c *cmdClient) create(ctx context.Context, args ...string) ([]byte, error) {
	_, stderr, err := c.runner(ctx, "tmux", OpsArgs(args...)...)
	return stderr, err
}

// onSession runs a command addressing an existing session: on the ops socket
// first, then (transition) on the default server when the session is not there.
func (c *cmdClient) onSession(ctx context.Context, args ...string) ([]byte, []byte, error) {
	return onSession(ctx, c.runner, args...)
}

func onSession(ctx context.Context, run runFunc, args ...string) ([]byte, []byte, error) {
	out, stderr, err := run(ctx, "tmux", OpsArgs(args...)...)
	if err != nil && SocketName() != "" && missingServerOrSession(stderr) {
		return run(ctx, "tmux", args...)
	}
	return out, stderr, err
}

// RunOnSession runs `tmux args...` against an Operator session wherever it
// lives (ops socket, else the default server).
func RunOnSession(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	return onSession(ctx, defaultRun, args...)
}

// ListPanes runs `tmux list-panes -a -F format` on the ops socket and the
// default server and returns both outputs joined. A server that is not running
// contributes nothing; any other failure is returned.
func ListPanes(ctx context.Context, format string) (string, error) {
	var out strings.Builder
	socks := []string{""}
	if sock := SocketName(); sock != "" {
		socks = []string{sock, ""}
	}
	for _, sock := range socks {
		args := []string{"list-panes", "-a", "-F", format}
		if sock != "" {
			args = append([]string{"-L", sock}, args...)
		}
		stdout, stderr, err := defaultRun(ctx, "tmux", args...)
		if err != nil {
			if missingServerOrSession(stderr) || strings.Contains(string(stderr), "No such file or directory") {
				continue
			}
			return "", fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(stderr)))
		}
		out.Write(stdout)
	}
	return out.String(), nil
}

// NewSession creates a detached tmux session with the supplied cwd.
func (c *cmdClient) NewSession(ctx context.Context, name, cwd string) error {
	if name == "" {
		return fmt.Errorf("tmux: NewSession requires name")
	}
	if cwd == "" {
		return fmt.Errorf("tmux: NewSession requires cwd")
	}
	stderr, err := c.create(ctx, "new-session", "-d", "-s", name, "-c", cwd)
	if err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// NewSessionCommand creates a detached session whose pane runs argv directly.
func (c *cmdClient) NewSessionCommand(ctx context.Context, name, cwd string, argv ...string) error {
	if name == "" {
		return fmt.Errorf("tmux: NewSessionCommand requires name")
	}
	if cwd == "" {
		return fmt.Errorf("tmux: NewSessionCommand requires cwd")
	}
	if len(argv) == 0 {
		return fmt.Errorf("tmux: NewSessionCommand requires a command")
	}
	args := append([]string{"new-session", "-d", "-s", name, "-c", cwd}, paneCommand(argv)...)
	stderr, err := c.create(ctx, args...)
	if err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// paneCommand removes the variables tmux synthesizes for every pane before
// starting the shell. Removing them from the session environment is insufficient.
func paneCommand(argv []string) []string {
	return append([]string{"env", "-u", "TMUX", "-u", "TMUX_PANE"}, argv...)
}

// RespawnPane kills whatever runs in the target pane and starts argv there.
func (c *cmdClient) RespawnPane(ctx context.Context, target, cwd string, argv ...string) error {
	if target == "" {
		return fmt.Errorf("tmux: RespawnPane requires target")
	}
	if cwd == "" {
		return fmt.Errorf("tmux: RespawnPane requires cwd")
	}
	if len(argv) == 0 {
		return fmt.Errorf("tmux: RespawnPane requires a command")
	}
	args := append([]string{"respawn-pane", "-k", "-t", target, "-c", cwd}, paneCommand(argv)...)
	_, stderr, err := c.onSession(ctx, args...)
	if err != nil {
		return fmt.Errorf("tmux respawn-pane: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// SendKeys types keys + Enter into the named target.
func (c *cmdClient) SendKeys(ctx context.Context, target, keys string) error {
	if target == "" {
		return fmt.Errorf("tmux: SendKeys requires target")
	}
	_, stderr, err := c.onSession(ctx, "send-keys", "-t", target, keys, "Enter")
	if err != nil {
		return fmt.Errorf("tmux send-keys: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// PipePane appends the target pane's output to fifoPath (see Client).
func (c *cmdClient) PipePane(ctx context.Context, target, fifoPath string) error {
	if target == "" {
		return fmt.Errorf("tmux: PipePane requires target")
	}
	if fifoPath == "" {
		return fmt.Errorf("tmux: PipePane requires fifoPath")
	}
	// exec: one writer process, not a shell waiting on a forked cat.
	shell := "exec cat >> '" + strings.ReplaceAll(fifoPath, "'", `'\''`) + "'"
	_, stderr, err := c.onSession(ctx, "pipe-pane", "-t", target, "-O", shell)
	if err != nil {
		return fmt.Errorf("tmux pipe-pane: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// KillPane kills the pane. A missing session is treated as success.
func (c *cmdClient) KillPane(ctx context.Context, target string) error {
	if target == "" {
		return fmt.Errorf("tmux: KillPane requires target")
	}
	_, stderr, err := c.onSession(ctx, "kill-pane", "-t", target)
	if err != nil {
		s := strings.TrimSpace(string(stderr))
		// tmux exits non-zero on missing session/pane; idempotent cleanup
		// should not surface that as an error.
		if strings.Contains(s, "can't find session") ||
			strings.Contains(s, "can't find pane") ||
			strings.Contains(s, "no server running") {
			return nil
		}
		return fmt.Errorf("tmux kill-pane: %w: %s", err, s)
	}
	return nil
}

// SessionExists returns true iff `tmux has-session -t <name>` exits 0.
func (c *cmdClient) SessionExists(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, fmt.Errorf("tmux: SessionExists requires name")
	}
	_, stderr, err := c.onSession(ctx, "has-session", "-t", name)
	if err == nil {
		return true, nil
	}
	s := strings.TrimSpace(string(stderr))
	// tmux returns exit 1 with these stderrs when the session is absent —
	// that's a definitive "no", not an error.
	if strings.Contains(s, "can't find session") ||
		strings.Contains(s, "no server running") {
		return false, nil
	}
	// Exit code without recognizable stderr — propagate.
	return false, fmt.Errorf("tmux has-session: %w: %s", err, s)
}
