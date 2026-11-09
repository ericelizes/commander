package main

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"strings"
	"testing"
)

func TestGatePublishCommandPreservesExistingID(t *testing.T) {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "sample-stage", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "sample-action", Labels: []string{"kind:objective"}})
	m.SetParent(context.Background(), "/p", "sample-action", "sample-stage")
	m.Seed(beads.Bead{ID: "sample-card", Labels: []string{"kind:gate", "gate-of:sample-action", "approver:user"}})
	setupObjectiveProject(t, m)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalGate([]string{"publish", "--project", "manifest", "--bead", "sample-card", "--parent", "sample-stage"}, stdout, stderr)
	out, errOut := read()
	if code != 0 || !strings.Contains(out, `"gate_bead":"sample-card"`) {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	ds, err := m.Details(context.Background(), "/p", []string{"sample-card"})
	if err != nil || ds["sample-card"].Parent != "sample-stage" {
		t.Fatalf("details=%+v err=%v", ds, err)
	}
}
