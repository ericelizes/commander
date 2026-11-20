package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

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

// Nov 20 (4.1): the Commander paused a step with --hold and no agent ran. A
// step is never paused: --hold and --park are refused for everyone, and the
// refusal says what to add instead.
func TestParkAndHoldAreRefused(t *testing.T) {
	for _, role := range []string{"commander", "manager-ws", ""} {
		t.Setenv("COMMANDER_ROLE", role)
		m := parkFixture(t, ownercompat.LegacyNeedsLabel, "approver:user")
		stdout, stderr, readOutput := withTempOutputFiles(t)
		for _, flags := range [][]string{{"--park"}, {"--park", "--until", "2h", "--reason", "CI red"}, {"--hold", "no Modal access"}} {
			args := append([]string{"--project", "manifest", "--bead", "obj-1"}, flags...)
			if code := runInternalSetObjective(args, stdout, stderr); code == 0 {
				t.Fatalf("%q allowed %v", role, flags)
			}
			if _, e := readOutput(); !strings.Contains(e, "never paused") || !strings.Contains(e, "blocked by it") {
				t.Fatalf("%v: refusal does not say what to do instead: %s", flags, e)
			}
		}
		b, _ := m.Show(context.Background(), "", "obj-1")
		if b.Status != "open" || slices.Contains(b.Labels, beads.HoldLabel) || slices.Contains(b.Labels, beads.ParkedLabel) {
			t.Fatalf("%q: bead changed: %+v", role, b)
		}
	}
}

// --unpark still brings back a park made before --park went, with its card.
func TestUnparkBringsBackAnOldPark(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	m := parkFixture(t, ownercompat.LegacyNeedsLabel, "approver:user")
	if err := beads.Park(context.Background(), m, "", "obj-1", "manager-ws", "CI red", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--unpark"}, stdout, stderr); code != 0 {
		t.Fatal("unpark")
	}
	if b, _ := m.Show(context.Background(), "", "obj-1"); b.Status != "open" || beads.ParkedUntil(b.Labels) != 0 || !slices.Contains(b.Labels, ownercompat.LegacyNeedsLabel) {
		t.Fatalf("unparked: %+v", b)
	}
}

func TestParkOperatorRefused(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-x")
	m := parkFixture(t)
	stdout, stderr, _ := withTempOutputFiles(t)
	for _, flag := range []string{"--park", "--unpark", "--no-hold"} {
		if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", flag}, stdout, stderr); code == 0 {
			t.Fatalf("operator allowed %s", flag)
		}
	}
	if b, _ := m.Show(nil, "", "obj-1"); b.Status != "open" {
		t.Fatalf("status %s", b.Status)
	}
}
