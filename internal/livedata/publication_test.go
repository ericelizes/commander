package livedata

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
)

func TestApprovalGateOnlyRows(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "sample", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "sample.1", ParentID: "sample", Labels: []string{"gate", "kind:gate", "approver:user", beads.NeedsUserLabel}})
	m.Seed(beads.Bead{ID: "sample.2", ParentID: "sample", Labels: []string{"kind:objective", "gate"}})
	m.Seed(beads.Bead{ID: "unpublished-card", Labels: []string{"gate", "kind:gate", "approver:user"}})
	m.Seed(beads.Bead{ID: "sample-check", Labels: []string{"kind:gate"}})
	rows, err := projectObjectiveRows(context.Background(), m, store.Project{ID: 1, Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%+v", rows)
	}
	for _, row := range rows {
		if row.BeadID == "sample.1" {
			if !row.Gate || row.ParentBeadID != "sample" {
				t.Fatalf("row=%+v", row)
			}
			return
		}
	}
	t.Fatal("approval gate missing")
}

func TestApprovalPublicationEligibility(t *testing.T) {
	for _, mode := range []string{"valid", "held-card", "held-root", "proposed", "unpublished", "missing", "cycle", "invalid-root", "unloaded"} {
		t.Run(mode, func(t *testing.T) {
			m := &beads.MemClient{EdgeParentsOnly: true}
			m.Seed(beads.Bead{ID: "sample", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "card", ParentID: "sample", Labels: []string{"kind:gate", "approver:user"}})
			ctx := context.Background()
			m.SetParent(ctx, "/p", "card", "sample")
			switch mode {
			case "held-card":
				m.SetLabel(ctx, "/p", "card", "hold", true)
			case "held-root":
				m.SetLabel(ctx, "/p", "sample", "hold", true)
			case "proposed":
				m.SetLabel(ctx, "/p", "card", "proposed:gate:100", true)
			case "unpublished":
				m.SetParent(ctx, "/p", "card", "")
			case "missing":
				m.SetParent(ctx, "/p", "card", "absent")
			case "cycle":
				m.SetParent(ctx, "/p", "sample", "card")
			case "invalid-root":
				m.SetLabel(ctx, "/p", "sample", "kind:gate", true)
			case "unloaded":
				m.Seed(beads.Bead{ID: "hidden"})
				m.SetParent(ctx, "/p", "card", "hidden")
			}
			rows, err := projectObjectiveRows(ctx, m, store.Project{ID: 1, Path: "/p"})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range rows {
				if r.BeadID == "card" {
					found = true
					if !r.Gate {
						t.Fatal("kind-only approval lacks gate semantics")
					}
				}
			}
			if found != (mode == "valid" || mode == "proposed") {
				t.Fatalf("mode %s rows %+v", mode, rows)
			}
		})
	}
}
func TestUnpublishedApprovalOnlyProjectIsEmpty(t *testing.T) {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "card", Labels: []string{"kind:gate", "approver:user"}})
	rows, err := projectObjectiveRows(context.Background(), m, store.Project{ID: 1, Path: "/p"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v error=%v", rows, err)
	}
}

func TestProposedGateRetainsBlockerAndCommanderResponsibility(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "root", Title: "Repair", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "key", Title: "Register key", ParentID: "root", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "approval", Title: "Register a new key?", ParentID: "root", Description: "New key registration requires user permission.", Labels: []string{"kind:gate", "approver:user", "needs-user", beads.ProposedLabel(beads.ProposedAsk, 100, beads.StateNotStarted)}})
	for _, id := range []string{"key", "approval"} {
		if err := m.SetParent(ctx, "/p", id, "root"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DependOn(ctx, "/p", "key", "approval"); err != nil {
		t.Fatal(err)
	}
	rows, err := projectObjectiveRows(ctx, m, store.Project{ID: 1, Name: "p", Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if rows[i].BeadID == "approval" {
			rows[i].Note = "Recorded decision card: new key registration requires permission."
		}
	}
	d := Data{Projects: []store.Project{{ID: 1, Name: "p", Path: "/p"}}, Objectives: rows, Agents: map[string]store.AgentState{"manager-root": {Role: "manager-root", State: store.AgentIdle}}}
	s := BuildSnapshot(d, nil)
	if len(s.Broken) != 0 || len(s.NeedsYou) != 0 {
		t.Fatalf("pending approval became orphaned or actionable: broken=%+v asks=%+v", s.Broken, s.NeedsYou)
	}
	if len(s.NoPitch) != 1 || s.NoPitch[0].Proposed != beads.ProposedAsk {
		t.Fatalf("Commander custody disappeared: %+v", s.NoPitch)
	}
	for _, now := range []int64{110, 100 + pitchDeadline + 1} {
		row := Board(s, now).Workstreams[0]
		if row.Text == "State missing" || row.Kind == "needs" || !strings.Contains(row.Text, "Commander") {
			t.Fatalf("incorrect pending state %+v", row)
		}
	}
	if err = m.SetLabel(ctx, "/p", "approval", beads.ProposedLabel(beads.ProposedAsk, 100, beads.StateNotStarted), false); err != nil {
		t.Fatal(err)
	}
	rows, err = projectObjectiveRows(ctx, m, store.Project{ID: 1, Name: "p", Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if rows[i].BeadID == "approval" {
			rows[i].Note = "Recorded decision card: new key registration requires permission."
		}
	}
	d.Objectives = rows
	accepted := BuildSnapshot(d, nil)
	if len(accepted.NeedsYou) != 1 || len(accepted.Broken) != 0 {
		t.Fatalf("accepted card not published correctly: %+v", accepted)
	}
}
