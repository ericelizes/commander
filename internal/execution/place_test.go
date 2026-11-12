package execution

import (
	"testing"
	"time"

	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresMacSlotAdmission(t *testing.T) {
	for _, change := range []string{"ready", "ordinary", "wrong-place", "revoked", "disabled", "held"} {
		t.Run(change, func(t *testing.T) {
			st, pid, ctx := fixture(t)
			// Only one candidate: retain its normal knowledge project capability.
			if _, err := st.DB().Exec(`UPDATE bead_tasks SET execution_ready=FALSE WHERE project_id=? AND id='two'`, pid); err != nil {
				t.Fatal(err)
			}
			caps := `["mac"]`
			if change == "ordinary" {
				caps = `[]`
			}
			if _, err := st.DB().Exec(`UPDATE bead_tasks SET execution_capabilities=?::jsonb WHERE project_id=? AND id='one'`, caps, pid); err != nil {
				t.Fatal(err)
			}
			dev := place.NewDevice("mac:unit")
			if err := st.AddPlace(ctx, store.Place{Name: dev.Place, Kind: "mac", PublicKey: dev.Pub, PairedBy: "fixture", PairedAt: 1}); err != nil {
				t.Fatal(err)
			}
			if err := st.ConfigureExecutionSlot(ctx, "place:"+dev.Place, []string{"knowledge", "mac"}, change != "disabled"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "wrong-place":
				if _, err := st.DB().Exec(`UPDATE bead_tasks SET labels='["run-on:mac:other"]' WHERE project_id=? AND id='one'`, pid); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if _, err := st.RevokePlace(ctx, dev.Place, time.Now().Unix()); err != nil {
					t.Fatal(err)
				}
			case "held":
				if _, err := st.DB().Exec(`UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id='one'`, pid); err != nil {
					t.Fatal(err)
				}
			}
			w := Worker{Store: st, Place: dev.Place, Capacity: 2}
			if err := w.PassSlot(ctx, "place:"+dev.Place); err != nil {
				t.Fatal(err)
			}
			runs, err := st.ListPlaceRuns(ctx, dev.Place, true)
			if err != nil {
				t.Fatal(err)
			}
			if (len(runs) == 1) != (change == "ready") {
				t.Fatalf("%s: unexpected offers %+v", change, runs)
			}
			if change == "ready" {
				run, err := st.GetAgentRun(ctx, runs[0].RunID)
				if err != nil {
					t.Fatal(err)
				}
				if run.OpencodeSessionID == "" {
					t.Fatal("missing immutable session")
				}
				if err = w.PassSlot(ctx, "place:"+dev.Place); err != nil {
					t.Fatal(err)
				}
				if ok, err := st.EndPlaceRun(ctx, run.ID, store.PlaceStopped, "fixture stop", time.Now().Unix()); err != nil || !ok {
					t.Fatal(ok, err)
				}
				var result int64
				if err = st.DB().QueryRow(`SELECT result_run_id FROM bead_tasks WHERE project_id=? AND id='one'`, pid).Scan(&result); err != nil || result != run.ID {
					t.Fatal("missing inspection result", result, err)
				}
				if err = w.PassSlot(ctx, "place:"+dev.Place); err != nil {
					t.Fatal(err)
				}
				all, err := st.ListPlaceRuns(ctx, dev.Place, false)
				if err != nil || len(all) != 1 {
					t.Fatal("automatically redispatched terminal work", all, err)
				}
			}
		})
	}
}

func TestPostgresMacOfferCannotBorrowAnotherPlaceSlot(t *testing.T) {
	st, pid, ctx := fixture(t)
	if _, err := st.DB().Exec(`UPDATE bead_tasks SET execution_capabilities='["mac"]' WHERE project_id=?`, pid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mac:one", "mac:two"} {
		dev := place.NewDevice(name)
		if err := st.AddPlace(ctx, store.Place{Name: name, Kind: "mac", PublicKey: dev.Pub, PairedAt: 1, PairedBy: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ConfigureExecutionSlot(ctx, "place:mac:one", []string{"knowledge", "mac"}, true); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimNextExecution(ctx, "place:mac:one", 2, time.Now())
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	_, err = place.RecordOffer(ctx, st, place.OfferRequest{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskID: claim.BeadID, StepID: claim.BeadID, AgentName: "operator-wrong-place", Place: "mac:two", Brief: claim.Description})
	if err == nil {
		t.Fatal("another place borrowed slot custody")
	}
	var n int
	if err = st.DB().QueryRow(`SELECT count(*) FROM agent_runs`).Scan(&n); err != nil || n != 0 {
		t.Fatal("failed offer retained partial run", n, err)
	}
}
