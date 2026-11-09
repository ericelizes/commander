package store

import (
	"context"
	"testing"
	"time"
)

func TestGateRoundTrip(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	g := Gate{ProjectID: 1, BeadID: "p-1.2", Name: "unit", Check: "go test ./...", Verifier: VerifierAuto}
	if err := st.UpsertGate(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertGate(ctx, Gate{ProjectID: 1, BeadID: "p-1.2", Name: "review", Check: "no TODOs", Verifier: "reviewer:reviewer-a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGateResult(ctx, 1, "p-1.2", "unit", GatePassed, "ok", "auto", time.Unix(50, 0)); err != nil {
		t.Fatal(err)
	}
	gs, _ := st.ListGates(ctx, 1, "p-1.2")
	if len(gs) != 2 || gs[0].Name != "unit" || gs[0].Status != GatePassed || gs[0].Evidence != "ok" || gs[0].At != 50 || gs[1].Status != GateOpen {
		t.Fatalf("gates = %+v", gs)
	}
	if un := UnpassedGates(gs); len(un) != 1 || un[0].Name != "review" {
		t.Fatalf("unpassed = %+v", un)
	}
	// Redefining a gate reopens it.
	g.Check = "go test -race ./..."
	if err := st.UpsertGate(ctx, g); err != nil {
		t.Fatal(err)
	}
	gs, _ = st.ListGates(ctx, 1, "p-1.2")
	if gs[0].Status != GateOpen || gs[0].Evidence != "" || gs[0].Check != g.Check {
		t.Fatalf("redefined = %+v", gs[0])
	}
	if by, _ := st.ListGatesByBead(ctx, "p-1.2"); len(by) != 2 {
		t.Fatalf("by bead = %+v", by)
	}
	if err := st.DeleteGate(ctx, 1, "p-1.2", "unit"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGate(ctx, 1, "p-1.2", "unit"); err == nil {
		t.Error("deleting a missing gate must fail")
	}
	if err := st.SetGateResult(ctx, 1, "p-1.2", "nope", GatePassed, "x", "user", time.Now()); err == nil {
		t.Error("result on a missing gate must fail")
	}
}

func TestPassedWithoutEvidenceStillBlocks(t *testing.T) {
	if un := UnpassedGates([]Gate{{Name: "a", Status: GatePassed}}); len(un) != 1 {
		t.Fatalf("unpassed = %+v", un)
	}
}

func TestValidateGate(t *testing.T) {
	ok := Gate{Name: "ci_green-1", Check: "gh pr checks", Verifier: "reviewer:Reviewer-A"}
	if err := ValidateGate(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Gate{
		{Name: "has space", Check: "x", Verifier: "auto"},
		{Name: "a", Check: "  ", Verifier: "auto"},
		{Name: "a", Check: "x", Verifier: "bob"},
		{Name: "a", Check: "x", Verifier: "reviewer:"},
		{Name: "a", Check: "x", Verifier: "reviewer:a b"},
	} {
		if ValidateGate(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
