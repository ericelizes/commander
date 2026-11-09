package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// wfTree: workstream w1 > track w1.t > step w1.t.1, step w1.1; gates are
// children by parent-child edge. gate fields come from the caller.
func wfTree(gates ...beads.Bead) *beads.MemClient {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "w1", Title: "Area", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w1.t", Title: "Outcome", Labels: []string{"kind:objective", beads.TrackLabel}})
	m.Seed(beads.Bead{ID: "w1.t.1", Title: "Step in track", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w1.1", Title: "Step in workstream", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w1.ops", Title: "Chore", Labels: []string{"kind:objective", "kind:operational"}})
	m.Edges = []beads.Dep{
		{IssueID: "w1.t", DependsOnID: "w1", Type: "parent-child"},
		{IssueID: "w1.t.1", DependsOnID: "w1.t", Type: "parent-child"},
		{IssueID: "w1.1", DependsOnID: "w1", Type: "parent-child"},
		{IssueID: "w1.ops", DependsOnID: "w1", Type: "parent-child"},
	}
	for _, g := range gates {
		parent := strings.SplitN(g.Description, ":", 2)[1] // description "under:<parent>"
		g.Description = ""
		m.Seed(g)
		m.Edges = append(m.Edges, beads.Dep{IssueID: g.ID, DependsOnID: parent, Type: "parent-child"})
	}
	return m
}

func gateUnder(id, parent, title, status string) beads.Bead {
	return beads.Bead{ID: id, Title: title, Status: status, Labels: []string{beads.GateLabel}, Description: "under:" + parent}
}

