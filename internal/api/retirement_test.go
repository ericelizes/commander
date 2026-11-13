package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Exercises the real guarded write service with tonight's parked legacy stage
// and both obsolete six-step pr-review blocks, without a writer binding.
func TestPostgresPlanningRetirementScenario(t *testing.T) {
	var scenario struct {
		Project string   `json:"project"`
		Stage   string   `json:"legacy_stage"`
		Gate    string   `json:"legacy_gate"`
		Blocks  []string `json:"review_blocks"`
		Reason  string   `json:"reason"`
	}
	raw, e := os.ReadFile("../../tests/evals/scenarios/planning-retirement.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	dir := projectDir(t)
	pid, e := st.InsertProject(ctx, store.Project{Name: scenario.Project, Path: dir, Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "retirement regression"); e != nil {
		t.Fatal(e)
	}
	insert := func(id, parent, title, description, status string, labels []string) {
		t.Helper()
		raw, _ := json.Marshal(labels)
		if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,status,labels,created_at,updated_at) VALUES(?,?,NULLIF(?,''),?,?,?,?::jsonb,'now','now')`, pid, id, parent, title, description, status, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	edge := func(from, to, kind string) {
		t.Helper()
		if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,?)`, pid, from, to, kind); e != nil {
			t.Fatal(e)
		}
	}
	insert("root", "", "Commander", "", "open", []string{"kind:objective", beads.WorkstreamLabel})
	insert(scenario.Stage, "root", "Reviewed board repair fast-forwarded to local main", "", "deferred", []string{"kind:objective", "parked"})
	insert(scenario.Gate, "root", "gate merged", "git merge-base --is-ancestor old main", "open", []string{"kind:gate", "verifier:auto", "gate-name:merged", "gate-status:failed", "gate-of:" + beads.EscapeLabel(scenario.Stage)})
	ghCalls := 0
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st, WorkflowGH: func(context.Context, ...string) ([]byte, error) {
		ghCalls++
		return []byte(`{"headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"OPEN","commits":[],"statusCheckRollup":[]}`), nil
	}}).Handler())
	defer ts.Close()
	write := func(args []string, env ...string) beads.DoorResponse {
		t.Helper()
		if env == nil {
			env = []string{"COMMANDER_ROLE=commander"}
		}
		_, r := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: args, Atomic: true, NoWait: true, Env: env})
		return r
	}
	if r := write([]string{"close", scenario.Stage, "--reason", "superseded"}); r.Exit == 0 {
		t.Fatal("failed merged gate did not refuse ordinary close")
	}
	for _, id := range []string{"root", scenario.Gate} {
		mode := "legacy-stage"
		if r := write([]string{"retire", id, "--mode", mode, "--reason", scenario.Reason}); r.Exit == 0 {
			t.Fatalf("protected target retired: %s", id)
		}
	}
	if r := write([]string{"retire", scenario.Stage, "--mode", "legacy-stage", "--reason", scenario.Reason, "--dry-run", "true"}); r.Exit != 0 {
		t.Fatal(r)
	}
	if r := write([]string{"retire", scenario.Stage, "--mode", "legacy-stage", "--reason", scenario.Reason}); r.Exit != 0 {
		t.Fatal(r)
	}
	data, e := os.ReadFile("../workflowtemplate/testdata/pr-review-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	tpl, e := workflowtemplate.Parse(data)
	if e != nil {
		t.Fatal(e)
	}
	for _, block := range scenario.Blocks {
		target := block + "-ship"
		pr := "https://github.com/example/repo/pull/42"
		in, e := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: scenario.Project, Parent: "root", Before: target, PR: pr})
		if e != nil {
			t.Fatal(e)
		}
		insert(target, "root", "Ship reviewed PR", "", "open", []string{"kind:objective", "pr:42"})
		insert(block, "root", in.Title, "Review "+pr+"\n\n"+in.Snapshot, "open", in.Labels)
		edge(target, block, "blocks")
		stepIDs := map[string]string{}
		for i, s := range in.Steps {
			id := fmt.Sprintf("%s.%d", block, i+1)
			stepIDs[s.ID] = id
			insert(id, block, s.Title, "", "open", s.Labels)
			edge(id, block, "parent-child")
		}
		for step, deps := range tpl.Waits() {
			for _, dep := range deps {
				edge(stepIDs[step], stepIDs[dep], "blocks")
			}
		}
		last := tpl.Steps[len(tpl.Steps)-1]
		if r := write([]string{"close", stepIDs[last.ID]}); r.Exit == 0 || !(strings.Contains(r.Error, "still needs closed predecessor") || strings.Contains(r.Error, "unpassed dependencies")) {
			t.Fatalf("predecessor refusal: %+v", r)
		}
		// Preserve an already completed Devin step, as in old plugged blocks;
		// the first remaining review step still cannot close without writers.
		if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='closed',close_reason='prior completed check' WHERE project_id=? AND id=?`, pid, stepIDs[tpl.Steps[0].ID]); e != nil {
			t.Fatal(e)
		}
		if r := write([]string{"close", stepIDs[tpl.Steps[1].ID]}); r.Exit == 0 || !strings.Contains(r.Error, "writer binding required") {
			t.Fatalf("writer binding refusal: %+v", r)
		}
		args := []string{"retire", block, "--mode", "workflow-block", "--reason", scenario.Reason}
		if r := write(args, "COMMANDER_ROLE=commander", "COMMANDER_RUN_ID=999", "COMMANDER_RUN_SESSION=execution"); r.Exit == 0 {
			t.Fatal("execution borrowed planner authority")
		}
		beforeGH := ghCalls
		basis := &beads.PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{}}
		for _, id := range append([]string{block}, mapValues(stepIDs)...) {
			var revision int64
			if e = st.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&revision); e != nil {
				t.Fatal(e)
			}
			basis.Beads[id] = revision
		}
		_, r := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Ops: [][]string{args}, Atomic: true, NoWait: true, Plan: basis, Env: []string{"COMMANDER_ROLE=commander"}})
		if r.Exit != 0 {
			t.Fatal(r)
		}
		if ghCalls != beforeGH {
			t.Fatal("retirement contacted GitHub")
		}
		if r := write(args); r.Exit != 0 {
			t.Fatalf("retry not idempotent: %+v", r)
		}
		for _, id := range stepIDs {
			var status, desc string
			if e = st.DB().QueryRowContext(ctx, `SELECT status,description FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&status, &desc); e != nil || status != "closed" {
				t.Fatalf("%s: %s %v", id, status, e)
			}
		}
		var keptReason, definition string
		if e = st.DB().QueryRowContext(ctx, `SELECT close_reason FROM bead_tasks WHERE project_id=? AND id=?`, pid, stepIDs[tpl.Steps[0].ID]).Scan(&keptReason); e != nil || keptReason != "prior completed check" {
			t.Fatal("completed history changed", keptReason, e)
		}
		if e = st.DB().QueryRowContext(ctx, `SELECT description FROM bead_tasks WHERE project_id=? AND id=?`, pid, block).Scan(&definition); e != nil || definition != "Review "+pr+"\n\n"+in.Snapshot {
			t.Fatal("workflow snapshot changed", e)
		}
		if r := write([]string{"close", target}); r.Exit == 0 || !strings.Contains(r.Error, "MERGE_REVIEW") {
			t.Fatalf("retirement fabricated review approval: %+v", r)
		}
	}
	var failed bool
	if e = st.DB().QueryRowContext(ctx, `SELECT jsonb_exists(labels,'gate-status:failed') FROM bead_tasks WHERE project_id=? AND id=?`, pid, scenario.Gate).Scan(&failed); e != nil || !failed {
		t.Fatal("failed gate history lost", e)
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, value := range m {
		out = append(out, value)
	}
	return out
}
