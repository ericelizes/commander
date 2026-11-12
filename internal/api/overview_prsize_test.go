package api

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func init() { prSizeLookup = func(string, int) string { return "" } }

func TestOverviewPRSizesAreRecapEvidence(t *testing.T) {
	calls := 0
	prSizeLookup = func(repo string, n int) string {
		calls++
		return "Pull request #7 in o/r (Add notes, open): +120 / -30 lines across 4 files"
	}
	defer func() { prSizeLookup = func(string, int) string { return "" } }()
	ws := livedata.SnapWorkstream{Repo: "o/r", Steps: []livedata.SnapStep{
		{BeadID: "w.1", PR: []string{"7"}},
		{BeadID: "w.2", Track: true, Children: []livedata.SnapStep{{BeadID: "w.2.1", PR: []string{"7"}}}},
	}}
	ev := overviewPRSizes(ws, "")
	if len(ev) != 1 || ev[0].ID != "pr-size-7" || !strings.Contains(ev[0].Text, "+120 / -30 lines across 4 files") {
		t.Fatalf("evidence %+v", ev)
	}
	overviewPRSizes(ws, "w.2")
	if calls != 1 {
		t.Fatalf("a pull request size is cached, asked %d times", calls)
	}
}
