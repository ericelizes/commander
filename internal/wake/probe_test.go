package wake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	rule     = "\x1b[38;5;244m────────────────────────────────────────\x1b[39m\n"
	footer   = "\x1b[39m  \x1b[38;5;211m⏵⏵ bypass permissions on\x1b[38;5;246m (shift+tab to cycle) · ← 8 agents\x1b[39m\n"
	busyFoot = "\x1b[39m  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt\n"
)

// Shapes taken from a real `tmux capture-pane -p -e` of Claude Code 2.x.
func screen(input, foot string) string {
	return "some earlier output\n\n" + rule + "\x1b[39m❯ " + input + "\n" + rule + foot
}

const placeholder = "\x1b[2mTry \"how does <filepath> work?\"\x1b[0m"

func TestParseScreen(t *testing.T) {
	tests := []struct {
		name string
		cap  string
		want Screen
	}{
		{"idle with dim placeholder is empty", screen(placeholder, footer), Screen{HasInputBox: true}},
		{"idle with nothing after prompt", screen("", footer), Screen{HasInputBox: true}},
		{"typed text", screen("hel", footer), Screen{HasInputBox: true, Input: "hel"}},
		{"typed text then dim suffix suggestion", screen("hel\x1b[2mlo\x1b[0m", footer), Screen{HasInputBox: true, Input: "hel"}},
		{"turn in progress", screen(placeholder, busyFoot), Screen{HasInputBox: true, Busy: true}},
		{"permission dialog is not an input box", "Do you want to proceed?\n\x1b[38;5;246m❯ 1. Yes\x1b[39m\n  2. No\n", Screen{}},
		{"color index 2 is not faint", screen("\x1b[38;5;2mabc\x1b[39m", footer), Screen{HasInputBox: true, Input: "abc"}},
		{"truecolor 2 is not faint", screen("\x1b[38;2;1;2;3mabc\x1b[39m", footer), Screen{HasInputBox: true, Input: "abc"}},
		{"quoted phrase far above the footer is not busy", "esc to interrupt\n" + strings.Repeat("x\n", 6) + screen(placeholder, footer), Screen{HasInputBox: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseScreen(tt.cap); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// fakePane scripts tmux for one pane: the current screen and the display-message line.
type fakePane struct {
	screen string
	client string // "<window_active>|<session_attached>|<client_activity>"
	err    error
}

func (f *fakePane) tmux(_ context.Context, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch args[0] {
	case "capture-pane":
		return []byte(f.screen), nil
	case "display-message":
		return []byte(f.client + "\n"), nil
	}
	return nil, errors.New("unexpected tmux " + args[0])
}

func probeAt(f *fakePane, now *time.Time) *Probe {
	return &Probe{Tmux: f.tmux, Now: func() time.Time { return *now }, Quiet: 10 * time.Second}
}

func wantDeferred(t *testing.T, err error, reason string) {
	t.Helper()
	if !errors.Is(err, ErrDeferred) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("got %v, want deferred (%s)", err, reason)
	}
}

func TestProbe_IdleEmptyDetachedIsSafe(t *testing.T) {
	now := time.Now()
	f := &fakePane{screen: screen(placeholder, footer), client: "1|0|"}
	if err := probeAt(f, &now).Check(context.Background(), "%1"); err != nil {
		t.Fatal(err)
	}
}

func TestProbe_DefersWhileTurnInProgress(t *testing.T) {
	now := time.Now()
	f := &fakePane{screen: screen(placeholder, busyFoot)}
	wantDeferred(t, probeAt(f, &now).Check(context.Background(), "%1"), "turn in progress")
}

func TestProbe_DefersWhenInputBoxHasText(t *testing.T) {
	now := time.Now()
	f := &fakePane{screen: screen("half a sent", footer)}
	wantDeferred(t, probeAt(f, &now).Check(context.Background(), "%1"), "not empty")
}

func TestProbe_DefersOnUnknownScreen(t *testing.T) {
	now := time.Now()
	f := &fakePane{screen: "$ bash prompt\n"}
	wantDeferred(t, probeAt(f, &now).Check(context.Background(), "%1"), "no input box")
}

// The user typed and then cleared (or submitted) between two polls: the box is
// empty now, but they were active a moment ago, so wait out the quiet period.
func TestProbe_DefersUntilQuietAfterInputChange(t *testing.T) {
	now := time.Now()
	f := &fakePane{screen: screen("typing", footer)}
	p := probeAt(f, &now)
	wantDeferred(t, p.Check(context.Background(), "%1"), "not empty")

	f.screen = screen(placeholder, footer)
	now = now.Add(5 * time.Second)
	wantDeferred(t, p.Check(context.Background(), "%1"), "changed recently")
	now = now.Add(4 * time.Second)
	wantDeferred(t, p.Check(context.Background(), "%1"), "changed recently")
	now = now.Add(7 * time.Second) // 11s after the box was seen cleared
	if err := p.Check(context.Background(), "%1"); err != nil {
		t.Fatalf("quiet period elapsed, want safe: %v", err)
	}
}

func TestProbe_TypingStateIsPerPane(t *testing.T) {
	now := time.Now()
	f := &fakePane{screen: screen("typing", footer)}
	p := probeAt(f, &now)
	wantDeferred(t, p.Check(context.Background(), "%1"), "not empty")
	f.screen = screen(placeholder, footer)
	if err := p.Check(context.Background(), "%2"); err != nil {
		t.Fatalf("other pane must be unaffected: %v", err)
	}
}

func TestProbe_DefersOnRecentClientActivityInActiveWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakePane{screen: screen(placeholder, footer), client: "1|1|1799999995"}
	p := probeAt(f, &now)
	wantDeferred(t, p.Check(context.Background(), "%1"), "client active")

	f.client = "1|1|1799999980" // 20s ago
	if err := p.Check(context.Background(), "%1"); err != nil {
		t.Fatal(err)
	}
	f.client = "0|1|1799999999" // user is looking at another window
	if err := p.Check(context.Background(), "%1"); err != nil {
		t.Fatal(err)
	}
}

func TestProbe_TmuxErrorIsNotADeferral(t *testing.T) {
	now := time.Now()
	f := &fakePane{err: errors.New("no such pane")}
	err := probeAt(f, &now).Check(context.Background(), "%1")
	if err == nil || errors.Is(err, ErrDeferred) {
		t.Fatalf("got %v, want a real error", err)
	}
}
