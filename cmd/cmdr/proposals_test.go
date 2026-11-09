package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/decisions"
	"github.com/example-user/commander/internal/livedata"
)

// proposalFixture is workstream ms-1 with an in-progress step ms-1.92 and its
// Workflow approved gate ms-1.91 (closed when approved).
func proposalFixture(t *testing.T, approved bool) (*beads.MemClient, *fakeMailer) {
	t.Helper()
	mailer := stubMail(t)
	m := &beads.MemClient{}
	gateStatus := "open"
	if approved {
		gateStatus = "closed"
	}
	m.Seed(beads.Bead{ID: "ms-1", Title: "Workstream one", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "ms-1.91", Title: "Workflow approved", Status: gateStatus, Labels: []string{"kind:objective", "gate", "approver:user"}})
	m.Seed(beads.Bead{ID: "ms-1.92", Title: "Verify the panel", Status: "in_progress", Labels: []string{"kind:objective"}})
	setupObjectiveProject(t, m)
	return m, mailer
}

func proposalOf(t *testing.T, m *beads.MemClient, id string) (beads.Proposal, bool) {
	t.Helper()
	b, err := m.Show(context.Background(), "", id)
	if err != nil || b == nil {
		t.Fatalf("show %s: %v", id, err)
	}
	return beads.ProposalOf(b.Labels)
}

func runOK(t *testing.T, run func([]string, *os.File, *os.File) int, args ...string) string {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	if code := run(args, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("%v: exit %d: %s", args, code, e)
	}
	out, _ := read()
	return out
}

func askArgs(id string) []string {
	return []string{"--project", "manifest", "--bead", id, "--state", "needs-user", "--ask", "Pick the VMs", "--decision", testDecision}
}

func TestProposal_ManagerAskIsHeldForCommanderUntilAccepted(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	out := runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
	if !strings.Contains(out, `"proposed":"ask"`) {
		t.Fatalf("result: %s", out)
	}
	p, ok := proposalOf(t, m, "ms-1.92")
	if !ok || p.Kind != beads.ProposedAsk || p.Prev != beads.StateInProgress {
		t.Fatalf("proposal %+v %v", p, ok)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("want only the proposal mail to Commander, got %+v", mailer.sent)
	}
	got := mailer.sent[0]
	if got.to[0] != "CalmCave" || !strings.HasPrefix(got.subject, "[proposed] ask on Verify the panel") || strings.Contains(got.subject, "needs you") {
		t.Fatalf("mail %+v", got)
	}
	for _, want := range []string{"manager-ms-1 proposes a new ask", "does not reach the user until you accept", "cmdr proposals accept --project manifest --bead ms-1.92", "cmdr proposals overrule", "never relay it to the user"} {
		if !strings.Contains(got.body, want) {
			t.Fatalf("body missing %q:\n%s", want, got.body)
		}
	}

	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runProposals([]string{"accept", "--project", "manifest", "--bead", "ms-1.92"}, stdout, stderr); code == 0 {
		t.Fatal("a Manager accepted its own proposal")
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runProposals, "accept", "--project", "manifest", "--bead", "ms-1.92")
	if _, ok := proposalOf(t, m, "ms-1.92"); ok {
		t.Fatal("accepted, but still proposed")
	}
	if b, _ := m.Show(context.Background(), "", "ms-1.92"); !slices.Contains(b.Labels, beads.NeedsUserLabel) || !hasCard(context.Background(), "manifest", "ms-1.92") {
		t.Fatalf("an accepted ask stands with its card: %+v", b.Labels)
	}
}

func TestProposal_CommanderAndUserAsksStandAsWritten(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
	if _, ok := proposalOf(t, m, "ms-1.92"); ok {
		t.Fatal("Commander's own ask became a proposal")
	}
	if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].subject, "needs you") {
		t.Fatalf("the ordinary needs-you notice: %+v", mailer.sent)
	}
}

