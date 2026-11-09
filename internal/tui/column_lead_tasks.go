package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/tui/widgets"
)

// LeadTasksColumn renders the per-project tasks tree shown in the Lead view's
// middle pane (`cmdr __column lead-tasks --project NAME`). Each Objective is
// depth 0 with its child Tasks at depth 1, mirroring the cross-fleet
// objectives column but scoped to one project.
//
// The column does NOT spawn operators — that's the daemon's job, and the
// Lead opencode pane (pane 0) shells out via Bash to trigger it. This
// program is a passive viewer; q / Ctrl-C quits.
type LeadTasksColumn struct {
	width, height int
	focused       bool // hints show only while the pane has focus

	projectName string
	projectID   int64

	tree *widgets.Tree

	svc        *Services
	live       LeadData
	liveLoaded bool
}

// NewLeadTasksColumn constructs the column for the named project. If a live
// store is wired and the project is found there, projectID is captured so
// LoadLeadData can run; otherwise we fall back to fixtures matched by name.
func NewLeadTasksColumn(svc *Services, projectName string) *LeadTasksColumn {
	c := &LeadTasksColumn{
		projectName: projectName,
		tree:        widgets.NewTree(),
		svc:         svc,
	}
	c.resolveProjectID()
	if svc == nil {
		c.loadFixtureSeed()
	}
	return c
}

// RunLeadTasksColumn is the `cmdr __column lead-tasks --project NAME` entry.
func RunLeadTasksColumn(projectName string) error {
	svc, _ := openColumnServices()
	m := NewLeadTasksColumn(svc, projectName)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithReportFocus()).Run()
	return err
}

// resolveProjectID hits the store (if available) to translate projectName
// into a numeric ID. Fixture mode falls back to the matching FixtureProject.
func (c *LeadTasksColumn) resolveProjectID() {
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

func (c *LeadTasksColumn) loadFixtureSeed() {
	c.tree.SetNodes(buildLeadTasksTreeFromFixtures(c.projectID))
}

func (c *LeadTasksColumn) Init() tea.Cmd {
	cmds := []tea.Cmd{columnTick()}
	if c.svc != nil && c.svc.Store != nil && c.projectID != 0 {
		cmds = append(cmds, c.loadLiveCmd())
	}
	return tea.Batch(cmds...)
}

func (c *LeadTasksColumn) loadLiveCmd() tea.Cmd {
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

// leadDataLoadedMsg is the lead-tier counterpart of commanderLiveDataMsg.
// Both lead-tasks and lead-fleet columns consume it.
type leadDataLoadedMsg struct {
	data LeadData
}

func (c *LeadTasksColumn) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = m.Width
		c.height = m.Height
		c.tree.SetSize(m.Width-2, m.Height-2)
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

	case widgets.TreeNodeSelectedMsg:
		if sel := c.tree.Selected(); sel != nil {
			if sel.Depth == 1 {
				return c, runCmdrCommand("--task", sel.ID, "__status:Opened task "+sel.ID+".")
			}
			if sel.Depth == 0 {
				updated, cmd := c.tree.Update(tea.KeyMsg{Type: tea.KeyRight})
				*c.tree = updated
				return c, cmd
			}
		}
		return c, nil

	case tea.KeyMsg:
		switch m.String() {
		case "q", "esc":
			return c, fastSelectWindowCmd("commander", "Returned to projects.")
		case "ctrl+c":
			return c, tea.Quit
		}
		updated, cmd := c.tree.Update(m)
		*c.tree = updated
		return c, cmd
	}
	return c, nil
}

func (c *LeadTasksColumn) applyLiveData(d LeadData) {
	c.live = d
	c.liveLoaded = true
	c.tree.SetNodes(buildLeadTasksTreeFromLive(d.Objectives, d.Tasks))
}

func (c *LeadTasksColumn) View() string {
	if c.width <= 0 || c.height <= 0 {
		return ""
	}
	header := widgets.AccentBar + " " + widgets.HeaderStyle.Render("WORKSTREAMS · "+strings.ToUpper(c.projectName))
	body := c.tree.View()
	footer := focusHints(c.focused, c.width, [2]string{"↑↓", "select"}, [2]string{"enter", "open task"}, [2]string{"esc/q", "projects"})
	return strings.Join(withFooter([]string{header, body}, footer), "\n")
}

