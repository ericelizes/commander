package tmuxctl

import (
	"context"
	"errors"
	"testing"
)

// recorder captures argv from one call so tests can assert on it.
type recorder struct {
	args   []string
	stdout []byte
	stderr []byte
	err    error
}

func (r *recorder) fn() runFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		r.args = append([]string{name}, args...)
		return r.stdout, r.stderr, r.err
	}
}

// argvHasFlag returns true iff `flag value` appears as adjacent tokens.
func argvHasFlag(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func argvHasToken(args []string, tok string) bool {
	for _, a := range args {
		if a == tok {
			return true
		}
	}
	return false
}

func TestNewSession_Argv(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}

	if err := c.NewSession(context.Background(), "cmdr-proj-deadbeef", "/host/proj"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if r.args[0] != "tmux" {
		t.Errorf("argv[0]: got %q want tmux", r.args[0])
	}
	if !argvHasToken(r.args, "new-session") {
		t.Errorf("missing new-session: %v", r.args)
	}
	if !argvHasToken(r.args, "-d") {
		t.Errorf("missing -d (detached): %v", r.args)
	}
	if !argvHasFlag(r.args, "-s", "cmdr-proj-deadbeef") {
		t.Errorf("missing -s <name>: %v", r.args)
	}
	if !argvHasFlag(r.args, "-c", "/host/proj") {
		t.Errorf("missing -c <cwd>: %v", r.args)
	}
}

func TestNewSession_Validation(t *testing.T) {
	c := &cmdClient{runner: (&recorder{}).fn()}
	if err := c.NewSession(context.Background(), "", "/x"); err == nil {
		t.Error("expected empty-name error")
	}
	if err := c.NewSession(context.Background(), "n", ""); err == nil {
		t.Error("expected empty-cwd error")
	}
}

func TestSendKeys_Argv(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}

	if err := c.SendKeys(context.Background(), "session-x", "opencode run --agent operator-knowledge"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	if !argvHasToken(r.args, "send-keys") {
		t.Errorf("missing send-keys: %v", r.args)
	}
	if !argvHasFlag(r.args, "-t", "session-x") {
		t.Errorf("missing -t <target>: %v", r.args)
	}
	// Last arg is "Enter" (sent as a literal key).
	if r.args[len(r.args)-1] != "Enter" {
		t.Errorf("argv tail: got %q want Enter", r.args[len(r.args)-1])
	}
	// Keys appear immediately before Enter.
	if r.args[len(r.args)-2] != "opencode run --agent operator-knowledge" {
		t.Errorf("keys at -2: got %q", r.args[len(r.args)-2])
	}
}

func TestPipePane_Argv(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}

	if err := c.PipePane(context.Background(), "session-x", "/host/proj/.commander/operators/abc.fifo"); err != nil {
		t.Fatalf("PipePane: %v", err)
	}
	if !argvHasToken(r.args, "pipe-pane") {
		t.Errorf("missing pipe-pane: %v", r.args)
	}
	if !argvHasFlag(r.args, "-t", "session-x") {
		t.Errorf("missing -t <target>: %v", r.args)
	}
	if !argvHasToken(r.args, "-O") {
		t.Errorf("missing -O (open append): %v", r.args)
	}
	// Final arg is the shell command appending to the fifo.
	last := r.args[len(r.args)-1]
	if last != "exec cat >> '/host/proj/.commander/operators/abc.fifo'" {
		t.Errorf("shell cmd: got %q", last)
	}
}

func TestPipePane_Validation(t *testing.T) {
	c := &cmdClient{runner: (&recorder{}).fn()}
	if err := c.PipePane(context.Background(), "", "/f"); err == nil {
		t.Error("expected empty-target error")
	}
	if err := c.PipePane(context.Background(), "t", ""); err == nil {
		t.Error("expected empty-fifo error")
	}
}

func TestKillPane_Argv(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}
	if err := c.KillPane(context.Background(), "session-x"); err != nil {
		t.Fatalf("KillPane: %v", err)
	}
	if !argvHasToken(r.args, "kill-pane") {
		t.Errorf("missing kill-pane: %v", r.args)
	}
	if !argvHasFlag(r.args, "-t", "session-x") {
		t.Errorf("missing -t <target>: %v", r.args)
	}
}

func TestKillPane_IgnoresMissingSession(t *testing.T) {
	r := &recorder{
		stderr: []byte("can't find session: session-gone\n"),
		err:    errors.New("exit status 1"),
	}
	c := &cmdClient{runner: r.fn()}
	if err := c.KillPane(context.Background(), "session-gone"); err != nil {
		t.Errorf("KillPane should swallow missing-session error: %v", err)
	}
}

func TestKillPane_PropagatesUnknownError(t *testing.T) {
	r := &recorder{
		stderr: []byte("permission denied\n"),
		err:    errors.New("exit status 1"),
	}
	c := &cmdClient{runner: r.fn()}
	if err := c.KillPane(context.Background(), "session-x"); err == nil {
		t.Error("KillPane should surface unrecognized errors")
	}
}

func TestSessionExists_True(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}
	ok, err := c.SessionExists(context.Background(), "session-x")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if !ok {
		t.Error("expected exists=true on exit 0")
	}
	if !argvHasToken(r.args, "has-session") {
		t.Errorf("missing has-session: %v", r.args)
	}
	if !argvHasFlag(r.args, "-t", "session-x") {
		t.Errorf("missing -t <target>: %v", r.args)
	}
}

func TestSessionExists_FalseOnAbsent(t *testing.T) {
	r := &recorder{
		stderr: []byte("can't find session: nope\n"),
		err:    errors.New("exit status 1"),
	}
	c := &cmdClient{runner: r.fn()}
	ok, err := c.SessionExists(context.Background(), "nope")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if ok {
		t.Error("expected exists=false")
	}
}

func TestSessionExists_FalseWhenNoServer(t *testing.T) {
	r := &recorder{
		stderr: []byte("no server running on /tmp/tmux-1000/default\n"),
		err:    errors.New("exit status 1"),
	}
	c := &cmdClient{runner: r.fn()}
	ok, err := c.SessionExists(context.Background(), "x")
	if err != nil {
		t.Fatalf("SessionExists: %v", err)
	}
	if ok {
		t.Error("expected exists=false when no server")
	}
}

func TestMockClient_RecordsCalls(t *testing.T) {
	m := &MockClient{}
	_ = m.NewSession(context.Background(), "n", "/c")
	_ = m.PipePane(context.Background(), "n", "/f")
	_ = m.SendKeys(context.Background(), "n", "k")
	_ = m.KillPane(context.Background(), "n")

	want := []string{
		"NewSession(name=n cwd=/c)",
		"PipePane(target=n fifo=/f)",
		"SendKeys(target=n keys=k)",
		"KillPane(target=n)",
	}
	if len(m.Calls) != len(want) {
		t.Fatalf("Calls: got %d want %d (%v)", len(m.Calls), len(want), m.Calls)
	}
	for i, w := range want {
		if m.Calls[i] != w {
			t.Errorf("Calls[%d]: got %q want %q", i, m.Calls[i], w)
		}
	}
}
