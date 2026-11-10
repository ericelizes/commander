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
