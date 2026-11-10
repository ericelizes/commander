package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
)

func withdrawalFixture(t *testing.T) (*beads.MemClient, *store.Store, withdrawalRequest) {
	t.Helper()
	m, st, _ := retirementFixture(t)
	if e := m.RemoveDep(context.Background(), "/d", "sample-flow.2", "sample-flow.3"); e != nil {
		t.Fatal(e)
	}
	return m, st, withdrawalRequest{Bead: "sample-flow.3", State: "open", Generation: 1, Reason: "The requested experiment is cancelled", Instruction: "planning-note:sample-17 cancellation by owning Manager"}
}
func withdrawalPreview(t *testing.T, st *store.Store, r withdrawalRequest) withdrawalRequest {
	t.Helper()
	out, e := gateWithdraw(context.Background(), st, "manifest", r, true)
	if e != nil {
		t.Fatal(e)
	}
	r.Snapshot = out.(map[string]any)["expected_snapshot"].(string)
	return r
}
func TestGateWithdrawPreservesHistoryAndNeverApproves(t *testing.T) {
	m, st, r := withdrawalFixture(t)
	ctx := context.Background()
	before := retirementSnapshot(t, m, st)
	r = withdrawalPreview(t, st, r)
	if before != retirementSnapshot(t, m, st) {
		t.Fatal("dry-run wrote")
	}
	p, _ := st.GetProjectByName(ctx, "manifest")
	pitches, _ := st.ListPitches(ctx, p.ID, r.Bead)
	resolutions, _ := st.ListNeedsResolutions(ctx, p.ID, r.Bead)
	original, _ := m.Show(ctx, "/d", r.Bead)
	for i := 0; i < 2; i++ {
		if _, e := gateWithdraw(ctx, st, "manifest", r, false); e != nil {
			t.Fatal(e)
		}
	}
	b, _ := m.Show(ctx, "/d", r.Bead)
	if b.Status != beads.StatusDeferred || !slices.Contains(b.Labels, beads.WithdrawnLabel) || slices.Contains(b.Labels, "needs-user") || b.Description != original.Description || b.AcceptanceCriteria != original.AcceptanceCriteria {
		t.Fatalf("bad withdrawal: %+v", b)
	}
	if card, e := st.GetNeedsYou(ctx, p.ID, r.Bead); e != nil || card != nil {
		t.Fatalf("card remains: %+v %v", card, e)
	}
	after, _ := st.ListPitches(ctx, p.ID, r.Bead)
	rs, _ := st.ListNeedsResolutions(ctx, p.ID, r.Bead)
	if !reflect.DeepEqual(pitches, after) || !reflect.DeepEqual(resolutions, rs) {
		t.Fatal("pitch or answer journal changed")
	}
	d, _ := m.Details(ctx, "/d", []string{r.Bead})
	if len(d[r.Bead].Comments) != 2 {
		t.Fatal("retry duplicated audit")
	}
	// Even a later accidental close can never turn the cancellation into approval.
	_ = m.SetStatus(ctx, "/d", r.Bead, "closed")
	if _, e := checkProdGate(ctx, m, "/d", "sample-flow.2", r.Bead, "manager-sample-flow"); e == nil || !strings.Contains(e.Error(), "withdrawn") {
		t.Fatalf("production approved: %v", e)
	}
	closed, _ := m.Show(ctx, "/d", r.Bead)
	closed.Title = "Workflow approved"
	if ok, _ := firstGateOf("sample-flow", []beads.Bead{*closed}, map[string]string{r.Bead: "sample-flow"}); ok {
		t.Fatal("workflow approved")
	}
	gs, e := gates.ForStageFresh(ctx, m, "/d", p.ID, "sample-flow.2")
	if e != nil || len(gs) != 1 || gs[0].Gate.Status == store.GatePassed {
		t.Fatalf("gate passed: %+v %v", gs, e)
	}
	if e = gates.SetResult(ctx, m, "/d", gs[0], store.GatePassed, "invalid", "user", time.Now()); e == nil {
		t.Fatal("SetResult approved withdrawal")
	}
}

