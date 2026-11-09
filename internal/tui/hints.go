package tui

import "github.com/example-user/commander/internal/tui/widgets"

// focusHints is the key-hint footer of a column (spec §2.8): "key label  key
// label", shown only while the column's pane has focus. Unfocused it is empty,
// and callers leave the line out entirely.
func focusHints(focused bool, width int, pairs ...[2]string) string {
	if !focused {
		return ""
	}
	return widgets.HintLine(width, pairs...)
}

// withFooter appends a footer line to rows unless it is empty.
func withFooter(rows []string, footer string) []string {
	if footer == "" {
		return rows
	}
	return append(rows, footer)
}
