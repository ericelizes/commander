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

func TestPostgresPlanCompletionUsesFreshSharedVerifier(t *testing.T) {
	var scenario struct {
		PR      string `json:"pr"`
		Head    string `json:"head"`
		Updated string `json:"updated"`
	}
	data, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-shared-completion.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &scenario); e != nil {
		t.Fatal(e)
	}
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(DoorChecksEnv, "off")
	if e = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	dir := projectDir(t)
	pid, e := st.InsertProject(ctx, store.Project{Name: "completion", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	tpl := &workflowtemplate.Template{Schema: workflowtemplate.SchemaID, ID: "ci-required", Version: 1, Title: "Check CI", Steps: []workflowtemplate.Step{{ID: "ci", Title: "CI passed", Kind: "checkpoint", Actor: "automatic", Verifier: "ci-green"}}}
	in, e := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "completion", Parent: "root", Before: "ship", PR: scenario.PR})
	if e != nil {
		t.Fatal(e)
	}
	insert := func(id, parent, title, description string, labels []string) {
		t.Helper()
		raw, _ := json.Marshal(labels)
		if _, e := st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,description,labels,created_at,updated_at) VALUES(?,?,NULLIF(?,''),?,?,?::jsonb,'now','now')`, pid, id, parent, title, description, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	insert("root", "", "Goal", "", []string{"kind:workstream"})
	insert("block", "root", in.Title, "Check "+scenario.PR+"\n\n"+in.Snapshot, in.Labels)
	insert("ci", "block", in.Steps[0].Title, "", in.Steps[0].Labels)
	binding := workflowtemplate.WriterBinding{Revision: "binding-1", PR: scenario.PR, Head: scenario.Head, Tasks: []string{"writer"}}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,'block','binding','commander',?,'now')`, pid, binding.Comment()); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "shared completion test"); e != nil {
		t.Fatal(e)
	}
	if _, _, directErr := beads.RunBr(beads.WithDoorEnv(ctx, []string{"COMMANDER_ROLE=commander"}), dir, "close", "ci"); directErr == nil || !strings.Contains(directErr.Error(), "shared workflow verifier") {
		t.Fatalf("direct active close bypassed the service verifier: %v", directErr)
	}
	green := false
	head := scenario.Head
	calls := 0
	prState := "OPEN"
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if len(args) < 1 || args[0] != "pr" {
			return nil, fmt.Errorf("unexpected GitHub request %v", args)
		}
		conclusion := "FAILURE"
		if green {
			conclusion = "SUCCESS"
		}
		return []byte(fmt.Sprintf(`{"headRefOid":%q,"state":%q,"commits":[{"oid":%q}],"statusCheckRollup":[{"status":"COMPLETED","conclusion":%q}]}`, head, prState, head, conclusion)), nil
	}
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st, WorkflowGH: gh}).Handler())
	defer ts.Close()
	single := beads.DoorRequest{Dir: dir, Args: []string{"close", "ci"}, NoWait: true, Env: []string{"COMMANDER_ROLE=commander"}}
	_, out := postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "CI") {
		t.Fatalf("raw close bypassed failed CI: %+v", out)
	}
	green = true
	head = "new-unbound-head"
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "stale writer binding") {
		t.Fatalf("raw close ignored changed head: %+v", out)
	}
	head = scenario.Head
	var ciRevision, rootRevision int64
	if e = st.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='ci'`, pid).Scan(&ciRevision); e != nil {
		t.Fatal(e)
	}
	if e = st.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='root'`, pid).Scan(&rootRevision); e != nil {
		t.Fatal(e)
	}
	batch := beads.DoorRequest{Dir: dir, Atomic: true, NoWait: true, Plan: &beads.PlanBasis{RequestID: "verified-close", JurisdictionRevision: 1, Beads: map[string]int64{"ci": ciRevision, "root": rootRevision}}, Ops: [][]string{{"close", "ci"}, {"update", "root", "--title", scenario.Updated}}, Env: single.Env}
	// A later operation's refusal must roll back both closure and generated proof.
	bad := batch
	bad.Ops = [][]string{{"close", "ci"}, {"update", "root", "--status", "made-up"}}
	_, out = postWrite(t, ts.URL, testToken, bad)
	if out.Exit == 0 {
		t.Fatal("invalid final op unexpectedly committed")
	}
	var status string
	var proofCount int
	assertState := func(want string, n int) {
		t.Helper()
		if e = st.DB().QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id='ci'`, pid).Scan(&status); e != nil {
			t.Fatal(e)
		}
		if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='ci'`, pid).Scan(&proofCount); e != nil {
			t.Fatal(e)
		}
		if status != want || proofCount != n {
			t.Fatalf("close/proof escaped transaction: status=%s proofs=%d", status, proofCount)
		}
	}
	assertState("open", 0)
	_, out = postWrite(t, ts.URL, testToken, batch)
	if out.Exit != 0 {
		t.Fatalf("verified plan could not close through shared verifier: %+v", out)
	}
	assertState("closed", 1)
	before := calls
	_, out = postWrite(t, ts.URL, testToken, batch)
	if out.Exit != 0 || !out.Replayed || calls != before {
		t.Fatalf("committed receipt repeated side effects: %+v calls=%d->%d", out, before, calls)
	}
	// Review independence is checked against every immutable run, including
	// generic executors, rather than trusting a role-shaped mutable assignee.
	reviewTemplate := &workflowtemplate.Template{Schema: workflowtemplate.SchemaID, ID: "review-required", Version: 1, Title: "Independent review", Steps: []workflowtemplate.Step{{ID: "review", Title: "Review passed", Kind: "checkpoint", Actor: "separate-reviewer", Verifier: "review-clean"}}}
	reviewInstance, e := workflowtemplate.Instantiate(reviewTemplate, workflowtemplate.Params{Project: "completion", Parent: "root", Before: "ship", PR: scenario.PR})
	if e != nil {
		t.Fatal(e)
	}
	insert("review-block", "root", reviewInstance.Title, "Review "+scenario.PR+"\n\n"+reviewInstance.Snapshot, reviewInstance.Labels)
	insert("review-step", "review-block", reviewInstance.Steps[0].Title, "", reviewInstance.Steps[0].Labels)
	comment := func(bead, id, text string) {
		t.Helper()
		if _, e := st.DB().ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,?,?,'commander',?,'now')`, pid, bead, id, text); e != nil {
			t.Fatal(e)
		}
	}
	comment("review-block", "binding", binding.Comment())
	for _, run := range []struct{ task, actor string }{{"writer", "executor-generic-a"}, {"reviewer", "executor-generic-a"}, {"independent", "executor-generic-b"}} {
		insert(run.task, "root", run.task, "", []string{})
		if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='closed' WHERE project_id=? AND id=?`, pid, run.task); e != nil {
			t.Fatal(e)
		}
		if _, e = st.DB().ExecContext(ctx, `INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(?,?,?,?,1,'closed')`, pid, run.task, run.actor, "session-"+run.task); e != nil {
			t.Fatal(e)
		}
		if run.task != "writer" {
			comment(run.task, "report", "Head: "+scenario.Head+"\nCLEAN")
		}
	}
	proof := workflowtemplate.Proof{ReviewBead: "reviewer", Head: scenario.Head, NoFindings: true}
	proofRaw, _ := json.Marshal(proof)
	comment("review-step", "proof-1", workflowtemplate.ProofPrefix+string(proofRaw))
	single.Args = []string{"close", "review-step"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "reviewer must differ from writer") {
		t.Fatalf("shared verifier allowed self-review: %+v", out)
	}
	proof.ReviewBead = "independent"
	proofRaw, _ = json.Marshal(proof)
	comment("review-step", "proof-2", workflowtemplate.ProofPrefix+string(proofRaw))
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("independent generic execution review was not accepted: %+v", out)
	}

	// A merged-without-review settlement retains its different meaning and does
	// not require a fabricated writer binding or passing review report.
	settledTemplate, e := workflowtemplate.Load("pr-review")
	if e != nil {
		t.Fatal(e)
	}
	settledInstance, e := workflowtemplate.Instantiate(settledTemplate, workflowtemplate.Params{Project: "completion", Parent: "root", Before: "ship", PR: scenario.PR})
	if e != nil {
		t.Fatal(e)
	}
	insert("settle-block", "root", settledInstance.Title, "Review "+scenario.PR+"\n\n"+settledInstance.Snapshot, settledInstance.Labels)
	insert("settle-step", "settle-block", settledInstance.Steps[0].Title, "", settledInstance.Steps[0].Labels)
	insert("ship", "root", "PR merged", "", []string{"kind:objective", "pr:" + beads.EscapeLabel(scenario.PR)})
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'ship','settle-block','blocks')`, pid); e != nil {
		t.Fatal(e)
	}
	receipt := workflowtemplate.Evidence{Instance: settledInstance.Key, Step: settledInstance.Steps[0].ID, Verifier: workflowtemplate.SettledVerifier, PR: scenario.PR, Head: scenario.Head, Finding: workflowtemplate.SettledFinding(scenario.Head, "The PR was merged externally before the review finished")}
	comment("settle-step", "settlement", receipt.Comment())
	single.Args = []string{"close", "settle-step"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "settlement receipt") {
		t.Fatalf("open PR was falsely settled: %+v", out)
	}
	prState = "MERGED"
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("valid merged settlement was blocked or demanded fake review evidence: %+v", out)
	}
	var receipts int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='settle-step'`, pid).Scan(&receipts); e != nil || receipts != 1 {
		t.Fatalf("settlement fabricated passing review evidence: %d %v", receipts, e)
	}

	// Raw edits cannot erase generated identities or release work by unlinking
	// or parking its unfinished review block.
	for _, args := range [][]string{{"update", "ci", "--remove-label", "workflow-template:ci-required"}, {"update", "ci", "--parent", "root"}, {"update", "block", "--description", "Replace the snapshot with a passing check"}, {"delete", "block"}} {
		single.Args = args
		_, out = postWrite(t, ts.URL, testToken, single)
		if out.Exit == 0 {
			t.Fatalf("raw edit erased required workflow history: %v %+v", args, out)
		}
	}
	insert("required-work", "root", "Wait for the required review", "", []string{"kind:objective"})
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'required-work','review-block','blocks')`, pid); e != nil {
		t.Fatal(e)
	}
	single.Args = []string{"dep", "remove", "required-work", "review-block"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 {
		t.Fatalf("raw unlink released an unfinished review: %+v", out)
	}
	single.Args = []string{"update", "review-block", "--status", "deferred"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("unfinished review could not be parked: %+v", out)
	}
	var allowed bool
	if e = st.DB().QueryRowContext(ctx, `SELECT commander_execution_allowed(?,'required-work')`, pid).Scan(&allowed); e != nil || allowed {
		t.Fatal("parked review was mistaken for a passed check", allowed, e)
	}
	// A gate retirement, identity edit, or dependency removal cannot hide the
	// missing review. No-PR permission is explicit and definition bound.
	insert("waiver-stage", "root", "VM deployment", "", []string{"kind:objective"})
	waiverLabels := []string{"kind:gate", "gate-of:waiver-stage", "gate-name:merged", "verifier:operator"}
	insert("waiver-gate", "root", "Deployment released", "The approved VM deployment has no pull request", waiverLabels)
	single.CompletionWaivers = nil
	single.Args = []string{"close", "waiver-stage"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "unpassed gate") {
		t.Fatalf("stage bypassed its own approval gate: %+v", out)
	}
	for _, args := range [][]string{{"delete", "waiver-stage"}, {"update", "waiver-stage", "--add-label", "retired"}, {"update", "waiver-gate", "--add-label", "retired"}, {"update", "waiver-gate", "--remove-label", "verifier:operator"}, {"close", "waiver-gate"}} {
		single.Args = args
		_, out = postWrite(t, ts.URL, testToken, single)
		if out.Exit == 0 {
			t.Fatalf("raw gate mutation bypassed completion: %v %+v", args, out)
		}
	}
	captureWaiver := func(id string) beads.CompletionWaiver {
		t.Helper()
		client := beads.New()
		b, e := client.Show(ctx, dir, id)
		if e != nil || b == nil {
			t.Fatal(b, e)
		}
		d, e := beads.FreshDetails(ctx, client, dir, []string{id})
		if e != nil {
			t.Fatal(e)
		}
		return beads.CompletionWaiver{Bead: id, Definition: beads.CompletionDefinition(*b, d[id]), Reason: "Deploy directly on the approved VM; there is nothing to merge", Evidence: "The user approved this VM deployment"}
	}
	insert("named-pr-gate", "root", "PR merged", "Check PR #42 before releasing this work", []string{"kind:gate", "gate-name:merged", "verifier:operator"})
	single.Args = []string{"close", "named-pr-gate"}
	single.CompletionWaivers = []beads.CompletionWaiver{captureWaiver("named-pr-gate")}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "--no-pr refused") {
		t.Fatalf("waiver bypassed a gate naming a real PR: %+v", out)
	}
	single.CompletionWaivers = nil
	waiver := captureWaiver("waiver-gate")
	single.Args = []string{"update", "waiver-gate", "--description", "Changed VM deployment conditions"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatal(out)
	}
	single.Args = []string{"close", "waiver-gate"}
	single.CompletionWaivers = []beads.CompletionWaiver{waiver}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 || !strings.Contains(out.Error, "definition changed") {
		t.Fatalf("old waiver approved changed criteria: %+v", out)
	}
	waiver = captureWaiver("waiver-gate")
	single.CompletionWaivers = []beads.CompletionWaiver{waiver}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("explicit current no-PR waiver failed: %+v", out)
	}
	var waiverNotes int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='waiver-gate' AND text LIKE 'merge rule waived for a ship with no PR by %'`, pid).Scan(&waiverNotes); e != nil || waiverNotes != 1 {
		t.Fatalf("waiver was not committed with the close: %d %v", waiverNotes, e)
	}
	single.CompletionWaivers = nil
	single.Args = []string{"close", "waiver-stage"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("a stage could not follow its passed gate: %+v", out)
	}
	// The same typed request can finish its own retirement despite operational
	// changes, while a new hold, changed criteria or ownership changes refuse.
	insert("retire-stage", "root", "Retire a VM gate", "", []string{"kind:objective"})
	insert("retire-gate", "root", "Deployment released", "The VM deployment has no pull request", []string{"kind:gate", "gate-of:retire-stage", "gate-name:merged", "verifier:operator"})
	retirement := captureWaiver("retire-gate")
	single.CompletionWaivers = []beads.CompletionWaiver{retirement}
	single.Args = []string{"update", "retire-gate", "--add-label", "retired"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("typed retirement refused: %+v", out)
	}
	single.CompletionWaivers = nil
	single.Args = []string{"close", "retire-gate"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit == 0 {
		t.Fatalf("a retirement marker was mistaken for passing proof: %+v", out)
	}
	single.CompletionWaivers = []beads.CompletionWaiver{retirement}
	single.Args = []string{"close", "retire-gate"}
	_, out = postWrite(t, ts.URL, testToken, single)
	if out.Exit != 0 {
		t.Fatalf("typed retirement could not finish: %+v", out)
	}

}
