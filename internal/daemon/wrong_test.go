package daemon

import (
	"encoding/json"
	"github.com/example-user/commander/internal/nextactor"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
)

func TestWrongOnScreenCollectsEveryMismatch(t *testing.T) {
	d := livedata.Display{
		Workstreams: []livedata.BoardRow{
			{Key: "p:a", N: 1, Title: "Jobs (TEAM-1)", Kind: "working", Working: 0, Why: "Agent is no longer running and has not reported", Progress: livedata.Progress{Done: 2, Total: 4}},
			{Key: "p:b", N: 2, Title: "Logs (TEAM-2)", Kind: "working", Working: 1},
		},
		Asks: []livedata.BoardAsk{{Key: "ask:x", Rank: 2, WsKey: "p:b", Text: "Approve the sign-in?", StaleWhy: "The spec changed since this was asked"}},
	}
	sprint := linearsync.Response{State: "ok", Issues: []linearsync.Issue{
		{ID: "TEAM-1", Title: "jobs", StatusName: "Done", StatusType: "completed"},
		{ID: "TEAM-2", Title: "logs", StatusName: "Todo", StatusType: "unstarted"},
		{ID: "TEAM-3", Title: "free", StatusName: "In Progress", StatusType: "started"},
	}}
	raw := []livedata.SnapWorkstream{{Project: "p", BeadID: "a", Title: "Jobs (TEAM-1)"}, {Project: "p", BeadID: "b", Title: "Logs (TEAM-2)"}}
	got := wrongOnScreen(d, raw, sprint, t.TempDir(), time.Now())
	var keys []string
	for _, w := range got {
		keys = append(keys, w.Key)
	}
	want := "why:p:a ask-hidden:ask:x stale:ask:x ticket-done:TEAM-1 linear:TEAM-2 linear:TEAM-3"
	if strings.Join(keys, " ") != want {
		t.Fatalf("keys = %v\nwant %s", keys, want)
	}
	if es := wrongEntries(got); len(es) != len(got) || !strings.HasPrefix(es[0].Text, "#1 Jobs: agent is no longer running and has not reported. The planning Commander needs to inspect this mismatch.") {
		t.Fatalf("entries = %+v", es)
	}
}

// linearLines runs wrongOnScreen over one snapshot workstream for its board row.
func linearLines(t *testing.T, ws livedata.SnapWorkstream, working int, issues ...linearsync.Issue) map[string]livedata.SnapWrong {
	t.Helper()
	d := livedata.Display{Workstreams: []livedata.BoardRow{{Key: ws.Project + ":" + ws.BeadID, N: 1, Title: ws.Title, Working: working, Progress: livedata.Progress{Done: 1, Total: 3}}}}
	out := map[string]livedata.SnapWrong{}
	for _, w := range wrongOnScreen(d, []livedata.SnapWorkstream{ws}, linearsync.Response{State: "ok", Issues: issues}, t.TempDir(), time.Now()) {
		out[w.Key] = w
	}
	return out
}

func TestWrongLinearLinesTellCommanderToUpdateLinearItself(t *testing.T) {
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "a", Title: "Jobs (TEAM-1) (TEAM-2)"}
	lines := linearLines(t, ws, 1,
		linearsync.Issue{ID: "TEAM-1", Title: "one", StatusName: "Todo", StatusType: "unstarted"},
		linearsync.Issue{ID: "TEAM-2", Title: "two", StatusName: "Done", StatusType: "completed"},
		linearsync.Issue{ID: "TEAM-3", Title: "three", StatusName: "In Progress", StatusType: "started"})
	if len(lines) != 3 {
		t.Fatalf("lines = %+v", lines)
	}
	if !strings.HasSuffix(lines["linear:TEAM-1"].Text, "Update it in Linear") || !strings.HasSuffix(lines["linear:TEAM-3"].Text, "update it in Linear") {
		t.Fatalf("the user's badge keeps its action: %+v", lines)
	}
	var all []livedata.SnapWrong
	for _, w := range lines {
		all = append(all, w)
	}
	for _, e := range wrongEntries(all) {
		want := ". Update the ticket's status in Linear yourself to match the work (decision d27)."
		if e.ID == "wrong:ticket-done:TEAM-2" {
			want = ". Check whether the work or the ticket is out of date, and fix that one (decision d27)."
		}
		if !strings.HasSuffix(e.Text, want) {
			t.Fatalf("Commander's line = %q", e.Text)
		}
		for _, banned := range []string{"Tell the user", "tell the user", "never change Linear", "Fix it"} {
			if strings.Contains(e.Text, banned) {
				t.Fatalf("Commander's line says %q: %q", banned, e.Text)
			}
		}
	}
	other := wrongEntries([]livedata.SnapWrong{{Key: "why:p:a", Text: "stopped"}})
	if other[0].Text != "stopped. Fix it, or tell the user." {
		t.Fatalf("other lines keep their ending: %q", other[0].Text)
	}
}

func TestWrongJoinsLabelsLikeTheCard(t *testing.T) {
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "a", Title: "Plain", Tickets: []string{"TEAM-1", "TEAM-2"}}
	lines := linearLines(t, ws, 1,
		linearsync.Issue{ID: "TEAM-1", Title: "worked", StatusName: "In Progress", StatusType: "started"},
		linearsync.Issue{ID: "TEAM-2", Title: "finished", StatusName: "Done", StatusType: "completed"})
	if _, ok := lines["linear:TEAM-1"]; ok {
		t.Fatalf("a label-joined ticket with an agent reads as unworked: %+v", lines)
	}
	if w, ok := lines["ticket-done:TEAM-2"]; !ok || w.Target != "p:a" {
		t.Fatalf("a label-joined Done ticket needs its line: %+v", lines)
	}
}

