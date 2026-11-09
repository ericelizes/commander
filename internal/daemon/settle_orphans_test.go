package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// addedWaits are the wait labels a test's calls set on bead id.
func addedWaits(calls []string, id string) []beads.Wait {
	var out []beads.Wait
	for _, c := range calls {
		rest, ok := strings.CutPrefix(c, "SetLabel:"+id+"|label=")
		if !ok || !strings.Contains(rest, "|on=true") {
			continue
		}
		if w, ok := beads.ParseWaitLabel(rest[:strings.Index(rest, "|on=")]); ok {
			out = append(out, w)
		}
	}
	return out
}

// A finished step waits on its Manager to close it, with a deadline; it is
// never closed for the Manager, and an earlier run's markers go.
func TestSettleExitedWaitsOnTheManagerUntilADeadline(t *testing.T) {
	old := beads.Wait{Kind: beads.WaitUntil, Since: time.Unix(100, 0), Until: time.Unix(200, 0), Ref: beads.ManagerCloseWait}.Label()
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x", beads.OperatorDiedLabel, old}}}
	var sent []sentMail
	s := settler(b, &sent)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	if err := s.exited(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(b.Calls, "\n")
	for _, want := range []string{"label=" + beads.OperatorDiedLabel + "|on=false", "label=" + old + "|on=false", "status=in_progress"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "Close") {
		t.Fatalf("closed the step for the Manager:\n%s", calls)
	}
	ws := addedWaits(b.Calls, "p-ws.2.1")
	if len(ws) != 1 || ws[0].Kind != beads.WaitUntil || ws[0].Ref != beads.ManagerCloseWait || !ws[0].Until.Equal(now.Add(managerCloseWithin)) {
		t.Fatalf("waits = %+v", ws)
	}
	if len(sent) != 1 || sent[0].to != "manager-p-ws" {
		t.Fatalf("mail: %+v", sent)
	}
}

type orphanStore struct{ runs []store.AgentRun }

func (orphanStore) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 3, Name: "p", Path: "/p"}}, nil
}

func (o orphanStore) ListAgentRunsByProject(context.Context, int64, string) ([]store.AgentRun, error) {
	return o.runs, nil
}

// The startup sweep settles steps still claimed by an Operator that is gone:
// in review as finished (a wait on the Manager), otherwise as died (claim
// cleared, marked). Live Operators, needs-user steps and other owners are left.
func TestSweepOrphansSettlesStaleOperatorClaims(t *testing.T) {
	b := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "p-ws.1", Status: "in_progress", Labels: []string{"kind:objective", "owner:operator-done", "review"}},
		{ID: "p-ws.2", Status: "in_progress", Labels: []string{"kind:objective", "owner:operator-gone", "waiting:1:operator:operator-gone"}},
		{ID: "p-ws.3", Status: "in_progress", Labels: []string{"kind:objective", "owner:operator-live"}},
		{ID: "p-ws.4", Status: "open", Labels: []string{"kind:objective", "owner:operator-gone", "needs-user"}},
		{ID: "p-ws.5", Status: "in_progress", Labels: []string{"kind:objective", "owner:manager-p-ws"}},
		{ID: "p-ws.6", Status: "closed", Labels: []string{"kind:objective", "owner:operator-gone"}},
		{ID: "p-ws.7", Status: "in_progress", Labels: []string{"kind:objective", "owner:operator-never-spawned"}},
	}}
	var sent []sentMail
	s := settler(b, &sent)
	s.comments = func(context.Context, string, string) ([]beadComment, error) { return nil, nil }
	s.sweepOrphans(context.Background(), orphanStore{runs: []store.AgentRun{
		{AgentName: "operator-done", Status: "exited", SpawnTime: 1},
		{AgentName: "operator-gone", Status: "closed", SpawnTime: 1},
		{AgentName: "operator-live", Status: "running", SpawnTime: 1},
	}})
	touched := map[string]bool{}
	for _, c := range b.Calls {
		if id, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(c, "SetLabel:"), "SetStatus:"), "|"); ok && strings.HasPrefix(c, "Set") {
			touched[id] = true
		}
	}
	if len(touched) != 2 || !touched["p-ws.1"] || !touched["p-ws.2"] {
		t.Fatalf("touched %v:\n%s", touched, strings.Join(b.Calls, "\n"))
	}
	calls := strings.Join(b.Calls, "\n")
	for _, want := range []string{
		"SetLabel:p-ws.1|label=owner:manager-p-ws|on=true", "SetStatus:p-ws.1|status=in_progress",
		"SetLabel:p-ws.2|label=owner:operator-gone|on=false", "SetLabel:p-ws.2|label=" + beads.OperatorDiedLabel + "|on=true", "SetStatus:p-ws.2|status=open",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	if ws := addedWaits(b.Calls, "p-ws.1"); len(ws) != 1 || ws[0].Ref != beads.ManagerCloseWait {
		t.Fatalf("finished step waits = %+v", ws)
	}
	if len(sent) != 2 || !strings.Contains(sent[0].subject, "finished p-ws.1") || sent[1].subject != "Operator died on p-ws.2" {
		t.Fatalf("mail: %+v", sent)
	}
}