// buildLeadTasksTreeFromFixtures groups fixture tasks for the project by
// objective. Mirrors the deleted lead.go's buildTaskNodes shape so existing
// fixture-driven smoke tests still pass.
func buildLeadTasksTreeFromFixtures(projectID int64) []widgets.TreeNode {
	tasks := filterTasksByProject(projectID)
	if len(tasks) == 0 {
		return nil
	}
	taskByID := make(map[string]FixtureTask, len(tasks))
	for _, t := range tasks {
		taskByID[t.BeadID] = t
	}

	var nodes []widgets.TreeNode
	used := make(map[string]struct{}, len(tasks))
	for _, obj := range FixtureObjectives() {
		if obj.ProjectID != projectID {
			continue
		}
		var children []FixtureTask
		for _, beadID := range obj.TaskBeadIDs {
			if t, ok := taskByID[beadID]; ok {
				children = append(children, t)
			}
		}
		if len(children) == 0 {
			continue
		}
		nodes = append(nodes, widgets.TreeNode{
			ID:       fmt.Sprintf("obj:%d", obj.ID),
			Title:    obj.Title,
			Status:   "executing",
			Depth:    0,
			Expanded: true,
		})
		for _, t := range children {
			nodes = append(nodes, widgets.TreeNode{
				ID:     t.BeadID,
				Title:  t.Title,
				Status: t.Phase,
				Depth:  1,
			})
			used[t.BeadID] = struct{}{}
		}
	}
	// Orphan tasks (no objective link) get bucketed under "Other".
	var orphans []FixtureTask
	for _, t := range tasks {
		if _, ok := used[t.BeadID]; !ok {
			orphans = append(orphans, t)
		}
	}
	if len(orphans) > 0 {
		nodes = append(nodes, widgets.TreeNode{
			ID:       "obj:other",
			Title:    "Other",
			Status:   "executing",
			Depth:    0,
			Expanded: true,
		})
		for _, t := range orphans {
			nodes = append(nodes, widgets.TreeNode{
				ID:     t.BeadID,
				Title:  t.Title,
				Status: t.Phase,
				Depth:  1,
			})
		}
	}
	return nodes
}

// buildLeadTasksTreeFromLive renders bd-loaded TaskRow data as a flat tree.
// The objective grouping (objective_tasks join) is only available cross-fleet
// at the moment — once per-project objective grouping lands here we'll expand
// this; for now a flat tasks list is honest about what bd returned.
func buildLeadTasksTreeFromLive(objectives []ObjectiveBead, tasks []TaskRow) []widgets.TreeNode {
	nodes := make([]widgets.TreeNode, 0, len(objectives)+len(tasks))
	childrenByObjective := make(map[string][]TaskRow)
	var orphans []TaskRow
	for _, task := range tasks {
		if task.ObjectiveBeadID == "" {
			orphans = append(orphans, task)
			continue
		}
		childrenByObjective[task.ObjectiveBeadID] = append(childrenByObjective[task.ObjectiveBeadID], task)
	}
	for _, objective := range objectives {
		nodes = append(nodes, widgets.TreeNode{
			ID:       objective.ID,
			Title:    objective.Title,
			Status:   normalizeObjectiveStatus(objective.Status),
			Depth:    0,
			Expanded: true,
		})
		for _, task := range childrenByObjective[objective.ID] {
			nodes = append(nodes, widgets.TreeNode{
				ID:     task.BeadID,
				Title:  task.Title,
				Status: task.Phase,
				Depth:  1,
			})
		}
	}
	if len(orphans) > 0 {
		nodes = append(nodes, widgets.TreeNode{
			ID:       "obj:other",
			Title:    "Other",
			Status:   "planning",
			Depth:    0,
			Expanded: true,
		})
		for _, task := range orphans {
			nodes = append(nodes, widgets.TreeNode{
				ID:     task.BeadID,
				Title:  task.Title,
				Status: task.Phase,
				Depth:  1,
			})
		}
	}
	return nodes
}

// filterTasksByProject is lifted from the deleted lead.go. Same shape, just
// kept here so the column doesn't depend on the deleted package surface.
func filterTasksByProject(projectID int64) []FixtureTask {
	all := FixtureTasks()
	out := make([]FixtureTask, 0, len(all))
	for _, t := range all {
		if t.ProjectID == projectID {
			out = append(out, t)
		}
	}
	return out
}