func TestGateWithdrawRefusals(t *testing.T) {
	for _, name := range []string{"ordinary", "legacy", "foreign", "operator", "answered", "journal", "approved", "closed", "generation", "snapshot", "waiter", "parked-waiter", "superseded-waiter", "child", "running", "exited", "assignee", "waiting", "changed-scope", "instruction"} {
		t.Run(name, func(t *testing.T) {
			m, st, r := withdrawalFixture(t)
			ctx := context.Background()
			r = withdrawalPreview(t, st, r)
			p, _ := st.GetProjectByName(ctx, "manifest")
			switch name {
			case "ordinary":
				_ = m.SetLabel(ctx, "/d", r.Bead, "kind:gate", false)
			case "legacy":
				_ = m.SetLabel(ctx, "/d", r.Bead, "kind:gate", false)
				_ = m.SetLabel(ctx, "/d", r.Bead, "gate", true)
				r = withdrawalPreview(t, st, withdrawalRequest{Bead: r.Bead, State: r.State, Generation: r.Generation, Reason: r.Reason, Instruction: r.Instruction})
				if _, e := gateWithdraw(ctx, st, "manifest", r, false); e != nil {
					t.Fatal(e)
				}
				return
			case "foreign":
				t.Setenv("COMMANDER_ROLE", "manager-other")
			case "operator":
				t.Setenv("COMMANDER_ROLE", "operator-example")
			case "answered":
				_ = st.AnswerPitch(ctx, p.ID, r.Bead, "Wait")
			case "journal":
				e := st.SaveNeedsResolution(ctx, store.NeedsResolution{ProjectID: p.ID, BeadID: r.Bead, Generation: "1", AnswerID: "synthetic-answer", Action: "approve"}, "Approve")
				if e != nil {
					t.Fatal(e)
				}
			case "approved":
				_ = m.SetLabel(ctx, "/d", r.Bead, "approved", true)
			case "closed":
				_ = m.SetStatus(ctx, "/d", r.Bead, "closed")
			case "generation":
				r.Generation++
			case "snapshot":
				r.Snapshot = strings.Repeat("0", 64)
			case "waiter", "parked-waiter", "superseded-waiter":
				_ = m.DependOn(ctx, "/d", "sample-flow.2", r.Bead)
				if name == "parked-waiter" {
					_ = m.SetStatus(ctx, "/d", "sample-flow.2", beads.StatusDeferred)
				}
				if name == "superseded-waiter" {
					_ = m.SetLabel(ctx, "/d", "sample-flow.2", "superseded", true)
				}
			case "child":
				m.Seed(beads.Bead{ID: "sample-flow.4", Status: "open"})
				_ = m.SetParent(ctx, "/d", "sample-flow.4", r.Bead)
			case "running", "exited":
				_, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: p.ID, TaskBeadID: r.Bead, AgentName: "operator-fixture", Status: name})
				if e != nil {
					t.Fatal(e)
				}
			case "assignee":
				for _, b := range m.Beads {
					if b.ID == r.Bead {
						b.AssignedAgent = "operator-fixture"
					}
				}
			case "waiting":
				_ = m.SetLabel(ctx, "/d", r.Bead, "waiting:user", true)
			case "changed-scope":
				changed := "Changed scope"
				_ = m.UpdateFields(ctx, "/d", r.Bead, beads.Fields{Description: &changed})
			case "instruction":
				r.Instruction = ""
			}
			before := retirementSnapshot(t, m, st)
			if _, e := gateWithdraw(ctx, st, "manifest", r, false); e == nil {
				t.Fatal("accepted unsafe request")
			}
			if before != retirementSnapshot(t, m, st) {
				t.Fatal("refusal wrote")
			}
		})
	}
}

