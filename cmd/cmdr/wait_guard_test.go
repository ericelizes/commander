package main

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestCheckWaitsAreReal(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	ctx := context.Background()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, _ := st.GetProjectByName(ctx, "manifest")

	user := []beads.Wait{{Kind: beads.WaitUser}}
	if err := checkWaitsAreReal(ctx, "manifest", "ms-1.2", user); err == nil || !strings.Contains(err.Error(), "no question") {
		t.Fatalf("a user wait with no question must be refused, got %v", err)
	}
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.2.3", Ask: "Approve?"}); err != nil {
		t.Fatal(err)
	}
	if err := checkWaitsAreReal(ctx, "manifest", "ms-1.2", user); err != nil {
		t.Fatalf("a question under the step makes the user wait real: %v", err)
	}
	if err := checkWaitsAreReal(ctx, "manifest", "ms-1.20", user); err == nil {
		t.Fatal("ms-1.2.3 is not under ms-1.20")
	}

	op := []beads.Wait{{Kind: beads.WaitOperator, Ref: "operator-op-run-ms-1-2-1"}}
	if err := checkWaitsAreReal(ctx, "manifest", "ms-1.2", op); err == nil || !strings.Contains(err.Error(), "no Operator") {
		t.Fatalf("a wait on an Operator that is not running must be refused, got %v", err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: p.ID, TaskBeadID: "ms-1.2.1", AgentName: "operator-op-run-ms-1.2.1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := checkWaitsAreReal(ctx, "manifest", "ms-1.2", op); err != nil {
		t.Fatalf("a running Operator is a real wait: %v", err)
	}
	if err := checkWaitsAreReal(ctx, "manifest", "ms-1.2", []beads.Wait{{Kind: beads.WaitCI, PR: 7}}); err != nil {
		t.Fatalf("other waits are not checked: %v", err)
	}
}
