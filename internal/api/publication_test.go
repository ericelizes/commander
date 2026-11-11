package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type publicationFixture struct {
	st        *store.Store
	pid       int64
	dir, base string
	req       beads.PublicationRequest
}

func newPublicationFixture(t *testing.T, beforeActivation ...func(*store.Store, int64)) publicationFixture {
	t.Helper()
	st, dsn := pgtest.Open(t)
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "publication", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,status,labels,created_at,updated_at) VALUES(?,'root',NULL,'Goal','open','["kind:objective","kind:workstream"]','now','now'),(?,'track','root','Scope','open','["kind:objective","kind:track"]','now','now'),(?,'leaf','track','Protected action','open','["kind:objective"]','now','now'),(?,'card',NULL,'Approve bounded action','open','["kind:gate","gate-of:leaf","gate-name:scope","verifier:user","approver:user"]','now','now'),(?,'prior',NULL,'Prior check','closed','["kind:objective"]','now','now')`, pid, pid, pid, pid, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'track','card','blocks'),(?,'leaf','card','blocks'),(?,'card','prior','blocks')`, pid, pid, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,'card','original','user','Original approval history','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"root", "card"} {
		j, e := st.Jurisdiction(ctx, pid, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: id, ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander-urgent", Reason: "publication repair scope"}); e != nil {
			t.Fatal(e)
		}
	}
	for _, prepare := range beforeActivation {
		prepare(st, pid)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "publication repair fixture"); e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewServer(New(Options{Token: testToken, StateStore: st}).Handler())
	t.Cleanup(ts.Close)
	f := publicationFixture{st: st, pid: pid, dir: dir, base: ts.URL, req: beads.PublicationRequest{Dir: dir, Gate: "card", Parent: "track", Waiters: []string{"track"}, Reason: "Preserve the leaf approval while repairing the redundant ancestor wait", Env: []string{"COMMANDER_ROLE=commander-urgent"}}}
	f.refresh(t)
	return f
}
func (f *publicationFixture) refresh(t *testing.T) {
	t.Helper()
	f.req.Basis = beads.PlanBasis{RequestID: "publication-once", Beads: map[string]int64{}}
	for _, id := range []string{"card", "track", "leaf"} {
		b, e := beads.New().Show(context.Background(), f.dir, id)
		if e != nil {
			t.Fatal(e)
		}
		f.req.Basis.Beads[id] = b.PlanRevision
		f.req.Basis.JurisdictionRevision = b.JurisdictionRevision
	}
}
func postPublication(t *testing.T, base string, req beads.PublicationRequest) beads.DoorResponse {
	t.Helper()
	raw, e := json.Marshal(req)
	if e != nil {
		t.Fatal(e)
	}
	r, e := http.NewRequest(http.MethodPost, base+beads.PublicationPath, bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	response, e := (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	var out beads.DoorResponse
	if e = json.NewDecoder(response.Body).Decode(&out); e != nil {
		t.Fatal(e)
	}
	return out
}
func (f publicationFixture) assertOriginal(t *testing.T) {
	t.Helper()
	var parent string
	var edges, comments, receipts int
	e := f.st.DB().QueryRow(`SELECT COALESCE(parent_id,''),(SELECT COUNT(*) FROM bead_dependencies WHERE project_id=? AND depends_on_id='card'),(SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='card'),(SELECT COUNT(*) FROM commander_plan_receipts WHERE project_id=?) FROM bead_tasks WHERE project_id=? AND id='card'`, f.pid, f.pid, f.pid, f.pid).Scan(&parent, &edges, &comments, &receipts)
	if e != nil || parent != "" || edges != 2 || comments != 1 || receipts != 0 {
		t.Fatalf("failed/dry repair leaked state: parent=%s edges=%d comments=%d receipts=%d %v", parent, edges, comments, receipts, e)
	}
}

func TestPostgresPlanPublicationRepairIsAtomicAndRetainsApproval(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	before, e := beads.New().Show(ctx, f.dir, "card")
	if e != nil {
		t.Fatal(e)
	}
	rawScenario, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "publication-repair.json"))
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct{ Reason string }
	if e = json.Unmarshal(rawScenario, &scenario); e != nil {
		t.Fatal(e)
	}
	f.req.Reason = scenario.Reason
	dry := f.req
	dry.Dry = true
	if result := postPublication(t, f.base, dry); result.Exit != 0 || !result.Atomic {
		t.Fatalf("valid dry run refused: %+v", result)
	}
	f.assertOriginal(t)
	// Neither an audit-looking comment nor a caller environment variable grants
	// the private edge authority used inside the canonical validator.
	_, raw := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"dep", "remove", "track", "card"}, NoWait: true, Env: []string{"COMMANDER_ROLE=commander-urgent", "COMMANDER_PUBLICATION_REPAIR=granted"}})
	if raw.Exit == 0 {
		t.Fatal("raw unlink bypassed the human gate")
	}
	f.assertOriginal(t)
	_, unrelated := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"dep", "remove", "track", "card", "--type", "discovered-from"}, NoWait: true, Env: f.req.Env})
	if unrelated.Exit != 0 {
		t.Fatalf("unrelated relationship removal refused: %+v", unrelated)
	}
	f.assertOriginal(t)
	_, parentRemoval := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"dep", "remove", "leaf", "track", "--type", "parent-child"}, NoWait: true, Env: f.req.Env})
	if parentRemoval.Exit == 0 {
		t.Fatal("relationship deletion bypassed the guarded parent operation")
	}
	f.assertOriginal(t)

	// Force a late placement failure after the audit and unlink ran. The gate,
	// redundant edge, original history and receipt must all roll back.
	if _, e = f.st.DB().ExecContext(ctx, `CREATE FUNCTION fail_publication_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='card' AND NEW.parent_id='track' THEN RAISE EXCEPTION 'fixture placement failure'; END IF; RETURN NEW; END $$`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.DB().ExecContext(ctx, `CREATE TRIGGER fail_publication_fixture BEFORE UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION fail_publication_fixture()`); e != nil {
		t.Fatal(e)
	}
	result := postPublication(t, f.base, f.req)
	if result.Exit == 0 || !strings.Contains(result.Error, "fixture placement failure") {
		t.Fatalf("late failure not exercised: %+v", result)
	}
	f.assertOriginal(t)
	if _, e = f.st.DB().ExecContext(ctx, `DROP TRIGGER fail_publication_fixture ON bead_tasks`); e != nil {
		t.Fatal(e)
	}
	result = postPublication(t, f.base, f.req)
	if result.Exit != 0 || !result.Atomic || result.Replayed {
		t.Fatalf("canonical repair failed: %+v", result)
	}
	var parent, status, labels string
	var leafWait, priorWait bool
	var comments int
	if e = f.st.DB().QueryRowContext(ctx, `SELECT parent_id,status,labels::text,(SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id=? AND issue_id='leaf' AND depends_on_id='card')),(SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id=? AND issue_id='card' AND depends_on_id='prior')),(SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='card') FROM bead_tasks WHERE project_id=? AND id='card'`, f.pid, f.pid, f.pid, f.pid).Scan(&parent, &status, &labels, &leafWait, &priorWait, &comments); e != nil {
		t.Fatal(e)
	}
	if parent != "track" || status != "open" || !leafWait || !priorWait || comments != 3 || !strings.Contains(labels, "approver:user") {
		t.Fatalf("repair changed approval authority: parent=%s status=%s leaf=%v prior=%v comments=%d labels=%s", parent, status, leafWait, priorWait, comments, labels)
	}
	afterGate, e := beads.New().Show(ctx, f.dir, "card")
	if e != nil {
		t.Fatal(e)
	}
	if before.Title != afterGate.Title || before.Description != afterGate.Description || before.AcceptanceCriteria != afterGate.AcceptanceCriteria || before.Type != afterGate.Type || !reflect.DeepEqual(before.Labels, afterGate.Labels) {
		t.Fatal("publication changed the original approval definition")
	}
	var ancestorWait bool
	var original string
	if e = f.st.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id=? AND issue_id='track' AND depends_on_id='card' AND type='blocks'),(SELECT text FROM bead_comments WHERE project_id=? AND bead_id='card' AND id='original')`, f.pid, f.pid).Scan(&ancestorWait, &original); e != nil || ancestorWait || original != "Original approval history" {
		t.Fatal("wrong edge or altered original history", ancestorWait, original, e)
	}

	replay := postPublication(t, f.base, f.req)
	if replay.Exit != 0 || !replay.Replayed {
		t.Fatalf("lost-response replay failed: %+v", replay)
	}
	changed := f.req
	changed.Reason = "A different requested repair"
	if response := postPublication(t, f.base, changed); response.Exit == 0 {
		t.Fatal("receipt key accepted different intent")
	}
	var after int
	if e = f.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id='card'`, f.pid).Scan(&after); e != nil || after != comments {
		t.Fatal("replay manufactured new audit/progress", after, e)
	}
}

