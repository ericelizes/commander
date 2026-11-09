package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

func withUnread(c *ChatTitleColumn, n int, ok bool) *ChatTitleColumn {
	m, _ := c.Update(chatUnreadMsg{n: n, ok: ok})
	return m.(*ChatTitleColumn)
}

func TestChatTitle_UnreadBadgeLeadsTheStatusRowAndSurvivesNarrowWidths(t *testing.T) {
	msg := chatTitleMsg{kind: "commander", title: "Commander"}
	wide := titleRowsOf(withUnread(withLive(titleBar(140, msg)), 3, true))[1]
	if !strings.HasPrefix(strings.TrimSpace(wide), "✉ 3 unread   ⚑ 1 need you") {
		t.Fatalf("wide status = %q", wide)
	}
	compact := titleRowsOf(withUnread(withLive(titleBar(80, msg)), 3, true))[1]
	if !strings.HasPrefix(strings.TrimSpace(compact), "✉ 3  ⚑ 1") {
		t.Fatalf("compact status = %q", compact)
	}
	// Squeezed far below what the other parts need: the badge is what is left.
	tiny := titleRowsOf(withUnread(withLive(titleBar(12, msg)), 3, true))[1]
	if !strings.Contains(tiny, "✉ 3") {
		t.Fatalf("badge was truncated first: %q", tiny)
	}
}

func TestChatTitle_UnreadBadgeShowsWithoutLiveDataAndAtZero(t *testing.T) {
	c := withUnread(titleBar(120, chatTitleMsg{kind: "manager", title: "Manager | X", ws: "ws-1"}), 0, true)
	if got := titleRowsOf(c)[1]; !strings.Contains(got, "✉ 0 unread") {
		t.Fatalf("status = %q", got)
	}
}

func TestChatTitle_UnreadBadgeIsQuietAtZeroAndAttnAboveIt(t *testing.T) {
	c := withUnread(titleBar(120, chatTitleMsg{kind: "commander", title: "Commander"}), 0, true)
	if p, _ := c.badgePart(); p.color == widgets.T.Attn || p.bold {
		t.Fatalf("zero unread must be quiet: %+v", p)
	}
	c = withUnread(c, 2, true)
	if p, _ := c.badgePart(); p.color != widgets.T.Attn || !p.bold {
		t.Fatalf("unread mail must use the Attn color: %+v", p)
	}
}

func TestChatTitle_UnreadBadgeIsHiddenWhenMailIsUnavailableOrForOperators(t *testing.T) {
	down := withUnread(withLive(titleBar(140, chatTitleMsg{kind: "commander", title: "Commander"})), 0, false)
	if strings.Contains(titleRowsOf(down)[1], "✉") {
		t.Fatalf("badge shown while the mail server is down: %q", titleRowsOf(down)[1])
	}
	op := withUnread(titleBar(140, chatTitleMsg{kind: "operator", title: "Operator | TEAM-1"}), 2, true)
	if strings.Contains(titleRowsOf(op)[1], "✉") {
		t.Fatalf("operator windows have no badge: %q", titleRowsOf(op)[1])
	}
}

func TestChatTitle_UnreadCountsTheWindowsOwnAgentRole(t *testing.T) {
	var asked []string
	count := func(_ context.Context, role string) (int, error) { asked = append(asked, role); return 2, nil }

	tests := []struct {
		msg  chatTitleMsg
		want string
	}{
		{chatTitleMsg{kind: "commander", title: "Commander"}, "commander"},
		{chatTitleMsg{kind: "manager", title: "Manager | X", ws: "ws-1"}, store.WorkstreamManagerRole("ws-1")},
	}
	for _, tc := range tests {
		asked = nil
		c := NewChatTitleColumn()
		c.unread = count
		_, cmd := c.Update(tc.msg) // learning which window this is triggers a first count
		if cmd == nil {
			t.Fatalf("%s: no count scheduled", tc.want)
		}
		got, ok := cmd().(chatUnreadMsg)
		if !ok || got.n != 2 || !got.ok || len(asked) != 1 || asked[0] != tc.want {
			t.Fatalf("got %+v asked %v, want role %q", got, asked, tc.want)
		}
	}

	op := NewChatTitleColumn()
	op.unread = count
	if _, cmd := op.Update(chatTitleMsg{kind: "operator", title: "Operator | A"}); cmd != nil {
		t.Fatal("operator windows must not poll mail")
	}
}

func TestChatTitle_UnreadErrorHidesTheBadge(t *testing.T) {
	c := NewChatTitleColumn()
	c.unread = func(context.Context, string) (int, error) { return 0, errors.New("mail server down") }
	m, cmd := c.Update(chatTitleMsg{kind: "commander", title: "Commander"})
	m, _ = m.(*ChatTitleColumn).Update(cmd())
	m, _ = m.(*ChatTitleColumn).Update(tea.WindowSizeMsg{Width: 120, Height: 2})
	if strings.Contains(stripANSI(m.(*ChatTitleColumn).View()), "✉") {
		t.Fatal("badge must hide when the count cannot be read")
	}
}
