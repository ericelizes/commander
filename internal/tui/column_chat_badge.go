package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// The unread-mail badge ("✉ N") sits first in the header bar's status row so
// it is the last thing a narrow bar cuts. It counts the mail the window's
// agent has not been shown yet (see internal/mailstate), and is hidden
// whenever the count cannot be read, such as when the mail server is down.

// unreadFunc returns the unread mail count for a mail role.
type unreadFunc = livedata.UnreadFunc

type chatUnreadMsg struct {
	n  int
	ok bool
}

// mailRoleFor is the mail role of the agent in this window: "commander" for
// the Commander window, the workstream Manager's role for a workstream
// window, and "" for windows that show no badge (operators).
func mailRoleFor(kind, ws string) string {
	switch kind {
	case "commander":
		return "commander"
	case "manager":
		if ws != "" {
			return store.WorkstreamManagerRole(ws)
		}
	}
	return ""
}

// loadUnread fetches the badge count once per tick.
func (c *ChatTitleColumn) loadUnread() tea.Cmd {
	role := mailRoleFor(c.kind, c.ws)
	if c.unread == nil || role == "" {
		return nil
	}
	count := c.unread
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		n, err := count(ctx, role)
		return chatUnreadMsg{n: n, ok: err == nil}
	}
}

// badgePart is the "✉ N" status part, or false while the count is unknown.
func (c *ChatTitleColumn) badgePart() (statusPart, bool) {
	if !c.unreadOK || mailRoleFor(c.kind, c.ws) == "" {
		return statusPart{}, false
	}
	color := widgets.T.TextMuted // zero is quiet; Attn only ever means "needs you"
	if c.unreadN > 0 {
		color = widgets.T.Attn
	}
	return statusPart{
		wide:    fmt.Sprintf("✉ %d unread", c.unreadN),
		compact: fmt.Sprintf("✉ %d", c.unreadN),
		color:   color,
		bold:    c.unreadN > 0,
	}, true
}

// newUnreadFunc counts unread mail for a role (see livedata.NewUnreadFunc).
func newUnreadFunc(st *store.Store) unreadFunc { return livedata.NewUnreadFunc(st) }
