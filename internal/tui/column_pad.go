package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// PadColumn is a spacer pane: it draws nothing (the pane's window-style supplies
// the chat background), takes no keys and never has focus. Four of them frame
// the chat so it is not flush with the pane edges.
type PadColumn struct{}

func (PadColumn) Init() tea.Cmd                         { return nil }
func (p PadColumn) Update(tea.Msg) (tea.Model, tea.Cmd) { return p, nil }
func (PadColumn) View() string                          { return "" }

// RunPadColumn is the `cmdr __column pad` entry point.
func RunPadColumn() error {
	_, err := tea.NewProgram(PadColumn{}, tea.WithAltScreen()).Run()
	return err
}