func TestGateWithdrawInterruptedRetry(t *testing.T) {
	for _, point := range []string{"intent", "label", "completion", "mutation"} {
		t.Run(point, func(t *testing.T) {
			m, st, r := withdrawalFixture(t)
			r = withdrawalPreview(t, st, r)
			c := &retirementClient{Client: m}
			switch point {
			case "intent":
				c.lostAudit = "intent"
			case "label":
				c.failLabel = beads.NeedsUserLabel
				c.lost = true
			case "completion":
				c.lostAudit = "completion"
			case "mutation":
				c.mutate = func() { _ = m.DependOn(context.Background(), "/d", "sample-flow.2", r.Bead) }
			}
			old := newBeadsClientFn
			newBeadsClientFn = func() beads.Client { return c }
			t.Cleanup(func() { newBeadsClientFn = old })
			if _, e := gateWithdraw(context.Background(), st, "manifest", r, false); e == nil {
				t.Fatal("expected interruption")
			}
			_, e := gateWithdraw(context.Background(), st, "manifest", r, false)
			if point == "mutation" {
				if e == nil || !strings.Contains(e.Error(), "sample-flow.2") {
					t.Fatal("new waiter ignored", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			changed := r
			changed.Reason = "Different cancellation"
			if _, e = gateWithdraw(context.Background(), st, "manifest", changed, false); e == nil {
				t.Fatal("changed retry accepted")
			}
		})
	}
}

func runGateWithdrawalScenario(t *testing.T, inactive bool) {
	t.Helper()
	data, e := os.ReadFile(func() string {
		if inactive {
			return "../../tests/evals/scenarios/gate-withdrawal-inactive.json"
		}
		return "../../tests/evals/scenarios/gate-withdrawal.json"
	}())
	if e != nil {
		t.Fatal(e)
	}
	var r withdrawalRequest
	if e = json.Unmarshal(data, &r); e != nil {
		t.Fatal(e)
	}
	var m *beads.MemClient
	if inactive {
		m, _, _ = inactiveWithdrawalFixture(t)
	} else {
		m, _, _ = withdrawalFixture(t)
	}
	args := []string{"withdraw", "--project", "manifest", "--bead", r.Bead, "--expected-state", r.State, "--generation", "1", "--reason", r.Reason, "--instruction", r.Instruction, "--dry-run"}
	code, out, msg := gateCmd(t, args...)
	if code != 0 {
		t.Fatal(msg)
	}
	var preview struct {
		Snapshot string `json:"expected_snapshot"`
	}
	if e = json.Unmarshal([]byte(out), &preview); e != nil {
		t.Fatal(e)
	}
	args = append(args[:len(args)-1], "--expected-snapshot", preview.Snapshot)
	code, out, msg = gateCmd(t, args...)
	if code != 0 {
		t.Fatal(msg)
	}
	t.Log(strings.TrimSpace(out))
	b, _ := m.Show(context.Background(), "/d", r.Bead)
	if b.Status != "deferred" || !slices.Contains(b.Labels, beads.WithdrawnLabel) {
		t.Fatal("not withdrawn")
	}
	t.Log("withdrawn; active card removed; unanswered pitch and scope retained; never approved")
}

func TestGateWithdrawalScenario(t *testing.T)         { runGateWithdrawalScenario(t, false) }
func TestGateWithdrawalInactiveScenario(t *testing.T) { runGateWithdrawalScenario(t, true) }

func TestGateWithdrawalReservedAuditAndLabel(t *testing.T) {
	withdrawalFixture(t)
	out, errOut, read := withTempOutputFiles(t)
	code := runInternalComment([]string{"--project", "manifest", "--bead", "sample-flow.3", "--text", withdrawalPrefix + "{}"}, strings.NewReader(""), out, errOut)
	_, msg := read()
	if code == 0 || !strings.Contains(msg, "only by internal-gate withdraw") {
		t.Fatal(code, msg)
	}
	if verb, ok := reservedLabel(beads.WithdrawnLabel); !ok || verb != "cmdr internal-gate withdraw" {
		t.Fatal("unreserved withdrawal label")
	}
}

func TestGateWithdrawClosedWaiterAndMissingSnapshot(t *testing.T) {
	m, st, r := withdrawalFixture(t)
	ctx := context.Background()
	_ = m.DependOn(ctx, "/d", "sample-flow.2", r.Bead)
	_ = m.SetStatus(ctx, "/d", "sample-flow.2", "closed")
	if _, e := gateWithdraw(ctx, st, "manifest", r, false); e == nil {
		t.Fatal("apply without preview")
	}
	r = withdrawalPreview(t, st, r)
	if _, e := gateWithdraw(ctx, st, "manifest", r, false); e != nil {
		t.Fatal(e)
	}
	// The old closed wait remains a historical link, never removed.
	d, _ := m.Details(ctx, "/d", []string{"sample-flow.2"})
	found := false
	for _, e := range d["sample-flow.2"].Deps {
		found = found || e.Type == "blocks" && e.DependsOnID == r.Bead
	}
	if !found {
		t.Fatal("withdrawal unlinked history")
	}
}

func TestGateWithdrawCheckNeverRunsOrAddsWait(t *testing.T) {
	for _, verifier := range []string{"user", "auto"} {
		t.Run(verifier, func(t *testing.T) {
			m, st, r := withdrawalFixture(t)
			ctx := context.Background()
			if verifier == "auto" {
				_ = m.SetLabel(ctx, "/d", r.Bead, "verifier:user", false)
				_ = m.SetLabel(ctx, "/d", r.Bead, "verifier:auto", true)
			}
			r = withdrawalPreview(t, st, r)
			if _, e := gateWithdraw(ctx, st, "manifest", r, false); e != nil {
				t.Fatal(e)
			}
			ran := false
			fakeGateRun(t, func(string) (string, error) { ran = true; return "", nil })
			if _, e := gateCheck(ctx, st, "manifest", "sample-flow.2", ""); e != nil {
				t.Fatal(e)
			}
			if ran {
				t.Fatal("withdrawn check executed")
			}
			b, _ := m.Show(ctx, "/d", "sample-flow.2")
			for _, l := range b.Labels {
				if strings.HasPrefix(l, "waiting:") {
					t.Fatal("withdrawn gate added wait")
				}
			}
		})
	}
}

// The card was removed by a normal state change, not answered. The legacy
// approval gate and its exact retained pitch must still support cancellation.
func inactiveWithdrawalFixture(t *testing.T) (*beads.MemClient, *store.Store, withdrawalRequest) {
	t.Helper()
	m, st, r := withdrawalFixture(t)
	ctx := context.Background()
	p, _ := st.GetProjectByName(ctx, "manifest")
	if e := st.DeleteNeedsYou(ctx, p.ID, r.Bead); e != nil {
		t.Fatal(e)
	}
	_ = m.SetLabel(ctx, "/d", r.Bead, beads.NeedsUserLabel, false)
	_ = m.SetLabel(ctx, "/d", r.Bead, "kind:gate", false)
	_ = m.SetLabel(ctx, "/d", r.Bead, "gate", true)
	_ = m.DependOn(ctx, "/d", "sample-flow.2", r.Bead)
	_ = m.SetStatus(ctx, "/d", "sample-flow.2", "closed")
	return m, st, r
}

func TestGateWithdrawInactiveRetainedPitch(t *testing.T) {
	m, st, r := inactiveWithdrawalFixture(t)
	ctx := context.Background()
	p, _ := st.GetProjectByName(ctx, "manifest")
	pitches, _ := st.ListPitches(ctx, p.ID, r.Bead)
	before := retirementSnapshot(t, m, st)
	r = withdrawalPreview(t, st, r)
	if before != retirementSnapshot(t, m, st) {
		t.Fatal("preview wrote")
	}
	for i := 0; i < 2; i++ {
		if _, e := gateWithdraw(ctx, st, "manifest", r, false); e != nil {
			t.Fatal(e)
		}
	}
	after, _ := st.ListPitches(ctx, p.ID, r.Bead)
	if !reflect.DeepEqual(pitches, after) {
		t.Fatal("retained pitch changed")
	}
	card, e := st.GetNeedsYou(ctx, p.ID, r.Bead)
	if e != nil || card != nil {
		t.Fatal("created active card", card, e)
	}
	b, _ := m.Show(ctx, "/d", r.Bead)
	if b.Status != beads.StatusDeferred || !slices.Contains(b.Labels, beads.WithdrawnLabel) || slices.Contains(b.Labels, beads.NeedsUserLabel) {
		t.Fatal(b)
	}
	ds, _ := m.Details(ctx, "/d", []string{r.Bead})
	if len(ds[r.Bead].Comments) != 2 {
		t.Fatal("duplicated audit")
	}
	t.Log("inactive legacy gate withdrawn; exact unanswered pitch retained; no card or approval created")
}

func TestGateWithdrawInactiveRefusals(t *testing.T) {
	for _, name := range []string{"card-added", "marker-added", "generation", "scope", "answered", "journal", "ordinary", "no-pitch", "invalid-pitch", "active-card-removed", "active-marker-removed"} {
		t.Run(name, func(t *testing.T) {
			m, st, r := inactiveWithdrawalFixture(t)
			ctx := context.Background()
			p, _ := st.GetProjectByName(ctx, "manifest")
			ps, _ := st.ListPitches(ctx, p.ID, r.Bead)
			if strings.HasPrefix(name, "active-") {
				_ = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: r.Bead, Ask: ps[0].Title, Decision: ps[0].Decision})
				_ = m.SetLabel(ctx, "/d", r.Bead, beads.NeedsUserLabel, true)
			}
			r = withdrawalPreview(t, st, r)
			switch name {
			case "card-added":
				_ = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: r.Bead, Ask: ps[0].Title, Decision: ps[0].Decision})
			case "marker-added":
				_ = m.SetLabel(ctx, "/d", r.Bead, beads.NeedsUserLabel, true)
			case "generation":
				_ = st.RecordPitch(ctx, p.ID, r.Bead, "Changed question", `{"question":"Changed?","input":true}`, nil)
			case "scope":
				v := "Changed scope"
				_ = m.UpdateFields(ctx, "/d", r.Bead, beads.Fields{Description: &v})
			case "answered":
				_ = st.AnswerPitch(ctx, p.ID, r.Bead, "Wait")
			case "journal":
				if e := st.SaveNeedsResolution(ctx, store.NeedsResolution{ProjectID: p.ID, BeadID: r.Bead, Generation: "1", AnswerID: "fixture-answer", Action: "approve"}, "Approve"); e != nil {
					t.Fatal(e)
				}
			case "ordinary":
				_ = m.SetLabel(ctx, "/d", r.Bead, "gate", false)
			case "no-pitch":
				r.Bead = "sample-flow.2"
				_ = m.SetStatus(ctx, "/d", r.Bead, "open")
			case "invalid-pitch":
				_ = st.RecordPitch(ctx, p.ID, r.Bead, "Invalid", "{}", nil)
				r.Generation++
			case "active-card-removed":
				_ = st.DeleteNeedsYou(ctx, p.ID, r.Bead)
			case "active-marker-removed":
				_ = m.SetLabel(ctx, "/d", r.Bead, beads.NeedsUserLabel, false)
			}
			before := retirementSnapshot(t, m, st)
			if _, e := gateWithdraw(ctx, st, "manifest", r, false); e == nil {
				t.Fatal("unsafe request accepted")
			}
			if before != retirementSnapshot(t, m, st) {
				t.Fatal("refusal wrote")
			}
		})
	}
}

