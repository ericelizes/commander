package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/tui/widgets"
)

// LeadFleetColumn is the per-project counterpart of FleetColumn — it stacks
// project metrics on top and the operators-for-this-project list below.
// Rendered in the Lead view's rightmost pane via
// `cmdr __column lead-fleet --project NAME`.
type LeadFleetColumn struct {
	width, height int
	focused       bool // hints show only while the pane has focus

	projectName string
	projectID   int64

	metrics       *widgets.Metrics
	operators     []FixtureOperator
	operatorIndex int

	svc        *Services
	live       LeadData
	liveLoaded bool
}

// NewLeadFleetColumn constructs the column for the named project. Live data
// loads happen on Init when the store is wired; fixture data seeds the
// first paint.
func NewLeadFleetColumn(svc *Services, projectName string) *LeadFleetColumn {
	c := &LeadFleetColumn{
		projectName: projectName,
		metrics:     widgets.NewMetrics(),
		svc:         svc,
	}
	c.resolveProjectID()
	if svc == nil {
		c.loadFixtureSeed()
	}
	return c
}

// RunLeadFleetColumn is the `cmdr __column lead-fleet --project NAME` entry.
func RunLeadFleetColumn(projectName string) error {
	svc, _ := openColumnServices()
	m := NewLeadFleetColumn(svc, projectName)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithReportFocus()).Run()
	return err
}

func (c *LeadFleetColumn) resolveProjectID() {
	if c.svc != nil && c.svc.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if p, err := projectByName(ctx, c.svc.Store, c.projectName); err == nil && p != nil {
			c.projectID = p.ID
			return
		}
		return
	}
	for _, p := range FixtureProjects() {
		if p.Name == c.projectName {
			c.projectID = p.ID
			return
		}
	}
}

func (c *LeadFleetColumn) loadFixtureSeed() {
	c.operators = filterOperatorsByProject(c.projectID)
	c.metrics.SetData(buildFixtureProjectMetrics(c.projectID, c.operators))
}

func (c *LeadFleetColumn) Init() tea.Cmd {
	cmds := []tea.Cmd{columnTick()}
	if c.svc != nil && c.svc.Store != nil && c.projectID != 0 {
		cmds = append(cmds, c.loadLiveCmd())
	}
	return tea.Batch(cmds...)
}

func (c *LeadFleetColumn) loadLiveCmd() tea.Cmd {
	if c.svc == nil || c.svc.Store == nil || c.projectID == 0 {
		return nil
	}
	deps := LeadDataDeps{Store: c.svc.Store, Beads: c.svc.Beads}
	pid := c.projectID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return leadDataLoadedMsg{data: LoadLeadData(ctx, deps, pid)}
	}
}

func (c *LeadFleetColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = m.Width
		c.height = m.Height
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

	case leadDataLoadedMsg:
		c.applyLiveData(m.data)
		return c, nil

	case tea.KeyMsg:
		switch m.String() {
		case "q", "esc":
			return c, fastSelectWindowCmd("commander", "Returned to projects.")
		case "ctrl+c":
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

func (c *LeadFleetColumn) applyLiveData(d LeadData) {
	c.live = d
	c.liveLoaded = true
	c.operators = operatorsForRender(d.Operators, nil)
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
	c.metrics.SetHeader("PROJECT")
}

func (c *LeadFleetColumn) View() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}
	metricsHeader := widgets.AccentBar + " " + widgets.HeaderStyle.Render("PROJECT METRICS")
	opsHeader := widgets.AccentBar + " " + widgets.HeaderStyle.Render("OPERATORS · "+strings.ToUpper(c.projectName))
	footer := focusHints(c.focused, c.width, [2]string{"↑↓", "select"}, [2]string{"esc/q", "projects"})

	var ops string
	if len(c.operators) == 0 {
		ops = widgets.FaintText.Render("No operators yet. Drill into a task from the chat pane to spawn one.")
	} else {
		ops = c.renderOperators()
	}

	return strings.Join(withFooter([]string{metricsHeader, c.metrics.View(), "", opsHeader, ops}, footer), "\n")
}

func (c *LeadFleetColumn) renderOperators() string {
	rows := make([]string, 0, len(c.operators))
	for i, op := range c.operators {
		row := fmt.Sprintf("%s %s · $%.2f",
			widgets.StatusIcon(op.Status),
			op.AgentName,
			op.USDUsed,
		)
		if i == c.operatorIndex {
			row = widgets.AccentBar + " " + row
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

// filterOperatorsByProject lifts the helper from the deleted lead.go so the
// column can keep rendering fixture operators with no dependency on the old
// package.
func filterOperatorsByProject(projectID int64) []FixtureOperator {
	all := FixtureOperators()
	out := make([]FixtureOperator, 0, len(all))
	for _, op := range all {
		if op.ProjectID == projectID {
			out = append(out, op)
		}
	}
	return out
}

// buildFixtureProjectMetrics derives a project-scoped MetricsData from the
// fixture operator slice. Mirrors the helper formerly in lead.go.
func buildFixtureProjectMetrics(_ int64, operators []FixtureOperator) widgets.MetricsData {
	var cost24h float64
	var blocked, active int
	for _, op := range operators {
		cost24h += op.USDUsed
		switch op.Status {
		case "blocked":
			blocked++
		case "working":
			active++
		}
	}
	idle := 0.0
	if n := len(operators); n > 0 {
		idle = float64(n-active-blocked) / float64(n)
	}
	return widgets.MetricsData{
		TotalCostUSD24h: cost24h,
		TotalCostUSD7d:  cost24h * 5,
		AgentCount:      len(operators),
		ActiveCount:     active,
		BlockedCount:    blocked,
		IdleRatio:       idle,
	}
}
