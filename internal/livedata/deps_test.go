package livedata

import (
	"context"
	"reflect"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// A step's "blocks" dependencies reach the snapshot as BlockedBy; its
// parent-child edge does not, and no step costs a br dep list of its own.
func TestObjectiveRowsCarryBlockingDeps(t *testing.T) {
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "p-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-1.1", Title: "Build", Status: "open", Labels: []string{"kind:objective"}, DependencyCount: 1},
			{ID: "p-1.2", Title: "Go live", Status: "open", Labels: []string{"kind:objective", "until:live"}, DependencyCount: 2},
		},
		DepsResult: map[string][]beads.Dep{
			"p-1.2": {
				{IssueID: "p-1.2", DependsOnID: "p-1", Type: "parent-child"},
				{IssueID: "p-1.2", DependsOnID: "p-1.1", Type: "blocks"},
			},
		},
	}
	rows, err := objectiveRowsFromBeads(context.Background(), bc, []store.Project{{ID: 1, Name: "p", Path: "/p"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rows {
		got[r.BeadID] = r.BlockedBy
	}
	if !reflect.DeepEqual(got["p-1.2"], []string{"p-1.1"}) {
		t.Fatalf("Go live BlockedBy = %v, want [p-1.1]", got["p-1.2"])
	}
	for _, r := range rows {
		if r.BeadID == "p-1.2" && r.Until != "live" {
			t.Fatalf("Go live Until = %q, want live (from its until: label)", r.Until)
		}
	}
	if got["p-1.1"] != nil || got["p-1"] != nil {
		t.Fatalf("no blocks edges expected: %v", got)
	}
	for _, c := range bc.Calls {
		if c == "Deps:p-1.1|dir=/p" {
			t.Fatal("fetched deps for a step whose only edge is its parent")
		}
	}
}

// A bead moved under another one nests by its parent-child edge, not by its
// dotted id; a failed Details read must reject the new project build.
func TestObjectiveRowsNestByParentEdge(t *testing.T) {
	list := []beads.Bead{
		{ID: "q-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}, UpdatedAt: "t1"},
		{ID: "q-1.2", Title: "Stage", Status: "open", Labels: []string{"kind:objective"}, UpdatedAt: "t1"},
		{ID: "q-1.2.1", Title: "Moved up", Status: "closed", Labels: []string{"kind:objective"}, UpdatedAt: "t1"},
	}
	parents := func(bc *beads.MockClient) map[string]string {
		rows, err := objectiveRowsFromBeads(context.Background(), bc, []store.Project{{ID: 1, Name: "q", Path: "/q"}})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, r := range rows {
			got[r.BeadID] = r.ParentBeadID
		}
		return got
	}
	got := parents(&beads.MockClient{ListResult: list, ParentsResult: map[string]string{"q-1": "", "q-1.2": "q-1", "q-1.2.1": "q-1"}})
	if got["q-1.2.1"] != "q-1" {
		t.Fatalf("moved bead's parent = %q, want q-1 (its parent-child edge)", got["q-1.2.1"])
	}
	rows, err := objectiveRowsFromBeads(context.Background(), &beads.MockClient{ListResult: list, ParentsErr: context.DeadlineExceeded}, []store.Project{{ID: 1, Name: "q", Path: "/q"}})
	if err == nil || len(rows) != 0 {
		t.Fatalf("failed read published rows: rows=%+v err=%v", rows, err)
	}
}
