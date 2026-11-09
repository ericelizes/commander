package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type selectiveBeadsClient struct {
	objectives []beads.Bead
	tasks      []beads.Bead
}

func (s selectiveBeadsClient) Init(context.Context, string) error {
	return nil
}

func (s selectiveBeadsClient) SetParent(context.Context, string, string, string) error { return nil }

func (s selectiveBeadsClient) SetState(context.Context, string, string, string, string, string) error {
	return nil
}
func (s selectiveBeadsClient) Create(context.Context, string, string, beads.CreateOptions) (string, error) {
	return "", nil
}
func (s selectiveBeadsClient) Rename(context.Context, string, string, string) error {
	return nil
}

func (s selectiveBeadsClient) Close(context.Context, string, string) error {
	return nil
}

func (s selectiveBeadsClient) DependOn(context.Context, string, string, string) error {
	return nil
}

func (s selectiveBeadsClient) RemoveDep(context.Context, string, string, string) error { return nil }

func (s selectiveBeadsClient) UpdateFields(context.Context, string, string, beads.Fields) error {
	return nil
}

func (s selectiveBeadsClient) CloseWithReason(context.Context, string, string, string) error {
	return nil
}

func (s selectiveBeadsClient) Delete(context.Context, string, string, string) error {
	return nil
}

func (s selectiveBeadsClient) Deps(context.Context, string, string) ([]beads.Dep, error) {
	return nil, nil
}

func (s selectiveBeadsClient) Dependents(context.Context, string, string) ([]beads.Dep, error) {
	return nil, nil
}

func (s selectiveBeadsClient) Details(context.Context, string, []string) (map[string]beads.Detail, error) {
	return nil, nil
}

func (s selectiveBeadsClient) SetStatus(context.Context, string, string, string) error {
	return nil
}

func (s selectiveBeadsClient) SetLabel(context.Context, string, string, string, bool) error {
	return nil
}

func (s selectiveBeadsClient) Link(context.Context, string, string, string, string) error {
	return nil
}
func (s selectiveBeadsClient) Show(context.Context, string, string) (*beads.Bead, error) {
	return nil, nil
}
func (s selectiveBeadsClient) List(_ context.Context, _ string, opts beads.ListOptions) ([]beads.Bead, error) {
	if len(opts.Labels) == 1 && opts.Labels[0] == "kind:objective" {
		return s.objectives, nil
	}
	if len(opts.Labels) == 1 && opts.Labels[0] == "kind:task" {
		return s.tasks, nil
	}
	return []beads.Bead{}, nil
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedTestObjective(t *testing.T, st *store.Store, projectID int64, title, taskID string) {
	t.Helper()
	ctx := context.Background()
	objectiveID, err := st.InsertObjective(ctx, store.Objective{
		Title:     title,
		Status:    "open",
		CreatedAt: 1,
	})
	if err != nil {
		t.Fatalf("insert objective %q: %v", title, err)
	}
	if _, err := st.InsertObjectiveTask(ctx, store.ObjectiveTask{
		ObjectiveID: objectiveID,
		ProjectID:   projectID,
		TaskBeadID:  taskID,
		CreatedAt:   1,
	}); err != nil {
		t.Fatalf("insert objective task %q: %v", taskID, err)
	}
}

// Smoke tests for the column renderers. Each one constructs the column in
// fixture mode (no live services), feeds a WindowSizeMsg so SetSize fires,
// and asserts that View() contains some marker text appropriate to the
// column. These guard against accidental nil-panics or empty-render
// regressions; they don't try to be visual snapshot tests.

func TestObjectivesColumn_RendersFixtureObjectives(t *testing.T) {
	c := NewObjectivesColumn(nil)
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "frontend") {
		t.Errorf("expected fixture project tag in view:\n%s", view)
	}
	if !strings.Contains(view, " 1  ") {
		t.Errorf("expected numbered workstream rows in view:\n%s", view)
	}
	if !strings.Contains(view, "auth refactor") {
		t.Errorf("expected fixture objective text in view:\n%s", view)
	}
	if !strings.Contains(view, "Land Q3 roadmap doc") {
		t.Errorf("expected multiple project objective groups in view:\n%s", view)
	}
	if strings.Contains(view, "NO WORKSTREAMS YET") {
		t.Errorf("empty NO WORKSTREAMS YET section should be hidden:\n%s", view)
	}
	if !strings.Contains(view, "WORKSTREAMS") || strings.Contains(view, "ACTIVE PROJECTS") {
		t.Errorf("expected WORKSTREAMS header in view:\n%s", view)
	}
}

