package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestColumnsShowKeyHintsOnlyWhileFocused(t *testing.T) {
	cases := []struct {
		name string
		col  tea.Model
		hint string
	}{
		{"fleet", NewFleetColumn(nil), "q quit"},
		{"lead-fleet", NewLeadFleetColumn(nil, "frontend"), "esc/q projects"},
		{"lead-tasks", NewLeadTasksColumn(nil, "frontend"), "enter open task"},
		{"operator-side", NewOperatorSideColumn(nil, "task-1"), "q quit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := tc.col.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
			if got := stripANSI(m.View()); strings.Contains(got, tc.hint) {
				t.Fatalf("hints must be hidden before focus, got:\n%s", got)
			}
			m, _ = m.Update(tea.FocusMsg{})
			if got := stripANSI(m.View()); !strings.Contains(got, tc.hint) {
				t.Fatalf("focused view lacks %q:\n%s", tc.hint, got)
			}
			m, _ = m.Update(tea.BlurMsg{})
			if got := stripANSI(m.View()); strings.Contains(got, tc.hint) {
				t.Fatalf("hints must hide again on blur, got:\n%s", got)
			}
		})
	}
}
