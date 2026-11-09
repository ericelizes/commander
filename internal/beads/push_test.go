package beads

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var pushNow = time.Unix(1_800_000_000, 0)

func untilLabel(t time.Time) string {
	return WaitLabelPrefix + "1:until:" + itoa(t.Unix()) + ":x"
}
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestPushFirstWaitAndSmallMovesAreNotPushes(t *testing.T) {
	lim := PushLimits{Count: 3, Days: 7}
	// no previous target: the first wait
	if p, err := PlanPush(nil, nil, pushNow.Add(time.Hour), "", lim, pushNow); err != nil || p.Counted || p.Comment != "" {
		t.Fatalf("first wait: %+v %v", p, err)
	}
	labels := []string{untilLabel(pushNow.Add(time.Hour))}
	for _, d := range []time.Duration{-time.Hour, 0, 10 * time.Minute} { // earlier, same, within slack
		if p, err := PlanPush(labels, nil, pushNow.Add(time.Hour+d), "", lim, pushNow); err != nil || p.Counted {
			t.Fatalf("move %v: %+v %v", d, p, err)
		}
	}
	if p, _ := PlanPush(labels, nil, pushNow.Add(time.Hour+11*time.Minute), "", lim, pushNow); !p.Counted || p.N != 1 {
		t.Fatalf("11 minutes later is a push: %+v", p)
	}
}

func TestPushCountsAndRefusesPastTheCount(t *testing.T) {
	lim := PushLimits{Count: 3, Days: 0}
	labels := []string{untilLabel(pushNow.Add(time.Hour))}
	target := pushNow.Add(time.Hour)
	for i := 1; i <= 3; i++ {
		target = target.Add(time.Hour)
		p, err := PlanPush(labels, nil, target, "", lim, pushNow)
		if err != nil || !p.Counted || p.N != i {
			t.Fatalf("push %d: %+v %v", i, p, err)
		}
		labels = []string{untilLabel(target), PushedPrefix + itoa(int64(i)), PushedFirstPrefix + itoa(pushNow.Add(time.Hour).Unix()), PushedLastPrefix + itoa(target.Unix())}
	}
	target = target.Add(time.Hour)
	if _, err := PlanPush(labels, nil, target, "", lim, pushNow); err == nil || !strings.Contains(err.Error(), "--push-reason") {
		t.Fatalf("4th unexplained push must be refused naming --push-reason: %v", err)
	}
}

func TestPushRefusedPastDaysOfSlide(t *testing.T) {
	lim := PushLimits{Count: 0, Days: 7}
	first := pushNow.Add(time.Hour)
	labels := []string{untilLabel(first)}
	if _, err := PlanPush(labels, nil, first.Add(6*24*time.Hour), "", lim, pushNow); err != nil {
		t.Fatalf("6 days of slide is fine: %v", err)
	}
	if _, err := PlanPush(labels, nil, first.Add(8*24*time.Hour), "", lim, pushNow); err == nil {
		t.Fatal("8 days of slide must be refused without a reason")
	}
}