func TestGateWithdrawInactiveInterruptedRetry(t *testing.T) {
	for _, point := range []string{"intent", "label", "completion", "card-added", "marker-added", "generation", "journal"} {
		t.Run(point, func(t *testing.T) {
			m, st, r := inactiveWithdrawalFixture(t)
			ctx := context.Background()
			p, _ := st.GetProjectByName(ctx, "manifest")
			r = withdrawalPreview(t, st, r)
			c := &retirementClient{Client: m}
			switch point {
			case "intent":
				c.lostAudit = "intent"
			case "label":
				c.failLabel = beads.WithdrawnLabel
				c.lost = true
			case "completion":
				c.lostAudit = "completion"
			default:
				c.lostAudit = "intent"
			}
			old := newBeadsClientFn
			newBeadsClientFn = func() beads.Client { return c }
			t.Cleanup(func() { newBeadsClientFn = old })
			if _, e := gateWithdraw(ctx, st, "manifest", r, false); e == nil {
				t.Fatal("expected interruption")
			}
			switch point {
			case "card-added":
				ps, _ := st.ListPitches(ctx, p.ID, r.Bead)
				_ = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: r.Bead, Ask: ps[0].Title, Decision: ps[0].Decision})
			case "marker-added":
				_ = m.SetLabel(ctx, "/d", r.Bead, beads.NeedsUserLabel, true)
			case "generation":
				_ = st.RecordPitch(ctx, p.ID, r.Bead, "New", `{"question":"New?","input":true}`, nil)
			case "journal":
				if e := st.SaveNeedsResolution(ctx, store.NeedsResolution{ProjectID: p.ID, BeadID: r.Bead, Generation: "1", AnswerID: "retry-answer", Action: "approve"}, "Approve"); e != nil {
					t.Fatal(e)
				}
			}
			before := retirementSnapshot(t, m, st)
			_, e := gateWithdraw(ctx, st, "manifest", r, false)
			if point == "intent" || point == "label" || point == "completion" {
				if e != nil {
					t.Fatal(e)
				}
				changed := r
				changed.Instruction = "Different instruction"
				if _, e = gateWithdraw(ctx, st, "manifest", changed, false); e == nil {
					t.Fatal("changed retry accepted")
				}
			} else {
				if e == nil {
					t.Fatal("changed evidence accepted")
				}
				if before != retirementSnapshot(t, m, st) {
					t.Fatal("refusal wrote")
				}
			}
		})
	}
}