func TestObjectivesColumn_LiveStoreProjectsRenderOnFirstPaint(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	commanderPath := filepath.Join(t.TempDir(), "commander")
	if err := os.MkdirAll(commanderPath, 0o755); err != nil {
		t.Fatalf("mkdir commander path: %v", err)
	}
	commanderID, err := st.InsertProject(ctx, store.Project{
		Name:        "commander",
		Type:        "code",
		Path:        commanderPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert commander project: %v", err)
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(manifestPath, 0o755); err != nil {
		t.Fatalf("mkdir manifest path: %v", err)
	}
	manifestID, err := st.InsertProject(ctx, store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        manifestPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert manifest project: %v", err)
	}
	seedTestObjective(t, st, commanderID, "Ship auth refactor", "cmdr-001")
	seedTestObjective(t, st, manifestID, "Land launch site", "mft-001")

	c := NewObjectivesColumn(&Services{Store: st})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "commander") && !strings.Contains(view, "manifest") {
		t.Fatalf("the open workstream's meta line names its project on first paint, got:\n%s", view)
	}
	if !strings.Contains(view, "Ship auth refactor") || !strings.Contains(view, "Land launch site") {
		t.Fatalf("expected live objectives nested under projects, got:\n%s", view)
	}
	if strings.Contains(view, "(no projects yet)") {
		t.Fatalf("expected live store, not empty state:\n%s", view)
	}
}

func TestObjectivesColumn_LiveBeadsObjectivesRenderUnderProjects(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir manifest path: %v", err)
	}
	_, err := st.InsertProject(ctx, store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	c := NewObjectivesColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{
			objectives: []beads.Bead{
				{ID: "obj-1", Title: "Fix 2 bugs", Status: "open", Labels: []string{"kind:objective"}},
			},
			tasks: []beads.Bead{
				{ID: "task-1", Title: "Bug one", Status: "open", Labels: []string{"kind:task"}, ParentID: "obj-1"},
				{ID: "task-2", Title: "Bug two", Status: "open", Labels: []string{"kind:task"}, ParentID: "obj-1"},
			},
		},
	})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())
	if !strings.Contains(view, "Fix 2 bugs") {
		t.Fatalf("expected beads objective in view, got:\n%s", view)
	}
}

func TestObjectivesColumn_CreateObjectiveShowsImmediately(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir manifest path: %v", err)
	}
	projectID, err := st.InsertProject(ctx, store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	c := NewObjectivesColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{},
	})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*ObjectivesColumn)

	updated, _ = c.Update(objectiveActionResultMsg{
		status:    `Created objective "Investigate login bug" for manifest.`,
		projectID: projectID,
		created: &ObjectiveRow{
			BeadID:      "br-test",
			Title:       "Investigate login bug",
			ProjectName: "manifest",
			ProjectID:   projectID,
			Status:      "open",
		},
	})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())
	if !strings.Contains(view, "Investigate login bug") {
		t.Fatalf("expected optimistic objective in view, got:\n%s", view)
	}
	if strings.Contains(view, "No objectives yet") && strings.Contains(view, "manifest") {
		t.Fatalf("expected manifest empty state replaced after create, got:\n%s", view)
	}
}

func TestObjectivesColumn_EmptyProjectsRenderMutedPlaceholder(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "commander")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir commander path: %v", err)
	}
	_, err := st.InsertProject(ctx, store.Project{
		Name:        "commander",
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	c := NewObjectivesColumn(&Services{Store: st})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())

	if strings.Contains(view, "commander (code)") {
		t.Fatalf("project without objectives should not render in active tree, got:\n%s", view)
	}
	if !strings.Contains(view, "NO WORKSTREAMS YET") {
		t.Fatalf("expected no-objectives section in view, got:\n%s", view)
	}
	if !strings.Contains(view, "commander") {
		t.Fatalf("expected project listed in no-objectives section, got:\n%s", view)
	}
	if strings.Contains(view, "(no objectives yet)") {
		t.Fatalf("empty placeholder should not render as a tree row anymore, got:\n%s", view)
	}
}