func TestPostgresPlanPublicationRepairRefusesUnsafeOrForeignState(t *testing.T) {
	var scenario struct {
		Refusals []string `json:"canonical_refusals"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "publication-repair.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	if len(scenario.Refusals) != 11 {
		t.Fatal("canonical publication scenario missing")
	}
	for _, change := range scenario.Refusals {
		t.Run(change, func(t *testing.T) {
			f := newPublicationFixture(t)
			ctx := context.Background()
			var e error
			switch change {
			case "foreign-owner":
				f.req.Env = []string{"COMMANDER_ROLE=commander"}
			case "registered-executor":
				f.req.Env = append(f.req.Env, "COMMANDER_RUN_ID=17", "COMMANDER_RUN_SESSION=current-execution")
			case "executor":
				f.req.Env = []string{"COMMANDER_ROLE=executor-unknown", "COMMANDER_RUN_ID=12", "COMMANDER_RUN_SESSION=stale"}
			case "inherited-executor":
				f.req.Env = []string{"COMMANDER_RUN_SESSION=stale"}
			case "coordination-grant":
				f.req.Basis.CoordinationID = "unrelated-plan-approval"
			case "held-root":
				_, e = f.st.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=labels||'["hold"]'::jsonb WHERE project_id=? AND id='root'`, f.pid)
			case "unprotected-sibling", "parked-sibling":
				status := "open"
				if change == "parked-sibling" {
					status = "deferred"
				}
				_, e = f.st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,status,labels,created_at,updated_at) VALUES(?,'sibling','track','Unprotected work',?,'["kind:objective"]','now','now')`, f.pid, status)
				f.refresh(t)
			case "changed-basis":
				_, e = f.st.DB().ExecContext(ctx, `UPDATE bead_tasks SET description='Changed criteria' WHERE project_id=? AND id='card'`, f.pid)
			case "saved-answer":
				_, e = f.st.DB().ExecContext(ctx, `INSERT INTO needs_resolutions(project_id,bead_id,generation,payload,state) VALUES(?,'card','0','{}',1)`, f.pid)
			case "missing-leaf-basis":
				delete(f.req.Basis.Beads, "leaf")
			default:
				t.Fatalf("unknown publication scenario %q", change)
			}
			if e != nil {
				t.Fatal(e)
			}
			result := postPublication(t, f.base, f.req)
			if result.Exit == 0 {
				t.Fatalf("publication bypassed %s: %+v", change, result)
			}
			f.assertOriginal(t)
			t.Logf("%s refused: %s", change, result.Error)
		})
	}
}

func splitPublicationFixture(t *testing.T) publicationFixture {
	f := newPublicationFixture(t)
	ctx := context.Background()
	j, e := f.st.Jurisdiction(ctx, f.pid, "card")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: f.pid, BeadID: "card", ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander", Reason: "gate and protected action have different planning owners"}); e != nil {
		t.Fatal(e)
	}
	f.refresh(t)
	return f
}
func proposePublicationFixture(t *testing.T, f publicationFixture) string {
	t.Helper()
	proposal := f.req
	proposal.Propose = true
	result := postPublication(t, f.base, proposal)
	if result.Exit != 0 || result.ProposalID == "" {
		t.Fatalf("proposal failed: %+v", result)
	}
	f.assertOriginal(t)
	again := postPublication(t, f.base, proposal)
	if again.Exit != 0 || again.ProposalID != result.ProposalID {
		t.Fatal("proposal retry changed identity", again)
	}
	return result.ProposalID
}
func approvePublicationFixture(t *testing.T, f publicationFixture, id string) {
	t.Helper()
	for _, role := range []string{"commander", "commander-urgent"} {
		if _, e := f.st.AnswerQuestion(context.Background(), id, role, "Agree to the exact saved publication action", true); e != nil {
			t.Fatal(e)
		}
	}
}
func TestPostgresPlanPublicationCoordinationCommitsOnlyExactTypedAction(t *testing.T) {
	f := splitPublicationFixture(t)
	if result := postPublication(t, f.base, f.req); result.Exit == 0 {
		t.Fatal("foreign placement allowed without agreement")
	}
	id := proposePublicationFixture(t, f)
	req := f.req
	req.Basis.CoordinationID = id
	if result := postPublication(t, f.base, req); result.Exit == 0 {
		t.Fatal("pending proposal granted authority")
	}
	if done, e := f.st.AnswerQuestion(context.Background(), id, "commander-urgent", "Agree to the exact saved publication action", true); e != nil || done {
		t.Fatal("one owner approved every scope", done, e)
	}
	if result := postPublication(t, f.base, req); result.Exit == 0 {
		t.Fatal("partial agreement granted authority")
	}
	f.assertOriginal(t)
	if _, e := f.st.AnswerQuestion(context.Background(), id, "commander", "Agree to the exact saved publication action", true); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e := f.st.DB().QueryRow(`SELECT payload FROM commander_plan_proposals WHERE id=?`, id).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var saved beads.PlanDocument
	if e := json.Unmarshal(raw, &saved); e != nil || saved.Publication == nil || len(saved.Ops) != 0 || saved.Publication.Reason != req.Reason {
		t.Fatal("saved proposal is not the exact typed action", string(raw), e)
	}
	// The same-looking internal receipt operations remain an ordinary plan. A
	// publication agreement cannot authorize writing that identity comment.
	identity, _ := json.Marshal(struct {
		Gate, Parent, Reason string
		Waiters              []string
		Dry                  bool
	}{req.Gate, req.Parent, req.Reason, req.Waiters, false})
	ops := [][]string{{"comments", "add", req.Gate, "canonical-publication@v1 " + string(identity)}, {"update", req.Gate, "--parent", req.Parent}, {"dep", "remove", "track", "card"}}
	_, wrongEndpoint := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Ops: ops, Atomic: true, NoWait: true, Env: req.Env, Plan: &req.Basis})
	if wrongEndpoint.Exit == 0 {
		t.Fatal("typed action agreement authorized ordinary operations")
	}
	f.assertOriginal(t)
	result := postPublication(t, f.base, req)
	if result.Exit != 0 {
		t.Fatalf("agreed publication failed: %+v", result)
	}
	retry := postPublication(t, f.base, req)
	if retry.Exit != 0 || !retry.Replayed {
		t.Fatal("lost-response replay failed", retry)
	}
	var parent, status string
	var leafWait, ancestorWait, cards int
	if e := f.st.DB().QueryRow(`SELECT parent_id,status,(SELECT count(*) FROM bead_dependencies WHERE project_id=? AND issue_id='leaf' AND depends_on_id='card'),(SELECT count(*) FROM bead_dependencies WHERE project_id=? AND issue_id='track' AND depends_on_id='card'),(SELECT count(*) FROM needs_you) FROM bead_tasks WHERE project_id=? AND id='card'`, f.pid, f.pid, f.pid).Scan(&parent, &status, &leafWait, &ancestorWait, &cards); e != nil || parent != "track" || status != "open" || leafWait != 1 || ancestorWait != 0 || cards != 0 {
		t.Fatal("agreement bypassed approval or lost exact repair", parent, status, leafWait, ancestorWait, cards, e)
	}
}
func TestPostgresPlanPublicationCoordinationFencesChangedAuthorityAndPayload(t *testing.T) {
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "publication-repair.json"))
	if e != nil {
		t.Fatal(e)
	}
	var scenario struct {
		Refusals []string `json:"canonical_coordination_refusals"`
	}
	if e = json.Unmarshal(raw, &scenario); e != nil || len(scenario.Refusals) != 7 {
		t.Fatal("missing coordination scenario", e)
	}
	for _, change := range scenario.Refusals {
		t.Run(change, func(t *testing.T) {
			f := splitPublicationFixture(t)
			id := proposePublicationFixture(t, f)
			if change == "refused" {
				if _, e := f.st.AnswerQuestion(context.Background(), id, "commander", "This change conflicts with my scope", false); e != nil {
					t.Fatal(e)
				}
			} else {
				approvePublicationFixture(t, f, id)
			}
			req := f.req
			req.Basis.CoordinationID = id
			switch change {
			case "reason":
				req.Reason = "Different repair intent"
			case "waiters":
				req.Waiters = nil
			case "requester":
				req.Env = []string{"COMMANDER_ROLE=commander"}
			case "handoff":
				j, e := f.st.Jurisdiction(context.Background(), f.pid, "card")
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.st.TransferJurisdiction(context.Background(), store.JurisdictionTransfer{ProjectID: f.pid, BeadID: "card", ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: "commander-urgent", Reason: "ownership changed after agreement"}); e != nil {
					t.Fatal(e)
				}
			case "definition":
				if _, e := f.st.DB().Exec(`UPDATE bead_tasks SET description='Different bounded approval' WHERE project_id=? AND id='card'`, f.pid); e != nil {
					t.Fatal(e)
				}
			case "refused":
			case "ordinary-plan":
				req.Basis.RequestID = "ordinary-plan-agreement"
				req.Basis.CoordinationID = ""
				identity, _ := json.Marshal(struct {
					Gate, Parent, Reason string
					Waiters              []string
					Dry                  bool
				}{req.Gate, req.Parent, req.Reason, req.Waiters, false})
				ops := [][]string{{"comments", "add", req.Gate, "canonical-publication@v1 " + string(identity)}, {"update", req.Gate, "--parent", req.Parent}, {"dep", "remove", "track", "card"}}
				_, ordinary := postWrite(t, f.base, testToken, beads.DoorRequest{Dir: f.dir, Ops: ops, Atomic: true, NoWait: true, Propose: true, Env: req.Env, Plan: &req.Basis})
				if ordinary.Exit != 0 || ordinary.ProposalID == "" {
					t.Fatal("ordinary proposal failed", ordinary)
				}
				approvePublicationFixture(t, f, ordinary.ProposalID)
				req.Basis.CoordinationID = ordinary.ProposalID
			default:
				t.Fatal("unknown coordination scenario", change)
			}
			if result := postPublication(t, f.base, req); result.Exit == 0 {
				t.Fatal("changed authority/payload accepted", change, result)
			}
			f.assertOriginal(t)
		})
	}
}
