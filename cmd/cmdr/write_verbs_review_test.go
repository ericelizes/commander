package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// showByID answers Show per id (MockClient has one ShowResult for all).
type showByID struct {
	*beads.MockClient
	by map[string]*beads.Bead
}

func (s showByID) Show(_ context.Context, _, id string) (*beads.Bead, error) { return s.by[id], nil }

func reviewMock(t *testing.T) showByID {
	m := showByID{MockClient: &beads.MockClient{}, by: map[string]*beads.Bead{
		"x-gate":  {ID: "x-gate", Status: "open", Labels: []string{"gate", "kind:gate"}},
		"x-track": {ID: "x-track", Status: "open", Labels: []string{"kind:track"}},
		"x-1":     {ID: "x-1", Status: "open"},
		"x-2":     {ID: "x-2", Status: "open"},
		"x-done":  {ID: "x-done", Status: "closed"},
	}}
	setupObjectiveProject(t, m)
	return m
}

func wrote(m showByID, prefix string) bool {
	for _, c := range m.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestInternalUpdateRefusesGateClosedAndApprovalTitle(t *testing.T) {
	m := reviewMock(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--bead", "x-gate", "--description", "true"}, "internal-gate add"},
		{[]string{"--bead", "x-gate", "--acceptance", "ok"}, "internal-gate add"},
		{[]string{"--bead", "x-1", "--title", "Greenlight the build"}, "gate"},
		{[]string{"--bead", "x-done", "--notes", "n"}, "closed"},
	} {
		code, out := runVerb(t, func(o, e *os.File) int {
			return runInternalUpdate(append([]string{"--project", "manifest"}, tc.args...), strings.NewReader(""), o, e)
		})
		if code == 0 || !strings.Contains(out, tc.want) {
			t.Errorf("%v: exit %d, %q lacks %q", tc.args, code, out, tc.want)
		}
	}
	if wrote(m, "UpdateFields") {
		t.Error("a refused update was written")
	}
	for _, args := range [][]string{
		{"--bead", "x-1", "--description", "d", "--title", "Build it"},
		{"--bead", "x-gate", "--notes", "n", "--priority", "1"},
	} {
		if code, out := runVerb(t, func(o, e *os.File) int {
			return runInternalUpdate(append([]string{"--project", "manifest"}, args...), strings.NewReader(""), o, e)
		}); code != 0 {
			t.Errorf("%v refused: %s", args, out)
		}
	}
}

func TestInternalDepRefusesGateEdgesAndCycles(t *testing.T) {
	m := reviewMock(t)
	m.DepsResult = map[string][]beads.Dep{"x-2": {{DependsOnID: "x-1", Type: "blocks"}}}
	for _, args := range [][]string{
		{"add", "x-1", "x-gate"}, {"add", "x-gate", "x-1"}, {"rm", "x-1", "x-gate"}, {"add", "x-track", "x-1"},
		{"add", "x-1", "x-2"}, // x-2 already waits on x-1: cycle
		{"add", "x-1", "x-1"},
	} {
		code, out := runVerb(t, func(o, e *os.File) int {
			return runInternalDep(append([]string{"--project", "manifest"}, args...), o, e)
		})
		if code == 0 {
			t.Errorf("%v accepted: %s", args, out)
		}
	}
	if wrote(m, "DependOn") || wrote(m, "RemoveDep") {
		t.Errorf("a refused edge was written: %v", m.Calls)
	}
	if code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalDep([]string{"--project", "manifest", "add", "x-2", "x-done"}, o, e)
	}); code != 0 {
		t.Errorf("plain edge refused: %s", out)
	}
}

