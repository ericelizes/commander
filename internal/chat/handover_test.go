package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// fakeTmux is a recording tmux; it never touches a real server.
type fakeTmux struct {
	mu        sync.Mutex
	calls     []string
	panes     string
	capture   string
	env       string // show-environment's answer
	onRespawn func(args []string)
}

func (f *fakeTmux) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	hook := f.onRespawn
	f.mu.Unlock()
	switch args[0] {
	case "list-panes":
		return []byte(f.panes), nil, nil
	case "capture-pane":
		return []byte(f.capture), nil, nil
	case "show-environment":
		return []byte(f.env), nil, nil
	case "respawn-pane":
		if hook != nil {
			hook(args)
		}
	}
	return nil, nil, nil
}

func (f *fakeTmux) called(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func handoverHarness(t *testing.T) (*harness, *fakeTmux, string) {
	h := newHarness(t)
	id := h.role("manager-ws1")
	tm := &fakeTmux{
		// pane_id|@cmdr_chat|@cmdr_ws|window_name|pane_start_command
		panes: "%1||ws1|#1 Thing|cmdr __column cmdr-objectives\n" +
			"%2|1|ws1|#1 Thing|DISABLE_AUTOUPDATER=1 claude -n manager-ws1 --append-system-prompt-file '/p.md' --resume " + id + "\n" +
			"%3|1||commander|DISABLE_AUTOUPDATER=1 claude -n commander --resume other\n",
		capture: "some output\n✻ Cooked for 2m\n",
	}
	// The TUI claude owns the session until its pane is respawned.
	h.procs.set(id, []Proc{{PID: 777, Command: "claude -n manager-ws1 --resume " + id}})
	tm.onRespawn = func(args []string) {
		if !strings.Contains(strings.Join(args, " "), "claude -n") {
			h.procs.set(id, nil) // the placeholder replaced the TUI
		}
	}
	h.opts.Tmux, h.opts.AllowHandover = tm, true
	h.e = New(h.opts)
	return h, tm, id
}

func TestHandoverIsOffByDefault(t *testing.T) {
	h, tm, _ := handoverHarness(t)
	ctx := context.Background()
	if _, err := h.e.Handover(ctx, "manager-ws1", HandoverOptions{}); !errors.Is(err, ErrHandover) {
		t.Fatalf("without confirm: %v", err)
	}
	off := New(Options{Home: h.home, Resolver: h.opts.Resolver, Tmux: tm, Procs: h.procs.scan})
	if _, err := off.Handover(ctx, "manager-ws1", HandoverOptions{Confirm: true}); !errors.Is(err, ErrHandover) {
		t.Fatalf("engine without AllowHandover: %v", err)
	}
	if err := off.PopOut(ctx, "manager-ws1", HandoverOptions{Confirm: true}); !errors.Is(err, ErrHandover) {
		t.Fatalf("popout without AllowHandover: %v", err)
	}
	if len(tm.called("respawn-pane")) != 0 {
		t.Fatal("touched a pane while disabled")
	}
	// A plain Start refuses: the terminal owns the session.
	var owned *OwnedError
	if _, err := h.e.Start(ctx, "manager-ws1", StartOptions{}); !errors.As(err, &owned) || owned.By != "terminal" {
		t.Fatalf("start over the TUI: %v", err)
	}
}

func TestHandoverRefusesMidTurn(t *testing.T) {
	h, tm, _ := handoverHarness(t)
	tm.capture = "⠋ Thinking… (esc to interrupt)\n"
	if _, err := h.e.Handover(context.Background(), "manager-ws1", HandoverOptions{Confirm: true}); err == nil || !strings.Contains(err.Error(), "mid-turn") {
		t.Fatalf("mid-turn handover: %v", err)
	}
	if len(tm.called("respawn-pane")) != 0 {
		t.Fatal("stopped a working TUI")
	}
}

func TestHandoverAndPopOutRoundTrip(t *testing.T) {
	h, tm, id := handoverHarness(t)
	ctx := context.Background()
	writeTranscript(t, h.projects, id, `{"type":"user","sessionId":"`+id+`","timestamp":"2026-10-20T10:00:00Z","message":{"role":"user","content":"earlier question"}}`+"\n")
	_ = writeFakeCost(h, id, "0.5")

	st, err := h.e.Handover(ctx, "manager-ws1", HandoverOptions{Confirm: true, Wait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || !st.Resumed || st.SessionID != id {
		t.Fatalf("status after handover = %+v", st)
	}
	resp := tm.called("respawn-pane")
	if len(resp) != 1 || !strings.HasPrefix(resp[0], "respawn-pane -k -t %2 "+PlaceholderMarker+"manager-ws1 ") {
		t.Fatalf("respawn calls = %v", resp)
	}
	hist, sub, _ := h.e.Subscribe("manager-ws1", 0)
	defer sub.Close()
	if len(hist) == 0 || hist[0].Text != "earlier question" || hist[0].Source != SourceTranscript {
		t.Fatalf("history not hydrated: %s", summarize(hist))
	}
	_ = h.e.Send(ctx, "manager-ws1", "hi from the app")
	waitFor(t, sub, isKind(KindResult))

	// The placeholder pane no longer names the agent; PopOut finds it by @cmdr_ws.
	tm.panes = strings.Replace(tm.panes, "DISABLE_AUTOUPDATER=1 claude -n manager-ws1 --append-system-prompt-file '/p.md' --resume "+id,
		placeholderCmd("manager-ws1"), 1)
	if err := h.e.PopOut(ctx, "manager-ws1", HandoverOptions{Confirm: true, Wait: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if h.e.Running("manager-ws1") {
		t.Fatal("headless still running after pop-out")
	}
	resp = tm.called("respawn-pane")
	if len(resp) != 2 || !strings.HasPrefix(resp[1], "respawn-pane -k -t %2 env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 ") ||
		!strings.Contains(resp[1], " -n manager-ws1 ") || !strings.Contains(resp[1], id) {
		t.Fatalf("pop-out respawn = %v", resp)
	}
	rows := strings.Join(h.sink.all(), "\n")
	if !strings.Contains(rows, "manager-ws1 "+store.AgentWorking) {
		t.Fatalf("rows = %s", rows)
	}
}

func writeFakeCost(h *harness, id, v string) error {
	return writeFile(h.fakeDir+"/"+id+".cost", v)
}
