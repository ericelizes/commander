package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// captureExec returns an execFunc that records its argv into the given
// slice and returns canned stdout/stderr/err.
func captureExec(captured *[]string, stdout, stderr []byte, retErr error) execFunc {
	return func(_ context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		got := append([]string{name}, args...)
		*captured = append([]string{}, got...)
		*captured = append(*captured, "cwd="+dir)
		return stdout, stderr, retErr
	}
}

func TestSetState_Argv(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, nil, nil)}

	err := c.SetState(context.Background(), "/tmp/p", "bd-1", "cmdr", "brainstorming", "operator spawned")
	if err != nil {
		t.Fatalf("SetState: %v", err)
	}

	want := []string{"br", "set-state", "bd-1", "cmdr=brainstorming", "--reason", "operator spawned", "cwd=/tmp/p"}
	assertArgv(t, got, want)
}

func TestSetState_NoReason_OmitsFlag(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, nil, nil)}

	if err := c.SetState(context.Background(), "/tmp/p", "bd-2", "cmdr", "done", ""); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	want := []string{"br", "set-state", "bd-2", "cmdr=done", "cwd=/tmp/p"}
	assertArgv(t, got, want)

	for _, a := range got {
		if a == "--reason" {
			t.Fatalf("--reason flag should be omitted when reason is empty: %v", got)
		}
	}
}

func TestSetState_ValidatesInput(t *testing.T) {
	c := &cmdClient{run: func(_ context.Context, _, _ string, _ ...string) ([]byte, []byte, error) {
		t.Fatal("exec should not run when input is invalid")
		return nil, nil, nil
	}}

	if err := c.SetState(context.Background(), "/tmp/p", "", "cmdr", "done", ""); err == nil {
		t.Error("expected error for empty issueID")
	}
	if err := c.SetState(context.Background(), "/tmp/p", "bd-1", "", "done", ""); err == nil {
		t.Error("expected error for empty dimension")
	}
	if err := c.SetState(context.Background(), "/tmp/p", "bd-1", "cmdr", "", ""); err == nil {
		t.Error("expected error for empty value")
	}
}

func TestSetState_PropagatesExecError(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, []byte("nope"), errors.New("exit 1"))}

	err := c.SetState(context.Background(), "/tmp/p", "bd-1", "cmdr", "done", "")
	if err == nil {
		t.Fatal("expected error from exec failure")
	}
}

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv len mismatch:\n got=%v\nwant=%v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] mismatch:\n got=%v\nwant=%v", i, got, want)
		}
	}
}

func TestSetState_FallsBackToLabelsWhenBrHasNoSetState(t *testing.T) {
	var calls [][]string
	c := &cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string{}, args...))
		switch args[0] {
		case "set-state":
			return nil, []byte("error: unrecognized subcommand 'set-state'"), errors.New("exit status 2")
		case "label":
			return []byte(`["kind:task","cmdr:brainstorming"]`), nil, nil
		}
		return nil, nil, nil
	}}
	if err := c.SetState(context.Background(), "/tmp/p", "bd-1", "cmdr", "executing", "go"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	var joined string
	for _, a := range calls {
		joined += "|" + fmt.Sprint(a)
	}
	for _, want := range []string{"[update bd-1 --remove-label cmdr:brainstorming]", "[update bd-1 --add-label cmdr:executing]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	if strings.Contains(joined, "remove-label kind:task") {
		t.Error("only the cmdr dimension is replaced")
	}
}
