package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/store"
)

func nestedRow(steps ...SnapStep) BoardRow {
	s := Snapshot{Workstreams: []SnapWorkstream{{N: 3, Project: "p", BeadID: "w", Title: "Root", ManagerRole: "manager-w", Steps: steps}},
		Agents: map[string]SnapAgent{"manager-w": caseIdle}}
	return Board(s, caseNow).Workstreams[0]
}

func nested(id, name string, children ...SnapStep) SnapStep {
	return SnapStep{BeadID: id, Title: name, Status: "open", Track: true, Children: children}
}

func leafStep(id string) SnapStep { return SnapStep{BeadID: id, Title: id, Status: "open"} }

func closedNested(id string) SnapStep {
	return SnapStep{BeadID: id, Title: id, Status: "closed", Track: true}
}

// Numbers run as deep as the tree, and only open siblings take one.
func TestNested_NumbersFollowTheTree(t *testing.T) {
	r := nestedRow(
		closedNested("w.0"),
		nested("w.1", "One", nested("w.1.1", "One one", leafStep("a")), nested("w.1.2", "One two", leafStep("b"))),
		nested("w.2", "Two", leafStep("c")),
	)
	var got []string
	for _, x := range r.AllTracks() {
		got = append(got, x.Letter+" "+x.Title)
	}
	want := []string{".1 One", ".1.1 One one", ".1.2 One two", ".2 Two"}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows %v, want %v", got, want)
		}
	}
	if r.Scope != "" || r.Title != "Root" {
		t.Fatalf("a parent with two children stays itself: %q %q", r.Title, r.Scope)
	}
}

// One open child stands in for its parent, down a chain, and takes its number.
func TestNested_SoleChildStandsIn(t *testing.T) {
	r := nestedRow(closedNested("w.0"), nested("w.1", "Middle", nested("w.1.1", "Deep", nested("w.1.1.1", "A", leafStep("a")), nested("w.1.1.2", "B", leafStep("b")))))
	if r.Title != "Deep" || r.Scope != "w.1.1" {
		t.Fatalf("row %q scope %q", r.Title, r.Scope)
	}
	if len(r.Tracks) != 2 || r.Tracks[0].Letter != ".1" || r.Tracks[1].Letter != ".2" {
		t.Fatalf("children %+v", r.Tracks)
	}

	// Inside a parent too: the child keeps its parent's number and shows its own title.
	r = nestedRow(nested("w.1", "Middle", nested("w.1.1", "Only", leafStep("a"))), nested("w.2", "Other", leafStep("b")))
	if r.Tracks[0].Title != "Only" || r.Tracks[0].BeadID != "w.1.1" || r.Tracks[0].Letter != ".1" || r.Tracks[1].Letter != ".2" {
		t.Fatalf("tracks %+v", r.Tracks)
	}
}

// A row never hides open steps: one child beside open work stays a child.
func TestNested_OpenStepsKeepTheParent(t *testing.T) {
	r := nestedRow(leafStep("own"), nested("w.1", "Child", leafStep("a")))
	if r.Scope != "" || r.Title != "Root" || len(r.Tracks) != 1 {
		t.Fatalf("row %q scope %q tracks %+v", r.Title, r.Scope, r.Tracks)
	}
}

// A card deep in the tree reaches the row and is labelled with its full number.
func TestNested_DeepCardRollsUp(t *testing.T) {
	ask := SnapStep{BeadID: "a", Title: "Pick one", Status: "open", State: "needs-user", Ask: "Pick one"}
	s := Snapshot{Workstreams: []SnapWorkstream{{N: 3, Project: "p", BeadID: "w", Title: "Root", ManagerRole: "manager-w", Steps: []SnapStep{
		nested("w.1", "One", nested("w.1.1", "Deep", ask), nested("w.1.2", "Other", leafStep("b"))),
		nested("w.2", "Two", leafStep("c")),
	}}},
		Agents:   map[string]SnapAgent{"manager-w": caseIdle},
		NeedsYou: []NeedsYou{{Project: "p", BeadID: "a", Title: "Pick one", Workstream: "Root", Track: "Deep", TrackID: "w.1.1", Rank: 3}}}
	d := Board(s, caseNow)
	r := d.Workstreams[0]
	if r.Kind != "needs" || r.Tracks[0].Kind != "needs" || r.Tracks[0].Children[0].Kind != "needs" {
		t.Fatalf("row %s, parent %s, child %s", r.Kind, r.Tracks[0].Kind, r.Tracks[0].Children[0].Kind)
	}
	if r.Tracks[1].Kind == "needs" {
		t.Fatalf("the sibling reads %s", r.Tracks[1].Kind)
	}
	if d.Asks[0].TrackLetter != ".1.1" {
		t.Fatalf("ask letter %q", d.Asks[0].TrackLetter)
	}
}

// Every nested workstream has its own diagram, whatever its depth.
func TestNested_DiagramAtEveryDepth(t *testing.T) {
	r := nestedRow(nested("w.1", "One", nested("w.1.1", "Deep", leafStep("a")), nested("w.1.2", "Other", leafStep("b"))), nested("w.2", "Two", leafStep("c")))
	for _, id := range []string{"w.1", "w.1.1", "w.1.2", "w.2"} {
		if r.Diagram.Sub(id) == nil {
			t.Fatalf("no diagram for %s", id)
		}
	}
}

// Siblings read by priority number; the unranked follow in the order added.
func TestNestObjectives_RanksSiblings(t *testing.T) {
	d := Data{
		Projects: []store.Project{{ID: 1, Name: "p"}},
		Objectives: []ObjectiveRow{
			{BeadID: "w", ProjectID: 1, Title: "W"},
			{BeadID: "w.1", ParentBeadID: "w", ProjectID: 1, Title: "first added"},
			{BeadID: "w.2", ParentBeadID: "w", ProjectID: 1, Title: "second added"},
			{BeadID: "w.3", ParentBeadID: "w", ProjectID: 1, Title: "third added"},
		},
		BeadRanks: map[store.WorkstreamKey]int{{ProjectID: 1, BeadID: "w.3"}: 1, {ProjectID: 1, BeadID: "w.1"}: 2},
	}
	ws := Workstreams(d)
	var got []string
	for _, s := range ws[0].Steps {
		got = append(got, s.Row.BeadID)
	}
	if len(got) != 3 || got[0] != "w.3" || got[1] != "w.1" || got[2] != "w.2" {
		t.Fatalf("order %v", got)
	}
}