func TestPushReasonBelowLimitIsCommentOnly(t *testing.T) {
	lim := PushLimits{Count: 3, Days: 7}
	labels := []string{untilLabel(pushNow.Add(time.Hour))}
	p, err := PlanPush(labels, nil, pushNow.Add(5*time.Hour), "review moved to Thursday by the owner", lim, pushNow)
	if err != nil || p.Counted || len(p.Add) != 0 || !strings.HasPrefix(p.Comment, "push reason (to ") {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestPushReasonRules(t *testing.T) {
	lim := PushLimits{Count: 3, Days: 7}
	labels := []string{untilLabel(pushNow.Add(time.Hour))}
	if _, err := PlanPush(labels, nil, pushNow.Add(5*time.Hour), "too short", lim, pushNow); err == nil {
		t.Fatal("a reason under 15 characters is refused")
	}
	prior := []Comment{{Text: "push reason (to 2026-01-01T00:00:00Z): Review moved to  Thursday by the owner"}}
	if _, err := PlanPush(labels, prior, pushNow.Add(5*time.Hour), "review moved to thursday by the owner", lim, pushNow); err == nil || !strings.Contains(err.Error(), "verbatim") {
		t.Fatalf("a repeated reason is refused: %v", err)
	}
	// a reason on a move that is no push is not judged
	if p, err := PlanPush(labels, nil, pushNow.Add(time.Hour), "x", lim, pushNow); err != nil || p.Counted || p.Comment != "" {
		t.Fatalf("no push: %+v %v", p, err)
	}
}

func TestPushPastThresholdWithReasonCountsAndFlagsOnce(t *testing.T) {
	lim := PushLimits{Count: 3, Days: 7}
	t0 := pushNow.Add(time.Hour)
	labels := []string{untilLabel(t0.Add(3 * time.Hour)), "pushed:3", PushedFirstPrefix + itoa(t0.Unix()), PushedLastPrefix + itoa(t0.Add(3*time.Hour).Unix())}
	p, err := PlanPush(labels, nil, t0.Add(4*time.Hour), "the vendor confirmed the new window", lim, pushNow)
	if err != nil || !p.Counted || !p.NewlyFlag || p.N != 4 || !slices.Contains(p.Add, PushedFlaggedLabel) || !slices.Contains(p.Remove, "pushed:3") {
		t.Fatalf("%+v %v", p, err)
	}
	flagged := append(slices.Clone(labels), PushedFlaggedLabel)
	p, err = PlanPush(flagged, nil, t0.Add(5*time.Hour), "the vendor confirmed another window", lim, pushNow)
	if err != nil || !p.Counted || p.NewlyFlag || slices.Contains(p.Add, PushedFlaggedLabel) {
		t.Fatalf("already flagged: no second mail: %+v %v", p, err)
	}
}

func TestPushOnlyUntilAndParkedUntilCount(t *testing.T) {
	lim := PushLimits{Count: 1, Days: 1}
	// ci, review, user waits and the Manager-to-close wait give no previous target
	for _, l := range []string{WaitLabelPrefix + "1:ci:4101", WaitLabelPrefix + "1:user", WaitLabelPrefix + "1:review:bob",
		WaitLabelPrefix + "1:until:" + itoa(pushNow.Unix()) + ":" + EscapeLabel(ManagerCloseWait)} {
		if !PrevTarget([]string{l}).IsZero() {
			t.Fatalf("%s must not give a previous target", l)
		}
	}
	if got := UntilTarget([]Wait{{Kind: WaitCI, PR: 1}, {Kind: WaitUntil, Ref: ManagerCloseWait, Until: pushNow}}); !got.IsZero() {
		t.Fatalf("UntilTarget: %v", got)
	}
	// Every Manager custody receipt is the same: the Manager's first real until:
	// on a step a close released (Manager to start) or an approval released
	// (Manager dispatch) is no push of anything.
	for _, ref := range []string{ManagerStartWait, ManagerDispatchWait} {
		receipt := Wait{Kind: WaitUntil, Ref: ref, Since: pushNow, Until: pushNow.Add(time.Hour)}
		if !PrevTarget([]string{receipt.Label()}).IsZero() || !UntilTarget([]Wait{receipt}).IsZero() {
			t.Fatalf("%s gave a previous target", ref)
		}
		if p, err := PlanPush([]string{receipt.Label()}, nil, pushNow.Add(3*24*time.Hour), "", lim, pushNow); err != nil || p.Counted {
			t.Fatalf("first wait after %s counted as a push: %+v %v", ref, p, err)
		}
	}
	// parked-until is a previous target
	if p, _ := PlanPush([]string{ParkedUntilPrefix + itoa(pushNow.Unix())}, nil, pushNow.Add(time.Hour), "", lim, pushNow); !p.Counted {
		t.Fatalf("a later park date is a push: %+v", p)
	}
}

func TestPushApplyAndClear(t *testing.T) {
	ctx := context.Background()
	m := &MemClient{}
	m.Seed(Bead{ID: "w.1", Status: "open", Labels: []string{"keep", "pushed:2", "pushed-first:5", "pushed-last:9", PushedFlaggedLabel}})
	p, err := PlanPush([]string{"pushed:2", "pushed-first:5", "pushed-last:9", untilLabel(pushNow.Add(time.Hour))}, nil, pushNow.Add(3*time.Hour), "", PushLimits{Count: 9}, pushNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPush(ctx, m, "d", "w.1", p); err != nil {
		t.Fatal(err)
	}
	b, _ := m.Show(ctx, "d", "w.1")
	st := PushStateOf(b.Labels)
	if st.Count != 3 || st.First != 5 || st.Last != pushNow.Add(3*time.Hour).Unix() || !slices.Contains(b.Labels, "keep") {
		t.Fatalf("%+v %v", st, b.Labels)
	}
	if cleared, err := ClearPush(ctx, m, "d", "w.1"); err != nil || !cleared {
		t.Fatal(cleared, err)
	}
	b, _ = m.Show(ctx, "d", "w.1")
	if !slices.Equal(b.Labels, []string{"keep"}) {
		t.Fatalf("cleared: %v", b.Labels)
	}
}
