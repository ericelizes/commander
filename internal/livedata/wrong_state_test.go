package livedata

import (
	"strings"
	"testing"
)

func TestBoardParkedNestedWorkstreamRemainsPaused(t *testing.T) {
	ws := SnapWorkstream{N: 1, Project: "p", BeadID: "w", Title: "Parent", Steps: []SnapStep{
		{BeadID: "w.1", Title: "Paused scope", Track: true, Status: "deferred", Children: []SnapStep{{BeadID: "w.1.1", Status: "open"}}},
		{BeadID: "w.2", Title: "Other scope", Track: true, Status: "open"},
	}}
	d := Board(Snapshot{Workstreams: []SnapWorkstream{ws}}, 100)
	if len(d.Workstreams) != 1 || len(d.Workstreams[0].Tracks) != 2 {
		t.Fatalf("paused track disappeared: %+v", d.Workstreams)
	}
	row := d.Workstreams[0].Tracks[0]
	if row.BeadID != "w.1" || row.Text != "Paused" || row.Working != 0 {
		t.Fatalf("paused row: %+v", row)
	}
}

func TestTerminalBoardUsesDeliveryMilestones(t *testing.T) {
	d := Display{Workstreams: []BoardRow{{N: 1, Title: "Delivery", Progress: Progress{Done: 4, Total: 5}, Milestones: Progress{Total: 1}, Tracks: []BoardTrack{{Letter: ".1", Title: "Nested", Progress: Progress{Done: 4, Total: 5}, Milestones: Progress{Total: 1}}}}}}
	lines := strings.Join(BoardLines(d, 100, "commander"), "\n")
	if strings.Contains(lines, "80%") || strings.Count(lines, "0% done") != 2 {
		t.Fatalf("legacy progress: %s", lines)
	}
}

func TestTerminalNestedManagerUsesDeliveryMilestones(t *testing.T) {
	d := Display{Workstreams: []BoardRow{{N: 1, Title: "Delivery", ManagerRole: "manager-root", Milestones: Progress{Done: 2, Total: 3}, Tracks: []BoardTrack{{BeadID: "nested", Letter: ".1", Title: "Nested", ManagerRole: "manager-nested", Progress: Progress{Done: 4, Total: 5}, Milestones: Progress{Total: 1}}}}}}
	lines := strings.Join(BoardLines(d, 100, "manager-nested"), "\n")
	if strings.Contains(lines, "80%") || strings.Contains(lines, "67%") || !strings.Contains(lines, "0% done") {
		t.Fatalf("nested scope used another progress calculation: %s", lines)
	}
}
