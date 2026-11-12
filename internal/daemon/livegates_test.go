package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
)

const prCheck = `gh pr checks 7 --repo acme/web >/dev/null`

func gateBead(id, verifier, status string, extra ...string) beads.Bead {
	labels := append([]string{"kind:gate", "gate-of:p-ws.1", "gate-name:g", "verifier:" + verifier}, extra...)
	return beads.Bead{ID: id, Status: status, Labels: labels, Description: prCheck}
}

type gateRig struct {
	b     *beads.MockClient
	w     *gateWatcher
	sent  *[]sentMail
	runs  *int
	now   *time.Time
	heads map[string]string
	pass  *bool
}

func newGateRig(gate beads.Bead) gateRig {
	var sent []sentMail
	runs, pass := 0, true
	now := epoch
	heads := map[string]string{"pr": "aaa", "api": "m1"}
	b := &beads.MockClient{ListResult: []beads.Bead{gate}, ShowResults: map[string]*beads.Bead{gate.ID: &gate}}
	w := &gateWatcher{
		projects: fakeProjects{}, beads: b, last: map[string]gateSeen{}, failing: map[string]int{},
		gh: func(_ context.Context, args ...string) ([]byte, error) { return []byte(heads[args[0]]), nil },
		run: func(context.Context, string, string) (string, error) {
			runs++
			if pass {
				return "", nil
			}
			return "1 failing", errors.New("exit status 1")
		},
		mail: func(_ context.Context, to, subject, _ string) error {
			sent = append(sent, sentMail{to, subject})
			return nil
		},
		repoOf: func(string) string { return "acme/web" },
		now:    func() time.Time { return now },
	}
	return gateRig{b, w, &sent, &runs, &now, heads, &pass}
}

func (r gateRig) poll(after time.Duration) {
	*r.now = r.now.Add(after)
	r.w.poll(context.Background())
}

func (r gateRig) calls() string { return strings.Join(r.b.Calls, "\n") }

// A passed gate whose condition breaks is reopened with the failure as
// evidence and the Manager is mailed once; recovery mails once more.
func TestAutoGateReopensWhenConditionBreaks(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.poll(0)
	if len(*r.sent) != 0 || strings.Contains(r.calls(), "AddComment") {
		t.Fatalf("a held gate changed: %v %s", *r.sent, r.calls())
	}
	*r.pass = false
	r.poll(4 * time.Minute) // first failure: debounced
	r.poll(time.Minute)
	if len(*r.sent) != 1 || (*r.sent)[0].to != "manager-p-ws" || !strings.Contains((*r.sent)[0].subject, "stopped holding") {
		t.Fatalf("mail: %+v", *r.sent)
	}
	for _, want := range []string{"AddComment:p-g1|text=gate failed by auto at 1000300: 1 failing", "SetStatus:p-g1|status=open", "SetLabel:p-g1|label=gate-status:failed|on=true"} {
		if !strings.Contains(r.calls(), want) {
			t.Errorf("missing %q in\n%s", want, r.calls())
		}
	}
	// Now failed in the store: staying broken is silent.
	r.b.ListResult = []beads.Bead{gateBead("p-g1", "auto", "open", "gate-status:failed")}
	r.poll(4 * time.Minute)
	if len(*r.sent) != 1 {
		t.Fatalf("woke again while still failed: %+v", *r.sent)
	}
	*r.pass = true
	r.poll(4 * time.Minute)
	if len(*r.sent) != 2 || !strings.Contains((*r.sent)[1].subject, "holds again") {
		t.Fatalf("recovery mail: %+v", *r.sent)
	}
	if !strings.Contains(r.calls(), "Close:p-g1") {
		t.Errorf("recovered gate not closed: %s", r.calls())
	}
}

func TestDecisionGatesLatch(t *testing.T) {
	for _, v := range []string{"user", "operator", "reviewer:amy"} {
		r := newGateRig(gateBead("p-g1", v, "closed"))
		*r.pass = false
		r.poll(0)
		r.poll(10 * time.Minute)
		if *r.runs != 0 || strings.Contains(r.calls(), "AddComment") || strings.Contains(r.calls(), "SetStatus") {
			t.Errorf("%s gate was touched: runs=%d calls=%v", v, *r.runs, r.b.Calls)
		}
	}
}

