package layout

import (
	"context"
	"strings"
	"testing"
)

type scriptedRunner struct {
	calls  []string
	stdout string
}

func (r *scriptedRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	return []byte(r.stdout), nil, nil
}

func TestChatPanePrefersTaggedPane(t *testing.T) {
	r := &scriptedRunner{stdout: "%4 \n%5 1\n%6 \n"}
	d := NewDirectorWith(r)
	got, err := d.chatPane(context.Background(), "op-x")
	if err != nil || got != "%5" {
		t.Fatalf("chatPane = %q, %v", got, err)
	}
	if r.calls[0] != "list-panes -t commander:op-x -F #{pane_id} #{@cmdr_chat}" {
		t.Fatalf("argv = %q", r.calls[0])
	}
}

func TestChatPaneFallsBackToFirstForUntaggedWindows(t *testing.T) {
	r := &scriptedRunner{stdout: "%4 \n%5 \n"}
	got, err := NewDirectorWith(r).chatPane(context.Background(), "op-x")
	if err != nil || got != "%4" {
		t.Fatalf("chatPane = %q, %v", got, err)
	}
}

func TestSendToPaneTypesLiterallyThenEnter(t *testing.T) {
	r := &scriptedRunner{}
	d := NewDirectorWith(r)
	if err := d.SendToPane(context.Background(), "%4", "hello; $(x)"); err != nil {
		t.Fatal(err)
	}
	want := []string{"send-keys -t %4 -l hello; $(x)", "send-keys -t %4 Enter"}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
}
