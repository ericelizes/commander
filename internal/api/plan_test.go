package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresPlanThroughGuardedWriteAPI(t *testing.T) {
	st, dsn := pgtest.Open(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := projectDir(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "planning", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'work','Original','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st}).Handler())
	defer ts.Close()
	req := beads.DoorRequest{Dir: dir, Atomic: true, NoWait: true, Plan: &beads.PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{"work": 1}}, Ops: [][]string{{"update", "work", "--title", "Updated atomically"}}, Env: []string{"COMMANDER_ROLE=commander"}}
	nonAtomic := req
	nonAtomic.Atomic = false
	if response, _ := postWrite(t, ts.URL, testToken, nonAtomic); response.StatusCode != http.StatusBadRequest {
		t.Fatal("non-atomic plan accepted")
	}
	_, out := postWrite(t, ts.URL, testToken, req)
	if out.Exit != 0 || out.Applied != 1 || !out.Atomic {
		t.Fatalf("plan failed through real write door: %+v", out)
	}
	_, out = postWrite(t, ts.URL, testToken, req)
	if out.Exit != 0 || !out.Replayed || out.PlanReceipt == "" {
		t.Fatalf("API lost committed receipt: %+v", out)
	}
	stale := req
	stale.Ops = [][]string{{"update", "work", "--title", "Different stale intent"}}
	_, out = postWrite(t, ts.URL, testToken, stale)
	if out.Exit == 0 || out.Applied != 0 {
		t.Fatalf("API discarded planning basis: %+v", out)
	}
	var title string
	if e = st.DB().QueryRowContext(ctx, `SELECT title FROM bead_tasks WHERE project_id=? AND id='work'`, pid).Scan(&title); e != nil || title != "Updated atomically" {
		t.Fatal("incorrect committed plan", e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "work", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "handoff after commit"}); e != nil {
		t.Fatal(e)
	}
	_, historical := postWrite(t, ts.URL, testToken, req)
	if historical.Exit != 0 || !historical.Replayed {
		t.Fatalf("ownership handoff lost historical receipt: %+v", historical)
	}

}

func TestPostgresPlanCreatesNestedTreeAndReplaysOriginalIDs(t *testing.T) {
	var scenario struct {
		Ops     [][]string `json:"ops"`
		Beads   int        `json:"expected_beads"`
		Aliases int        `json:"expected_aliases"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-nested-plan.json"))
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
	dir := projectDir(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "nested-plan", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st}).Handler())
	defer ts.Close()
	req := beads.DoorRequest{Dir: dir, Atomic: true, NoWait: true, Plan: &beads.PlanBasis{RequestID: "create-urgent-tree", JurisdictionRevision: 1, Beads: map[string]int64{}}, Ops: scenario.Ops, Env: []string{"COMMANDER_ROLE=commander"}}
	_, out := postWrite(t, ts.URL, testToken, req)
	if out.Exit != 0 || len(out.Created) != scenario.Aliases {
		t.Fatalf("nested plan failed: %+v", out)
	}
	first := out.Created
	_, retry := postWrite(t, ts.URL, testToken, req)
	if retry.Exit != 0 || !retry.Replayed || !reflect.DeepEqual(first, retry.Created) {
		t.Fatalf("retry lost created identities: %+v", retry)
	}
	var count int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks WHERE project_id=?`, pid).Scan(&count); e != nil || count != scenario.Beads {
		t.Fatal("retry duplicated tree", count, e)
	}
	var parent, brief string
	if e = st.DB().QueryRowContext(ctx, `SELECT parent_id,description FROM bead_tasks WHERE project_id=? AND id=?`, pid, first["task"]).Scan(&parent, &brief); e != nil || parent != first["goal"] || brief != "Preserve literal $goal in this brief" {
		t.Fatal("reference expanded outside identity or wrong parent", parent, brief, e)
	}
	var edge bool
	if e = st.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id=? AND issue_id=? AND depends_on_id=? AND type='blocks')`, pid, first["verify"], first["task"]).Scan(&edge); e != nil || !edge {
		t.Fatal("nested dependency missing", e)
	}
}

func TestPostgresPlanCoordinationScenarioThroughAPI(t *testing.T) {
	var scenario struct {
		OrdinaryTitle  string `json:"ordinary_title"`
		UrgentTitle    string `json:"urgent_title"`
		OrdinaryAnswer string `json:"ordinary_answer"`
		UrgentAnswer   string `json:"urgent_answer"`
		Cards          int    `json:"expected_user_cards"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-coordinated-plan.json"))
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
	dir := projectDir(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "coordination", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'ordinary','Original ordinary','now','now'),(?,'urgent','Original urgent','now','now')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "urgent", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent focus"}); e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st}).Handler())
	defer ts.Close()
	req := beads.DoorRequest{Dir: dir, Atomic: true, NoWait: true, Plan: &beads.PlanBasis{RequestID: "agreed-change", JurisdictionRevision: 2, Beads: map[string]int64{"ordinary": 1, "urgent": 1}}, Ops: [][]string{{"update", "ordinary", "--title", scenario.OrdinaryTitle}, {"update", "urgent", "--title", scenario.UrgentTitle}}, Env: []string{"COMMANDER_ROLE=commander"}}
	_, out := postWrite(t, ts.URL, testToken, req)
	if out.Exit == 0 {
		t.Fatal("uncoordinated cross-scope plan committed")
	}
	req.Propose = true
	_, out = postWrite(t, ts.URL, testToken, req)
	if out.Exit != 0 || out.ProposalID == "" || out.Applied != 0 {
		t.Fatalf("proposal API failed: %+v", out)
	}
	id := out.ProposalID
	req.Propose = false
	req.Plan.CoordinationID = id
	for _, decision := range []struct{ actor, answer string }{{"commander", scenario.OrdinaryAnswer}, {"commander-urgent", scenario.UrgentAnswer}} {
		inbox, e := st.QuestionInbox(ctx, decision.actor)
		if e != nil || len(inbox) != 1 || inbox[0].ID != id {
			t.Fatalf("owner inbox failed: %s %+v %v", decision.actor, inbox, e)
		}
		if _, e = st.AnswerQuestion(ctx, id, decision.actor, decision.answer, true); e != nil {
			t.Fatal(e)
		}
	}
	_, out = postWrite(t, ts.URL, testToken, req)
	if out.Exit != 0 || out.Applied != 2 {
		t.Fatalf("agreed plan did not commit through API: %+v", out)
	}
	var title string
	if e = st.DB().QueryRowContext(ctx, `SELECT title FROM bead_tasks WHERE project_id=? AND id='urgent'`, pid).Scan(&title); e != nil || title != scenario.UrgentTitle {
		t.Fatal("urgent scope did not receive agreed change", title, e)
	}
	_, out = postWrite(t, ts.URL, testToken, req)
	if out.Exit != 0 || !out.Replayed {
		t.Fatalf("coordinated plan retry failed: %+v", out)
	}
	var cards int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM needs_you`).Scan(&cards); e != nil || cards != scenario.Cards {
		t.Fatal("internal coordination created a user card", cards, e)
	}
}

func TestPostgresPlanHumanHoldScenarioThroughAPI(t *testing.T) {
	var scenario struct {
		Question, Answer string
		RequestID        string `json:"request_id"`
		SecondRequestID  string `json:"second_request_id"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-human-hold.json"))
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
	dir := projectDir(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "human-hold", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'held','Held work','["hold"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "human hold scenario"); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(New(Options{Token: testToken, StateStore: st}).Handler())
	defer server.Close()
	// An execution session cannot lift a hold; the planning Commander may,
	// without a recorded answer: keeping the board true is its job.
	req := beads.DoorRequest{Dir: dir, Atomic: true, NoWait: true, Plan: &beads.PlanBasis{RequestID: scenario.RequestID, JurisdictionRevision: 1, Beads: map[string]int64{"held": 1}}, Ops: [][]string{{"update", "held", "--remove-label", "hold"}}, Env: []string{"COMMANDER_ROLE=commander", "COMMANDER_RUN_SESSION=executor-session"}}
	_, out := postWrite(t, server.URL, testToken, req)
	if out.Exit == 0 {
		t.Fatal("execution session erased a human hold")
	}
	req.Plan.RequestID = scenario.SecondRequestID
	req.Env = []string{"COMMANDER_ROLE=commander"}
	_, out = postWrite(t, server.URL, testToken, req)
	if out.Exit != 0 {
		t.Fatalf("planning Commander could not lift a hold: %+v", out)
	}
}
