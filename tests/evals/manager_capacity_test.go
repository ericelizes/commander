package evals

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func TestManagerCapacityScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/manager-capacity.json")
	if err != nil {
		t.Fatal(err)
	}
	var s livedata.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	row := livedata.Board(s, s.GeneratedAt.Unix()).Workstreams[0]
	if row.Text != "Manager out of usage" || len(row.Tracks) != 2 {
		t.Fatalf("row %+v", row)
	}
	for _, tr := range row.Tracks {
		if tr.Text != "Manager out of usage" || !strings.Contains(tr.Why, "no account has usage left") || tr.Since != 0 {
			t.Fatalf("track %+v", tr)
		}
	}
	s.ManagerCapacity = nil
	row = livedata.Board(s, s.GeneratedAt.Unix()).Workstreams[0]
	if row.Kind != "stalled" || row.Text != "Stalled" {
		t.Fatalf("recovered row %+v", row)
	}
	for _, tr := range row.Tracks {
		if tr.Kind != "stalled" || !strings.Contains(tr.Why, "Automation failure") {
			t.Fatalf("unread mail recolored recovery: %+v", tr)
		}
	}
}

func TestNestedCapacityOwnerScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/nested-capacity-owner.json")
	if err != nil {
		t.Fatal(err)
	}
	var s livedata.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		row := livedata.Board(s, s.GeneratedAt.Unix()).Workstreams[0]
		if len(row.Tracks) != 2 || len(row.Tracks[0].Children) != 2 {
			t.Fatalf("tracks: %+v", row.Tracks)
		}
		for _, child := range row.Tracks[0].Children {
			if child.Text != want {
				t.Fatalf("child: %+v want %q", child, want)
			}
		}
	}
	check("Not started: Manager to plan")
	s.Agents["manager-sample-root"] = livedata.SnapAgent{State: "stuck"}
	check("Not started: Manager to plan")
	s.Agents["manager-sample-nested"] = livedata.SnapAgent{State: "stuck"}
	check("Stuck")
	s.ManagerCapacity = map[string]string{"manager-sample-nested": "no account has usage left"}
	check("Manager out of usage")
	s.ManagerCapacity = nil
	s.Agents["manager-sample-nested"] = livedata.SnapAgent{State: "idle"}
	check("Not started: Manager to plan")
}

func TestMissingCapacityClearScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/missing-capacity-clear.json")
	if err != nil {
		t.Fatal(err)
	}
	var s livedata.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		r := livedata.Board(s, s.GeneratedAt.Unix()).Workstreams[0]
		if r.Kind != "stalled" || r.Text != want || r.Flag != 0 || !strings.Contains(r.Why, "Owner: manager-sample-flow") {
			t.Fatalf("factual missing custody hidden: %+v", r)
		}
		if len(livedata.SnapshotViolations(s)) != 1 {
			t.Fatal("lost invariant failure")
		}
	}
	check("Manager out of usage")
	s.ManagerCapacity = nil
	check("State missing")
	s.Unread = map[string]int{"manager-sample-flow": 2}
	check("State missing")
	// A genuine user card keeps its priority while the failure remains visible
	// to Checkin. Projection must not synthesize a second card or gate.
	s.Workstreams[0].Steps = append(s.Workstreams[0].Steps, livedata.SnapStep{BeadID: "sample-flow.card", Title: "Example access", Status: "open", State: "needs-user", Waiting: "you"})
	if r := livedata.Board(s, s.GeneratedAt.Unix()).Workstreams[0]; r.Kind != "needs" || r.Gate != nil {
		t.Fatalf("genuine user action hidden or duplicated: %+v", r)
	}
	if failures := livedata.StalledWork(s, s.GeneratedAt.Unix()); len(failures) != 1 {
		t.Fatalf("user card hid missing custody: %+v", failures)
	}
	s.Workstreams[0].Steps = s.Workstreams[0].Steps[:1]
	// Only actual custody repairs the failure; Manager activity alone cannot.
	s.Workstreams[0].Steps[0].Broken = false
	s.Workstreams[0].Steps[0].Owner = "operator-op-example"
	s.Workstreams[0].Steps[0].OwnerState = "working"
	if r := livedata.Board(s, s.GeneratedAt.Unix()).Workstreams[0]; r.Kind != "working" || r.Flag != 0 {
		t.Fatalf("healthy replacement worker hidden: %+v", r)
	}
	if len(livedata.SnapshotViolations(s)) != 0 {
		t.Fatal("actual custody did not recover")
	}
}
