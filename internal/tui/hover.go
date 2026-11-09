package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// hoverExpireMsg clears a stale hover band in the inspector: tmux sends no event
// when the pointer leaves a pane. (The sidebar has no hover state of its own: the
// pointer moves its one always-visible highlight.)
type hoverExpireMsg struct{ seq int }

func hoverExpire(seq int) tea.Cmd {
	return tea.Tick(2500*time.Millisecond, func(time.Time) tea.Msg { return hoverExpireMsg{seq: seq} })
}