func TestClosedStageStopsRechecks(t *testing.T) {
	stage := beads.Bead{ID: "p-ws.1", Status: "closed", Labels: []string{"kind:objective"}}
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.b.ListResult = append(r.b.ListResult, stage)
	r.poll(0)
	if *r.runs != 0 {
		t.Fatalf("closed stage's gate re-ran %d times", *r.runs)
	}
}

// Between ticks a check re-runs only when the PR head or main moved, else
// every gateRecheckEvery.
func TestRecheckOnPushMainMoveAndInterval(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.poll(0)
	r.poll(time.Minute)
	if *r.runs != 1 {
		t.Fatalf("re-ran with nothing new: %d", *r.runs)
	}
	r.heads["pr"] = "bbb"
	r.poll(time.Minute)
	if *r.runs != 2 {
		t.Fatalf("push did not re-check: %d", *r.runs)
	}
	r.heads["api"] = "m2"
	r.poll(time.Minute)
	if *r.runs != 3 {
		t.Fatalf("main moving did not re-check: %d", *r.runs)
	}
	r.poll(gateRecheckEvery)
	if *r.runs != 4 {
		t.Fatalf("interval did not re-check: %d", *r.runs)
	}
}

func TestDegradedStoreSkipsGates(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.w.degraded = func() bool { return true }
	r.poll(0)
	if *r.runs != 0 || len(r.b.Calls) != 0 {
		t.Fatalf("degraded poll did work: %v", r.b.Calls)
	}
}

func TestDeferredStageStopsRechecks(t *testing.T) {
	stage := beads.Bead{ID: "p-ws.1", Status: beads.StatusDeferred, Labels: []string{"kind:objective"}}
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.b.ListResult = append(r.b.ListResult, stage)
	r.poll(0)
	if *r.runs != 0 {
		t.Fatalf("parked stage's gate re-ran %d times", *r.runs)
	}
}

// One poll starts at most gateChecksPerPoll checks; the rest follow next minute.
func TestGateChecksPerPollAreBounded(t *testing.T) {
	var all []beads.Bead
	for i := 0; i < gateChecksPerPoll+3; i++ {
		all = append(all, gateBead("p-g"+string(rune('a'+i)), "auto", "closed"))
	}
	r := newGateRig(all[0])
	r.b.ListResult = all
	r.poll(0)
	if *r.runs != gateChecksPerPoll {
		t.Fatalf("first poll ran %d checks, want %d", *r.runs, gateChecksPerPoll)
	}
	r.poll(time.Minute)
	if *r.runs != len(all) {
		t.Fatalf("second poll left checks undone: %d of %d", *r.runs, len(all))
	}
}

// A List error must skip the project, not re-run gates of possibly closed stages.
func TestStageListErrorSkipsGates(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	*r.pass = false
	r.w.beads = &listFailsForStages{r.b}
	r.poll(0)
	if *r.runs != 0 || strings.Contains(r.calls(), "SetStatus") {
		t.Fatalf("gate ran on unreadable stages: runs=%d %s", *r.runs, r.calls())
	}
}

type listFailsForStages struct{ *beads.MockClient }

func (l *listFailsForStages) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	for _, lb := range o.Labels {
		if lb == "kind:objective" {
			return nil, errors.New("br busy")
		}
	}
	return l.MockClient.List(ctx, dir, o)
}

// A stage closed but lacking kind:objective is found by the direct read.
func TestClosedStageByDirectReadSkipsGate(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.b.ShowResult = &beads.Bead{ID: "p-ws.1", Status: "closed"}
	r.poll(0)
	if *r.runs != 0 {
		t.Fatalf("gate of a closed stage ran %d times", *r.runs)
	}
}

// One failed check of a passed gate does not reopen it; two in a row do.
func TestFailureNeedsTwoInARow(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	*r.pass = false
	r.poll(0)
	if strings.Contains(r.calls(), "SetStatus") || len(*r.sent) != 0 {
		t.Fatalf("reopened on one failure: %s", r.calls())
	}
	r.poll(time.Minute)
	if !strings.Contains(r.calls(), "SetStatus:p-g1|status=open") || len(*r.sent) != 1 {
		t.Fatalf("not reopened after two: %s %v", r.calls(), *r.sent)
	}
}

// An empty sha (gh failed) is not a main/PR move.
func TestEmptyShaIsNotAMove(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "closed"))
	r.poll(0)
	delete(r.heads, "pr")
	delete(r.heads, "api")
	r.poll(time.Minute)
	r.heads["pr"], r.heads["api"] = "aaa", "m1"
	r.poll(time.Minute)
	if *r.runs != 1 {
		t.Fatalf("phantom move re-ran the check: %d", *r.runs)
	}
}

