package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowclose"
	"github.com/example-user/commander/internal/workflowtemplate"
)

func TestPostgresPlanReviewAddendumSurvivesCommanderHandoff(t *testing.T) {
	var scenario struct {
		Jurisdiction struct {
			Commander string
			Flow      []string
		}
	}
	scenarioRaw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "review-addendum.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(scenarioRaw, &scenario); e != nil {
		t.Fatal(e)
	}
	if scenario.Jurisdiction.Commander == "" || len(scenario.Jurisdiction.Flow) != 7 {
		t.Fatal("jurisdiction scenario missing")
	}
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	st, dsn := pgtest.Open(t)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "manifest", Path: dir, Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	// Build retained review artifacts using the same fixture as the legacy eval,
	// then load their exact bytes and graph into a private canonical database.
	mem := &addendumSourceClient{MemClient: &beads.MemClient{}}
	res := seedReviewWorkflow(t, mem.MemClient)
	a, original := seedAddendumArtifacts(t)
	binding := workflowtemplate.WriterBinding{Revision: "current", PR: testPR, Head: a.Head, Tasks: []string{"writer-task"}}
	if e = mem.AddComment(ctx, dir, res.Container, binding.Comment()); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"review-task", "assessor-task", "policy-task"} {
		mem.Seed(beads.Bead{ID: id, Status: "closed"})
		if e = mem.SetParent(ctx, dir, id, "B"); e != nil {
			t.Fatal(e)
		}
	}
	if e = mem.AddComment(ctx, dir, "review-task", original); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(a)
	if e = mem.AddComment(ctx, dir, "assessor-task", workflowtemplate.AssessmentPrefix+string(raw)); e != nil {
		t.Fatal(e)
	}
	if e = mem.AddComment(ctx, dir, "policy-task", "Extra dedicated prelaunch failed-peer seal policy to withdraw."); e != nil {
		t.Fatal(e)
	}
	if e = mem.Close(ctx, dir, res.Remaining[0].BeadID); e != nil {
		t.Fatal(e)
	}
	all, e := mem.List(ctx, dir, beads.ListOptions{Status: "all"})
	if e != nil {
		t.Fatal(e)
	}
	details, e := mem.Details(ctx, dir, func() []string {
		var ids []string
		for _, b := range all {
			ids = append(ids, b.ID)
		}
		return ids
	}())
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range all {
		labels := b.Labels
		if labels == nil {
			labels = []string{}
		}
		encoded, _ := json.Marshal(labels)
		status := b.Status
		if status == "" {
			status = "open"
		}
		if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,status,labels,created_at,updated_at) VALUES(?,?,?,?,?,?::jsonb,'now','now')`, pid, b.ID, b.Title, b.Description, status, string(encoded)); e != nil {
			t.Fatal(e)
		}
	}
	for _, b := range all {
		d := details[b.ID]
		if d.Parent != "" {
			if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET parent_id=? WHERE project_id=? AND id=?`, d.Parent, pid, b.ID); e != nil {
				t.Fatal(e)
			}
		}
		for _, dep := range d.Deps {
			if dep.Type == "blocks" {
				if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,'blocks')`, pid, b.ID, dep.DependsOnID); e != nil {
					t.Fatal(e)
				}
			}
		}
		cs, e := mem.Comments(ctx, dir, b.ID)
		if e != nil {
			t.Fatal(e)
		}
		for i, c := range cs {
			at := c.CreatedAt
			if at == "" {
				at = time.Date(2026, 1, 2, 3, 4, 5+i, 0, time.UTC).Format(time.RFC3339Nano)
			}
			if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,?,?,?,?,?)`, pid, b.ID, c.ID, c.Author, c.Text, at); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, task := range []string{"review-task", "assessor-task", "writer-task"} {
		if _, e = st.DB().ExecContext(ctx, `INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(?,?,?,'fixture-session',1,'closed')`, pid, task, "executor-"+task); e != nil {
			t.Fatal(e)
		}
	}
	if e = st.RegisterCommander(ctx, scenario.Jurisdiction.Commander, "Urgent"); e != nil {
		t.Fatal(e)
	}
	basis, e := st.Jurisdiction(ctx, pid, "B")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "B", ExpectedOwner: "commander", ExpectedRevision: basis.Revision, NextCommander: scenario.Jurisdiction.Commander, Reason: "urgent review scope"}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "addendum jurisdiction test"); e != nil {
		t.Fatal(e)
	}
	gh := func(context.Context, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"headRefOid": a.Head, "baseRefOid": a.Base, "state": "OPEN", "commits": []any{}, "statusCheckRollup": []any{map[string]string{"status": "COMPLETED", "conclusion": "SUCCESS"}}})
	}
	pinWorkflowGH(t, gh)
	server := httptest.NewServer(api.New(api.Options{Token: "addendum-test", StateStore: st, WorkflowGH: gh}).Handler())
	defer server.Close()
	for name, value := range map[string]string{"api-url": server.URL, "api-token": "addendum-test"} {
		if e = os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	priorStore, priorClient, priorRoles, priorResults := needsYouStoreFn, newBeadsClientFn, workflowTaskRoles, workflowResultRuns
	needsYouStoreFn = func() (*store.Store, func(), error) { return st, func() {}, nil }
	newBeadsClientFn = func() beads.Client { return beads.New() }
	provenance := workflowclose.Provenance{Canonical: true, Q: st.DB(), Home: home}
	workflowTaskRoles = provenance.TaskRoles
	workflowResultRuns = provenance.ResultRuns
	t.Cleanup(func() {
		needsYouStoreFn = priorStore
		newBeadsClientFn = priorClient
		workflowTaskRoles = priorRoles
		workflowResultRuns = priorResults
	})
	args := []string{"--project", "manifest", "--block", res.Container, "--step", res.Remaining[1].BeadID, "--original-comment", "comment-1", "--assessment-bead", "assessor-task", "--assessment-comment", "comment-1", "--policy-bead", "policy-task", "--policy-comment", "comment-1"}
	t.Setenv("COMMANDER_ROLE", "commander")
	out, errout, read := withTempOutputFiles(t)
	if runWorkflowAddendum(args, out, errout) == 0 {
		t.Fatal("former owner recorded an addendum")
	}
	read()
	t.Setenv("COMMANDER_ROLE", scenario.Jurisdiction.Commander)
	out, errout, read = withTempOutputFiles(t)
	if code := runWorkflowAddendum(args, out, errout); code != 0 {
		_, why := read()
		t.Fatalf("current Commander recording failed: %d %s", code, why)
	}
	text, _ := read()
	var ref workflowtemplate.AddendumRef
	if e = json.Unmarshal([]byte(text), &ref); e != nil {
		t.Fatal(e)
	}
	out, errout, read = withTempOutputFiles(t)
	if code := runWorkflowAddendum(args, out, errout); code != 0 {
		_, why := read()
		t.Fatalf("idempotent recording failed: %d %s", code, why)
	}
	replay, _ := read()
	var repeated workflowtemplate.AddendumRef
	if e = json.Unmarshal([]byte(replay), &repeated); e != nil || repeated != ref {
		t.Fatal("recording retry changed the original receipt", repeated, e)
	}
	var count int
	var author string
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*),MIN(author) FROM bead_comments WHERE project_id=? AND bead_id=? AND fact_kind='review-addendum'`, pid, res.Container).Scan(&count, &author); e != nil || count != 1 || author != scenario.Jurisdiction.Commander {
		t.Fatalf("missing canonical recorder attribution: %d %s %v", count, author, e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "B", ExpectedOwner: scenario.Jurisdiction.Commander, ExpectedRevision: basis.Revision + 1, Inherit: true, Reason: "return reviewed scope"}); e != nil {
		t.Fatal(e)
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	// A copied, unstamped record cannot borrow the old Commander's authority.
	var recorded workflowtemplate.AddendumRecord
	comments, e := beads.New().Comments(ctx, dir, res.Container)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range comments {
		if len(c.Text) > len(workflowtemplate.AddendumPrefix) && c.Text[:len(workflowtemplate.AddendumPrefix)] == workflowtemplate.AddendumPrefix {
			if e = json.Unmarshal([]byte(c.Text[len(workflowtemplate.AddendumPrefix):]), &recorded); e != nil {
				t.Fatal(e)
			}
		}
	}
	forged := recorded
	forged.ID = "unstamped-copy"
	if e = beads.New().AddComment(ctx, dir, res.Container, forged.Comment()); e == nil {
		t.Fatal("current actor impersonated the previous recorder")
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,?,'unstamped-copy',?,?,'2026-01-02T04:00:00Z')`, pid, res.Container, forged.Actor, forged.Comment()); e != nil {
		t.Fatal(e)
	}
	forgedRef := forged.Reference()
	proof := workflowtemplate.Proof{ReviewBead: "review-task", Head: a.Head, NoFindings: true, Addendum: &forgedRef}
	raw, _ = json.Marshal(proof)
	client := beads.New()
	step := res.Remaining[1].BeadID
	if e = client.AddComment(ctx, dir, step, workflowtemplate.ProofPrefix+string(raw)); e != nil {
		t.Fatal(e)
	}
	if e = client.Close(ctx, dir, step); e == nil {
		t.Fatal("unstamped record passed canonical completion")
	}
	proof.Addendum = &ref
	raw, _ = json.Marshal(proof)
	if e = client.AddComment(ctx, dir, step, workflowtemplate.ProofPrefix+string(raw)); e != nil {
		t.Fatal(e)
	}
	if e = client.Close(ctx, dir, step); e != nil {
		t.Fatalf("handoff invalidated recorded authority or artifact proof: %v", e)
	}
	var retained string
	if e = st.DB().QueryRowContext(ctx, `SELECT text FROM bead_comments WHERE project_id=? AND bead_id='review-task' AND id='comment-1'`, pid).Scan(&retained); e != nil || retained != original {
		t.Fatal("original review history changed", e)
	}
}
