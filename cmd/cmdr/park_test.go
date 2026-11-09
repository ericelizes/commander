package main

import (
	"slices"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ownercompat"
)

func parkFixture(t *testing.T, labels ...string) *beads.MemClient {
	t.Helper()
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "obj-1", Status: "open", Labels: append([]string{"kind:objective", "gate"}, labels...)})
	setupObjectiveProject(t, m)
	return m
}

func TestParkAndUnparkThroughSetObjective(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	m := parkFixture(t, ownercompat.LegacyNeedsLabel, "approver:user")
	stdout, stderr, readOutput := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "obj-1", "--park", "--until", "2h", "--reason", "CI red"}
	if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
		_, e := readOutput()
		t.Fatalf("park: %s", e)
	}
	b, _ := m.Show(nil, "", "obj-1")
	if b.Status != beads.StatusDeferred || beads.ParkedUntil(b.Labels) == 0 || !slices.Contains(b.Labels, ownercompat.LegacyNeedsLabel) {
		t.Fatalf("parked: %+v", b)
	}
	if code := runInternalSetObjective(args[:5], stdout, stderr); code != 0 {
		t.Fatal("park again")
	}
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--unpark"}, stdout, stderr); code != 0 {
		t.Fatal("unpark")
	}
	if b, _ = m.Show(nil, "", "obj-1"); b.Status != "open" || beads.ParkedUntil(b.Labels) != 0 || !slices.Contains(b.Labels, ownercompat.LegacyNeedsLabel) {
		t.Fatalf("unparked: %+v", b)
	}
}

func TestParkOperatorRefused(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-x")
	m := parkFixture(t)
	stdout, stderr, _ := withTempOutputFiles(t)
	for _, flag := range []string{"--park", "--unpark"} {
		if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", flag}, stdout, stderr); code == 0 {
			t.Fatalf("operator allowed %s", flag)
		}
	}
	if b, _ := m.Show(nil, "", "obj-1"); b.Status != "open" {
		t.Fatalf("status %s", b.Status)
	}
}

func TestParkClosedRefusedThroughSetObjective(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "obj-1", Status: "closed", Labels: []string{"kind:objective"}})
	setupObjectiveProject(t, m)
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--park"}, stdout, stderr); code == 0 {
		t.Fatal("parked a closed bead")
	}
}