func TestRequireWorkflowGate(t *testing.T) {
	wsClosed := gateUnder("w1.g", "w1", "Workflow approved", "closed")
	wsOpen := gateUnder("w1.g", "w1", "Workflow approved", "open")
	trClosed := gateUnder("w1.tg", "w1.t", "Workflow approved", "closed")
	trOpen := gateUnder("w1.tg", "w1.t", "Workflow approved", "open")
	specClosed := gateUnder("w1.sg", "w1", "Spec approved", "closed")
	other := gateUnder("w1.og", "w1", "Merged", "closed")
	prefixed := gateUnder("w1.pg", "w1", "Dev flow: workflow approved", "closed")
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")

	cases := []struct {
		name  string
		gates []beads.Bead
		bead  string
		want  string // "" = allowed, else an error substring
	}{
		{"no gate", nil, "w1.1", "w1 has no Workflow approved gate"},
		{"no gate message says how", nil, "w1.1", "--parent w1 --title 'Workflow approved' --gate --approver user --before"},
		{"open gate", []beads.Bead{wsOpen}, "w1.1", "gate w1.g of w1 is still open"},
		{"closed gate", []beads.Bead{wsClosed}, "w1.1", ""},
		{"spec gate counts", []beads.Bead{specClosed}, "w1.1", ""},
		{"prefixed title counts", []beads.Bead{prefixed}, "w1.1", ""},
		{"unrelated gate does not count", []beads.Bead{other}, "w1.1", "no Workflow approved gate"},
		{"track needs its own gate", []beads.Bead{wsClosed}, "w1.t.1", "w1.t has no Workflow approved gate"},
		{"track open gate", []beads.Bead{wsClosed, trOpen}, "w1.t.1", "gate w1.tg of w1.t is still open"},
		{"track closed gate", []beads.Bead{trClosed}, "w1.t.1", ""},
		{"workstream gate does not cover the track's steps' siblings", []beads.Bead{trClosed}, "w1.1", "w1 has no Workflow approved gate"},
		{"track itself is held by the workstream gate", nil, "w1.t", "w1 has no Workflow approved gate"},
		{"workstream bead itself is exempt", nil, "w1", ""},
		{"gate bead is exempt", []beads.Bead{wsOpen}, "w1.g", ""},
		{"operational bead is exempt", nil, "w1.ops", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireWorkflowGate(context.Background(), wfTree(tc.gates...), "/p", tc.bead, io.Discard)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRequireWorkflowGate_WarnOnlyByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKFLOW_GATE", "")
	var warn strings.Builder
	if err := requireWorkflowGate(context.Background(), wfTree(), "/p", "w1.1", &warn); err != nil {
		t.Fatalf("warn mode must pass: %v", err)
	}
	if !strings.HasPrefix(warn.String(), "warning (will refuse after the install): workflow not approved: w1 has no Workflow approved gate") {
		t.Fatalf("stderr = %q", warn.String())
	}
	data, err := os.ReadFile(home + "/logs/workflow-gate-warnings.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"bead":"w1.1"`, `"scope":"w1"`, `"gate":""`, `"reason":"workflow not approved`, `"time":`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("log %s lacks %s", data, want)
		}
	}
	warn.Reset()
	if err := requireWorkflowGate(context.Background(), wfTree(gateUnder("w1.g", "w1", "Workflow approved", "closed")), "/p", "w1.1", &warn); err != nil || warn.Len() != 0 {
		t.Fatalf("approved scope: %v %q", err, warn.String())
	}
}

func TestRequireWorkflowGate_ClosedAndDeferredAreExempt(t *testing.T) {
	m := wfTree()
	for id, status := range map[string]string{"w1.1": "closed", "w1.t.1": "deferred"} {
		if err := m.SetStatus(context.Background(), "/p", id, status); err != nil {
			t.Fatal(err)
		}
		if err := requireWorkflowGate(context.Background(), m, "/p", id, io.Discard); err != nil {
			t.Fatalf("%s %s: %v", id, status, err)
		}
	}
	// A closed workstream is finished history: its leftovers are not held.
	m = wfTree()
	_ = m.SetStatus(context.Background(), "/p", "w1", "closed")
	if err := requireWorkflowGate(context.Background(), m, "/p", "w1.1", io.Discard); err != nil {
		t.Fatalf("closed workstream: %v", err)
	}
}

func TestWorkflowGaps(t *testing.T) {
	m := wfTree(gateUnder("w1.tg", "w1.t", "Workflow approved", "open"))
	gaps, err := workflowGaps(context.Background(), m, "p", "/p")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, g := range gaps {
		got[g.Bead] = g.Kind + "/" + g.Problem + "/" + g.Gate
	}
	if len(got) != 2 || got["w1"] != "workstream/missing/" || got["w1.t"] != "track/open/w1.tg" {
		t.Fatalf("gaps = %v", got)
	}

	m = wfTree(gateUnder("w1.g", "w1", "Workflow approved", "closed"), gateUnder("w1.tg", "w1.t", "Route approved", "closed"))
	if gaps, _ = workflowGaps(context.Background(), m, "p", "/p"); len(gaps) != 0 {
		t.Fatalf("approved scopes are no gaps: %v", gaps)
	}
}

func TestSpawnOperator_RefusesWhenNoWorkflowGateExists(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{CreateID: "ms-1.2.1"}
	useMockBeadsRaw(t, mock)
	stubMail(t)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, stdout, stderr); code == 0 {
		t.Fatal("expected a refusal with no gate")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "ms-1 has no Workflow approved gate") || !strings.Contains(errOut, "internal-create-objective --parent ms-1") {
		t.Fatalf("stderr = %s", errOut)
	}
	if len(sp.reqs) != 0 || strings.Contains(strings.Join(mock.Calls, "\n"), "Create:") {
		t.Fatalf("nothing may be created: %v %v", mock.Calls, sp.reqs)
	}

	stdout, stderr, readOutput = withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--writes-workflow"}, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("--writes-workflow is exempt: %s", errOut)
	}
}

func TestSetObjective_InProgressRefusedWithoutWorkflowGate(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	mock := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.6", Title: "Verify", Status: "open"}}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.6", "--state", "in-progress", "--owner", "manager-manifest"}, stdout, stderr); code == 0 {
		t.Fatal("expected a refusal")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "ms-1 has no Workflow approved gate") {
		t.Fatalf("stderr = %s", errOut)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "SetStatus") || strings.HasPrefix(c, "SetLabel") {
			t.Fatalf("a refused request changes nothing: %v", mock.Calls)
		}
	}

	stdout, stderr, readOutput = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.6", "--state", "in-progress", "--owner", "manager-manifest", "--writes-workflow"}, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("--writes-workflow is exempt: %s", errOut)
	}
}

func TestSpawnOperator_WarnsWithoutWorkflowGateByDefault(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "")
	t.Setenv("COMMANDER_HOME", t.TempDir())
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	useMockBeadsRaw(t, &beads.MockClient{CreateID: "ms-1.2.1"})
	stubMail(t)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("warn mode must spawn: %s", errOut)
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "warning (will refuse after the install): workflow not approved: ms-1 has no Workflow approved gate") {
		t.Fatalf("stderr = %s", errOut)
	}
}

// Replay the Greenlight acceptance scenario through the same check used by
// spawning and state changes, in both warning and enforcement modes.
func TestWorkflowGate_GreenlightScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/workflow-gates/greenlight.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Title, Status, Approver, Parent, Bead, Problem string
			Approved                                             bool
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) == 0 {
		t.Fatal("empty scenario")
	}
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			gate := gateUnder("approval", tc.Parent, tc.Title, tc.Status)
			gate.Labels = []string{"kind:gate"}
			if tc.Approver != "" {
				gate.Labels = append(gate.Labels, "approver:"+tc.Approver)
			}
			for _, mode := range []string{"enforce", ""} {
				t.Run("mode="+mode, func(t *testing.T) {
					t.Setenv("COMMANDER_WORKFLOW_GATE", mode)
					t.Setenv("COMMANDER_HOME", t.TempDir())
					var warn strings.Builder
					err := requireWorkflowGate(context.Background(), wfTree(gate), "/p", tc.Bead, &warn)
					if tc.Approved {
						if err != nil || warn.Len() != 0 {
							t.Fatalf("approved: err=%v warning=%q", err, warn.String())
						}
					} else if mode == "enforce" {
						if err == nil || !strings.Contains(err.Error(), tc.Problem) {
							t.Fatalf("want %q, got %v", tc.Problem, err)
						}
					} else if err != nil || !strings.Contains(warn.String(), tc.Problem) {
						t.Fatalf("want warning %q, got err=%v warning=%q", tc.Problem, err, warn.String())
					}
				})
			}
		})
	}
}

// idRecorder records every id the enforcement reads ask br for.
type idRecorder struct {
	beads.Client
	asked []string
}

func (r *idRecorder) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	r.asked = append(r.asked, ids...)
	return r.Client.Details(ctx, dir, ids)
}

// The spawn-gate check reads the parents of the scope's chain and of candidate
// first gates only, never every bead or every gate of the project.
func TestRequireWorkflowGate_ReadsOnlyNeededIDs(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	first := gateUnder("w1.g", "w1", "Workflow approved", "closed")
	var gates []beads.Bead
	gates = append(gates, first)
	for i := 0; i < 5; i++ {
		gates = append(gates, gateUnder("w1.m"+string(rune('a'+i)), "w1", "Merged "+string(rune('a'+i)), "open"))
	}
	rec := &idRecorder{Client: wfTree(gates...)}
	if err := requireWorkflowGate(context.Background(), rec, "/p", "w1.1", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, id := range rec.asked {
		if strings.HasPrefix(id, "w1.m") || id == "w1.ops" || id == "w1.t.1" {
			t.Fatalf("read %q, which the check does not need; asked %v", id, rec.asked)
		}
	}
	if !slices.Contains(rec.asked, "w1.g") {
		t.Fatalf("candidate gate not read: %v", rec.asked)
	}
}
