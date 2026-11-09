package livedata

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// nestedRows: root R with nested workstreams A (over A1) and B, each owning
// (its project's nested Managers on); step S under A1 is open with nothing on it,
// step BS under B waits on CI. Invented ids.
func nestedRows() []ObjectiveRow {
	return []ObjectiveRow{
		{BeadID: beads.NestedRoot, Title: "Root", ProjectID: 1, ProjectName: "demo", Status: "open"},
		{BeadID: beads.NestedA, ParentBeadID: beads.NestedRoot, ProjectID: 1, Status: "open", Owns: true, Track: true},
		{BeadID: beads.NestedA1, ParentBeadID: beads.NestedA, ProjectID: 1, Status: "open", Owns: true, Track: true},
		{BeadID: beads.NestedStep, ParentBeadID: beads.NestedA1, ProjectID: 1, Status: "open"},
		{BeadID: beads.NestedB, ParentBeadID: beads.NestedRoot, ProjectID: 1, Status: "open", Owns: true, Track: true},
		{BeadID: beads.NestedBStep, ParentBeadID: beads.NestedB, ProjectID: 1, Status: "open",
			Waits: []beads.Wait{{Kind: beads.WaitCI, Ref: "#12", Since: time.Unix(100, 0)}}},
	}
}

func nestedWorkstream() Workstream {
	rows := nestedRows()
	ws := Workstream{Root: rows[0]}
	for _, r := range rows[1:] {
		ws.Steps = append(ws.Steps, NestedObjective{Row: r, Depth: 1})
	}
	return ws
}

func TestInvariantViolationNamesNestedManager(t *testing.T) {
	vs := OpenBeadInvariant(nestedRows(), nil)
	if len(vs) != 1 || vs[0].BeadID != beads.NestedStep {
		t.Fatalf("violations %+v, want only %s", vs, beads.NestedStep)
	}
	if vs[0].ManagerRole != "manager-"+beads.NestedA1 || vs[0].Workstream != beads.NestedA1 {
		t.Fatalf("violation goes to %s (%s), want the nested Manager", vs[0].ManagerRole, vs[0].Workstream)
	}
	// Switched off (no row Owns): the root's Manager, as before.
	rows := nestedRows()
	for i := range rows {
		rows[i].Owns = false
	}
	if vs := OpenBeadInvariant(rows, nil); len(vs) != 1 || vs[0].ManagerRole != "manager-"+beads.NestedRoot {
		t.Fatalf("switch off: %+v", vs)
	}
}

func TestManagerWaitsGoToTheNestedOwner(t *testing.T) {
	got := ManagerWaits([]Workstream{nestedWorkstream()})
	if _, ok := got["manager-"+beads.NestedB]; !ok || len(got) != 1 {
		t.Fatalf("waits %+v, want only manager-%s", got, beads.NestedB)
	}
}

func TestWatchGroupsByNestedOwner(t *testing.T) {
	data := Data{Objectives: nestedRows(), Operators: []OperatorRow{
		{AgentName: "operator-op-x", TaskBeadID: beads.NestedStep + ".1", SpawnTime: time.Unix(100, 0)},
		{AgentName: "operator-op-y", TaskBeadID: beads.NestedBStep + ".1", SpawnTime: time.Unix(100, 0)},
	}}
	w := watchOf(data)
	if len(w["manager-"+beads.NestedA1].Operators) != 1 || len(w["manager-"+beads.NestedB].Operators) != 1 {
		t.Fatalf("watch %+v", w)
	}
	if data.ManagerOf(beads.NestedStep) != "manager-"+beads.NestedA1 {
		t.Fatalf("ManagerOf = %s", data.ManagerOf(beads.NestedStep))
	}
}

func TestMarkReportedNamesTheNestedManager(t *testing.T) {
	rows := nestedRows()
	rows[3].Status, rows[3].Owner = "in_progress", "operator-op-x"
	out := MarkReported(rows, []SnapReported{{Operator: "operator-op-x", ProjectID: 1, Bead: beads.NestedStep, Since: 100}}, LiveOperators{})
	if out[3].Owner != "manager-"+beads.NestedA1 {
		t.Fatalf("settle overlay owner %q, want the nested Manager", out[3].Owner)
	}
}

func TestPitchMailsNestedManager(t *testing.T) {
	rows := nestedRows()
	rows[3].State, rows[3].Ask = beads.StateNeedsUser, "Pick one"
	data := Data{Projects: nil, Objectives: rows}
	for _, np := range NoPitchList(data) {
		if np.BeadID == beads.NestedStep && np.ManagerRole != "manager-"+beads.NestedA1 {
			t.Fatalf("pitch owed by %s, want the nested Manager", np.ManagerRole)
		}
	}
}

// cmdr board --role: a nested Manager sees its own subtree; the parent sees all of
// it, each nested row naming its own Manager.
func TestBoardRoleNested(t *testing.T) {
	raw, err := os.ReadFile("testdata/board/nested-managers.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	d := Board(fx.Snapshot, fx.Now)
	nested := strings.Join(BoardLines(d, fx.Now, "manager-app-w1.1"), "\n")
	if !strings.Contains(nested, "Build track") || strings.Contains(nested, "Cutover track") {
		t.Fatalf("nested view:\n%s", nested)
	}
	parent := strings.Join(BoardLines(d, fx.Now, "manager-app-w1"), "\n")
	if !strings.Contains(parent, "Build track") || !strings.Contains(parent, "Cutover track") {
		t.Fatalf("parent view:\n%s", parent)
	}
	if tr := d.Workstreams[0].Tracks; len(tr) != 2 || tr[0].ManagerRole != "manager-app-w1.1" || tr[1].ManagerRole != "manager-app-w1.2" {
		t.Fatalf("tracks %+v", tr)
	}
	if other := BoardLines(d, fx.Now, "manager-app-zz"); len(other) != 0 {
		t.Fatalf("a stranger saw %v", other)
	}
}

// Rows built from the shared tree mark only real owners: a nested workstream
// under a parked one owns nothing, as wsmgr routes and registers it.
func TestRowsOwnByTheChainRule(t *testing.T) {
	mem := beads.NestedTree()
	rows, err := projectObjectiveRows(context.Background(), mem, store.Project{ID: 1, Name: "demo", Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	owns := map[string]bool{}
	for _, r := range rows {
		owns[r.BeadID] = r.Owns
	}
	for id, want := range map[string]bool{beads.NestedA: true, beads.NestedA1: true, beads.NestedB: true, beads.NestedP1: false, beads.NestedRoot: false} {
		if _, ok := owns[id]; !ok && want {
			t.Errorf("no row for %s", id)
		}
		if owns[id] != want {
			t.Errorf("%s Owns = %v, want %v", id, owns[id], want)
		}
	}
}
