package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

func TestWorkflowImmutableReviewProvenance(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		roles                  map[string][]string
		missing, foreign, want string
	}{
		{name: "completed tasks share Manager owner", roles: map[string][]string{}},
		{name: "same actual writer", roles: map[string][]string{"review-task": {"operator-writer-task"}}, want: "differ from writer"},
		{name: "any historical writer role overlaps", roles: map[string][]string{"writer-task": {"operator-old", "operator-shared"}, "review-task": {"operator-new", "operator-shared"}}, want: "differ from writer"},
		{name: "missing writer", missing: "writer-task", want: "no issue"},
		{name: "missing reviewer", missing: "review-task", want: "no issue"},
		{name: "foreign reviewer", foreign: "review-task", want: "outside workstream"},
		{name: "foreign writer", foreign: "writer-task", want: "outside workstream"},
		{name: "unavailable reviewer run", roles: map[string][]string{"review-task": nil}, want: "unavailable"},
		{name: "unavailable writer run", roles: map[string][]string{"writer-task": nil}, want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mem := &beads.MemClient{}
			res := seedReviewWorkflow(t, mem)
			pinWorkflowGH(t, workflowFakeGH)
			mem.Close(ctx, "/d", res.Remaining[0].BeadID)
			mem.Seed(beads.Bead{ID: "review-task", Status: "closed", Labels: []string{"owner:manager-B"}})
			mem.SetParent(ctx, "/d", "review-task", "B")
			mem.AddComment(ctx, "/d", "review-task", "Head: head1\nCLEAN")
			raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "review-task", Head: "head1", NoFindings: true})
			mem.AddComment(ctx, "/d", res.Remaining[1].BeadID, workflowtemplate.ProofPrefix+string(raw))
			if tc.missing != "" {
				mem.Delete(ctx, "/d", tc.missing, "test")
			}
			if tc.foreign != "" {
				mem.Seed(beads.Bead{ID: "foreign-root"})
				mem.SetParent(ctx, "/d", tc.foreign, "foreign-root")
			}
			old := workflowTaskRoles
			workflowTaskRoles = func(_ context.Context, _, task string) ([]string, error) {
				if roles, ok := tc.roles[task]; ok {
					if roles == nil {
						return nil, errors.New("run unavailable")
					}
					return roles, nil
				}
				return []string{"operator-" + task}, nil
			}
			t.Cleanup(func() { workflowTaskRoles = old })
			// Caller and all mutable owners coincide; none determine independence.
			t.Setenv("COMMANDER_ROLE", "manager-B")
			before, _ := mem.Comments(ctx, "/d", res.Remaining[1].BeadID)
			err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[1].BeadID)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v want %s", err, tc.want)
			}
			if tc.want != "" {
				after, _ := mem.Comments(ctx, "/d", res.Remaining[1].BeadID)
				if len(after) != len(before) {
					t.Fatal("refusal wrote evidence", after)
				}
			}
		})
	}
}