func TestProposal_OverruleUndoesTheAskAndTellsTheManagerWhy(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
	mailer.sent = nil

	t.Setenv("COMMANDER_ROLE", "commander")
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runProposals([]string{"overrule", "--project", "manifest", "--bead", "ms-1.92"}, stdout, stderr); code == 0 {
		t.Fatal("overrule without a reason")
	}
	runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", "ms-1.92", "--reason", "the user said pick them yourself")
	b, _ := m.Show(context.Background(), "", "ms-1.92")
	if _, ok := beads.ProposalOf(b.Labels); ok || slices.Contains(b.Labels, beads.NeedsUserLabel) || b.Status != "in_progress" {
		t.Fatalf("overruled ask not undone: %s %v", b.Status, b.Labels)
	}
	if hasCard(context.Background(), "manifest", "ms-1.92") {
		t.Fatal("the overruled ask's card is still queued")
	}
	if len(mailer.sent) != 1 || mailer.sent[0].to[0] != "GoldTiger" || !strings.HasPrefix(mailer.sent[0].subject, "[overruled] ask") {
		t.Fatalf("the Manager is mailed: %+v", mailer.sent)
	}
	if ws := beads.WaitsOf(b.Labels); len(ws) != 1 || ws[0].Ref != beads.ManagerStartWait {
		t.Fatalf("the overruled ask left the step with no holder: %v", b.Labels)
	}
	for _, want := range []string{"the user said pick them yourself", "Do not add it again", "You now hold ms-1.92"} {
		if !strings.Contains(mailer.sent[0].body, want) {
			t.Fatalf("overrule mail missing %q:\n%s", want, mailer.sent[0].body)
		}
	}
}

