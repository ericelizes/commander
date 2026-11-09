// Package tui hosts the per-pane Bubble Tea programs run by
// `cmdr __column <kind>`. Commander's UI architecture pivoted away from a
// monolithic chat-owning TUI: opencode now owns chat in every pane, so
// commander's job is just to render the chrome columns (objectives tree,
// fleet metrics, side panel) inside tmux panes laid out by internal/layout.
//
// Each column is a small focused tea.Model; the entry points live in
// column_*.go and are dispatched by cmd/cmdr/main.go's `__column` subcommand.
//
// This file holds shared helpers — the services bundle, periodic-tick
// machinery, and the fixture-vs-live data fallback that every column needs.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// columnRefreshInterval is the cadence at which column renderers reload
// state from the store. 5s mirrors the old TUI's tick — long enough to
// avoid hammering SQLite, short enough that an activation in another pane
// shows up before the user wonders.
const columnRefreshInterval = 5 * time.Second

// columnTickMsg is the periodic refresh pulse columns chain to themselves.
// Each consumer schedules the next tick — Bubble Tea has no idempotent
// timer, so we re-arm manually.
type columnTickMsg struct{}

// columnTick produces a tea.Cmd firing one columnTickMsg after the interval.
func columnTick() tea.Cmd {
	return tea.Tick(columnRefreshInterval, func(time.Time) tea.Msg {
		return columnTickMsg{}
	})
}

// openColumnServices opens the live services bundle if the SQLite store at
// ~/.commander/state.db can be opened. On failure it returns an empty
// Services bundle plus a warning string; production columns must render an
// empty/degraded state, never fixture demo data, when live state is
// unavailable.
//
// Mirrors what app.go's buildServices used to do, but trimmed to the bits
// columns actually consume (no Spawner/Watchdog — those are owned by the
// daemon, not the UI columns).
func openColumnServices() (*Services, string) {
	statePath, err := stateDBPath()
	if err != nil {
		return &Services{}, fmt.Sprintf("resolve state dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return &Services{}, fmt.Sprintf("mkdir %s: %v", filepath.Dir(statePath), err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return &Services{}, fmt.Sprintf("open store: %v", err)
	}
	return &Services{
		Store:    st,
		OpenCode: opencode.New(),
		AM:       agentmail.New(),
		Beads:    beads.New(),
		Docker:   docker.New(),
		Tmux:     tmuxctl.New(),
	}, ""
}

// stateDBPath returns the path to ~/.commander/state.db, honoring
// $COMMANDER_HOME. Mirrors the helper formerly in app.go so column
// programs can open the store the same way the daemon and activate
// subcommand do.
func stateDBPath() (string, error) {
	if home := os.Getenv("COMMANDER_HOME"); home != "" {
		return filepath.Join(home, "state.db"), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".commander", "state.db"), nil
}

// projectByName scans the store for a project whose Name matches. Returns
// nil if no live store is wired or no such project exists. ctx caps the
// query so a slow SQLite can't lock the renderer.
func projectByName(ctx context.Context, st *store.Store, name string) (*store.Project, error) {
	if st == nil {
		return nil, nil
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i], nil
		}
	}
	return nil, nil
}
