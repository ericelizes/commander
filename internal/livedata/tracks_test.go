package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func trackFleet() Data {
	d := fleet()
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.5", ParentBeadID: "a", Title: "Audit", ProjectID: 1, Track: true, State: beads.StateInProgress},
		ObjectiveRow{BeadID: "a.5.1", ParentBeadID: "a.5", Title: "Audit spec approved", ProjectID: 1, Gate: true, Approvers: []string{"user"}, State: beads.StateNotStarted, GateNote: "The audit spec."},
		ObjectiveRow{BeadID: "a.5.2", ParentBeadID: "a.5", Title: "Pick scope", ProjectID: 1, State: beads.StateNeedsUser, Ask: "Pick scope",
			Decision: &store.Decision{Question: "Pick scope", Context: "Two scopes."}},
	)
	return d
}

// A track is a step of its workstream, never a workstream of its own.
func TestTrack_IsNotAWorkstream(t *testing.T) {
	d := trackFleet()
	for _, ws := range Workstreams(d) {
		if ws.Root.Track {
			t.Fatalf("track %s enumerated as a workstream", ws.Root.BeadID)
		}
	}
	// A track whose workstream is gone (closed, or not loaded) is dropped, not promoted.
	d.Objectives = append(d.Objectives, ObjectiveRow{BeadID: "z.1", ParentBeadID: "z", Title: "Orphan track", ProjectID: 1, Track: true})
	if got := len(Workstreams(d)); got != 2 {
		t.Fatalf("workstreams = %d, want 2 (an orphan track must not become one)", got)
	}
	// ...while an orphan plain step (parent not listed) still becomes a root.
	d.Objectives = append(d.Objectives, ObjectiveRow{BeadID: "y.1", ParentBeadID: "y", Title: "Orphan step", ProjectID: 1})
	if got := len(Workstreams(d)); got != 3 {
		t.Fatalf("workstreams = %d, want 3", got)
	}
}

func TestSnapshot_TrackFlagAndWorkstream(t *testing.T) {
	snap := BuildSnapshot(trackFleet(), nil)
	var ws SnapWorkstream
	for _, w := range snap.Workstreams {
		if w.BeadID == "a" {
			ws = w
		}
	}
	var track *SnapStep
	for i := range ws.Steps {
		if ws.Steps[i].BeadID == "a.5" {
			track = &ws.Steps[i]
		}
		if ws.Steps[i].BeadID != "a.5" && ws.Steps[i].Track {
			t.Fatalf("%s flagged as a track", ws.Steps[i].BeadID)
		}
	}
	if track == nil || !track.Track || track.Workstream != "a" {
		t.Fatalf("track step = %+v", track)
	}
	if len(track.Children) != 2 {
		t.Fatalf("a track carries its own gate chain as children: %+v", track.Children)
	}
	if track.NoGates {
		t.Fatalf("a track with a gate child has gates")
	}
}

func TestSnapshot_TrackWithoutGatesIsFlagged(t *testing.T) {
	d := fleet()
	d.Objectives = append(d.Objectives, ObjectiveRow{BeadID: "a.6", ParentBeadID: "a", Title: "Bare", ProjectID: 1, Track: true, State: beads.StateNotStarted})
	for _, w := range BuildSnapshot(d, nil).Workstreams {
		for _, s := range w.Steps {
			if s.BeadID == "a.6" && !s.NoGates {
				t.Fatalf("a track with no gate chain must read no_gates")
			}
		}
	}
}

// NEEDS YOU names the track an item sits in; an item outside any track has none.
func TestNeedsYou_NamesTheTrack(t *testing.T) {
	got := NeedsYouList(trackFleet())
	byID := map[string]NeedsYou{}
	for _, n := range got {
		byID[n.BeadID] = n
	}
	for _, id := range []string{"a.5.1", "a.5.2"} {
		n, ok := byID[id]
		if !ok || n.Track != "Audit" || n.TrackID != "a.5" || n.Workstream != "WS A" {
			t.Errorf("%s = %+v", id, n)
		}
	}
	if n := byID["a.1.2"]; n.BeadID == "" || n.Track != "" {
		t.Errorf("a step outside a track: %+v", n)
	}
}
