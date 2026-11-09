package evals

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApprovalPublicationScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/approval-publication.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Beads        []beads.Bead
		Gate, Parent string
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := &beads.MemClient{EdgeParentsOnly: true}
	for _, b := range sc.Beads {
		m.Seed(b)
	}
	for _, b := range sc.Beads {
		if b.ParentID != "" {
			m.SetParent(ctx, "/p", b.ID, b.ParentID)
		}
	}
	if err := gates.Publish(ctx, m, "/p", sc.Gate, sc.Parent); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(ctx, store.Project{Name: "sample", Path: "/p", Type: "code", Status: "active", ActivatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: 1, BeadID: sc.Gate, Ask: "Choose sample pilot", Note: "Existing scope preserved", Since: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: m}}
	snap := src.Snapshot(ctx)
	if snap.Degraded != nil || len(snap.Workstreams) != 1 || len(snap.Workstreams[0].Steps) != 2 {
		t.Fatalf("published snapshot: %+v", snap)
	}
	if len(snap.NeedsYou) != 1 || snap.NeedsYou[0].BeadID != sc.Gate || snap.NeedsYou[0].Ask != "Choose sample pilot" {
		t.Fatalf("approval not actionable: %+v", snap)
	}
	// The loader-to-snapshot path must suppress actions even with a stored pitch.
	for _, label := range []string{"hold", "proposed:gate:100"} {
		if err := m.SetLabel(ctx, "/p", sc.Gate, label, true); err != nil {
			t.Fatal(err)
		}
		excluded := (&livedata.Source{Deps: livedata.Deps{Store: st, Beads: m}}).Snapshot(ctx)
		if excluded.Degraded != nil || len(excluded.NeedsYou) != 0 {
			t.Fatalf("excluded %s: %+v", label, excluded)
		}
		for _, ws := range excluded.Workstreams {
			for _, step := range ws.Steps {
				if step.BeadID == sc.Gate && label == "hold" {
					t.Fatalf("held card visible: %+v", step)
				}
			}
		}
		if label == "proposed:gate:100" && (len(excluded.NoPitch) != 1 || excluded.NoPitch[0].Proposed != beads.ProposedGate) {
			t.Fatalf("pending gate lost Commander custody: %+v", excluded.NoPitch)
		}
		m.SetLabel(ctx, "/p", sc.Gate, label, false)
	}
}
