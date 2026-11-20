package livedata

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// Every wait kind is drawn on the line of whoever has the move, with a who that
// says what it waits on; only a time wait (until:) reads as a timer. The golden
// fixture testdata/diagram/wait-kinds.json holds one step per kind, its waiting
// text the one beads.Wait.Text gives the typed wait.
func TestDiagram_EveryWaitKindHasAnHonestWho(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	const path = "testdata/diagram/wait-kinds.json"
	var d *Diagram
	for _, r := range fixtureBoard(t, path).Workstreams {
		if r.BeadID == "lab-wk" {
			d = r.Diagram
		}
	}
	if d == nil {
		t.Fatal("no diagram for lab-wk")
	}
	waiting := fixtureWaits(t, path)
	until := time.Unix(1793050000, 0)
	for _, c := range []struct {
		id   string
		wait beads.Wait
		want string
	}{
		{"lab-wk.1", beads.Wait{Kind: beads.WaitCI, PR: 4101}, "automatic|CI, runs on each push to the PR"},
		{"lab-wk.2", beads.Wait{Kind: beads.WaitCI, Repo: "acme/api", PR: 77}, "automatic|CI, runs on each push to the PR"},
		{"lab-wk.3", beads.Wait{Kind: beads.WaitMainCI, Sha: "349a7f4"}, "automatic|CI on main, runs on the merge"},
		{"lab-wk.4", beads.Wait{Kind: beads.WaitProdTag, Sha: "349a7f4"}, "automatic|the release tags prod"},
		{"lab-wk.5", beads.Wait{Kind: beads.WaitReview, Ref: "reviewer-a"}, "reviewers|reviewer-a"},
		{"lab-wk.6", beads.Wait{Kind: beads.WaitOperator, Ref: "operator-lab-wk-6"}, "operators|"},
		{"lab-wk.7", beads.Wait{Kind: beads.WaitOperator, Ref: "op-investigate-g"}, "operators|"},
		{"lab-wk.8", beads.Wait{Kind: beads.WaitUntil, Ref: "soak period", Until: until}, "automatic|a timer"},
		{"lab-wk.9", beads.Wait{Kind: beads.WaitUntil, Until: until}, "automatic|a timer"},
		{"lab-wk.10", beads.Wait{Kind: beads.WaitUntil, Ref: beads.ManagerRecoverWait, Until: until}, "operators|"},
		{"lab-wk.11", beads.Wait{Kind: beads.WaitDeploy, Sha: "2ac6897"}, "automatic|a prod deploy with 2ac6897"},
		{"lab-wk.12", beads.Wait{Kind: beads.WaitPR, Cond: beads.PRApproved, Repo: "acme/infrastructure", PR: 2163}, "reviewers|PR approval (infrastructure#2163)"},
		{"lab-wk.13", beads.Wait{Kind: beads.WaitPR, Cond: beads.PRMerged, PR: 3960}, "reviewers|PR merge (#3960)"},
		{"lab-wk.14", beads.Wait{Kind: beads.WaitPR, Cond: beads.PRGreen, PR: 3996}, "automatic|CI on the PR (#3996)"},
		{"lab-wk.15", beads.Wait{Kind: beads.WaitEvent, Cond: beads.EventSlack, Channel: "C0123", Ref: "deployed"}, "reviewers|a Slack message in C0123"},
		{"lab-wk.16", beads.Wait{Kind: beads.WaitEvent, Cond: beads.EventNotify, Channel: "deploy", Ref: "done"}, "automatic|a deploy notification"},
	} {
		text := c.wait.Text()
		if waiting[c.id] != text {
			t.Errorf("%s: fixture waits on %q, the typed wait reads %q", c.id, waiting[c.id], text)
		}
		s := SnapStep{BeadID: c.id}
		if c.wait.Kind == beads.WaitUntil {
			s.WaitUntils = map[string]int64{text: until.Unix()}
		}
		if h := waitHand(s, text); h.actor+"|"+h.who != c.want {
			t.Errorf("waitHand(%q) = %s|%s, want %s", text, h.actor, h.who, c.want)
		}
		st := diagramStep(d, c.id)
		if st == nil || st.Actor+"|"+st.Who != c.want || st.Lane != laneOfActor(st.Actor) {
			t.Errorf("%s drawn as %+v, want %s", c.id, st, c.want)
		}
	}
}

// A wait whose text only looks like an event is still a timer when the bead records it as a time wait.
func TestWaitHand_TimeWaitIsATimerWhateverItsLabel(t *testing.T) {
	s := SnapStep{WaitUntils: map[string]int64{"PR #12 merged": 1793050000}}
	if h := waitHand(s, "PR #12 merged"); h != (hand{ActorAutomatic, "a timer"}) {
		t.Errorf("time wait = %+v, want a timer", h)
	}
	if h := waitHand(SnapStep{}, "PR #12 merged"); h != (hand{ActorReviewers, "PR merge (#12)"}) {
		t.Errorf("pr wait = %+v, want the merge", h)
	}
}

// fixtureWaits is each step's waiting text in a diagram fixture.
func fixtureWaits(t *testing.T, path string) map[string]string {
	t.Helper()
	var fx struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	var walk func([]SnapStep)
	walk = func(ss []SnapStep) {
		for _, s := range ss {
			out[s.BeadID] = s.Waiting
			walk(s.Children)
		}
	}
	for _, w := range fx.Snapshot.Workstreams {
		walk(w.Steps)
	}
	return out
}