func TestObjectivesColumn_OnlyProjectsWithObjectivesRenderInActiveTree(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	base := t.TempDir()
	commanderPath := filepath.Join(base, "commander")
	manifestPath := filepath.Join(base, "manifest")
	if err := os.MkdirAll(commanderPath, 0o755); err != nil {
		t.Fatalf("mkdir commander path: %v", err)
	}
	if err := os.MkdirAll(manifestPath, 0o755); err != nil {
		t.Fatalf("mkdir manifest path: %v", err)
	}
	commanderID, err := st.InsertProject(ctx, store.Project{
		Name:        "commander",
		Type:        "code",
		Path:        commanderPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert commander project: %v", err)
	}
	manifestID, err := st.InsertProject(ctx, store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        manifestPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert manifest project: %v", err)
	}
	seedTestObjective(t, st, manifestID, "Fix auth login bug", "mft-001")
	_ = commanderID

	c := NewObjectivesColumn(&Services{Store: st})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())

	noObjIdx := strings.Index(view, "NO WORKSTREAMS YET")
	activeIdx := strings.Index(view, "WORKSTREAMS ─")
	if activeIdx < 0 || noObjIdx < 0 || activeIdx >= noObjIdx {
		t.Fatalf("expected the WORKSTREAMS list above NO WORKSTREAMS YET, got:\n%s", view)
	}
	activeBlock := view[activeIdx:noObjIdx]
	if strings.Contains(activeBlock, "commander") {
		t.Fatalf("project without objectives should not appear in the list, got:\n%s", activeBlock)
	}
	if !strings.Contains(activeBlock, "manifest") {
		t.Fatalf("project with objectives should be tagged in the list, got:\n%s", activeBlock)
	}
	noObjectivesBlock := view[noObjIdx:]
	if !strings.Contains(noObjectivesBlock, "commander") {
		t.Fatalf("project without objectives should appear in NO WORKSTREAMS YET list, got:\n%s", view)
	}
}

func TestObjectivesColumn_LiveLoadErrorShowsWarning(t *testing.T) {
	c := NewObjectivesColumn(&Services{})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*ObjectivesColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "Commander state unavailable") {
		t.Fatalf("expected live load warning, got:\n%s", view)
	}
	if strings.Contains(view, "frontend") {
		t.Fatalf("should not fall back to fixtures in live/degraded mode:\n%s", view)
	}
}

func TestFastSelectWindowCmd_UsesDirectTmuxSelect(t *testing.T) {
	oldExec := execCommand
	t.Cleanup(func() { execCommand = oldExec })

	var gotName string
	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return exec.Command("true")
	}

	msg := fastSelectWindowCmd("manager (manifest)", "Opened manager for manifest.")()
	result, ok := msg.(objectiveActionResultMsg)
	if !ok {
		t.Fatalf("msg type = %T, want objectiveActionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected err: %v", result.err)
	}
	if result.status != "Opened manager for manifest." {
		t.Fatalf("status = %q", result.status)
	}
	if gotName != "tmux" {
		t.Fatalf("command = %q, want tmux", gotName)
	}
	got := strings.Join(gotArgs, " ")
	want := "select-window -t commander:manager (manifest)"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestLeadTasksColumn_EscUsesFastCommanderSelect(t *testing.T) {
	oldExec := execCommand
	t.Cleanup(func() { execCommand = oldExec })

	var got string
	execCommand = func(name string, args ...string) *exec.Cmd {
		got = fmt.Sprintf("%s %s", name, strings.Join(args, " "))
		return exec.Command("true")
	}

	c := NewLeadTasksColumn(nil, "manifest")
	updated, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEsc})
	_ = updated
	if cmd == nil {
		t.Fatal("expected command on esc")
	}
	msg := cmd()
	result, ok := msg.(objectiveActionResultMsg)
	if !ok {
		t.Fatalf("msg type = %T, want objectiveActionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected err: %v", result.err)
	}
	if !strings.Contains(got, "tmux select-window -t commander:commander") {
		t.Fatalf("got command %q", got)
	}
}

func TestApplyCommanderStartupWarning_UsesRealBootstrapError(t *testing.T) {
	var got CommanderLiveData
	applyCommanderStartupWarning("open store: permission denied", &Services{}, func(d CommanderLiveData) {
		got = d
	})
	if got.LoadErr == nil {
		t.Fatal("expected startup warning to set LoadErr")
	}
	if !strings.Contains(got.LoadErr.Error(), "open store: permission denied") {
		t.Fatalf("unexpected warning: %v", got.LoadErr)
	}
}

func TestFleetColumn_RendersFixtureMetrics(t *testing.T) {
	c := NewFleetColumn(nil)
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	c = updated.(*FleetColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "FLEET METRICS") {
		t.Errorf("expected FLEET METRICS header in view:\n%s", view)
	}
	if !strings.Contains(view, "OPERATORS") {
		t.Errorf("expected OPERATORS header in view:\n%s", view)
	}
	// At least one fixture operator should appear.
	if !strings.Contains(view, "operator-extract-session") {
		t.Errorf("expected fixture operator in view:\n%s", view)
	}
}

