package main

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestUserCloseConfirmsTheConfirmDoneStepOnlyWhenTheRestIsDone(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{Prefix: "p", EdgeParentsOnly: true}
	mk := func(title, parent string, labels ...string) string {
		id, err := m.Create(ctx, "", title, beads.CreateOptions{Parent: parent, Labels: append([]string{"kind:objective"}, labels...)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ws := mk("Fast Unblocks", "", beads.WorkstreamLabel)
	work := mk("Grant access", ws)
	gate := mk("Confirm done", ws, beads.GateLabel, "approver:user", beads.ClosingGateLabel)

	if err := closeConfirmSteps(ctx, m, "", ws); err != nil {
		t.Fatal(err)
	}
	if b, _ := m.Show(ctx, "", gate); b.Status == "closed" {
		t.Fatal("confirmed while other work was open")
	}
	if err := m.Close(ctx, "", work); err != nil {
		t.Fatal(err)
	}
	if err := closeConfirmSteps(ctx, m, "", ws); err != nil {
		t.Fatal(err)
	}
	if b, _ := m.Show(ctx, "", gate); b.Status != "closed" {
		t.Fatal("the user's close did not confirm the Confirm done step")
	}
}