func TestProposal_LongWaitAndParkAreProposedShortOnesAreNot(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	soon := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	runOK(t, runInternalSetObjective, "--project", "manifest", "--bead", "ms-1.92", "--waiting", "until:"+soon+":the import finishes")
	if _, ok := proposalOf(t, m, "ms-1.92"); ok || len(mailer.sent) != 0 {
		t.Fatal("a two-hour wait is not a proposal")
	}
	later := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	runOK(t, runInternalSetObjective, "--project", "manifest", "--bead", "ms-1.92", "--waiting", "until:"+later+":come back to the dev box")
	if p, ok := proposalOf(t, m, "ms-1.92"); !ok || p.Kind != beads.ProposedWait || len(mailer.sent) != 1 {
		t.Fatalf("a two-day wait is a proposal: %+v %+v", p, mailer.sent)
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", "ms-1.92", "--reason", "come back later means today")
	b, _ := m.Show(context.Background(), "", "ms-1.92")
	if ws := beads.WaitsOf(b.Labels); len(ws) != 1 || ws[0].Ref != beads.ManagerStartWait {
		t.Fatalf("the overruled wait stays, or the step has no holder: %v", b.Labels)
	}
	if body := mailer.sent[len(mailer.sent)-1].body; !strings.Contains(body, "You now hold ms-1.92") {
		t.Fatalf("the mail does not say the Manager holds the step:\n%s", body)
	}

	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	runOK(t, runInternalSetObjective, "--project", "manifest", "--bead", "ms-1.92", "--park", "--reason", "come back later")
	if p, ok := proposalOf(t, m, "ms-1.92"); !ok || p.Kind != beads.ProposedPark {
		t.Fatalf("a park with no time is a proposal: %+v", p)
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", "ms-1.92", "--reason", "no park")
	if b, _ := m.Show(context.Background(), "", "ms-1.92"); b.Status == beads.StatusDeferred {
		t.Fatal("the overruled park is still parked")
	}
	if b, _ := m.Show(context.Background(), "", "ms-1.92"); len(beads.WaitsOf(b.Labels)) != 1 {
		t.Fatalf("the unparked step has no holder: %v", b.Labels)
	}
}

// An overrule leaves the step held by its Manager unless something else holds it.
func TestProposal_OverruleLeavesNoStepWithoutAHolder(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	// a step an Operator owns keeps its owner, no receipt
	if err := m.SetLabel(context.Background(), "", "ms-1.92", "owner:operator-x", true); err != nil {
		t.Fatal(err)
	}
	runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
	t.Setenv("COMMANDER_ROLE", "commander")
	mailer.sent = nil
	runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", "ms-1.92", "--reason", "no ask")
	b, _ := m.Show(context.Background(), "", "ms-1.92")
	for _, w := range beads.WaitsOf(b.Labels) {
		if w.Ref == beads.ManagerStartWait {
			t.Fatalf("a step another holder keeps got a receipt: %v", b.Labels)
		}
	}
	if len(mailer.sent) != 1 || strings.Contains(mailer.sent[0].body, "You now hold") {
		t.Fatalf("want one overrule mail claiming no hold: %+v", mailer.sent)
	}
}

func hasStartReceipt(t *testing.T, m *beads.MemClient, id string) bool {
	t.Helper()
	b, err := m.Show(context.Background(), "", id)
	if err != nil || b == nil {
		t.Fatalf("show %s: %v", id, err)
	}
	return hasManagerStart(b.Labels)
}

// overruleOut runs the overrule as Commander and returns its result JSON.
func overruleOut(t *testing.T, id string) map[string]any {
	t.Helper()
	t.Setenv("COMMANDER_ROLE", "commander")
	out := runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", id, "--reason", "only an approval was needed")
	var r map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return r
}

// A gate or review step proposed through internal-create-objective has no
// gate-of label: retiring it releases the steps that waited on it by blocks
// edges, and each is held for the Manager and named in the mail.
func TestProposal_OverruledCreatedGateHoldsTheStepsThatWaitedOnIt(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		gate        bool
	}{
		{"gate", "Greenlight: pick the VMs", true},
		{"review", "Re-review the fix", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, mailer := proposalFixture(t, true)
			m.Seed(beads.Bead{ID: "ms-1.95", Title: "Ship the panel", Labels: []string{"kind:objective"}})
			t.Setenv("COMMANDER_ROLE", "manager-ms-1")
			args := []string{"--project", "manifest", "--parent", "ms-1", "--title", tc.title}
			if tc.gate {
				args = append(args, "--before", "ms-1.95", "--gate", "--approver", "user", "--criteria", "The user says go")
			}
			var r internalCreateObjectiveResult
			if err := json.Unmarshal([]byte(strings.TrimSpace(runOK(t, runInternalCreateObjective, args...))), &r); err != nil {
				t.Fatal(err)
			}
			if r.Proposed == "" {
				t.Fatalf("not proposed: %+v", r)
			}
			if !tc.gate { // a review step: the later step waits on it
				if err := m.DependOn(context.Background(), "", "ms-1.95", r.BeadID); err != nil {
					t.Fatal(err)
				}
			}
			if hasStartReceipt(t, m, "ms-1.95") {
				t.Fatal("the waiter is held before the overrule")
			}
			mailer.sent = nil
			out := overruleOut(t, r.BeadID)
			if !hasStartReceipt(t, m, "ms-1.95") {
				b, _ := m.Show(context.Background(), "", "ms-1.95")
				t.Fatalf("the step that waited on the retired %s has no holder: %v", tc.name, b.Labels)
			}
			if held, _ := out["held"].([]any); len(held) != 1 || held[0] != "ms-1.95" {
				t.Fatalf("held: %v", out["held"])
			}
			if hasStartReceipt(t, m, "ms-1") {
				t.Fatal("the workstream bead got a step's receipt")
			}
			if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "You now hold ms-1.95") {
				t.Fatalf("the mail names no holder: %+v", mailer.sent)
			}
		})
	}
}

