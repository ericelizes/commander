package livedata

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestTitleTickets(t *testing.T) {
	got := TitleTickets("op_TEAM-12 team-4321 PLATFORM-12, TEAM-1,TEAM-2 x-TEAM-3 TEAM-4a TOOLONGAKEY-1")
	want := []string{"TEAM-12", "TEAM-4321", "PLATFORM-12", "TEAM-1", "TEAM-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWorkstreamTicketsOpenTracksJudgedAlone(t *testing.T) {
	ws := SnapWorkstream{Title: "W (TEAM-1)", Tickets: []string{"TEAM-9"}, Steps: []SnapStep{
		{Title: "Busy (TEAM-2)", Track: true, Status: "open", Children: []SnapStep{{Status: "in_progress", Owner: "operator-x", OwnerState: OwnerWorking}}},
		{Title: "Idle", Tickets: []string{"TEAM-3"}, Track: true, Status: "open", Children: []SnapStep{{Status: "open", Owner: "operator-y", OwnerState: OwnerDied}}},
		{Title: "Closed (TEAM-4)", Track: true, Status: "closed"},
		{Title: "Parked (TEAM-5)", Track: true, Status: statusDeferred},
		{Title: "Dropped (TEAM-6)", Track: true, Status: "open", Superseded: true},
		{Title: "Plain step (TEAM-7)", Status: "open"},
	}}
	want := []WorkstreamTicket{{ID: "TEAM-1"}, {ID: "TEAM-9"}, {ID: "TEAM-2", OnTrack: true, Working: true}, {ID: "TEAM-3", OnTrack: true}}
	if got := WorkstreamTickets(ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A ticket: label travels the whole display path: bead, row, snapshot, JSON.
func TestTicketLabelReachesTheSnapshot(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w", Title: "Plain", Labels: []string{"kind:objective", "ticket:team-9"}})
	mem.Seed(beads.Bead{ID: "w.1", Title: "Track", Labels: []string{"kind:objective", "kind:track", "ticket:team-10"}})
	project := store.Project{ID: 1, Name: "p", Path: "/p"}
	rows, err := projectObjectiveRows(context.Background(), mem, project)
	if err != nil {
		t.Fatal(err)
	}
	snap := BuildSnapshot(Data{Projects: []store.Project{project}, Objectives: rows, LoadedAt: time.Now()}, nil)
	if len(snap.Workstreams) != 1 {
		t.Fatalf("workstreams: %+v", snap.Workstreams)
	}
	got := WorkstreamTickets(snap.Workstreams[0])
	want := []WorkstreamTicket{{ID: "TEAM-9"}, {ID: "TEAM-10", OnTrack: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	out, err := json.Marshal(snap.Workstreams[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`"tickets":["TEAM-9"]`, `"tickets":["TEAM-10"]`} {
		if !strings.Contains(string(out), id) {
			t.Fatalf("%s is not in the JSON: %s", id, out)
		}
	}
}
