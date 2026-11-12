package livedata

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func patchFixture() Snapshot {
	s := Snapshot{
		Agents: map[string]SnapAgent{"manager-w": {State: "idle"}},
		Workstreams: []SnapWorkstream{{BeadID: "w", Project: "demo", Title: "Area", Status: "open", ManagerRole: "manager-w", Steps: []SnapStep{
			{BeadID: "w.1", Title: "Build", Status: "in_progress", State: beads.StateInProgress, Waiting: "CI #7"},
			{BeadID: "w.2", Title: "Ship", Status: "open", State: beads.StateNotStarted, BlockedBy: []string{"w.1"}},
		}}},
	}
	d := Board(s, 1000)
	s.Display = &d
	return s
}

// A write the door answered is on the board at once: closing w.1 with w.2 held by a wait reads
// as such, before any load shows it; the patched steps are pending.
func TestPatchedBoardGetsTheFinishingPass(t *testing.T) {
	s := patchFixture()
	called := false
	p := Patched(s, map[string]StepPatch{"w.1": {Status: "closed"}}, 1000, func(_ *Snapshot, d *Display) {
		called = true
		for i := range d.Workstreams {
			d.Workstreams[i].Text = "written line"
		}
	})
	if !called || p.Display == nil || len(p.Display.Workstreams) == 0 || p.Display.Workstreams[0].Text != "written line" {
		t.Fatal("a board rebuilt after a write skipped the finishing pass")
	}
}

func TestPatchedShowsTheAnsweredWrite(t *testing.T) {
	s := patchFixture()
	at := "waiting:900:until:2000:Manager_20dispatch"
	p := Patched(s, map[string]StepPatch{
		"w.1": {Status: "closed"},
		"w.2": {Status: "open", Labels: []string{at}},
	}, 1000, nil)
	st := p.Workstreams[0].Steps
	if st[0].Status != "closed" || st[0].State != beads.StateDone || st[1].Broken || st[1].Waiting == "" {
		t.Fatalf("steps %+v", st)
	}
	if strings.Contains(p.Display.Workstreams[0].Text, "State missing") || len(p.Broken) != 0 {
		t.Fatalf("row %q broken %v", p.Display.Workstreams[0].Text, p.Broken)
	}
	if strings.Join(p.Pending, ",") != "w.1,w.2" {
		t.Fatalf("pending %v", p.Pending)
	}
	if s.Workstreams[0].Steps[0].Status != "in_progress" {
		t.Fatal("Patched changed its input")
	}
	// The same close with nothing on w.2 is what the board would show as State missing: the door
	// refuses that write, and the patch shows it honestly when one is let through.
	q := Patched(s, map[string]StepPatch{"w.1": {Status: "closed"}}, 1000, nil)
	if !q.Workstreams[0].Steps[1].Broken {
		t.Fatalf("an unheld w.2 must read Broken: %+v", q.Workstreams[0].Steps[1])
	}
}