// The board's held rule decides: a step a live Manager's turn holds, or one
// with an open child, gets no receipt.
func TestProposal_OverruleWritesNoHoldOnAStepAlreadyHeld(t *testing.T) {
	t.Run("live Manager turn on the board", func(t *testing.T) {
		m, mailer := proposalFixture(t, true)
		if err := m.SetLabel(context.Background(), "", "ms-1.92", "owner:manager-ms-1", true); err != nil {
			t.Fatal(err)
		}
		t.Setenv("COMMANDER_ROLE", "manager-ms-1")
		runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
		snap := &livedata.Snapshot{
			Agents: map[string]livedata.SnapAgent{"manager-ms-1": {State: "working"}},
			Workstreams: []livedata.SnapWorkstream{{BeadID: "ms-1", Project: "manifest", Status: "open", ManagerRole: "manager-ms-1",
				Steps: []livedata.SnapStep{{BeadID: "ms-1.92", Status: "in_progress", State: beads.StateNeedsUser, Owner: "manager-ms-1"}}}},
		}
		withCloseSnapshot(t, snap, "commander")
		mailer.sent = nil
		out := overruleOut(t, "ms-1.92")
		if hasStartReceipt(t, m, "ms-1.92") {
			t.Fatal("a step the live Manager holds got a receipt")
		}
		if held, _ := out["held"].([]any); held == nil || len(held) != 0 {
			t.Fatalf("held should be an empty list: %v", out["held"])
		}
		// the same step with the Manager idle is unheld: the receipt goes on
		snap.Agents["manager-ms-1"] = livedata.SnapAgent{State: "idle"}
		t.Setenv("COMMANDER_ROLE", "manager-ms-1")
		runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
		overruleOut(t, "ms-1.92")
		if !hasStartReceipt(t, m, "ms-1.92") {
			t.Fatal("an idle Manager's step got no receipt")
		}
	})
	t.Run("open child, no board", func(t *testing.T) {
		m, _ := proposalFixture(t, true)
		m.Seed(beads.Bead{ID: "ms-1.92.1", Title: "Child", Labels: []string{"kind:objective"}})
		if err := m.SetParent(context.Background(), "", "ms-1.92.1", "ms-1.92"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("COMMANDER_ROLE", "manager-ms-1")
		runOK(t, runInternalSetObjective, askArgs("ms-1.92")...)
		overruleOut(t, "ms-1.92")
		if hasStartReceipt(t, m, "ms-1.92") {
			t.Fatal("a step with an open child got a receipt")
		}
	})
}

// A retry of an overrule whose label removal did not happen (a failed hold)
// finds the gate already retired and the receipt already written, and still
// tells the Manager it holds the step.
func TestProposal_OverruleRetryAfterTheUndo(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	m.Seed(beads.Bead{ID: "ms-1.95", Title: "Ship the panel", Labels: []string{"kind:objective"}})
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	var r internalCreateObjectiveResult
	_ = json.Unmarshal([]byte(strings.TrimSpace(runOK(t, runInternalCreateObjective, "--project", "manifest", "--parent", "ms-1", "--title", "Greenlight: pick the VMs", "--before", "ms-1.95", "--gate", "--approver", "user", "--criteria", "The user says go"))), &r)
	overruleOut(t, r.BeadID)
	p := beads.ProposedLabel(beads.ProposedGate, time.Now().Unix(), "")
	if err := m.SetLabel(context.Background(), "", r.BeadID, p, true); err != nil {
		t.Fatal(err)
	}
	mailer.sent = nil
	overruleOut(t, r.BeadID)
	if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "You now hold ms-1.95") {
		t.Fatalf("the retry's mail: %+v", mailer.sent)
	}
}

