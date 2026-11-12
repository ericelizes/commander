package daemon

import (
	"strings"
	"testing"
	"time"

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

func TestMultiTeamChecksUseEveryFreshTeam(t *testing.T) {
	r := linearsync.Response{State: "partial", Teams: []linearsync.TeamResponse{}}
	for _, team := range []string{"FOR", "ENGCHA", "ENGINT"} {
		state := "ok"
		if team == "FOR" {
			state = "unreachable"
		}
		r.Teams = append(r.Teams, linearsync.TeamResponse{Team: team, Response: linearsync.Response{State: state, Cycle: &linearsync.Cycle{Name: team + " cycle"}, Issues: []linearsync.Issue{{ID: team + "-1", Team: team, StatusName: "In Progress", StatusType: "started"}}}})
	}
	entries := sprintEntries(livedata.Display{}, nil, r)
	if len(entries) != 3 || entries[1].ID != "sprint:ENGCHA-1" || entries[2].ID != "sprint:ENGINT-1" {
		t.Fatalf("%+v", entries)
	}
	wrong := wrongOnScreen(livedata.Display{}, nil, r, t.TempDir(), time.Now())
	if len(wrong) != 2 || wrong[0].Key != "linear:ENGCHA-1" || wrong[1].Key != "linear:ENGINT-1" {
		t.Fatalf("%+v", wrong)
	}
}
