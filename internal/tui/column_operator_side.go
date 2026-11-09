package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/example-user/commander/internal/tui/widgets"
)

// Default ceilings for an unknown operator. Mirrors the constants formerly
// in operator.go — kept inline so the column file is self-contained.
const (
	colDefaultUSDMax       = 5.0
	colDefaultTokensMax    = 200_000
	colDefaultWallclockMax = 60
)

// OperatorSideColumn is the side panel rendered in the Operator view's right
// pane (`cmdr __column operator-side --task ID`). Three sections stacked:
//
//	worktree state (branch, commits ahead, dirty file count) — code only
//	container info (docker id, uptime) — code only; knowledge mode shows mode
//	ceilings (USD / tokens / wallclock progress bars)
//
// Live data is loaded via LoadOperatorData every columnRefreshInterval; the
// column still renders sensible empty-state copy when no live operator
// session exists. q / Ctrl-C quits.
type OperatorSideColumn struct {
	width, height int
	focused       bool // hints show only while the pane has focus

	taskBeadID string
	projectID  int64

	ceilings *widgets.Ceilings

	knowledgeMode bool

	svc        *Services
	live       OperatorLiveData
	liveLoaded bool

	// fallback fixture data so the first paint isn't blank when the store
	// is unavailable. Only used pre-load.
	fixtureOperator FixtureOperator
	fixtureFound    bool
}

// NewOperatorSideColumn constructs the column for the named task. The
// projectID is resolved from the live store first (joining agent_runs by
// task_bead_id), falling back to fixtures when no live record matches.
func NewOperatorSideColumn(svc *Services, taskBeadID string) *OperatorSideColumn {
	c := &OperatorSideColumn{
		taskBeadID: taskBeadID,
		ceilings:   widgets.NewCeilings(),
		svc:        svc,
	}
	c.resolveProjectID()
	if svc == nil {
		c.loadFixtureSeed()
	}
	return c
}

// RunOperatorSideColumn is the `cmdr __column operator-side --task ID` entry.
func RunOperatorSideColumn(taskBeadID string) error {
	svc, _ := openColumnServices()
	m := NewOperatorSideColumn(svc, taskBeadID)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithReportFocus()).Run()
	return err
}

// resolveProjectID looks up the project this task belongs to. Hits agent_runs
// first (live), then fixture operators, then defaults to 0 (renders empty).
func (c *OperatorSideColumn) resolveProjectID() {
	if c.svc != nil && c.svc.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if run, err := c.svc.Store.GetAgentRunByTaskBeadID(ctx, c.taskBeadID); err == nil && run != nil {
			c.projectID = run.ProjectID
			return
		}
		return
	}
	for _, op := range FixtureOperators() {
		if op.BeadID == c.taskBeadID {
			c.projectID = op.ProjectID
			return
		}
	}
}

// loadFixtureSeed seeds the ceilings widget with whatever fixture operator
// matches the bead id (zeros when none match), then independently flips
// knowledgeMode if the resolved project is knowledge-typed. The two checks
// are independent: an operator can be present AND the project can still be
// knowledge (e.g. fixture sur-003 in the "survey" project).
func (c *OperatorSideColumn) loadFixtureSeed() {
	for _, op := range FixtureOperators() {
		if op.BeadID == c.taskBeadID {
			c.fixtureOperator = op
			c.fixtureFound = true
			c.ceilings.SetData(widgets.CeilingsData{
				USDUsed:      op.USDUsed,
				USDMax:       colDefaultUSDMax,
				TokensUsed:   op.TokensUsed,
				TokensMax:    colDefaultTokensMax,
				WallclockMin: op.WallclockMin,
				WallclockMax: colDefaultWallclockMax,
			})
			break
		}
	}
	if !c.fixtureFound {
		c.ceilings.SetData(widgets.CeilingsData{
			USDMax:       colDefaultUSDMax,
			TokensMax:    colDefaultTokensMax,
			WallclockMax: colDefaultWallclockMax,
		})
	}
	// Knowledge detection runs regardless of operator presence — the
	// operator's project type drives the worktree-vs-mode rendering decision.
	for _, t := range FixtureTasks() {
		if t.BeadID == c.taskBeadID {
			for _, p := range FixtureProjects() {
				if p.ID == t.ProjectID && p.Type == "knowledge" {
					c.knowledgeMode = true
				}
			}
			break
		}
	}
}

// operatorLiveDataMsg carries an OperatorLiveData snapshot delivered by the
// periodic refresh tick.
type operatorLiveDataMsg struct {
	Data OperatorLiveData
}

func (c *OperatorSideColumn) Init() tea.Cmd {
	cmds := []tea.Cmd{columnTick()}
	if c.svc != nil && c.svc.Store != nil && c.projectID != 0 {
		cmds = append(cmds, c.loadLiveCmd())
	}
	return tea.Batch(cmds...)
}

func (c *OperatorSideColumn) loadLiveCmd() tea.Cmd {
	if c.svc == nil || c.svc.Store == nil || c.projectID == 0 {
		return nil
	}
	deps := OperatorDataDeps{Store: c.svc.Store, Beads: c.svc.Beads}
	taskID := c.taskBeadID
	pid := c.projectID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return operatorLiveDataMsg{Data: LoadOperatorData(ctx, deps, taskID, pid)}
	}
}