// Add and rm differ for a nested workstream only: a gate or track refuses both.
func TestInternalDepNestedWorkstreamAddAndRmDiffer(t *testing.T) {
	bead := func(id, parent string, labels ...string) *beads.Bead {
		return &beads.Bead{ID: id, Status: "open", ParentID: parent, Labels: labels}
	}
	for _, tc := range []struct {
		verb, a, b string
		refused    bool
	}{
		{"add", "n-1", "n-2", true},
		{"add", "s-1", "n-1", true},
		{"add", "n-1", "s-1", true},
		{"rm", "n-1", "n-2", false},
		{"rm", "s-1", "n-1", false},
		{"rm", "n-1", "s-1", false},
		{"add", "w-1", "w-2", false},
		{"rm", "w-1", "w-2", false},
		{"add", "t-1", "s-1", true},
		{"rm", "t-1", "s-1", true},
		{"rm", "s-1", "t-1", true},
		{"rm", "g-1", "s-1", true},
		{"rm", "n-1", "g-1", true},
		{"rm", "ng-1", "s-1", true}, // nested workstream that is also a gate
	} {
		t.Run(tc.verb+" "+tc.a+" "+tc.b, func(t *testing.T) {
			m := showByID{MockClient: &beads.MockClient{}, by: map[string]*beads.Bead{
				"n-1": bead("n-1", "w-1", beads.WorkstreamLabel), "n-2": bead("n-2", "w-1", beads.WorkstreamLabel),
				"w-1": bead("w-1", "", beads.WorkstreamLabel), "w-2": bead("w-2", "", beads.WorkstreamLabel),
				"s-1": bead("s-1", "w-1"), "t-1": bead("t-1", "w-1", beads.TrackLabel), "g-1": bead("g-1", "w-1", beads.GateLabel),
				"ng-1": bead("ng-1", "w-1", beads.WorkstreamLabel, beads.GateLabel),
			}}
			setupObjectiveProject(t, m)
			code, out := runVerb(t, func(o, e *os.File) int {
				return runInternalDep([]string{"--project", "manifest", tc.verb, tc.a, tc.b}, o, e)
			})
			if refused := code != 0; refused != tc.refused {
				t.Fatalf("exit %d, want refused=%v: %s", code, tc.refused, out)
			}
			if tc.refused && !strings.Contains(out, "internal-dep rm still drops a nested workstream's edge") {
				t.Errorf("the refusal does not say how add and rm differ: %s", out)
			}
			if did := wrote(m, "DependOn") || wrote(m, "RemoveDep"); did == tc.refused {
				t.Errorf("wrote=%v with refused=%v: %v", did, tc.refused, m.Calls)
			}
		})
	}
}

func TestInternalCreateDepsBlocksGateRefused(t *testing.T) {
	m := reviewMock(t)
	m.CreateID = "x-9"
	code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalCreateObjective([]string{"--project", "manifest", "--title", "work", "--parent", "x-1", "--waiting", "review:rev", "--deps", "blocks:x-gate"}, o, e)
	})
	if code == 0 || !strings.Contains(out, "gate") {
		t.Errorf("blocks:<gate> accepted: %d %s", code, out)
	}
	if wrote(m, "Create") {
		t.Error("bead created")
	}
}

func TestInternalLabelReviewReservedAndDash(t *testing.T) {
	m := reviewMock(t)
	for _, l := range []string{"superseded", "retired", "wait-nudged", "reviewed", "approved", "waiting", "owner", "-x", "--db"} {
		if code, _ := runVerb(t, func(o, e *os.File) int {
			return runInternalLabel([]string{"--project", "manifest", "--bead", "x-1", "add", l}, o, e)
		}); code == 0 {
			t.Errorf("label %q accepted", l)
		}
	}
	if wrote(m, "SetLabel") {
		t.Error("a refused label was written")
	}
	if code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalLabel([]string{"--project", "manifest", "--bead", "x-1", "add", "regression"}, o, e)
	}); code != 0 {
		t.Errorf("plain label refused: %s", out)
	}
}

func TestInternalPhaseValidates(t *testing.T) {
	m := reviewMock(t)
	if code, _ := runVerb(t, func(o, e *os.File) int {
		return runInternalPhase([]string{"--project", "manifest", "--bead", "x-1", "--phase", "bogus"}, o, e)
	}); code == 0 || wrote(m, "SetState") {
		t.Error("bad phase accepted")
	}
	if code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalPhase([]string{"--project", "manifest", "--bead", "x-1", "--phase", "reviewing"}, o, e)
	}); code != 0 {
		t.Errorf("good phase refused: %s", out)
	}
}