func TestLeadTasksColumn_RendersFixtureTasksForProject(t *testing.T) {
	c := NewLeadTasksColumn(nil, "frontend")
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	c = updated.(*LeadTasksColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "WORKSTREAMS") {
		t.Errorf("expected WORKSTREAMS header in view:\n%s", view)
	}
	// Frontend fixture tasks should appear (e.g. "Extract session middleware").
	if !strings.Contains(view, "Extract session middleware") {
		t.Errorf("expected frontend fixture task in view:\n%s", view)
	}
}

func TestLeadTasksColumn_LiveBeadsGroupTasksUnderObjectives(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir manifest path: %v", err)
	}
	if _, err := st.InsertProject(ctx, store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	c := NewLeadTasksColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{
			objectives: []beads.Bead{
				{ID: "obj-1", Title: "Fix 2 bugs", Status: "open", Labels: []string{"kind:objective"}},
			},
			tasks: []beads.Bead{
				{ID: "task-1", Title: "Bug one", Status: "open", Labels: []string{"kind:task"}, ParentID: "obj-1"},
			},
		},
	}, "manifest")
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	c = updated.(*LeadTasksColumn)
	updated, _ = c.Update(leadDataLoadedMsg{data: LoadLeadData(context.Background(), LeadDataDeps{Store: st, Beads: selectiveBeadsClient{
		objectives: []beads.Bead{{ID: "obj-1", Title: "Fix 2 bugs", Status: "open", Labels: []string{"kind:objective"}}},
		tasks:      []beads.Bead{{ID: "task-1", Title: "Bug one", Status: "open", Labels: []string{"kind:task"}, ParentID: "obj-1"}},
	}}, c.projectID)})
	c = updated.(*LeadTasksColumn)
	view := stripANSI(c.View())
	if !strings.Contains(view, "Fix 2 bugs") || !strings.Contains(view, "Bug one") {
		t.Fatalf("expected objective and nested task from beads, got:\n%s", view)
	}
}

func TestLeadFleetColumn_RendersFixtureMetricsForProject(t *testing.T) {
	c := NewLeadFleetColumn(nil, "frontend")
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	c = updated.(*LeadFleetColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "PROJECT METRICS") {
		t.Errorf("expected PROJECT METRICS header:\n%s", view)
	}
	if !strings.Contains(strings.ToUpper(view), "FRONTEND") {
		t.Errorf("expected FRONTEND scope hint in view:\n%s", view)
	}
}

func TestOperatorSideColumn_RendersFixtureCeilingsForTask(t *testing.T) {
	c := NewOperatorSideColumn(nil, "fro-001")
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	c = updated.(*OperatorSideColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "WORKTREE") {
		t.Errorf("expected WORKTREE header:\n%s", view)
	}
	if !strings.Contains(view, "CONTAINER") {
		t.Errorf("expected CONTAINER header:\n%s", view)
	}
	if !strings.Contains(view, "CEILINGS") {
		t.Errorf("expected CEILINGS header:\n%s", view)
	}
	// Branch label for fixture task fro-001 should include "cmdr/fro-001".
	if !strings.Contains(view, "cmdr/fro-001") {
		t.Errorf("expected cmdr/fro-001 branch label in view:\n%s", view)
	}
}

func TestOperatorSideColumn_KnowledgeProjectFlipsMode(t *testing.T) {
	// sur-003 is a knowledge-project task in the fixtures.
	c := NewOperatorSideColumn(nil, "sur-003")
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	c = updated.(*OperatorSideColumn)
	view := stripANSI(c.View())

	if !strings.Contains(view, "knowledge") {
		t.Errorf("expected 'knowledge' mode chip in view:\n%s", view)
	}
}

func TestColumn_QuitOnQ(t *testing.T) {
	// Sanity: every context column responds to "q" with tea.Quit. (The Workstreams
	// sidebar deliberately has no quit key; see TestObjectivesColumn_QDoesNotQuit.)
	cols := []tea.Model{
		NewFleetColumn(nil),
		NewLeadTasksColumn(nil, "frontend"),
		NewLeadFleetColumn(nil, "frontend"),
		NewOperatorSideColumn(nil, "fro-001"),
	}
	for i, m := range cols {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		if cmd == nil {
			t.Errorf("col[%d]: q should produce tea.Quit cmd, got nil", i)
		}
	}
}

func (s selectiveBeadsClient) AddComment(context.Context, string, string, string) error { return nil }

func (s selectiveBeadsClient) Comments(context.Context, string, string) ([]beads.Comment, error) {
	return nil, nil
}