func TestProposal_GateAfterTheApprovedWorkflowIsProposed(t *testing.T) {
	m, mailer := proposalFixture(t, false)
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	create := func(title string) internalCreateObjectiveResult {
		out := runOK(t, runInternalCreateObjective, "--project", "manifest", "--parent", "ms-1", "--title", title, "--gate", "--approver", "user", "--criteria", "The user says go")
		var r internalCreateObjectiveResult
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := create("Greenlight: merge to prod"); r.Proposed != "" || len(mailer.sent) != 0 {
		t.Fatalf("a gate written with the workflow (not yet approved) is part of it: %+v", r)
	}
	m.SetStatus(context.Background(), "", "ms-1.91", "closed")
	r := create("Greenlight: pick the VMs")
	if r.Proposed != beads.ProposedGate || len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "after the workflow was approved") {
		t.Fatalf("a gate past the approved workflow is proposed: %+v %+v", r, mailer.sent)
	}
	if p, ok := proposalOf(t, m, r.BeadID); !ok || p.Kind != beads.ProposedGate {
		t.Fatalf("label: %+v", p)
	}
	m.Seed(beads.Bead{ID: "ms-1.93", Title: "New track", Labels: []string{"kind:objective", beads.TrackLabel}})
	out := runOK(t, runInternalCreateObjective, "--project", "manifest", "--parent", "ms-1.93", "--title", "Workflow approved", "--gate", "--approver", "user", "--criteria", "The user says go")
	if strings.Contains(out, "proposed") {
		t.Fatalf("a new track's own workflow gate is part of its workflow: %s", out)
	}
	out = runOK(t, runInternalCreateObjective, "--project", "manifest", "--parent", "ms-1", "--title", "Re-review the fix")
	if !strings.Contains(out, `"proposed":"review"`) {
		t.Fatalf("a re-review step past the approved workflow is proposed: %s", out)
	}

	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", r.BeadID, "--reason", "only an approval was needed")
	b, _ := m.Show(context.Background(), "", r.BeadID)
	if b.Status != "closed" || !slices.Contains(b.Labels, "retired") {
		t.Fatalf("the overruled gate is retired: %s %v", b.Status, b.Labels)
	}
}

