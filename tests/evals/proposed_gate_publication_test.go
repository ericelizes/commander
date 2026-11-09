package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

func TestProposedGatePublicationScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/proposed-gate-publication.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Cases []struct {
			Proposal, Want string
			Now            int64
		}
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Proposal+tc.Want, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			pid, err := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: "/p", Status: "active", ActivatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			m := &beads.MemClient{EdgeParentsOnly: true}
			proposal := beads.ProposedLabel(tc.Proposal, 100, beads.StateNotStarted)
			m.Seed(beads.Bead{ID: "root", Title: "Repair", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "step", Title: "Register key", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "card", Title: "Register a key?", Labels: []string{"kind:gate", "approver:user", "needs-user", proposal}})
			for _, id := range []string{"step", "card"} {
				if err = m.SetParent(ctx, "/p", id, "root"); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.DependOn(ctx, "/p", "step", "card"); err != nil {
				t.Fatal(err)
			}
			if err = st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "card", Ask: "Register a key?", Note: "Register a new development key; no production access."}); err != nil {
				t.Fatal(err)
			}
			snapshot := func() livedata.Snapshot {
				d := livedata.Load(ctx, livedata.Deps{Store: st, Beads: m})
				if d.LoadErr != nil {
					t.Fatal(d.LoadErr)
				}
				return livedata.BuildSnapshot(d, nil)
			}
			s := snapshot()
			row := livedata.Board(s, tc.Now).Workstreams[0]
			if len(s.Broken) > 0 || len(s.NeedsYou) > 0 || row.Kind == "needs" || row.Working != 0 || !strings.Contains(row.Text, tc.Want) {
				t.Fatalf("proposal lost custody: row=%+v broken=%+v asks=%+v", row, s.Broken, s.NeedsYou)
			}
			if err = m.SetLabel(ctx, "/p", "card", proposal, false); err != nil {
				t.Fatal(err)
			}
			accepted := snapshot()
			if len(accepted.NeedsYou) != 1 || len(accepted.Broken) > 0 {
				t.Fatalf("accepted decision not published: %+v", accepted)
			}
			card, err := m.Show(ctx, "/p", "card")
			if err != nil || card.Status == "closed" {
				t.Fatal("publication granted user approval", card, err)
			}
		})
	}
}