// F4: cmdrd has no completion door, so a check redefined to pass closes nothing here.
func TestRedefinedCheckNeverClosesAMergeGate(t *testing.T) {
	merge := gateBead("p-g1", "auto", "open", "gate-name:merged")
	merge.Description = "true"
	r := newGateRig(merge)
	r.b.ShowResult = &merge
	r.poll(0)
	r.poll(10 * time.Minute)
	if *r.runs != 0 || strings.Contains(r.calls(), "Close:") {
		t.Fatalf("runs=%d calls=%s", *r.runs, r.calls())
	}
}

func TestStandardMergedCheckStillClosesAMergeGate(t *testing.T) {
	merge := gateBead("p-g1", "auto", "open", "gate-name:merged", "pr:7")
	merge.Description = gates.MergedCheck([]string{"7", "8"}, "acme/web")
	r := newGateRig(merge)
	r.b.ShowResult = &merge
	r.poll(0)
	if *r.runs != 1 || !strings.Contains(r.calls(), "Close:p-g1") {
		t.Fatalf("runs=%d calls=%s", *r.runs, r.calls())
	}
}

// The standard check must name the PRs its target is judged on: one redefined to an
// old merged PR closes nothing while the stage's own PR is open.
func TestStandardMergedCheckOnAnotherPRNeverClosesAMergeGate(t *testing.T) {
	stage := beads.Bead{ID: "p-ws.1", Status: "open", Labels: []string{"kind:objective", "pr:7"}}
	for _, c := range []struct {
		name  string
		check string
		close bool
	}{
		{"names an old merged PR", gates.MergedCheck([]string{"1"}, "acme/web"), false},
		{"names another repo's PR 7", gates.MergedCheck([]string{"7"}, "acme/api"), false},
		{"names the stage's PR", gates.MergedCheck([]string{"7"}, "acme/web"), true},
		{"names it through the project repo", gates.MergedCheck([]string{"7"}, ""), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			merge := gateBead("p-g1", "auto", "open", "gate-name:merged")
			merge.Description = c.check
			r := newGateRig(merge)
			r.b.ShowResults = map[string]*beads.Bead{"p-g1": &merge, "p-ws.1": &stage}
			r.poll(0)
			if closed := strings.Contains(r.calls(), "Close:p-g1"); closed != c.close || (*r.runs == 1) != c.close {
				t.Fatalf("runs=%d calls=%s", *r.runs, r.calls())
			}
		})
	}
}

// A merge gate with no PR to hold its check to is not closed by cmdrd.
func TestStandardMergedCheckWithNoTargetPRIsProtected(t *testing.T) {
	merge := gateBead("p-g1", "auto", "open", "gate-name:merged")
	merge.Description = gates.MergedCheck([]string{"7"}, "acme/web")
	r := newGateRig(merge)
	r.b.ShowResult = &merge
	r.poll(0)
	if *r.runs != 0 || strings.Contains(r.calls(), "Close:") {
		t.Fatalf("runs=%d calls=%s", *r.runs, r.calls())
	}
}

// An unreadable gate is treated as protected: never guess.
func TestUnreadableGateIsNotRechecked(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "open"))
	r.b.ShowErr = errors.New("busy")
	r.poll(0)
	if *r.runs != 0 {
		t.Fatalf("runs=%d", *r.runs)
	}
}

// An open gate passing for the first time releases what waited on it: the Manager is told to dispatch it.
func TestLiveGatePassHolds(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "open"))
	r.poll(0)
	if len(*r.sent) != 1 || (*r.sent)[0].to != "manager-p-ws" || !strings.Contains((*r.sent)[0].subject, "released") {
		t.Fatalf("a gate passing for the first time mails its Manager: %+v", *r.sent)
	}
	if !strings.Contains(r.calls(), "Close:p-g1") {
		t.Errorf("passed gate not closed: %s", r.calls())
	}
}

func TestWithdrawnAutoGateNeverRunsOrApproves(t *testing.T) {
	r := newGateRig(gateBead("p-g1", "auto", "deferred", beads.WithdrawnLabel))
	r.poll(0)
	if *r.runs != 0 || len(*r.sent) != 0 || strings.Contains(r.calls(), "AddComment") || strings.Contains(r.calls(), "Close:") {
		t.Fatalf("withdrawn auto gate ran: %s", r.calls())
	}
}
