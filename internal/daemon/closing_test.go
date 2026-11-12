package daemon

import (
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func TestClosingJobsKeepOneConfirmStep(t *testing.T) {
	snap := livedata.Snapshot{
		Projects: []livedata.SnapProject{{Name: "p", Path: "/p"}},
		Workstreams: []livedata.SnapWorkstream{
			{Project: "p", BeadID: "new", Title: "New", Status: "open"},
			{Project: "p", BeadID: "busy", Title: "Busy", Status: "open", Steps: []livedata.SnapStep{
				{BeadID: "busy.1", Status: "open"},
				{BeadID: "busy.2", Status: "open", ClosingGate: true},
				{BeadID: "busy.3", Title: "Nested", Status: "open", Track: true, Children: []livedata.SnapStep{{BeadID: "busy.3.1", Status: "closed"}}},
			}},
			{Project: "p", BeadID: "done", Title: "Done work", Status: "open", Steps: []livedata.SnapStep{
				{BeadID: "done.1", Status: "closed"},
				{BeadID: "done.2", Status: "open", ClosingGate: true},
			}},
			{Project: "p", BeadID: "asked", Title: "Asked", Status: "open", Steps: []livedata.SnapStep{
				{BeadID: "asked.1", Status: "closed"},
				{BeadID: "asked.2", Status: "open", ClosingGate: true, State: "needs-user"},
			}},
			{Project: "p", BeadID: "gone", Status: "closed"},
		},
	}
	got := map[string]closingJob{}
	for _, j := range closingJobs(snap) {
		got[j.workstream] = j
	}
	if j, ok := got["new"]; !ok || j.dir != "/p" {
		t.Fatalf("a workstream without the step gets one: %+v", got)
	}
	if _, ok := got["busy.3"]; !ok {
		t.Fatalf("a nested workstream gets its own step: %+v", got)
	}
	for _, has := range []string{"busy", "done", "asked"} {
		if _, ok := got[has]; ok {
			t.Fatalf("%s already has its step: %+v", has, got)
		}
	}
	if _, ok := got["gone"]; ok {
		t.Fatal("a closed workstream needs nothing")
	}
}
