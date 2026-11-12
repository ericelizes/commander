package api

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func TestCompleteTellsTheCommanderWhichTicketsToClose(t *testing.T) {
	snap := livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{
		Project: "vibe-apps", BeadID: "qa", Title: "QA Hub", PlanningCommander: "commander",
		Steps: []livedata.SnapStep{{BeadID: "qa.5", Title: "ENGCHA-5197: QA Hub backups", Track: true, Tickets: []string{"ENGCHA-5197", "ENGCHA-5200"}}},
	}}}
	c := findCompleted(snap, "vibe-apps", "qa.5")
	if c == nil || c.commander != "commander" {
		t.Fatalf("%+v", c)
	}
	if msg := c.message(); !strings.Contains(msg, "ENGCHA-5197, ENGCHA-5200") || !strings.Contains(msg, "QA Hub backups") {
		t.Fatalf("message %q", msg)
	}
	if findCompleted(snap, "vibe-apps", "missing") != nil {
		t.Fatal("found a bead that is not there")
	}
}
