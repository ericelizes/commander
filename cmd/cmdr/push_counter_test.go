package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

func pushFixture(t *testing.T, role string, labels ...string) *beads.MemClient {
	t.Helper()
	t.Setenv("COMMANDER_ROLE", role)
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "ms-1.2", Title: "Reconcile", Status: "in_progress", Labels: append([]string{"kind:objective"}, labels...)})
	setupObjectiveProject(t, m)
	return m
}

// pushStep keeps every target under 4h, where a wait is not held for Commander's review.
const pushStep = 15 * time.Minute

func untilArg(d time.Duration) string {
	return "until:" + time.Now().Add(d).UTC().Format(time.RFC3339) + ":the nightly reconcile finishes"
}

func setUntil(t *testing.T, d time.Duration, extra ...string) (int, string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	args := append([]string{"--project", "manifest", "--bead", "ms-1.2", "--waiting", untilArg(d)}, extra...)
	code := runInternalSetObjective(args, stdout, stderr)
	_, e := read()
	return code, e
}

func labelsOf(m *beads.MemClient) []string {
	b, _ := m.Show(nil, "", "ms-1.2")
	return b.Labels
}

func TestPushCounterThroughWaitingUntil(t *testing.T) {
	m := pushFixture(t, "manager-ms-1")
	if code, e := setUntil(t, 2*pushStep); code != 0 {
		t.Fatalf("first wait: %s", e)
	}
	if beads.PushStateOf(labelsOf(m)).Count != 0 {
		t.Fatal("the first wait is no push")
	}
	for i, d := range []time.Duration{4, 6, 8} {
		if code, e := setUntil(t, d*pushStep); code != 0 {
			t.Fatalf("push %d: %s", i+1, e)
		}
	}
	st := beads.PushStateOf(labelsOf(m))
	if st.Count != 3 || st.First == 0 || st.Last == 0 || st.Flagged {
		t.Fatalf("after three pushes: %+v", st)
	}
	before := slices.Clone(labelsOf(m))
	code, e := setUntil(t, 10*pushStep)
	if code == 0 || !strings.Contains(e, "--push-reason") {
		t.Fatalf("the fourth unexplained push is refused: %d %s", code, e)
	}
	if !slices.Equal(before, labelsOf(m)) {
		t.Fatalf("a refused push writes nothing: %v -> %v", before, labelsOf(m))
	}
}

func TestPushReasonPastThresholdFlagsAndMailsCommanderOnce(t *testing.T) {
	mailer := stubMail(t)
	m := pushFixture(t, "manager-ms-1")
	for _, d := range []time.Duration{2, 4, 6, 8} {
		if code, e := setUntil(t, d*pushStep); code != 0 {
			t.Fatalf("setup: %s", e)
		}
	}
	reason := "the vendor confirmed the new window"
	if code, e := setUntil(t, 10*pushStep, "--push-reason", reason); code != 0 {
		t.Fatalf("with a reason it goes through: %s", e)
	}
	if st := beads.PushStateOf(labelsOf(m)); !st.Flagged || st.Count != 4 {
		t.Fatalf("flagged: %+v", st)
	}
	if n := len(mailer.sent); n != 1 {
		t.Fatalf("Commander mailed once, got %d", n)
	}
	if code, e := setUntil(t, 12*pushStep, "--push-reason", "the vendor moved the window again"); code != 0 {
		t.Fatalf("second flagged push: %s", e)
	}
	if n := len(mailer.sent); n != 1 {
		t.Fatalf("no second mail once flagged, got %d", n)
	}
}

func TestPushReasonBelowThresholdIsACommentOnly(t *testing.T) {
	m := pushFixture(t, "manager-ms-1")
	_, _ = setUntil(t, 2*pushStep)
	if code, e := setUntil(t, 5*pushStep, "--push-reason", "the owner is out until the afternoon"); code != 0 {
		t.Fatalf("%s", e)
	}
	if st := beads.PushStateOf(labelsOf(m)); st.Count != 0 {
		t.Fatalf("an explained push does not count: %+v", st)
	}
	cs, _ := m.Comments(nil, "", "ms-1.2")
	if len(cs) != 1 || !strings.Contains(cs[0].Text, "the owner is out until the afternoon") {
		t.Fatalf("comments: %+v", cs)
	}
	if code, _ := setUntil(t, 8*pushStep, "--push-reason", "the owner is out until the afternoon"); code == 0 {
		t.Fatal("the same reason again is refused")
	}
}

func TestPushNonUntilWaitsNeverCount(t *testing.T) {
	m := pushFixture(t, "manager-ms-1")
	stdout, stderr, _ := withTempOutputFiles(t)
	for _, w := range []string{"ci:#4101", "main-ci:abc1234", "ci:#4104"} {
		if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.2", "--waiting", w}, stdout, stderr); code != 0 {
			t.Fatalf("wait %s", w)
		}
	}
	for _, l := range labelsOf(m) {
		if strings.HasPrefix(l, "pushed") {
			t.Fatalf("non-until waits must not count: %s", l)
		}
	}
}

func TestPushParkUntilCounts(t *testing.T) {
	m := pushFixture(t, "manager-ms-1")
	park := func(until string, extra ...string) (int, string) {
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalSetObjective(append([]string{"--project", "manifest", "--bead", "ms-1.2", "--park", "--until", until}, extra...), stdout, stderr)
		_, e := read()
		return code, e
	}
	for _, u := range []string{"2h", "4h", "6h", "8h"} {
		if code, e := park(u); code != 0 {
			t.Fatalf("park %s: %s", u, e)
		}
	}
	if st := beads.PushStateOf(labelsOf(m)); st.Count != 3 {
		t.Fatalf("%+v", st)
	}
	if code, e := park("10h"); code == 0 || !strings.Contains(e, "--push-reason") {
		t.Fatalf("the fourth park push is refused: %d %s", code, e)
	}
}

func TestPushLabelsClearedOnClose(t *testing.T) {
	m := pushFixture(t, "manager-ms-1", "pushed:2", "pushed-first:5", "pushed-last:9", "pushed-flagged")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "ms-1.2", "--reason", "done"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("close: %s", e)
	}
	for _, l := range labelsOf(m) {
		if strings.HasPrefix(l, "pushed") {
			t.Fatalf("label left after close: %s", l)
		}
	}
}

func TestPushResetOnlyCommanderAndUser(t *testing.T) {
	for role, ok := range map[string]bool{"manager-ms-1": false, "operator-x": false, "commander": true, "": true} {
		t.Run(fmt.Sprintf("role=%q", role), func(t *testing.T) {
			m := pushFixture(t, role, "pushed:3", "pushed-first:5", "pushed-last:9", "pushed-flagged")
			stdout, stderr, _ := withTempOutputFiles(t)
			code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.2", "--push-reset"}, stdout, stderr)
			left := beads.PushStateOf(labelsOf(m))
			if ok && (code != 0 || left.Count != 0 || left.Flagged) {
				t.Fatalf("reset failed: %d %+v", code, left)
			}
			if !ok && (code == 0 || left.Count != 3) {
				t.Fatalf("reset must be refused: %d %+v", code, left)
			}
		})
	}
}

func TestPushFailsOpenOnAnUnreadableBead(t *testing.T) {
	pushFixture(t, "manager-ms-1")
	m := &beads.MemClient{} // the bead does not exist: Show finds nothing
	setupObjectiveProject(t, m)
	plan, err := planBeadPush(nil, m, "", "ghost", time.Now().Add(time.Hour), "")
	if err != nil || plan.Counted || plan.Comment != "" {
		t.Fatalf("%+v %v", plan, err)
	}
}
