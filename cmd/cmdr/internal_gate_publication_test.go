package main

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestGatePublicationOwnership(t *testing.T) {
	for _, role := range []string{"", "commander", "manager-sample", "manager-foreign", "op-sample"} {
		t.Run(role, func(t *testing.T) {
			m := &beads.MemClient{EdgeParentsOnly: true}
			m.Seed(beads.Bead{ID: "sample", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "action", ParentID: "sample", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "card", Labels: []string{"kind:gate", "gate-of:action", "approver:user"}})
			m.SetParent(context.Background(), "/p", "action", "sample")
			old := newBeadsClientFn
			newBeadsClientFn = func() beads.Client { return m }
			t.Cleanup(func() { newBeadsClientFn = old })
			t.Setenv("COMMANDER_ROLE", role)
			err := gatePublish(context.Background(), "/p", "card", "sample", false)
			allowed := role == "" || role == "commander" || role == "manager-sample"
			if (err == nil) != allowed {
				t.Fatalf("role %q err=%v", role, err)
			}
			ds, _ := m.Details(context.Background(), "/p", []string{"card"})
			if (ds["card"].Parent == "sample") != allowed {
				t.Fatal("incorrect write")
			}
		})
	}
}
