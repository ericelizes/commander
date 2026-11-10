package livedata

import (
	"strings"
	"testing"
)

func TestCapacityFailureReplacesPickupAtEveryScope(t *testing.T) {
	s := Snapshot{
		Agents: map[string]SnapAgent{"manager-w": caseIdle},
		Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "Work", ManagerRole: "manager-w", Status: "open", Steps: []SnapStep{
			st("a", asTrack, kids(st("a.1"))), st("b", asTrack, kids(st("b.1"))),
		}}},
		Unread:          map[string]int{"manager-w": 3},
		ManagerCapacity: map[string]string{"manager-w": "no account has usage left"},
	}
	row := Board(s, caseNow).Workstreams[0]
	expectLine(t, row, "stalled", "Manager out of usage")
	for _, tr := range row.Tracks {
		if tr.Text != "Manager out of usage" || !strings.Contains(tr.Why, "no account has usage left") || tr.Since != 0 {
			t.Fatalf("track: %+v", tr)
		}
	}
	delete(s.ManagerCapacity, "manager-w")
	// Clearing exhausted capacity restores the underlying stalled handoff;
	// unread mail alone cannot establish that recovery has started.
	recovered := Board(s, caseNow).Workstreams[0]
	expectLine(t, recovered, "stalled", "Stalled")
	for _, tr := range recovered.Tracks {
		if tr.Kind != "stalled" || tr.Text != "Stalled" || !strings.Contains(tr.Why, "Automation failure") {
			t.Fatalf("capacity cleared but failed track recolored: %+v", tr)
		}
	}
}

func TestCapacityFailureDoesNotInventAWaitOrHideLiveWork(t *testing.T) {
	s := Snapshot{ManagerCapacity: map[string]string{"manager-w": "no account has usage left"}}
	b := board{s: s}
	for _, kind := range []string{"needs", "waiting", "working"} {
		got := b.capacityLine("manager-w", line{kind: kind, text: "existing", since: 123})
		if got.kind != kind || got.text != "existing" || got.since != 123 || !strings.Contains(got.why, "no account") {
			t.Fatalf("%+v", got)
		}
	}
}

func TestCapacityFailureKeepsNestedOwnerThroughCollapsedTracks(t *testing.T) {
	nested := st("a", asTrack, kids(st("a.child", asTrack, kids(st("a.leaf")))))
	nested.ManagerRole = "manager-a"
	s := Snapshot{
		Workstreams:     []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "Work", ManagerRole: "manager-w", Status: "open", Steps: []SnapStep{nested, st("b", asTrack, kids(st("b.leaf")))}}},
		Agents:          map[string]SnapAgent{"manager-w": caseIdle, "manager-a": {State: "working"}},
		Unread:          map[string]int{"manager-a": 2},
		ManagerCapacity: map[string]string{"manager-a": "no account has usage left"},
	}
	row := Board(s, caseNow).Workstreams[0]
	if len(row.Tracks) != 2 || row.Tracks[0].Text != "Manager out of usage" || row.Tracks[0].ManagerRole != "manager-a" {
		t.Fatalf("tracks %+v", row.Tracks)
	}
	if row.Tracks[1].Text == "Manager out of usage" {
		t.Fatal("capacity failure leaked into independent sibling")
	}
}

// Legacy child tracks inherit the nested owner; their activity must never come
// from the root Manager passed into the first level.
func TestNestedCapacityOwnerActivity(t *testing.T) {
	for _, root := range []string{"absent", "idle", "stuck", "working"} {
		for _, nested := range []string{"idle", "stuck"} {
			t.Run(root+"/"+nested, func(t *testing.T) {
				owner := st("a", asTrack, kids(st("a.1", asTrack), st("a.2", asTrack)))
				owner.ManagerRole = "manager-a"
				s := Snapshot{
					Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "Work", ManagerRole: "manager-w", Status: "open", Steps: []SnapStep{owner, st("b", asTrack)}}},
					Agents:      map[string]SnapAgent{"manager-a": {State: nested}},
				}
				if root != "absent" {
					s.Agents["manager-w"] = SnapAgent{State: root}
				}
				row := Board(s, caseNow).Workstreams[0]
				want := "Not started: Manager to plan"
				if nested == "stuck" {
					want = "Stuck"
				}
				for _, child := range row.Tracks[0].Children {
					if child.Text != want {
						t.Fatalf("root=%s nested=%s: child %+v, want %q", root, nested, child, want)
					}
				}
				if len(row.Tracks[0].Children) != 2 {
					t.Fatalf("missing inherited children: %+v", row.Tracks)
				}
			})
		}
	}
}