func (c *OperatorSideColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = m.Width
		c.height = m.Height
		ceilingsH := m.Height / 3
		if ceilingsH < 3 {
			ceilingsH = 3
		}
		c.ceilings.SetSize(m.Width-2, ceilingsH)
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

	case operatorLiveDataMsg:
		c.applyLiveData(m.Data)
		return c, nil

	case tea.KeyMsg:
		switch m.String() {
		case "q", "ctrl+c":
			return c, tea.Quit
		}
	}
	return c, nil
}

func (c *OperatorSideColumn) applyLiveData(d OperatorLiveData) {
	c.live = d
	c.liveLoaded = true
	if d.Project != nil && d.Project.Type == "knowledge" {
		c.knowledgeMode = true
	}
	if d.Run != nil {
		wallclock := 0
		if d.Run.SpawnTime > 0 {
			wallclock = int(time.Since(time.Unix(d.Run.SpawnTime, 0)).Minutes())
			if wallclock < 0 {
				wallclock = 0
			}
		}
		c.ceilings.SetData(widgets.CeilingsData{
			USDUsed:      d.Run.UsdUsed,
			USDMax:       colDefaultUSDMax,
			TokensUsed:   int(d.Run.TokensUsed),
			TokensMax:    colDefaultTokensMax,
			WallclockMin: wallclock,
			WallclockMax: colDefaultWallclockMax,
		})
	}
}

func (c *OperatorSideColumn) View() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}
	worktree := c.renderWorktree()
	container := c.renderContainer()
	ceilingsHeader := widgets.AccentBar + " " + widgets.HeaderStyle.Render("CEILINGS")
	ceilingsBody := c.ceilings.View()
	footer := focusHints(c.focused, c.width, [2]string{"q", "quit"})

	sep := lipgloss.NewStyle().
		Foreground(widgets.Palette.Surface1).
		Render(strings.Repeat("─", c.width-2))

	parts := withFooter([]string{
		worktree,
		sep,
		container,
		sep,
		ceilingsHeader,
		ceilingsBody,
	}, footer)
	return strings.Join(parts, "\n")
}

func (c *OperatorSideColumn) renderWorktree() string {
	header := widgets.AccentBar + " " + widgets.HeaderStyle.Render("WORKTREE")
	if c.knowledgeMode {
		modeRow := widgets.DimText.Render("mode") + "  " + widgets.ModeChip("knowledge")
		return header + "\n" + modeRow
	}
	modeRow := widgets.DimText.Render("mode") + "  " + widgets.ModeChip("code")

	branchName := "cmdr/" + c.taskBeadID
	commitsAhead := "0"
	dirty := "0"
	if c.liveLoaded {
		branchName = formatBranchLabel(c.live.BranchName)
		commitsAhead = fmt.Sprintf("%d", c.live.CommitsAhead)
		dirty = fmt.Sprintf("%d", c.live.FilesDirty)
	}

	branch := widgets.DimText.Render("branch") + "  " +
		lipgloss.NewStyle().Foreground(widgets.Palette.Sky).Render(branchName)
	ahead := widgets.DimText.Render("ahead") + "   " + widgets.NumberHighStyle.Render(commitsAhead) +
		" " + widgets.FaintText.Render("commits")
	dirtyRow := widgets.DimText.Render("dirty") + "   " + widgets.NumberHighStyle.Render(dirty) +
		" " + widgets.FaintText.Render("files")
	return strings.Join([]string{header, modeRow, branch, ahead, dirtyRow}, "\n")
}

func (c *OperatorSideColumn) renderContainer() string {
	header := widgets.AccentBar + " " + widgets.HeaderStyle.Render("CONTAINER")
	if c.knowledgeMode {
		return header + "\n" + widgets.FaintText.Render("—  in-process")
	}

	containerID := "cmdr_pending"
	uptimeMin := 0
	if c.fixtureFound {
		containerID = "cmdr_" + shortStr(c.fixtureOperator.SessionID, 8)
		uptimeMin = c.fixtureOperator.WallclockMin
	}
	if c.liveLoaded && c.live.Run != nil {
		if c.live.Run.ContainerID.Valid && c.live.Run.ContainerID.String != "" {
			containerID = shortStr(c.live.Run.ContainerID.String, 12)
		} else if c.live.Run.OpencodeSessionID != "" {
			containerID = "cmdr_" + shortStr(c.live.Run.OpencodeSessionID, 8)
		}
		if c.live.Run.SpawnTime > 0 {
			mins := int(time.Since(time.Unix(c.live.Run.SpawnTime, 0)).Minutes())
			if mins < 0 {
				mins = 0
			}
			uptimeMin = mins
		}
	}

	docker := widgets.DimText.Render("docker") + "  " +
		lipgloss.NewStyle().Foreground(widgets.Palette.Teal).Render(containerID)
	uptime := widgets.DimText.Render("uptime") + "  " +
		widgets.NumberHighStyle.Render(fmt.Sprintf("%dm", uptimeMin))
	return strings.Join([]string{header, docker, uptime}, "\n")
}

// shortStr returns the first n bytes of s. Mirrors the helper formerly in
// operator.go — kept private to the columns family.
func shortStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
