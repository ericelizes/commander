package approvedcontext

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestRestore(t *testing.T) {
	for _, tc := range []struct {
		name, role, description, status, action string
		dry, deny                               bool
	}{
		{name: "restore", role: "manager-demo"},
		{name: "dry", role: "commander", dry: true},
		{name: "user"},
		{name: "operator", role: "operator-demo", deny: true},
		{name: "foreign manager", role: "manager-other", deny: true},
		{name: "overwrite", description: "different", deny: true},
		{name: "open", status: "open", deny: true},
		{name: "rejected", action: "defer", deny: true},
		{name: "stale generation", deny: true},
		{name: "wrong answer", deny: true},
		{name: "missing approver", deny: true},
		{name: "plain closed bead", deny: true},
		{name: "whitespace description", description: " ", deny: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			pid, err := st.InsertProject(ctx, store.Project{Name: "sample", Path: "/fixture", Type: "code"})
			if err != nil {
				t.Fatal(err)
			}
			decision := `{"question":"Proceed?","context":"Exact original scope.\nPreserve limits.","options":[{"label":"Continue","approves":true}]}`
			if err = st.RecordPitch(ctx, pid, "demo-gate", "Proceed", decision, nil); err != nil {
				t.Fatal(err)
			}
			action := tc.action
			if action == "" {
				action = "approve"
			}
			r := store.NeedsResolution{ProjectID: pid, BeadID: "demo-gate", Generation: "1", AnswerID: "fixture-answer", Action: action}
			if err = st.SaveNeedsResolution(ctx, r, "Approved"); err != nil {
				t.Fatal(err)
			}
			if err = st.AdvanceNeedsResolution(ctx, r, store.ResolutionMailed); err != nil {
				t.Fatal(err)
			}
			bc := &beads.MemClient{}
			status := tc.status
			if status == "" {
				status = "closed"
			}
			bc.Seed(beads.Bead{ID: "demo", Status: "open", Labels: []string{"kind:objective"}})
			original := beads.Bead{ID: "demo-gate", Status: status, Description: tc.description, Labels: []string{"kind:gate", "approver:user"}}
			if tc.name == "missing approver" {
				original.Labels = []string{"gate"}
			}
			if tc.name == "plain closed bead" {
				original.Labels = []string{"approver:user"}
			}
			bc.Seed(original)
			bc.Edges = append(bc.Edges, beads.Dep{IssueID: "demo-gate", DependsOnID: "demo", Type: "parent-child"})
			before, _ := st.ListNeedsResolutions(ctx, pid, "demo-gate")
			generation, answer := 1, r.AnswerID
			if tc.name == "stale generation" {
				generation = 2
			}
			if tc.name == "wrong answer" {
				answer = "different-answer"
			}
			result, err := Restore(ctx, st, bc, store.Project{ID: pid, Path: "/fixture"}, "demo-gate", tc.role, generation, answer, tc.dry)
			if (err != nil) != tc.deny {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			got, _ := bc.Show(ctx, "/fixture", "demo-gate")
			if tc.deny || tc.dry {
				if !reflect.DeepEqual(*got, original) {
					t.Fatalf("mutated: %+v", got)
				}
			} else {
				if got.Description != "Exact original scope.\nPreserve limits." || !result.Changed {
					t.Fatalf("%+v %+v", got, result)
				}
				expected := original
				expected.Description = got.Description
				if !reflect.DeepEqual(*got, expected) {
					t.Fatalf("other fields changed: %+v", got)
				}
				result, err = Restore(ctx, st, bc, store.Project{ID: pid, Path: "/fixture"}, "demo-gate", tc.role, 1, r.AnswerID, false)
				if err != nil || result.Changed {
					t.Fatalf("idempotence: %+v %v", result, err)
				}
			}
			after, _ := st.ListNeedsResolutions(ctx, pid, "demo-gate")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("answer history changed")
			}
		})
	}
}

func TestRetainedRefusesUnprovenApprovals(t *testing.T) {
	p := store.Pitch{ProjectID: 1, BeadID: "demo", Version: 2, Decision: `{"question":"Proceed?","context":"scope","input":true}`, Answer: "Approved", AnsweredAt: 1}
	r := store.NeedsResolution{ProjectID: 1, BeadID: "demo", Generation: "2", AnswerID: "fixture", State: store.ResolutionClosed, Action: "approve"}
	for _, mutate := range []func(*store.Pitch, *store.NeedsResolution){
		func(p *store.Pitch, r *store.NeedsResolution) { r.Generation = "1" },
		func(p *store.Pitch, r *store.NeedsResolution) { r.AnswerID = "" },
		func(p *store.Pitch, r *store.NeedsResolution) { r.State = store.ResolutionSuperseded },
		func(p *store.Pitch, r *store.NeedsResolution) {
			r.Action = "decide"
			r.Choice = "Unmarked"
			p.Answer = r.Choice
			r.Approved = true
		},
		func(p *store.Pitch, r *store.NeedsResolution) { p.Answer = "Not yet" },
	} {
		pp, rr := p, r
		mutate(&pp, &rr)
		if _, err := retained(pp, rr); err == nil {
			t.Fatalf("accepted %+v %+v", pp, rr)
		}
	}
}

// With nested Managers on, the nearest owner and any owner above it may restore;
// a sibling's Manager is refused before anything is read from the store.
func TestRestore_NestedOwnerChain(t *testing.T) {
	tree := beads.NestedTree()
	dir := t.TempDir()
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	for role, refused := range map[string]bool{"manager-" + beads.NestedB: true, "manager-" + beads.NestedA1: false, "manager-" + beads.NestedRoot: false} {
		// An empty store: an authorized role gets past the role check and finds no evidence.
		st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Restore(context.Background(), st, tree, store.Project{ID: 1, Path: dir}, beads.NestedStep, role, 1, "x", true)
		st.Close()
		got := err != nil && strings.Contains(err.Error(), "only the owning Manager")
		if got != refused || err == nil {
			t.Errorf("%s: err %v, refused %v", role, err, refused)
		}
	}
}