func TestWorkflowBindWritersScopeAndRevision(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	head := strings.Repeat("a", 40)
	pinWorkflowGH(t, func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := workflowFakeGH(ctx, args...)
		return []byte(strings.ReplaceAll(string(out), "head1", head)), err
	})
	t.Setenv("COMMANDER_ROLE", "manager-B")
	bind := func(tasks []string) (*workflowtemplate.WriterBinding, error) {
		return bindWorkflowWriters(ctx, mem, "/d", "p", res.Container, head, tasks)
	}
	b, err := bind([]string{"writer-task"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := bind([]string{"writer-task"})
	if err != nil || again.Revision != b.Revision {
		t.Fatal(again, err)
	}
	// A user CLI is supported; an owning Manager may inherit a host context.
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	if user, err := bind([]string{"writer-task"}); err != nil || user.Revision != b.Revision {
		t.Fatal(user, err)
	}
	t.Setenv("COMMANDER_ROLE", "manager-B")
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-other")
	if manager, err := bind([]string{"writer-task"}); err != nil || manager.Revision != b.Revision {
		t.Fatal(manager, err)
	}
	// Plug is idempotent and preserves the existing selection.
	if _, err := plugWorkflow(ctx, mem, "/d", plugInstance(t, testPR)); err != nil {
		t.Fatal(err)
	}
	cs, _ := mem.Comments(ctx, "/d", res.Container)
	preserved, err := workflowtemplate.Binding(cs)
	if err != nil || preserved.Revision != b.Revision {
		t.Fatal(preserved, err)
	}
	mem.Seed(beads.Bead{ID: "fix-task", Status: "closed"})
	mem.SetParent(ctx, "/d", "fix-task", "B")
	changed, err := bind([]string{"writer-task", "fix-task"})
	if err != nil || changed.Revision == b.Revision {
		t.Fatal(changed, err)
	}
	reverted, err := bind([]string{"writer-task"})
	if err != nil || reverted.Revision == b.Revision {
		t.Fatal(reverted, err)
	}
	for _, task := range []string{"absent", "foreign"} {
		if task == "foreign" {
			mem.Seed(beads.Bead{ID: task})
		}
		if _, err := bind([]string{task}); err == nil {
			t.Fatalf("accepted %s", task)
		}
	}
	t.Setenv("COMMANDER_ROLE", "operator-writer-task")
	if _, err := bind([]string{"writer-task"}); err == nil {
		t.Fatal("Operator bound writers")
	}
	t.Setenv("COMMANDER_ROLE", "manager-B")
	if _, err := bindWorkflowWriters(ctx, mem, "/d", "p", res.Container, strings.Repeat("b", 40), []string{"writer-task"}); err == nil {
		t.Fatal("stale head accepted")
	}
}

func TestWorkflowTaskRolesAllRunsAndProject(t *testing.T) {
	setupGateProject(t)
	ctx := context.Background()
	st, done, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	p, err := st.GetProjectByName(ctx, "manifest")
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
	for i, role := range []string{"op-build-t.1", "operator-op-fix-t.1", "op-build-t-1"} {
		_, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: p.ID, TaskBeadID: "t.1", AgentName: role, OpencodeSessionID: role, SpawnTime: int64(i + 1), Status: "closed"})
		if err != nil {
			t.Fatal(err)
		}
	}
	roles, err := workflowTaskRoles(ctx, "manifest", "t.1")
	if err != nil || len(roles) != 2 || !slices.Contains(roles, "operator-op-build-t-1") || !slices.Contains(roles, "operator-op-fix-t-1") {
		t.Fatal(roles, err)
	}
	if _, err := workflowTaskRoles(ctx, "missing", "t.1"); err == nil {
		t.Fatal("unknown project accepted")
	}
	if _, err := workflowTaskRoles(ctx, "manifest", "t.2"); err == nil {
		t.Fatal("unrun task accepted")
	}
}

func TestWorkflowTaskRolesRejectAmbiguousRows(t *testing.T) {
	setupGateProject(t)
	ctx := context.Background()
	st, done, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	p, err := st.GetProjectByName(ctx, "manifest")
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
	foreign, err := st.InsertProject(ctx, store.Project{Name: "foreign", Type: "code", Path: "/foreign", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.AgentRun{
		{ProjectID: p.ID, TaskBeadID: "ambiguous", AgentName: "operator-writer", OpencodeSessionID: "a", SpawnTime: 1, Status: "closed"},
		{ProjectID: foreign, TaskBeadID: "ambiguous", AgentName: "operator-foreign", OpencodeSessionID: "b", SpawnTime: 2, Status: "closed"},
		{ProjectID: p.ID, TaskBeadID: "missing-role", AgentName: "", OpencodeSessionID: "c", SpawnTime: 3, Status: "closed"},
		{ProjectID: p.ID, TaskBeadID: "manager-role", AgentName: "manager-root", OpencodeSessionID: "d", SpawnTime: 4, Status: "closed"},
	} {
		if _, err := st.InsertAgentRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []string{"ambiguous", "missing-role", "manager-role"} {
		if _, err := workflowTaskRoles(ctx, "manifest", task); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatal(task, err)
		}
	}
}

func TestWorkflowAdversarialRoleOverlap(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowGH(t, workflowFakeGH)
	for _, task := range []string{"primary", "peer"} {
		mem.Seed(beads.Bead{ID: task, Status: "closed", Labels: []string{"owner:manager-B"}})
		mem.SetParent(ctx, "/d", task, "B")
		mem.AddComment(ctx, "/d", task, "Head: head1\nCLEAN")
	}
	for _, s := range res.Remaining[:3] {
		if s.Step != "devin-comments" {
			raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "primary", Head: "head1", NoFindings: true})
			mem.AddComment(ctx, "/d", s.BeadID, workflowtemplate.ProofPrefix+string(raw))
		}
		if err := checkWorkflowClose(ctx, mem, "/d", "p", s.BeadID); err != nil {
			t.Fatal(err)
		}
		mem.Close(ctx, "/d", s.BeadID)
	}
	raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "peer", Head: "head1", NoFindings: true})
	mem.AddComment(ctx, "/d", res.Remaining[3].BeadID, workflowtemplate.ProofPrefix+string(raw))
	old := workflowTaskRoles
	workflowTaskRoles = func(_ context.Context, _, task string) ([]string, error) {
		if task == "peer" || task == "primary" {
			return []string{"operator-common"}, nil
		}
		return []string{"operator-" + task}, nil
	}
	t.Cleanup(func() { workflowTaskRoles = old })
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[3].BeadID); err == nil || !strings.Contains(err.Error(), "adversarial reviewer") {
		t.Fatal(err)
	}
}

