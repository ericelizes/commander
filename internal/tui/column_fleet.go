package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/inbox"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
)

// FleetColumn is the Bubble Tea program rendered in the Commander view's
// rightmost pane (`cmdr __column cmdr-fleet`). Stacks three sections in one
// pane:
//
//	top    — alerts (cross-tier am notifications, when present)
//	middle — fleet metrics card
//	bottom — operators list (selectable)
//
// As with every column post-pivot, this pane is passive — chat lives in
// pane 0 (real opencode), and `q` / Ctrl-C quits.
type FleetColumn struct {
	width, height int
	focused       bool // hints show only while the pane has focus

	metrics       *widgets.Metrics
	operators     []FixtureOperator
	operatorIndex int

	projectsByID map[int64]string

	notifications []inbox.Notification

	svc        *Services
	live       CommanderLiveData
	liveLoaded bool
}

// NewFleetColumn constructs the column with fixture data so the first paint
// always has something to show; live data swaps in once it loads.
func NewFleetColumn(svc *Services) *FleetColumn {
	c := &FleetColumn{
		metrics:      widgets.NewMetrics(),
		projectsByID: make(map[int64]string),
		svc:          svc,
	}
	if svc == nil {
		c.loadFixtureSeed()
	} else {
		c.preloadLiveData()
	}
	return c
}

// RunFleetColumn is the entry point dispatched by `cmdr __column cmdr-fleet`.
func RunFleetColumn() error {
	svc, warn := openColumnServices()
	m := NewFleetColumn(svc)
	applyCommanderStartupWarning(warn, svc, m.applyLiveData)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithReportFocus()).Run()
	return err
}

func (c *FleetColumn) loadFixtureSeed() {
	for _, p := range FixtureProjects() {
		c.projectsByID[p.ID] = p.Name
	}
	c.operators = FixtureOperators()
	fm := FixtureMetrics()
	c.metrics.SetData(widgets.MetricsData{
		TotalCostUSD24h: fm.TotalCostUSD24h,
		TotalCostUSD7d:  fm.TotalCostUSD7d,
		AgentCount:      fm.AgentCount,
		ActiveCount:     fm.ActiveCount,
		BlockedCount:    fm.BlockedCount,
		IdleRatio:       fm.IdleRatio,
	})
}

// Init schedules ticks + the first live load + (best effort) inbox listener.
func (c *FleetColumn) Init() tea.Cmd {
	cmds := []tea.Cmd{columnTick()}
	if c.svc != nil && c.svc.Store != nil {
		cmds = append(cmds, c.loadLiveCmd())
	}
	return tea.Batch(cmds...)
}

func (c *FleetColumn) loadLiveCmd() tea.Cmd {
	if c.svc == nil || c.svc.Store == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return commanderLiveDataMsg{Data: LoadCommanderData(ctx, CommanderDataDeps{Store: c.svc.Store, Beads: c.svc.Beads})}
	}
}

func (c *FleetColumn) preloadLiveData() {
	if c.svc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c.applyLiveData(LoadCommanderData(ctx, CommanderDataDeps{Store: c.svc.Store, Beads: c.svc.Beads}))
}

func (c *FleetColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = m.Width
		c.height = m.Height
		// Metrics gets ~1/4 of the pane height; operators take the rest.
		metricsH := m.Height / 4
		if metricsH < 4 {
			metricsH = 4
		}
		c.metrics.SetSize(m.Width-2, metricsH)
		return c, nil

	case tea.FocusMsg:
		c.focused = true
		return c, nil

	case tea.BlurMsg:
		c.focused = false
		return c, nil

	case columnTickMsg:
		cmds := []tea.Cmd{columnTick()}
		if cmd := c.loadLiveCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return c, tea.Batch(cmds...)

	case commanderLiveDataMsg:
		c.applyLiveData(m.Data)
		return c, nil

	case tea.KeyMsg:
		switch m.String() {
		case "q", "ctrl+c":
			return c, tea.Quit
		case "up", "k":
			if c.operatorIndex > 0 {
				c.operatorIndex--
			}
		case "down", "j":
			if c.operatorIndex < len(c.operators)-1 {
				c.operatorIndex++
			}
		}
		return c, nil
	}
	return c, nil
}

func (c *FleetColumn) applyLiveData(d CommanderLiveData) {
	c.live = d
	c.liveLoaded = true

	c.projectsByID = make(map[int64]string, len(d.Projects))
	for _, p := range d.Projects {
		c.projectsByID[p.ID] = p.Name
	}
	c.operators = operatorsForRender(d.Operators, d.Projects)
	if c.operatorIndex >= len(c.operators) {
		c.operatorIndex = 0
	}
	c.metrics.SetData(widgets.MetricsData{
		TotalCostUSD24h: d.Metrics.TotalCostUSD24h,
		TotalCostUSD7d:  d.Metrics.TotalCostUSD7d,
		AgentCount:      d.Metrics.AgentCount,
		ActiveCount:     d.Metrics.ActiveCount,
		BlockedCount:    d.Metrics.BlockedCount,
		IdleRatio:       d.Metrics.IdleRatio,
	})
}

func (c *FleetColumn) View() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}
	metricsHeader := widgets.AccentBar + " " + widgets.HeaderStyle.Render("FLEET METRICS")
	opsHeader := widgets.AccentBar + " " + widgets.HeaderStyle.Render("OPERATORS")
	footer := focusHints(c.focused, c.width, [2]string{"↑↓", "select"}, [2]string{"q", "quit"})

	parts := withFooter([]string{metricsHeader, c.metrics.View(), "", opsHeader, c.renderOperators()}, footer)
	return strings.Join(parts, "\n")
}

func (c *FleetColumn) renderOperators() string {
	if len(c.operators) == 0 {
		return widgets.FaintText.Render("(no operators)")
	}
	rows := make([]string, 0, len(c.operators))
	for i, op := range c.operators {
		project := c.projectsByID[op.ProjectID]
		row := fmt.Sprintf("%s %s · %s · $%.2f",
			widgets.StatusIcon(op.Status),
			op.AgentName,
			project,
			op.USDUsed,
		)
		if i == c.operatorIndex {
			row = widgets.AccentBar + " " + row
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

// operatorsForRender translates live OperatorRow data into the FixtureOperator
// shape the renderer expects. Lifted from the deleted commander.go so the
// column can render live data without importing the old view module.
func operatorsForRender(rows []OperatorRow, _ []store.Project) []FixtureOperator {
	out := make([]FixtureOperator, 0, len(rows))
	for _, r := range rows {
		out = append(out, FixtureOperator{
			BeadID:     r.TaskBeadID,
			AgentName:  r.AgentName,
			SessionID:  r.SessionID,
			Status:     r.Status,
			ProjectID:  r.ProjectID,
			USDUsed:    r.USDUsed,
			TokensUsed: int(r.TokensUsed),
		})
	}
	return out
}
