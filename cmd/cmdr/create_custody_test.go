package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// A new plain step that nothing holds carries its Manager's "Manager to start"
// receipt from the create on, so the board never reads it State missing once the
// creating turn ends. One queued behind open work, an Operator's own sub-task
// and a nested workstream get none.
func TestCreateGivesAReadyStepItsManagersCustody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		want  bool
	}{
		{"ready step", nil, true},
		{"behind open work", []string{"--deps", "blocked-by:w.open"}, false},
		{"behind finished work", []string{"--deps", "blocked-by:w.done"}, true},
		{"operational", []string{"--operational"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &beads.MemClient{}
			m.Seed(beads.Bead{ID: "w", Title: "Notify", Status: "open", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "w.open", Title: "Draft", Status: "open", Labels: []string{"kind:objective"}, ParentID: "w"})
			m.Seed(beads.Bead{ID: "w.done", Title: "Plan", Status: "closed", Labels: []string{"kind:objective"}, ParentID: "w"})
			setupObjectiveProject(t, m)
			t.Setenv("COMMANDER_ROLE", "manager-w")
			stdout, stderr, read := withTempOutputFiles(t)
			args := append([]string{"--project", "manifest", "--title", "Send the notice", "--parent", "w"}, tc.extra...)
			if code := runInternalCreateObjective(args, stdout, stderr); code != 0 {
				_, e := read()
				t.Fatalf("exit %d: %s", code, e)
			}
			out, _ := read()
			var res internalCreateObjectiveResult
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			b, _ := m.Show(context.Background(), "", res.BeadID)
			got := false
			for _, w := range beads.WaitsOf(b.Labels) {
				if w.Ref == beads.ManagerStartWait {
					got = true
				}
			}
			if got != tc.want || (len(res.Custody) > 0) != tc.want {
				t.Fatalf("receipt on %s = %v (custody %v), want %v; labels %v", res.BeadID, got, res.Custody, tc.want, b.Labels)
			}
		})
	}
}

// failStartReceipt refuses only the "Manager to start" receipt write.
type failStartReceipt struct{ *beads.MemClient }

func (f failStartReceipt) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	if strings.Contains(label, beads.EscapeLabel(beads.ManagerStartWait)) {
		return errors.New("busy")
	}
	return f.MemClient.SetLabel(ctx, dir, id, label, on)
}

// The bead exists once the create landed: a receipt that could not be written after it (the
// named blocker turned out finished) is a warning naming how to hold the step, never a failed
// create (repeating it would make a second bead). A step naming no holder carries the receipt in
// the create itself, so there is no later write to fail.
func TestCreateWhoseReceiptFailsStillReportsTheBead(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "w", Title: "Notify", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.done", Title: "Plan", Status: "closed", Labels: []string{"kind:objective"}, ParentID: "w"})
	setupObjectiveProject(t, failStartReceipt{m})
	t.Setenv("COMMANDER_ROLE", "manager-w")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "Send the notice", "--parent", "w", "--deps", "blocked-by:w.done"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("exit %d: %s", code, e)
	}
	out, errOut := read()
	var res internalCreateObjectiveResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res.BeadID == "" || len(res.Custody) > 0 {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(out+errOut, "hold was not written") {
		t.Fatalf("no warning about the missing hold: stdout %s stderr %s", out, errOut)
	}
}

// clearSettleMarkers keeps the start receipt through a Manager-only edit and
// drops it once a real successor (a wait, a card, an Operator) is recorded.
func TestClearSettleMarkersKeepsTheStartReceiptUntilASuccessor(t *testing.T) {
	ctx := context.Background()
	receipt := beads.Wait{Kind: beads.WaitUntil, Ref: beads.ManagerStartWait, Since: time.Now(), Until: time.Now().Add(time.Hour)}.Label()
	for _, accepted := range []bool{false, true} {
		m := &beads.MemClient{}
		m.Seed(beads.Bead{ID: "w.2", Status: "open", Labels: []string{"kind:objective", receipt}})
		if err := clearSettleMarkers(ctx, m, "", "w.2", accepted); err != nil {
			t.Fatal(err)
		}
		b, _ := m.Show(ctx, "", "w.2")
		if kept := slices.Contains(b.Labels, receipt); kept == accepted {
			t.Fatalf("successor recorded %v: receipt kept %v", accepted, kept)
		}
	}
}
