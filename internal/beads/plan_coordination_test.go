package beads

import (
	"context"
	"fmt"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func coordinatedPlanFixture(t *testing.T) (Client, string, *store.Store, context.Context, int64, string, string, PlanBasis, [][]string) {
	t.Helper()
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	a, e := c.Create(ctx, dir, "Ordinary goal", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Create(ctx, dir, "Urgent goal", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e = st.DB().QueryRowContext(ctx, `SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent incident"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: b, ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent scope"}); e != nil {
		t.Fatal(e)
	}
	first, e := c.Show(ctx, dir, a)
	if e != nil {
		t.Fatal(e)
	}
	second, e := c.Show(ctx, dir, b)
	if e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{RequestID: "cross-scope-plan", JurisdictionRevision: 2, Beads: map[string]int64{a: first.PlanRevision, b: second.PlanRevision}}
	ops := [][]string{{"update", a, "--title", "Coordinated ordinary goal"}, {"update", b, "--title", "Coordinated urgent goal"}}
	return c, dir, st, WithDoorEnv(ctx, []string{"COMMANDER_ROLE=commander"}), pid, a, b, basis, ops
}

func TestPostgresPlanCoordinationRequiresExactCurrentOwnerAgreement(t *testing.T) {
	c, dir, st, ctx, _, a, b, basis, ops := coordinatedPlanFixture(t)
	req := WithPlanRequest(ctx, &basis, ops, nil)
	if r := postgresApplyOps(req, dir, ops); r.Exit == 0 {
		t.Fatal("foreign edit committed without coordination")
	}
	proposal, e := ProposePlan(req, dir, ops, nil)
	if e != nil || proposal.ProposalID == "" {
		t.Fatalf("proposal failed: %+v %v", proposal, e)
	}
	same, e := ProposePlan(req, dir, ops, nil)
	if e != nil || same.ProposalID != proposal.ProposalID {
		t.Fatal("retry duplicated proposal", e)
	}
	for _, role := range []string{"commander", "commander-urgent"} {
		inbox, e := st.QuestionInbox(ctx, role)
		if e != nil || len(inbox) != 1 || inbox[0].ID != proposal.ProposalID {
			t.Fatalf("decision lost for %s: %+v %v", role, inbox, e)
		}
	}
	before, e := c.Show(ctx, dir, a)
	if e != nil || before.Title != "Ordinary goal" {
		t.Fatal("proposing changed tree", e)
	}
	granted := basis
	granted.CoordinationID = proposal.ProposalID
	if r := postgresApplyOps(WithPlanRequest(ctx, &granted, ops, nil), dir, ops); r.Exit == 0 {
		t.Fatal("pending proposal authorized writes")
	}
	complete, e := st.AnswerQuestion(ctx, proposal.ProposalID, "commander", "Ordinary scope agrees", true)
	if e != nil || complete {
		t.Fatal("partial approval completed coordination", e)
	}
	if r := postgresApplyOps(WithPlanRequest(ctx, &granted, ops, nil), dir, ops); r.Exit == 0 {
		t.Fatal("one scope authorized another scope")
	}
	complete, e = st.AnswerQuestion(ctx, proposal.ProposalID, "commander-urgent", "Urgent scope agrees", true)
	if e != nil || !complete {
		t.Fatal("all owner approvals did not complete", e)
	}
	changed := [][]string{{"update", b, "--title", "Unreviewed urgent change"}}
	if r := postgresApplyOps(WithPlanRequest(ctx, &granted, changed, nil), dir, changed); r.Exit == 0 {
		t.Fatal("agreement granted different edits")
	}
	result := postgresApplyOps(WithPlanRequest(ctx, &granted, ops, nil), dir, ops)
	if result.Exit != 0 {
		t.Fatalf("agreed plan did not commit: %+v", result)
	}
	after, e := c.Show(ctx, dir, b)
	if e != nil || after.Title != "Coordinated urgent goal" {
		t.Fatal("agreed plan missing", e)
	}
	retry := postgresApplyOps(WithPlanRequest(ctx, &granted, ops, nil), dir, ops)
	if retry.Exit != 0 || !retry.Replayed {
		t.Fatalf("agreed plan retry lost receipt: %+v", retry)
	}
	var cards int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM needs_you`).Scan(&cards); e != nil || cards != 0 {
		t.Fatal("internal coordination became a user decision", cards, e)
	}
}

func TestPostgresPlanCoordinationRefusalAndHandoffFenceAuthority(t *testing.T) {
	for _, failure := range []string{"refusal", "handoff", "plan-change"} {
		t.Run(failure, func(t *testing.T) {
			_, dir, st, ctx, pid, _, b, basis, ops := coordinatedPlanFixture(t)
			req := WithPlanRequest(ctx, &basis, ops, nil)
			proposal, e := ProposePlan(req, dir, ops, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.AnswerQuestion(ctx, proposal.ProposalID, "commander", "Agreed", true); e != nil {
				t.Fatal(e)
			}
			if _, e = st.AnswerQuestion(ctx, proposal.ProposalID, "commander-urgent", "Decision", failure != "refusal"); e != nil {
				t.Fatal(e)
			}
			if failure == "handoff" {
				if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: b, ExpectedOwner: "commander-urgent", ExpectedRevision: 2, NextCommander: "commander", Reason: "return urgent scope"}); e != nil {
					t.Fatal(e)
				}
			}
			if failure == "plan-change" {
				if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET description='Different requirements' WHERE project_id=? AND id=?`, pid, b); e != nil {
					t.Fatal(e)
				}
			}
			basis.CoordinationID = proposal.ProposalID
			if r := postgresApplyOps(WithPlanRequest(ctx, &basis, ops, nil), dir, ops); r.Exit == 0 {
				t.Fatalf("%s did not invalidate agreement", failure)
			}
		})
	}
}

func TestPostgresPlanCoordinationProjectScopeNeedsNoRepresentativeTask(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e := st.DB().QueryRow(`SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{RequestID: "empty-project-plan", JurisdictionRevision: 1, Beads: map[string]int64{}}
	ops := [][]string{{"create", "First goal", "--type", "epic"}}
	ctx = WithDoorEnv(ctx, []string{"COMMANDER_ROLE=commander"})
	req := WithPlanRequest(ctx, &basis, ops, nil)
	proposal, e := ProposePlan(req, dir, ops, nil)
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = st.DB().QueryRow(`SELECT count(*) FROM bead_tasks WHERE project_id=?`, pid).Scan(&count); e != nil || count != 0 {
		t.Fatal("proposal invented task", count, e)
	}
	question, e := st.Question(ctx, proposal.ProposalID)
	if e != nil || !question.ProjectScope || question.Kind != "planning" {
		t.Fatalf("missing project scope: %+v %v", question, e)
	}
	if done, e := st.AnswerQuestion(ctx, proposal.ProposalID, "commander", "Agree", true); e != nil || !done {
		t.Fatal(done, e)
	}
	basis.CoordinationID = proposal.ProposalID
	if r := postgresApplyOps(WithPlanRequest(ctx, &basis, ops, nil), dir, ops); r.Exit != 0 {
		t.Fatalf("project proposal failed: %+v", r)
	}
}

func TestPostgresPlanCoordinationUsesClosedScopeWithoutChangingProgress(t *testing.T) {
	_, dir, st, ctx, pid, a, b, basis, ops := coordinatedPlanFixture(t)
	if _, e := st.DB().Exec(`UPDATE bead_tasks SET status='closed' WHERE project_id=? AND id IN(?,?)`, pid, a, b); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{a, b} {
		var version int64
		if e := st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, id).Scan(&version); e != nil {
			t.Fatal(e)
		}
		basis.Beads[id] = version
	}
	var before int64
	if e := st.DB().QueryRow(`SELECT sum(progress_seq) FROM bead_tasks WHERE project_id=?`, pid).Scan(&before); e != nil {
		t.Fatal(e)
	}
	proposal, e := ProposePlan(WithPlanRequest(ctx, &basis, ops, nil), dir, ops, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, role := range []string{"commander", "commander-urgent"} {
		if _, e = st.AnswerQuestion(ctx, proposal.ProposalID, role, "Agree", true); e != nil {
			t.Fatal(e)
		}
	}
	var after int64
	if e = st.DB().QueryRow(`SELECT sum(progress_seq) FROM bead_tasks WHERE project_id=?`, pid).Scan(&after); e != nil || before != after {
		t.Fatal("coordination manufactured execution progress", before, after, e)
	}
	basis.CoordinationID = proposal.ProposalID
	if r := postgresApplyOps(WithPlanRequest(ctx, &basis, ops, nil), dir, ops); r.Exit != 0 {
		t.Fatalf("closed scope proposal failed: %+v", r)
	}
}

func TestPostgresPlanCoordinationSupportsMoreThanNineActualOwners(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e := st.DB().QueryRow(`SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	ops := [][]string{}
	basis := PlanBasis{RequestID: "twelve-owner-plan", Beads: map[string]int64{}}
	for i := 0; i < 12; i++ {
		id, e := c.Create(ctx, dir, fmt.Sprintf("Goal %d", i), CreateOptions{Silent: true})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
		owner := fmt.Sprintf("commander-scope-%d", i)
		if e = st.RegisterCommander(ctx, owner, owner); e != nil {
			t.Fatal(e)
		}
		j, e := st.Jurisdiction(ctx, pid, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: id, ExpectedOwner: j.CommanderID, ExpectedRevision: j.Revision, NextCommander: owner, Reason: "independent planning scope"}); e != nil {
			t.Fatal(e)
		}
		if i%2 == 0 {
			if _, e = st.DB().Exec(`UPDATE bead_tasks SET status='closed' WHERE project_id=? AND id=?`, pid, id); e != nil {
				t.Fatal(e)
			}
		}
		ops = append(ops, []string{"update", id, "--title", fmt.Sprintf("Agreed goal %d", i)})
	}
	for _, id := range ids {
		b, e := c.Show(ctx, dir, id)
		if e != nil {
			t.Fatal(e)
		}
		basis.Beads[id] = b.PlanRevision
		basis.JurisdictionRevision = b.JurisdictionRevision
	}
	ctx = WithDoorEnv(ctx, []string{"COMMANDER_ROLE=commander"})
	proposal, e := ProposePlan(WithPlanRequest(ctx, &basis, ops, nil), dir, ops, nil)
	if e != nil {
		t.Fatal(e)
	}
	question, e := st.Question(ctx, proposal.ProposalID)
	if e != nil || len(question.Scopes) != 12 {
		t.Fatal("actual scopes not preserved", question, e)
	}
	basis.CoordinationID = proposal.ProposalID
	for i := 0; i < 12; i++ {
		done, e := st.AnswerQuestion(ctx, proposal.ProposalID, fmt.Sprintf("commander-scope-%d", i), "Agree to exact twelve-scope edits", true)
		if e != nil || done != (i == 11) {
			t.Fatal("agreement completed at wrong boundary", i, done, e)
		}
		if i == 10 {
			if r := postgresApplyOps(WithPlanRequest(ctx, &basis, ops, nil), dir, ops); r.Exit == 0 {
				t.Fatal("eleven owners authorized the twelfth")
			}
		}
	}
	if r := postgresApplyOps(WithPlanRequest(ctx, &basis, ops, nil), dir, ops); r.Exit != 0 {
		t.Fatalf("twelve-owner agreed plan failed: %+v", r)
	}
}
