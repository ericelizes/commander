package main

import (
	"github.com/example-user/commander/internal/store"
	"testing"
)

func TestOrdinaryAccessActionIsNotApproval(t *testing.T) {
	for _, label := range []string{"Continue", "Approve", "Yes", "Done"} {
		d := &store.Decision{Question: "Connect the sample account", Options: []store.DecisionOption{{Label: label}}}
		if approvalAsk(d) {
			t.Fatalf("ordinary action %q forced into approval gate", label)
		}
		d.Options[0].Approves = true
		if !approvalAsk(d) {
			t.Fatal("explicit approval authority lost")
		}
	}
}

func TestAskDoorOrdinaryAccessActionOnApprovedWork(t *testing.T) {
	askBoard(t)
	// An approval already covers the work. Access still requires a person's
	// action; a generic Continue choice is not another authorization gate.
	asked(t, ask("ms-1.4", `{"question":"Connect the sample account", "options":[{"label":"Continue"}]}`))
}
