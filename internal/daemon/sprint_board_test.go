package daemon

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
)

func TestSprintEntriesFlagsOpenTicketsNoTitleNames(t *testing.T) {
	d := livedata.Display{Workstreams: []livedata.BoardRow{{Key: "p:a", Title: "Jobs (TEAM-1)"}}}
	raw := []livedata.SnapWorkstream{{Project: "p", BeadID: "a", Title: "Jobs (TEAM-1)", Steps: []livedata.SnapStep{{Title: "Move it (TEAM-2)", Track: true, Status: "open"}}}}
	r := linearsync.Response{State: "ok", Cycle: &linearsync.Cycle{Name: "Cycle 9", End: "2026-11-11T05:00:00.000Z"}, Issues: []linearsync.Issue{
		{ID: "TEAM-1", Title: "a", StatusType: "started"},
		{ID: "TEAM-2", Title: "b", StatusType: "unstarted"},
		{ID: "TEAM-3", Title: "c", StatusName: "Todo", StatusType: "unstarted"},
		{ID: "TEAM-4", Title: "d", StatusType: "completed"},
	}}
	es := sprintEntries(d, raw, r)
	if len(es) != 2 {
		t.Fatalf("entries = %d, want summary + TEAM-3: %+v", len(es), es)
	}
	if want := "Cycle 9, ends Nov 11: 2 of 4 tickets started or done, 1 open on no workstream"; es[0].Text != want {
		t.Fatalf("summary = %q", es[0].Text)
	}
	if es[1].ID != "sprint:TEAM-3" || !strings.Contains(es[1].Text, "add TEAM-3 to that title or give it the label ticket:TEAM-3") {
		t.Fatalf("unmapped = %+v", es[1])
	}
	if sprintEntries(d, raw, linearsync.Response{State: "no_key"}) != nil {
		t.Fatal("no sprint lines without a working key")
	}
}

func TestSprintEntriesCountsLabelsAsMapped(t *testing.T) {
	d := livedata.Display{Workstreams: []livedata.BoardRow{{Key: "p:a", Title: "Plain"}}}
	raw := []livedata.SnapWorkstream{{Project: "p", BeadID: "a", Title: "Plain", Tickets: []string{"TEAM-1"}, Steps: []livedata.SnapStep{{Title: "T", Track: true, Status: "open", Tickets: []string{"TEAM-2"}}}}}
	r := linearsync.Response{State: "ok", Cycle: &linearsync.Cycle{Name: "C"}, Issues: []linearsync.Issue{
		{ID: "TEAM-1", Title: "a", StatusType: "unstarted"},
		{ID: "TEAM-2", Title: "b", StatusType: "unstarted"},
	}}
	if es := sprintEntries(d, raw, r); len(es) != 1 {
		t.Fatalf("labelled tickets read as unmapped: %+v", es)
	}
}