func TestProposal_WriteContradictingAStandingDecisionIsRefusedWithItQuoted(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	d, err := decisions.Add(appCommanderHome(), decisions.Decision{Words: "no deploy windows unless asked", Rule: "no-deploy-window", Date: "2026-10-24"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	args := []string{"--project", "manifest", "--bead", "ms-1.92", "--waiting", "until:" + soon + ":the deploy window opens"}
	for _, want := range []string{"standing decision " + d.ID + " (global, 2026-10-24)", "no deploy windows unless asked", "[rule no-deploy-window]", "Nothing was written"} {
		refused(t, args, want)
	}
	if b, _ := m.Show(context.Background(), "", "ms-1.92"); len(beads.WaitsOf(b.Labels)) != 0 || len(mailer.sent) != 0 {
		t.Fatalf("a refused write changed the bead or mailed: %v %+v", b.Labels, mailer.sent)
	}
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--parent", "ms-1", "--title", "Greenlight: deploy window", "--gate", "--approver", "user", "--criteria", "The user says go"}, stdout, stderr); code == 0 {
		t.Fatal("a gate naming the ruled-out window was created")
	}
	if _, e := read(); !strings.Contains(e, "retire") {
		t.Fatalf("the refusal says how the call changes: %s", e)
	}

	t.Setenv("COMMANDER_ROLE", "")
	runOK(t, runInternalSetObjective, args...) // the user's own write stands
}

func TestDecisionsCLI_AddScopesAManagerToItsWorkstreamAndOnlyCommanderRetires(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	out := runOK(t, runDecisions, "add", "--words", "only heavy local work waits")
	if !strings.Contains(out, `"scope":"ms-1"`) || !strings.Contains(out, `"added_by":"manager-ms-1"`) {
		t.Fatalf("add: %s", out)
	}
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runDecisions([]string{"retire", "d1"}, stdout, stderr); code == 0 {
		t.Fatal("a Manager retired the user's decision")
	}
	t.Setenv("COMMANDER_ROLE", "operator-x")
	if code := runDecisions([]string{"add", "--words", "x"}, stdout, stderr); code == 0 {
		t.Fatal("an Operator recorded a decision")
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runDecisions, "retire", "d1", "--reason", "the user changed it")
	if out := runOK(t, runDecisions, "list"); !strings.Contains(out, "no standing decisions") {
		t.Fatalf("list after retire: %s", out)
	}
	if out := runOK(t, runDecisions, "list", "--all"); !strings.Contains(out, "retired") || !strings.Contains(out, "the user changed it") {
		t.Fatalf("list --all: %s", out)
	}
}

func TestMailHook_DeliversStandingDecisionsOncePerContext(t *testing.T) {
	useInbox(t)
	home := appCommanderHome()
	if _, err := decisions.Add(home, decisions.Decision{Words: "push with --no-verify, CI is the check"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, _ := runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit")
	if !strings.Contains(out, "standing decisions") || !strings.Contains(out, "push with --no-verify") {
		t.Fatalf("decisions not in the turn's context: %q", out)
	}
	if out, _ := runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit"); strings.Contains(out, "no-verify") {
		t.Fatalf("delivered twice in one context: %q", out)
	}
	runHook(t, "", "--role", "manager-x", "--event", "SessionStart")
	if out, _ := runHook(t, "", "--role", "manager-x", "--event", "UserPromptSubmit"); !strings.Contains(out, "no-verify") {
		t.Fatalf("a new context gets them again: %q", out)
	}
}

func TestProposal_GateAddPastTheApprovedWorkflowIsProposedAndOverruleRetiresIt(t *testing.T) {
	m, mailer := proposalFixture(t, true)
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	out := runOK(t, runInternalGate, "add", "--project", "manifest", "--bead", "ms-1.92", "--name", "ci-green-3-in-a-row", "--check", "true", "--verifier", "auto")
	var r struct {
		GateBead string `json:"gate_bead"`
		Proposed string `json:"proposed"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil || r.Proposed != beads.ProposedGate || len(mailer.sent) != 1 {
		t.Fatalf("an added gate past the approved workflow is proposed: %s %+v", out, mailer.sent)
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	runOK(t, runProposals, "overrule", "--project", "manifest", "--bead", r.GateBead, "--reason", "CI passing once is the check")
	if b, _ := m.Show(context.Background(), "", r.GateBead); b.Status != "closed" || !slices.Contains(b.Labels, "retired") {
		t.Fatalf("the overruled gate is retired: %+v", b)
	}
	if gs := gatesNow(t, "ms-1.92"); len(gs) != 0 {
		t.Fatalf("the stage still has the overruled gate: %+v", gs)
	}
}

func TestGateCardDoesNotCreateASecondProposal(t *testing.T) {
	for _, kind := range []string{"gate", "kind:gate"} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%v", kind, pending), func(t *testing.T) {
				m, _ := proposalFixture(t, true)
				labels := []string{kind, "approver:user"}
				if pending {
					labels = append(labels, beads.ProposedLabel(beads.ProposedGate, time.Now().Unix(), ""))
				}
				m.Seed(beads.Bead{ID: "ms-1.93", ParentID: "ms-1", Title: "Register a dev key", Labels: labels})
				t.Setenv("COMMANDER_ROLE", "manager-ms-1")
				out := runOK(t, runInternalSetObjective, askArgs("ms-1.93")...)
				p, ok := proposalOf(t, m, "ms-1.93")
				if pending {
					if !ok || p.Kind != beads.ProposedGate {
						t.Fatalf("gate review bypassed or replaced: %+v %v", p, ok)
					}
				} else if ok || strings.Contains(out, `"proposed":"ask"`) {
					t.Fatalf("accepted gate acquired another proposal: %+v %s", p, out)
				}
			})
		}
	}
}

func TestAutomaticGateQuestionStillRequiresCommanderReview(t *testing.T) {
	m, _ := proposalFixture(t, true)
	m.Seed(beads.Bead{ID: "ms-1.93", ParentID: "ms-1", Title: "Automatic deployment check", Labels: []string{"kind:gate", "verifier:auto"}})
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	runOK(t, runInternalSetObjective, askArgs("ms-1.93")...)
	if p, ok := proposalOf(t, m, "ms-1.93"); !ok || p.Kind != beads.ProposedAsk {
		t.Fatalf("automatic gate introduced an unreviewed user decision: %+v %v", p, ok)
	}
}
