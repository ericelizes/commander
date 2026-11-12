package beads

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func humanAuthorityFixture(t *testing.T) (Client, string, *store.Store, context.Context, int64, string, string, string) {
	t.Helper()
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	held, e := c.Create(ctx, dir, "Explicit hold", CreateOptions{Labels: []string{"hold"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	gate, e := c.Create(ctx, dir, "Human release approval", CreateOptions{Labels: []string{GateLabel, "kind:gate", NeedsUserLabel, "approver:user", "verifier:user"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	work, e := c.Create(ctx, dir, "Release work", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.DependOn(ctx, dir, work, gate); e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e = st.DB().QueryRowContext(ctx, `SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "human authority tests"); e != nil {
		t.Fatal(e)
	}
	return c, dir, st, WithDoorEnv(ctx, []string{"COMMANDER_ROLE=commander"}), pid, held, gate, work
}
func recordHumanApproval(t *testing.T, st *store.Store, ctx context.Context, pid int64, bead, generation string) {
	t.Helper()
	if e := st.RecordPitch(ctx, pid, bead, "Approve the current direction", `{"options":[{"label":"Approve"}]}`, nil); e != nil {
		t.Fatal(e)
	}
	r := store.NeedsResolution{ProjectID: pid, BeadID: bead, Generation: generation, AnswerID: "answer-" + generation, Action: "approve", GateChoice: GateApprove, Approved: true}
	if e := st.SaveNeedsResolution(ctx, r, "Approve"); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresPlanCannotEraseHumanDirectionThroughRawWrites(t *testing.T) {
	_, dir, _, ctx, _, _, gate, work := humanAuthorityFixture(t)
	for _, op := range [][]string{
		{"close", gate}, {"delete", gate}, {"update", gate, "--status", "closed"},
		{"update", gate, "--remove-label", "verifier:user"},
		{"update", gate, "--add-label", DecisionAnsweredLabel},
		{"create", "Invented approval", "--labels", DecisionAnsweredLabel},
		{"dep", "remove", work, gate},
		{"close", work},
	} {
		if _, _, e := postgresRun(ctx, dir, op); e == nil {
			t.Fatalf("raw planning write bypassed human direction: %v", op)
		}
	}
}

func allowCompletion(ctx context.Context) context.Context {
	return WithCompletionValidator(ctx, func(context.Context, store.Querier, int64, string) error { return nil })
}

// The planning Commander keeps the board true without a recorded answer: it
// lifts holds, drops stale cards, rewires and parks or retires steps and gates.
func TestPostgresPlanCommanderKeepsTheBoardTrue(t *testing.T) {
	c, dir, _, ctx, _, held, gate, work := humanAuthorityFixture(t)
	ctx = allowCompletion(ctx)
	user := WithDoorEnv(context.Background(), nil)
	fresh, e := c.Create(user, dir, "Fresh release approval", CreateOptions{Labels: []string{GateLabel, "kind:gate", NeedsUserLabel, "approver:user", "verifier:user"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	for _, op := range [][]string{
		{"update", held, "--remove-label", "hold"},
		{"update", held, "--add-label", "hold"},
		{"update", held, "--labels", "kind:objective"},
		{"update", held, "--add-label", "hold"},
		{"dep", "add", work, held},
		{"dep", "remove", work, held},
		{"close", held},
		{"update", gate, "--remove-label", NeedsUserLabel},
		{"dep", "add", work, fresh},
		{"dep", "remove", work, gate},
		{"close", gate, "--reason", "superseded by a fresh approval"},
		{"update", fresh, "--status", StatusDeferred},
	} {
		if _, _, e := postgresRun(ctx, dir, op); e != nil {
			t.Fatalf("Commander could not keep the board true: %v: %v", op, e)
		}
	}
}

// Retiring is not approving: a gate closed without a withdrawn or superseded
// marker reads as passed, and a retired gate must not release its waiters.
func TestPostgresPlanCommanderCannotPassUserGate(t *testing.T) {
	_, dir, _, ctx, _, _, gate, work := humanAuthorityFixture(t)
	ctx = allowCompletion(ctx)
	for _, op := range [][]string{
		{"close", gate, "--reason", "superseded"},
		{"update", gate, "--status", "closed", "--add-label", "retired"},
		{"dep", "remove", work, gate},
	} {
		if _, _, e := postgresRun(ctx, dir, op); e == nil {
			t.Fatalf("retiring released the work waiting on a user gate: %v", op)
		}
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", work, "--status", StatusDeferred}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"close", gate}); e == nil {
		t.Fatal("Commander passed a user gate by closing it")
	}
	if _, _, e := postgresRun(ctx, dir, []string{"close", gate, "--reason", "withdrawn: the release is cancelled"}); e != nil {
		t.Fatal("Commander could not withdraw a gate nothing waits on", e)
	}
}

// Execution agents are not planners: they keep the strict rules.
func TestPostgresPlanExecutionCannotEditHumanDirection(t *testing.T) {
	for _, env := range [][]string{{"COMMANDER_ROLE=operator-release"}, {"COMMANDER_ROLE=commander", "COMMANDER_RUN_ID=1", "COMMANDER_RUN_SESSION=s"}} {
		_, dir, _, _, _, held, gate, work := humanAuthorityFixture(t)
		ctx := allowCompletion(WithDoorEnv(context.Background(), env))
		for _, op := range [][]string{
			{"update", held, "--remove-label", "hold"},
			{"close", held},
			{"update", gate, "--remove-label", NeedsUserLabel},
			{"dep", "remove", work, gate},
			{"close", gate, "--reason", "superseded"},
		} {
			if _, _, e := postgresRun(ctx, dir, op); e == nil {
				t.Fatalf("%v edited human direction: %v", env, op)
			}
		}
	}
}
func TestPostgresPlanCanApplyRecordedGateAnswerButCannotReuseItAfterReopen(t *testing.T) {
	_, dir, st, ctx, pid, _, gate, _ := humanAuthorityFixture(t)
	// This test isolates human authority. API integration tests run the real
	// workflow verifier; the required callback still executes for each valid close.
	verified := 0
	ctx = WithCompletionValidator(ctx, func(checkCtx context.Context, q store.Querier, projectID int64, id string) error {
		if projectID != pid || id != gate {
			t.Fatalf("completion context escaped its bead: %d %s", projectID, id)
		}
		verified++
		return nil
	})
	recordHumanApproval(t, st, ctx, pid, gate, "1")
	if _, _, e := postgresRun(ctx, dir, []string{"update", gate, "--remove-label", NeedsUserLabel, "--add-label", DecisionAnsweredLabel}); e != nil {
		t.Fatal("typed answer cleanup refused", e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"close", gate}); e != nil {
		t.Fatal("recorded approval could not close gate", e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", gate, "--description", "Different release scope"}); e == nil {
		t.Fatal("closed approval silently changed its conditions")
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", gate, "--status", "open", "--description", "Different release scope"}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"close", gate}); e == nil {
		t.Fatal("old approval closed revised gate")
	}
	recordHumanApproval(t, st, ctx, pid, gate, "2")
	if _, _, e := postgresRun(ctx, dir, []string{"close", gate}); e != nil {
		t.Fatal("new approval did not close revised gate", e)
	}
	if verified != 2 {
		t.Fatalf("valid completion checks=%d, want 2", verified)
	}
}

func TestPostgresPlanDroppedGateDoesNotReleaseDependentExecution(t *testing.T) {
	_, dir, st, ctx, pid, _, gate, work := humanAuthorityFixture(t)
	if e := st.RecordPitch(ctx, pid, gate, "Release decision", `{"options":[{"label":"Drop"}]}`, nil); e != nil {
		t.Fatal(e)
	}
	decision := store.NeedsResolution{ProjectID: pid, BeadID: gate, Generation: "1", AnswerID: "drop-1", Action: "decide", GateChoice: GateDrop, Choice: GateDrop}
	if e := st.SaveNeedsResolution(ctx, decision, "Drop"); e != nil {
		t.Fatal(e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", gate, "--status", StatusDeferred, "--remove-label", NeedsUserLabel, "--add-label", DecisionAnsweredLabel}); e != nil {
		t.Fatal("recorded Drop could not park its gate", e)
	}
	var allowed bool
	if e := st.DB().QueryRowContext(ctx, `SELECT commander_execution_allowed(?,?)`, pid, work).Scan(&allowed); e != nil || allowed {
		t.Fatal("Drop was treated as release approval", allowed, e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"dep", "remove", work, gate}); e == nil {
		t.Fatal("removing deferred gate bypassed its refused approval")
	}
	if _, _, e := postgresRun(ctx, dir, []string{"close", work}); e == nil {
		t.Fatal("dependent work was recorded complete without approval")
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", work, "--status", StatusDeferred}); e != nil {
		t.Fatal("planner could not park the abandoned work", e)
	}
}

func TestPostgresPlanCommanderMayMoveOrFinishWorkUnderHold(t *testing.T) {
	c, dir, _, ctx, _, held, _, _ := humanAuthorityFixture(t)
	ctx = allowCompletion(ctx)
	user := WithDoorEnv(context.Background(), nil)
	child, e := c.Create(user, dir, "Held child", CreateOptions{Parent: held, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	other, e := c.Create(user, dir, "Stale child", CreateOptions{Parent: held, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	free, e := c.Create(user, dir, "Unheld parent", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"update", child, "--parent", free}); e != nil {
		t.Fatal("Commander could not move work out from under a hold", e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"close", other, "--reason", "superseded"}); e != nil {
		t.Fatal("Commander could not retire stale work under a hold", e)
	}
}
func TestPostgresPlanHumanApprovalDoesNotCoverExpandedScope(t *testing.T) {
	for _, change := range []string{"new-brief", "new-pr"} {
		t.Run(change, func(t *testing.T) {
			_, dir, st, ctx, pid, _, gate, _ := humanAuthorityFixture(t)
			ctx = allowCompletion(ctx)
			recordHumanApproval(t, st, ctx, pid, gate, "1")
			if change == "new-brief" {
				if _, _, e := postgresRun(ctx, dir, []string{"update", gate, "--description", "Expanded release requirements"}); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, _, e := postgresRun(ctx, dir, []string{"update", gate, "--add-label", "pr:999"}); e != nil {
					t.Fatal(e)
				}
			}
			if _, _, e := postgresRun(ctx, dir, []string{"close", gate}); e == nil {
				t.Fatalf("approval authorized expanded scope: %s", change)
			}
		})
	}
}
func TestPostgresPlanHumanRefusalRollsBackWholeBatch(t *testing.T) {
	c, dir, _, ctx, _, _, gate, work := humanAuthorityFixture(t)
	b, e := c.Show(ctx, dir, gate)
	if e != nil {
		t.Fatal(e)
	}
	other, e := c.Show(ctx, dir, work)
	if e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{gate: b.PlanRevision, work: other.PlanRevision}}
	result := postgresApplyOps(WithPlanBasis(ctx, &basis), dir, [][]string{{"update", work, "--title", "Must roll back"}, {"close", gate}})
	if result.Exit == 0 {
		t.Fatal("batch passed a user gate")
	}
	after, e := c.Show(ctx, dir, work)
	if e != nil || after.Title != other.Title {
		t.Fatal("rejected batch leaked earlier mutation", e)
	}
}
func TestPostgresPlanAnswerCannotApproveChangedQuestionScope(t *testing.T) {
	_, dir, st, ctx, pid, held, _, _ := humanAuthorityFixture(t)
	question := `{"question":"Release the existing hold?","options":[{"label":"Approve","approves":true}]}`
	if e := st.RecordPitch(ctx, pid, held, "Resume the work", question, nil); e != nil {
		t.Fatal(e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", held, "--description", "Different requested work"}); e != nil {
		t.Fatal(e)
	}
	answer := store.NeedsResolution{ProjectID: pid, BeadID: held, Generation: "1", AnswerID: "stale-answer", Action: "approve", Approved: true}
	if e := st.SaveNeedsResolution(ctx, answer, "Approve"); e == nil {
		t.Fatal("answer approved scope changed after the question")
	}
	if e := st.RecordPitch(ctx, pid, held, "Resume the work", question, nil); e != nil {
		t.Fatal(e)
	}
	answer.Generation = "2"
	answer.AnswerID = "current-answer"
	if e := st.SaveNeedsResolution(ctx, answer, "Approve"); e != nil {
		t.Fatal("refreshed question could not be answered", e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", held, "--remove-label", "hold"}); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresPlanQuestionAttachmentPreservesItsApprovalScope(t *testing.T) {
	_, dir, st, ctx, pid, held, _, _ := humanAuthorityFixture(t)
	if e := st.RecordPitch(ctx, pid, held, "Resume?", `{"question":"Resume?","options":[{"label":"Approve","approves":true}]}`, nil); e != nil {
		t.Fatal(e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", held, "--add-label", NeedsUserLabel}); e != nil {
		t.Fatal(e)
	}
	answer := store.NeedsResolution{ProjectID: pid, BeadID: held, Generation: "1", AnswerID: "after-attachment", Action: "approve", Approved: true}
	if e := st.SaveNeedsResolution(ctx, answer, "Approve"); e != nil {
		t.Fatal("attaching the question marker changed its subject", e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", held, "--remove-label", NeedsUserLabel, "--remove-label", "hold", "--add-label", DecisionAnsweredLabel}); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresPlanGoalApprovalSurvivesImplementationTaskExpansion(t *testing.T) {
	c, dir, st, ctx, pid, held, _, _ := humanAuthorityFixture(t)
	recordHumanApproval(t, st, ctx, pid, held, "1")
	if _, e := c.Create(WithDoorEnv(context.Background(), nil), dir, "Implement the unchanged approved goal", CreateOptions{Parent: held, Silent: true}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := postgresRun(ctx, dir, []string{"update", held, "--remove-label", "hold"}); e != nil {
		t.Fatal("execution detail invented another user decision for the same goal", e)
	}
}