func TestWrongJudgesATrackTicketByItsOwnTrack(t *testing.T) {
	op := livedata.SnapStep{BeadID: "a.2.1", Status: "in_progress", Owner: "operator-x", OwnerState: "working"}
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "a", Title: "Plain", Steps: []livedata.SnapStep{
		{BeadID: "a.1", Title: "Idle (TEAM-1)", Track: true, Status: "open"},
		{BeadID: "a.2", Title: "Busy (TEAM-2)", Track: true, Status: "in_progress", Children: []livedata.SnapStep{op}},
		{BeadID: "a.3", Title: "Finished (TEAM-3)", Track: true, Status: "closed"},
	}}
	lines := linearLines(t, ws, 2,
		linearsync.Issue{ID: "TEAM-1", Title: "idle", StatusName: "Todo", StatusType: "unstarted"},
		linearsync.Issue{ID: "TEAM-2", Title: "busy", StatusName: "In Progress", StatusType: "started"},
		linearsync.Issue{ID: "TEAM-3", Title: "closed track", StatusName: "Todo", StatusType: "unstarted"})
	if len(lines) != 0 {
		t.Fatalf("sibling track's Operator or a closed track made a false line: %+v", lines)
	}
}

func TestWrongLeavesIdleReviewTicketsAlone(t *testing.T) {
	ws := livedata.SnapWorkstream{Project: "p", BeadID: "a", Title: "Plain (TEAM-1) (TEAM-2) (TEAM-3)"}
	lines := linearLines(t, ws, 0,
		linearsync.Issue{ID: "TEAM-1", Title: "a", StatusName: "In Review", StatusType: "started"},
		linearsync.Issue{ID: "TEAM-2", Title: "b", StatusName: "Approval", StatusType: "started"},
		linearsync.Issue{ID: "TEAM-3", Title: "c", StatusName: "Blocked", StatusType: "started"})
	if len(lines) != 0 {
		t.Fatalf("idle In Review, Approval and Blocked are right: %+v", lines)
	}
}

func TestWrongNamesAWorkstreamWithNoTicket(t *testing.T) {
	bare := livedata.SnapWorkstream{Project: "p", BeadID: "a", Title: "Jobs"}
	w, ok := linearLines(t, bare, 0)["no-ticket:p:a"]
	if !ok {
		t.Fatal("a workstream with no ticket must be on the list")
	}
	if w.Target != "p:a" || !strings.HasPrefix(w.Text, "#1 Jobs has no Linear ticket") || !strings.Contains(w.Agent, "file one") || strings.Contains(w.Text, "has been told") {
		t.Fatalf("line = %+v", w)
	}
	for name, ws := range map[string]livedata.SnapWorkstream{
		"title": {Project: "p", BeadID: "a", Title: "Jobs (TEAM-1)"},
		"label": {Project: "p", BeadID: "a", Title: "Jobs", Tickets: []string{"TEAM-1"}},
		"track": {Project: "p", BeadID: "a", Title: "Jobs", Steps: []livedata.SnapStep{{BeadID: "a.1", Title: "Part (TEAM-1)", Track: true, Status: "open"}}},
	} {
		if _, on := linearLines(t, ws, 0)["no-ticket:p:a"]; on {
			t.Errorf("%s: a workstream carrying a ticket must not be listed", name)
		}
	}
	d := livedata.Display{Workstreams: []livedata.BoardRow{{Key: "p:a", N: 1, Title: "Jobs"}}}
	for _, w := range wrongOnScreen(d, []livedata.SnapWorkstream{bare}, linearsync.Response{State: "off"}, t.TempDir(), time.Now()) {
		if w.Key == "no-ticket:p:a" {
			t.Fatal("without Linear connected, no ticket is not a problem")
		}
	}
}

// Exercise the board health projection for both top-level and nested scopes.
func TestWrongOnScreenChainsScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/wrong-on-screen-chains.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Kind, Actor, Reason, Who, Words string
			Want                                  bool
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			actor := &nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Actor(c.Actor), Reason: c.Reason, Who: c.Who, Via: []string{"Reporting fix", "Decide"}}}
			for _, track := range []bool{false, true} {
				row := livedata.BoardRow{Key: "p:a", N: 1, Title: "QA Hub", Kind: c.Kind, Why: "via Reporting fix ← Decide", Actor: actor, PlanningCommander: "commander"}
				if track {
					row.Tracks = []livedata.BoardTrack{{Key: "p:b", Letter: "a", Title: "Track", Kind: row.Kind, Why: row.Why, Actor: actor, PlanningCommander: "commander"}}
					row.Why = ""
				}
				got := wrongOnScreen(livedata.Display{Workstreams: []livedata.BoardRow{row}}, nil, linearsync.Response{}, t.TempDir(), time.Now())
				if (len(got) > 0) != c.Want {
					t.Fatalf("track=%v entries=%+v want mismatch=%v", track, got, c.Want)
				}
				if c.Want && (!strings.Contains(got[0].Text, c.Words) || strings.Contains(got[0].Text, "Its Manager")) {
					t.Fatalf("wrong actor: %+v", got)
				}
			}
		})
	}
}