// Operator refusals happen before even reading a block, including legacy launches.
func TestWorkflowBindWritersCallerAuthorization(t *testing.T) {
	for _, tc := range []struct{ name, role, manager string }{
		{"operator", "operator-fix", "manager-B"},
		{"roleless operator", "", "manager-B"},
		{"foreign manager", "manager-other", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			res := seedReviewWorkflow(t, mem)
			t.Setenv("COMMANDER_ROLE", tc.role)
			t.Setenv("COMMANDER_MANAGER_ROLE", tc.manager)
			before, _ := mem.Comments(context.Background(), "/d", res.Container)
			_, err := bindWorkflowWriters(context.Background(), mem, "/d", "p", res.Container, strings.Repeat("a", 40), []string{"writer-task"})
			if err == nil || !strings.Contains(err.Error(), "writer selection belongs") {
				t.Fatal(err)
			}
			after, _ := mem.Comments(context.Background(), "/d", res.Container)
			if len(after) != len(before) {
				t.Fatal("refusal wrote a binding", after)
			}
			if tc.role == "" || strings.HasPrefix(tc.role, "operator-") {
				// A nil client would panic if authorization allowed any read.
				if _, err := bindWorkflowWriters(context.Background(), nil, "", "", "", "", nil); err == nil {
					t.Fatal("Operator passed early authorization")
				}
			}
		})
	}
}

func TestWorkflowWriterBindingDoorRefusalsBeforeWrite(t *testing.T) {
	for _, tc := range []struct{ name, pr, head, want string }{
		{"missing", "", "", "writer binding required"},
		{"foreign PR", "https://github.com/other/repo/pull/8", "head1", "writer binding PR differs from block"},
		{"stale head", testPR, "head0", "stale writer binding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			// A newly plugged workflow has no binding.
			mem.Seed(beads.Bead{ID: "B", Status: "open"})
			mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user"}})
			res, err := plugWorkflow(context.Background(), mem, "/d", plugInstance(t, testPR))
			if err != nil {
				t.Fatal(err)
			}
			if tc.pr != "" {
				mem.AddComment(context.Background(), "/d", res.Container, workflowtemplate.WriterBinding{Revision: "rev", PR: tc.pr, Head: tc.head, Tasks: []string{"writer"}}.Comment())
			}
			pinWorkflowGH(t, workflowFakeGH)
			id := res.Remaining[0].BeadID
			err = checkWorkflowClose(context.Background(), mem, "/d", "p", id)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v want %q", err, tc.want)
			}
			cs, _ := mem.Comments(context.Background(), "/d", id)
			b, _ := mem.Show(context.Background(), "/d", id)
			if len(cs) != 0 || b.Status == "closed" {
				t.Fatal("refusal wrote evidence or closed step", cs, b)
			}
		})
	}
}

func TestWorkflowBindWritersDispatchRefusesBeforeIO(t *testing.T) {
	for _, role := range []string{"operator-fix", ""} {
		t.Run("role="+role, func(t *testing.T) {
			t.Setenv("COMMANDER_ROLE", role)
			t.Setenv("COMMANDER_MANAGER_ROLE", "manager-B")
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			old := resolveProjectRecFn
			resolveProjectRecFn = func(string) (*store.Project, error) {
				t.Fatal("authorization reached project/store read")
				return nil, nil
			}
			t.Cleanup(func() { resolveProjectRecFn = old })
			out, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			args := []string{"bind-writers", "--project", "p", "--block", "R", "--writers", "writer-task", "--head", strings.Repeat("a", 40)}
			if code := runWorkflow(args, out, out); code != 1 {
				t.Fatalf("exit %d", code)
			}
			raw, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "writer selection belongs") {
				t.Fatalf("wrong refusal: %s", raw)
			}
			if _, err := os.Stat(filepath.Join(home, "workflow-locks")); !os.IsNotExist(err) {
				t.Fatalf("authorization reached lock: %v", err)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("home changed: %v %v", entries, err)
			}
		})
	}
}
