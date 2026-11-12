package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type fakeProjects struct{ typ string }

func (f fakeProjects) ListProjects(context.Context) ([]store.Project, error) {
	typ := f.typ
	if typ == "" {
		typ = "code"
	}
	return []store.Project{{ID: 1, Name: "p", Path: "/p", Type: typ}}, nil
}

var epoch = time.Unix(1_000_000, 0)

func watcher(b *beads.MockClient, gh map[string]string, sent *[]sentMail, now time.Time) *waitWatcher {
	if b.ShowResult == nil && b.ShowResults == nil {
		b.ShowResults = map[string]*beads.Bead{}
		for i := range b.ListResult {
			b.ShowResults[b.ListResult[i].ID] = &b.ListResult[i]
		}
	}
	return &waitWatcher{
		projects: fakeProjects{}, beads: b, woken: map[string]bool{},
		gh: func(_ context.Context, args ...string) ([]byte, error) {
			out, ok := gh[strings.Join(args[:2], " ")]
			if !ok {
				return nil, fmt.Errorf("gh %v", args)
			}
			return []byte(out), nil
		},
		mail: func(_ context.Context, to, subject, _ string) error {
			*sent = append(*sent, sentMail{to, subject})
			return nil
		},
		repoOf: func(string) string { return "acme/web" },
		now:    func() time.Time { return now },
	}
}

func waitBead(labels ...string) *beads.MockClient {
	return &beads.MockClient{ListResult: []beads.Bead{{ID: "p-ws.1", Status: "in_progress", Labels: labels}}}
}

func TestCIWaitWakesOnceWhenChecksSettle(t *testing.T) {
	b := waitBead("waiting:1000000:ci:4101")
	var sent []sentMail
	w := watcher(b, map[string]string{"pr checks": `[{"name":"a","bucket":"pass"},{"name":"b","bucket":"pending"}]`}, &sent, epoch.Add(time.Minute))
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke while a check was pending: %+v", sent)
	}
	w.gh = func(context.Context, ...string) ([]byte, error) {
		return []byte(`[{"bucket":"pass"},{"bucket":"fail"},{"bucket":"cancel"}]`), nil
	}
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || sent[0].subject != "[ci] #4101 finished: 2 failing" {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "label=waiting:1000000:ci:4101|on=false") {
		t.Errorf("wait not cleared: %v", b.Calls)
	}
}

func TestCIAllGreen(t *testing.T) {
	var sent []sentMail
	w := watcher(waitBead("waiting:1:ci:4101"), map[string]string{"pr checks": `[{"bucket":"pass"},{"bucket":"skipping"}]`}, &sent, epoch)
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[ci] #4101 finished: all green" {
		t.Fatalf("mail: %+v", sent)
	}
}

// A mail that went out but whose clear failed must not wake again.
func TestFailedClearDoesNotWakeTwice(t *testing.T) {
	b := waitBead("waiting:1:ci:7")
	b.SetErr = fmt.Errorf("br down")
	var sent []sentMail
	w := watcher(b, map[string]string{"pr checks": `[{"bucket":"pass"}]`}, &sent, epoch)
	w.poll(context.Background())
	w.poll(context.Background())
	if len(sent) != 1 {
		t.Fatalf("woke %d times", len(sent))
	}
}

func TestReviewWaitWakesOnAnyReviewSinceTheWaitBegan(t *testing.T) {
	reviews := `{"state":"OPEN","reviews":[
	  {"author":{"login":"reviewer-a"},"state":"APPROVED","submittedAt":"1970-01-01T00:00:10Z"},
	  {"author":{"login":"reviewer-b"},"state":"COMMENTED","submittedAt":"2030-01-01T00:00:00Z"}]}`
	// reviewer-a approved before this wait began: that is not news. reviewer-b's comment
	// came after it: a comment-only review is still the reviewer's move made.
	b := waitBead("in-review", "pr:42", "waiting:100:review:reviewer-a", "waiting:100:review:reviewer-b")
	var sent []sentMail
	w := watcher(b, map[string]string{"pr view": reviews}, &sent, time.Unix(200, 0))
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[review] reviewer-b commented on #42" {
		t.Fatalf("mail: %+v", sent)
	}
	calls := strings.Join(b.Calls, "\n")
	if !strings.Contains(calls, "waiting:100:review:reviewer-b|on=false") || strings.Contains(calls, "waiting:100:review:reviewer-a|on=false") {
		t.Errorf("only reviewer-b's wait should clear:\n%s", calls)
	}
	if f := w.ReviewFacts()["p-ws.1"]; !f.Acted || f.Text != "reviewer-b commented on #42" {
		t.Errorf("fact = %+v", f)
	}
}

func TestChangesRequested(t *testing.T) {
	var sent []sentMail
	w := watcher(waitBead("pr:9", "in-review", "waiting:5:review:reviewer-a"), map[string]string{"pr view": `{"reviews":[
	  {"author":{"login":"reviewer-a"},"state":"APPROVED","submittedAt":"1970-02-01T00:00:00Z"},
	  {"author":{"login":"reviewer-a"},"state":"CHANGES_REQUESTED","submittedAt":"1970-03-01T00:00:00Z"}]}`}, &sent, time.Unix(200, 0))
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[review] reviewer-a requested changes on #9" {
		t.Fatalf("mail: %+v", sent)
	}
}

func TestReviewFactWaitingAndNoPR(t *testing.T) {
	var sent []sentMail
	pending := waitBead("pr:7", "in-review", "waiting:5:review:reviewer-a")
	w := watcher(pending, map[string]string{"pr view": `{"reviews":[]}`}, &sent, time.Unix(200, 0))
	w.poll(context.Background())
	if f := w.ReviewFacts()["p-ws.1"]; f.Acted || f.NoPR || f.Text != "waiting on reviewer-a (#7)" {
		t.Errorf("pending fact = %+v", f)
	}
	nopr := waitBead("waiting:5:review:reviewer-a")
	w = watcher(nopr, nil, &sent, time.Unix(200, 0))
	w.poll(context.Background())
	if f := w.ReviewFacts()["p-ws.1"]; !f.NoPR {
		t.Errorf("a review wait with no PR must say so: %+v", f)
	}
}

func TestQuietPollRecordsCheckedInMemoryAndNeverMails(t *testing.T) {
	label := fmt.Sprintf("waiting:%d:user", epoch.Unix())
	var sent []sentMail
	b := waitBead(label, "wait-nudged", "wait-checked:5")
	now := epoch.Add(9 * time.Hour)
	w := watcher(b, nil, &sent, now)
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("mailed with no event: %+v", sent)
	}
	calls := strings.Join(b.Calls, "\n")
	for _, want := range []string{"label=wait-checked:5|on=false", "label=wait-nudged|on=false"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in %v", want, b.Calls)
		}
	}
	// The poll time lives in cmdrd, not on the bead: no write to the shared beads db.
	if strings.Contains(calls, "on=true") {
		t.Errorf("a quiet poll wrote a label: %v", b.Calls)
	}
	if !w.CheckedAt().Equal(now) {
		t.Errorf("CheckedAt = %v, want %v", w.CheckedAt(), now)
	}
}

func TestClearedWaitsDropTheCheckedStamp(t *testing.T) {
	var sent []sentMail
	b := waitBead("wait-checked:5")
	w := watcher(b, nil, &sent, epoch)
	w.poll(context.Background())
	if len(b.Calls) == 0 || !strings.Contains(strings.Join(b.Calls, "\n"), "label=wait-checked:5|on=false") {
		t.Fatalf("stale stamp kept: %v", b.Calls)
	}
}

func TestClosedBeadsAndOtherLabelsAreIgnored(t *testing.T) {
	b := &beads.MockClient{ListResult: []beads.Bead{{ID: "p-ws.1", Status: "closed", Labels: []string{"waiting:1:ci:5"}}, {ID: "p-ws.2", Status: "open", Labels: []string{"owner:x"}}}}
	var sent []sentMail
	watcher(b, map[string]string{"pr checks": `[{"bucket":"pass"}]`}, &sent, epoch).poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("mail: %+v", sent)
	}
}

const mainCIFullSha = "349a7f4e1f0c2b6d8a9e3c5b7d1f2a4c6e8b0d13"

// realGH answers as gh does through runGH: run list matches --commit against
// whole shas only (a short one is 0 runs and no error), commits/<sha> resolves
// a prefix, and an API error's body arrives on stdout with no Go error.
func realGH(runs string) ghRunner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		if sha, ok := strings.CutPrefix(cmd, "run list --repo acme/web --commit "); ok {
			if strings.HasPrefix(sha, mainCIFullSha+" ") {
				return []byte(runs), nil
			}
			return []byte(`[]`), nil
		}
		if rest, ok := strings.CutPrefix(cmd, "api repos/acme/web/commits/"); ok {
			if strings.HasPrefix(mainCIFullSha, strings.Fields(rest)[0]) {
				return []byte(mainCIFullSha + "\n"), nil
			}
			return []byte(`{"message":"No commit found for SHA: x","status":"422"}`), nil
		}
		return nil, fmt.Errorf("gh %v", args)
	}
}

func TestMainCIWaitWakesOnceRunsFinish(t *testing.T) {
	b := waitBead("waiting:1000000:main-ci:349a7f4")
	var sent []sentMail
	w := watcher(b, nil, &sent, epoch)
	w.gh = realGH(`[]`)
	w.poll(context.Background())
	w.gh = realGH(`[{"status":"completed","conclusion":"success"},{"status":"in_progress","conclusion":""}]`)
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke with no runs / a run in progress: %+v", sent)
	}
	w.gh = realGH(`[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure"}]`)
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || sent[0].subject != "[main-ci] 349a7f4 finished: 1 failing" {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "label=waiting:1000000:main-ci:349a7f4|on=false") {
		t.Errorf("wait not cleared: %v", b.Calls)
	}
}

// Waits recorded in one command share a Since: the one that fired is the one
// that clears, and it mails once.
func TestTwoMainCIWaitsClearTheOneThatFired(t *testing.T) {
	const other, done = "waiting:1000000:main-ci:aa11bb2", "waiting:1000000:main-ci:349a7f4"
	b := waitBead(other, done)
	var sent []sentMail
	w := watcher(b, nil, &sent, epoch)
	w.gh = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "aa11bb2") {
			return []byte("aa11bb2" + strings.Repeat("0", 33) + "\n"), nil
		}
		return realGH(`[{"status":"completed","conclusion":"success"}]`)(ctx, args...)
	}
	w.poll(context.Background())
	calls := strings.Join(b.Calls, "\n")
	if len(sent) != 1 || sent[0].subject != "[main-ci] 349a7f4 finished: all green" {
		t.Fatalf("mail: %+v", sent)
	}
	if strings.Contains(calls, "label="+other+"|on=false") || !strings.Contains(calls, "label="+done+"|on=false") {
		t.Fatalf("cleared the wrong wait: %v", calls)
	}
}

// A sha GitHub says does not exist can never settle: past the grace (it may
// not be pushed yet) the Manager hears so once and the wait clears.
func TestMainCIWaitOnAnUnknownShaWakesAfterTheGrace(t *testing.T) {
	label := "waiting:1000000:main-ci:deadbee"
	b := waitBead(label)
	var sent []sentMail
	w := watcher(b, nil, &sent, epoch.Add(mainCIMissingGrace-time.Second))
	w.gh = realGH(`[]`)
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke inside the grace: %+v", sent)
	}
	w.now = func() time.Time { return epoch.Add(mainCIMissingGrace) }
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || sent[0].subject != "[main-ci] deadbee not found in acme/web" {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "label="+label+"|on=false") {
		t.Errorf("wait not cleared: %v", b.Calls)
	}
}

// A read that failed says nothing about the commit: GitHub down or a rate
// limit, however long it lasts, never reads as "no such commit".
func TestMainCIWaitSurvivesAFailedRead(t *testing.T) {
	for name, gh := range map[string]ghRunner{
		"no answer": func(context.Context, ...string) ([]byte, error) { return nil, fmt.Errorf("dial failed") },
		"rate limit": func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"message":"API rate limit exceeded","status":"403"}`), nil
		},
		"server": func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"message":"Server Error","status":"502"}`), nil
		},
		"not json": func(context.Context, ...string) ([]byte, error) { return []byte("oops"), nil },
	} {
		t.Run(name, func(t *testing.T) {
			b := waitBead("waiting:1000000:main-ci:349a7f4")
			var sent []sentMail
			w := watcher(b, nil, &sent, epoch.Add(24*time.Hour))
			w.gh = gh
			w.poll(context.Background())
			if len(sent) != 0 || strings.Contains(strings.Join(b.Calls, "\n"), "on=false") {
				t.Fatalf("mail %+v, calls %v", sent, b.Calls)
			}
		})
	}
}

func TestProdTagWaitWakesWhenATagContainsTheCommit(t *testing.T) {
	b := waitBead("waiting:1000000:prod-tag:other:repo:349a7f4")
	var sent []sentMail
	w := watcher(b, nil, &sent, epoch)
	w.prodTagPrefix = "release-"
	status := map[string]string{"release-1": "behind", "release-2": "diverged"}
	w.gh = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case strings.HasSuffix(args[1], "/git/matching-refs/tags/release-"):
			if args[1] != "repos/other/repo/git/matching-refs/tags/release-" {
				return nil, fmt.Errorf("gh %v", args)
			}
			return []byte(`[{"ref":"refs/tags/release-1"},{"ref":"refs/tags/release-2"}]`), nil
		case strings.Contains(args[1], "/compare/349a7f4..."):
			return []byte(status[args[1][strings.LastIndex(args[1], "...")+3:]] + "\n"), nil
		}
		return nil, fmt.Errorf("gh %v", args)
	}
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke before a tag contained the commit: %+v", sent)
	}
	status["release-2"] = "ahead"
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[prod-tag] 349a7f4 is in release-2" {
		t.Fatalf("mail: %+v", sent)
	}
}

func TestUntilWaitWakesOnceAtTargetWithoutGH(t *testing.T) {
	label := beads.Wait{Kind: beads.WaitUntil, Since: epoch, Until: epoch.Add(10 * time.Minute), Ref: "12:00 UTC reconcile"}.Label()
	b := waitBead(label)
	var sent []sentMail
	w := watcher(b, nil, &sent, epoch.Add(9*time.Minute))
	w.pollUntil(context.Background())
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke before the target: %+v", sent)
	}
	if strings.Contains(strings.Join(b.Calls, "\n"), "wait-checked") {
		t.Fatalf("a time wait was stamped as checked: %v", b.Calls)
	}
	w.now = func() time.Time { return epoch.Add(10*time.Minute + 30*time.Second) }
	w.pollUntil(context.Background())
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || sent[0].subject != "[wait] 12:00 UTC reconcile time reached" {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "label="+label+"|on=false") {
		t.Fatalf("wait not cleared: %v", b.Calls)
	}
}

// Waits in a knowledge project (no worktrees, like a docs-only project) fire too:
// the poller used to skip every project that was not "code".
func TestUntilWaitFiresInAKnowledgeProject(t *testing.T) {
	label := beads.Wait{Kind: beads.WaitUntil, Since: epoch, Until: epoch.Add(time.Minute), Ref: "reconcile"}.Label()
	b := waitBead(label)
	var sent []sentMail
	w := watcher(b, nil, &sent, epoch.Add(2*time.Minute))
	w.projects = fakeProjects{typ: "knowledge"}
	w.pollUntil(context.Background())
	if len(sent) != 1 {
		t.Fatalf("knowledge project's time wait did not fire: %+v", sent)
	}
}

func TestParkUntilExpiryUnparksAndMailsOnce(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "p-ws.1", Title: "Merge approval", Status: "open", Labels: []string{"gate", "needs-user"}})
	ctx := context.Background()
	if err := beads.Park(ctx, m, "/p", "p-ws.1", "manager-p-ws", "CI red", epoch.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var sent []sentMail
	w := watcher(&beads.MockClient{}, nil, &sent, epoch.Add(9*time.Minute))
	w.beads = m
	w.pollUntil(ctx)
	if b, _ := m.Show(ctx, "/p", "p-ws.1"); len(sent) != 0 || b.Status != beads.StatusDeferred {
		t.Fatalf("un-parked early: %+v %+v", sent, b)
	}
	w.now = func() time.Time { return epoch.Add(11 * time.Minute) }
	w.pollUntil(ctx)
	w.pollUntil(ctx)
	b, _ := m.Show(ctx, "/p", "p-ws.1")
	if b.Status != "open" || beads.ParkedUntil(b.Labels) != 0 {
		t.Fatalf("not un-parked: %+v", b)
	}
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || sent[0].subject != "[parked] parked ask p-ws.1 is due again" {
		t.Fatalf("mail: %+v", sent)
	}
	// A gate with its card holds itself: no dispatch wait beside it.
	if len(beads.WaitsOf(b.Labels)) != 0 {
		t.Fatalf("a card's gate got a wait: %v", b.Labels)
	}
}

// A parked step with nothing of its own to hold it comes back held by the Manager dispatch wait,
// written in the unpark's own update.
func TestUnparkHoldsAStepWithNoOtherHolder(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "p-ws.1", Title: "Port the importer", Status: "open", Labels: []string{"kind:objective"}})
	ctx := context.Background()
	if err := beads.Park(ctx, m, "/p", "p-ws.1", "manager-p-ws", "after the release", epoch.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var sent []sentMail
	w := watcher(&beads.MockClient{}, nil, &sent, epoch.Add(11*time.Minute))
	w.beads = m
	w.pollUntil(ctx)
	b, _ := m.Show(ctx, "/p", "p-ws.1")
	if ws := beads.WaitsOf(b.Labels); b.Status != "open" || len(ws) != 1 || ws[0].Ref != beads.ManagerDispatchWait {
		t.Fatalf("unparked unheld: %s %v", b.Status, b.Labels)
	}
}

// A review wait on a PR that merged (or closed) unreviewed can never be
// answered: it clears instead of waiting forever.
func TestReviewWaitClearsWhenThePRMerges(t *testing.T) {
	b := waitBead("pr:42", "in-review", "waiting:100:review:reviewer-a")
	var sent []sentMail
	w := watcher(b, map[string]string{"pr view": `{"state":"MERGED","reviews":[]}`}, &sent, time.Unix(200, 0))
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[review] #42 merged without reviewer-a's review" {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "waiting:100:review:reviewer-a|on=false") {
		t.Errorf("wait not cleared: %v", b.Calls)
	}
}

func TestReviewWaitOnAnOpenPRKeepsWaiting(t *testing.T) {
	var sent []sentMail
	w := watcher(waitBead("pr:42", "waiting:100:review:reviewer-a"), map[string]string{"pr view": `{"state":"OPEN","reviews":[]}`}, &sent, time.Unix(200, 0))
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke on an open, unreviewed PR: %+v", sent)
	}
}

func TestOperatorWaitClearsWhenTheOperatorStops(t *testing.T) {
	b := waitBead("waiting:100:operator:operator-op-build")
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(200, 0))
	running := map[string]bool{"operator-op-build": true}
	w.running = func(context.Context) (map[string]bool, error) { return running, nil }
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("woke while the Operator runs: %+v", sent)
	}
	running = map[string]bool{}
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[operator] operator-op-build is no longer running" {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "waiting:100:operator:operator-op-build|on=false") {
		t.Errorf("wait not cleared: %v", b.Calls)
	}
}

// The running set is read once at the start of a poll. An Operator spawned during the poll (its wait
// written after the read) is not in it: the watcher must read again before it clears the wait.
func TestOperatorWaitWrittenMidPollIsNotCleared(t *testing.T) {
	b := waitBead("waiting:100:operator:operator-op-new")
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(200, 0))
	reads := 0
	w.running = func(context.Context) (map[string]bool, error) {
		reads++
		if reads == 1 {
			return map[string]bool{}, nil // the capture at the start of the poll, before the spawn
		}
		return map[string]bool{"operator-op-new": true}, nil
	}
	w.poll(context.Background())
	if len(sent) != 0 || strings.Contains(strings.Join(b.Calls, "\n"), "|on=false") {
		t.Fatalf("a running Operator's wait was judged against a stale set: mail %+v calls %v", sent, b.Calls)
	}
	if reads < 2 {
		t.Fatalf("the set was not read again before clearing: %d reads", reads)
	}
}

// The list can come from an export behind the database: a wait the Manager
// already replaced still shows there. The database decides; nothing is mailed
// or written for it.
func TestWaitGoneFromTheDatabaseIsNotMailed(t *testing.T) {
	b := waitBead("w:tnql40:op:op-build-sample-example-app-t012-2-1")
	b.ShowResult = &beads.Bead{ID: "p-ws.1", Status: "in_progress", Labels: []string{"w:tnql41:op:op-next"}}
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(1_800_000_000, 0))
	w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{"operator-op-next": true}, nil }
	for i := 0; i < 3; i++ {
		w.poll(context.Background())
	}
	if len(sent) != 0 {
		t.Fatalf("mailed a wait that is gone: %+v", sent)
	}
	if strings.Contains(strings.Join(b.Calls, "\n"), "on=false") {
		t.Errorf("wrote for a wait that is gone: %v", b.Calls)
	}
}

// A closed task in the database mails nothing and writes nothing for a wait the
// list still shows (the incident: mail about an Operator of a closed task).
func TestWaitOnAClosedBeadIsNotMailed(t *testing.T) {
	label := "w:tnql40:op:op-build-sample-example-app-t012-2-1"
	b := waitBead(label)
	b.ShowResult = &beads.Bead{ID: "p-ws.1", Status: "closed", Labels: []string{label}}
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(1_800_000_000, 0))
	w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	for i := 0; i < 3; i++ {
		w.poll(context.Background())
	}
	if len(sent) != 0 {
		t.Fatalf("mailed a wait on a closed bead: %+v", sent)
	}
	if strings.Contains(strings.Join(b.Calls, "\n"), "on=false") {
		t.Errorf("wrote for a closed bead: %v", b.Calls)
	}
}

// The wait fires once even while reads keep showing its label after the clear
// (cmdrd saw this once a minute until the export caught up).
func TestOperatorWaitFiresOnceWhileReadsStayStale(t *testing.T) {
	label := "w:tnql40:op:op-build-sample-example-app-t012-2-1"
	b := waitBead(label)
	b.ShowResult = &beads.Bead{ID: "p-ws.1", Status: "in_progress", Labels: []string{label}}
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(1_800_000_000, 0))
	w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	for i := 0; i < 3; i++ {
		w.poll(context.Background())
	}
	if len(sent) != 1 || !strings.Contains(sent[0].subject, "is no longer running") {
		t.Fatalf("mail: %+v", sent)
	}
	if !strings.Contains(strings.Join(b.Calls, "\n"), "label="+label+"|on=false") {
		t.Errorf("wait not cleared: %v", b.Calls)
	}
}

// A database that cannot be read mails nothing now; the next poll decides.
func TestWaitMailsNothingWhenTheFreshReadFails(t *testing.T) {
	b := waitBead("waiting:100:operator:operator-op-build")
	b.ShowErr = fmt.Errorf("busy")
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(200, 0))
	w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("mail on a failed read: %+v", sent)
	}
	b.ShowErr = nil
	w.poll(context.Background())
	if len(sent) != 1 {
		t.Fatalf("no mail once readable: %+v", sent)
	}
}

// Without an answer on who runs, an operator wait is left alone.
func TestOperatorWaitUntouchedWhenRunningIsUnknown(t *testing.T) {
	var sent []sentMail
	w := watcher(waitBead("waiting:100:operator:operator-op-build"), nil, &sent, time.Unix(200, 0))
	w.running = func(context.Context) (map[string]bool, error) { return nil, fmt.Errorf("store down") }
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("cleared on an unknown: %+v", sent)
	}
}

func TestParkDueMailsBatchedPerManagerOnce(t *testing.T) {
	m := &beads.MemClient{}
	ctx := context.Background()
	ids := []string{"p-a.1", "p-a.2", "p-a.3", "p-b.1"}
	for i := 1; i <= 10; i++ {
		ids = append(ids, fmt.Sprintf("p-a.1%d", i))
	}
	for _, id := range ids {
		m.Seed(beads.Bead{ID: id, Title: "ask " + id, Status: "open", Labels: []string{"gate"}})
		if err := beads.Park(ctx, m, "/p", id, "manager", "", epoch.Add(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var sent []sentMail
	w := watcher(&beads.MockClient{}, nil, &sent, epoch.Add(11*time.Minute))
	w.beads = m
	bodies := map[string]string{}
	w.mail = func(_ context.Context, to, subject, body string) error {
		sent = append(sent, sentMail{to, subject})
		bodies[to] = body
		return nil
	}
	w.pollUntil(ctx)
	w.pollUntil(ctx)
	if len(sent) != 2 {
		t.Fatalf("want one mail per Manager, got %d: %+v", len(sent), sent)
	}
	if body := bodies["manager-p-a"]; !strings.Contains(body, "+3 more") || strings.Count(body, "\n- ") != 10 {
		t.Fatalf("batched body: %s", body)
	}
	for _, id := range ids {
		if b, _ := m.Show(ctx, "/p", id); b.Status != "open" {
			t.Fatalf("%s not un-parked: %+v", id, b)
		}
	}
}

func TestParkRestartBetweenMailAndUnparkDoesNotRemail(t *testing.T) {
	m := &beads.MemClient{}
	ctx := context.Background()
	m.Seed(beads.Bead{ID: "p-ws.1", Title: "ask", Status: "open", Labels: []string{"gate"}})
	if err := beads.Park(ctx, m, "/p", "p-ws.1", "manager", "", epoch.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The mail went out and was stamped, but the un-park never ran.
	if err := m.SetLabel(ctx, "/p", "p-ws.1", beads.ParkedNotifiedPrefix+"1", true); err != nil {
		t.Fatal(err)
	}
	var sent []sentMail
	w := watcher(&beads.MockClient{}, nil, &sent, epoch.Add(11*time.Minute)) // fresh watcher: empty woken set
	w.beads = m
	w.pollUntil(ctx)
	b, _ := m.Show(ctx, "/p", "p-ws.1")
	if len(sent) != 0 || b.Status != "open" || hasLabelPrefix(b.Labels, beads.ParkedNotifiedPrefix) {
		t.Fatalf("sent=%+v bead=%+v", sent, b)
	}
}

// A long-form operator wait over br's label limit re-renders compact; the
// clear must remove the label that is on the bead.
func TestOperatorWaitLongStoredLabelClearsTheStoredLabel(t *testing.T) {
	long := "waiting:100:operator:op-investigate-the-example-app-x-8-17"
	b := waitBead(long)
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(200, 0))
	w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	w.poll(context.Background())
	if len(sent) != 1 || !strings.Contains(strings.Join(b.Calls, "\n"), long+"|on=false") {
		t.Fatalf("sent %+v calls %v", sent, b.Calls)
	}
}

// Over several minute ticks with a client whose SetLabel really removes the label,
// a stopped Operator's wait mails once and the label is gone (long and short form).
func TestOperatorWaitMailsOnceOverManyTicks(t *testing.T) {
	for _, label := range []string{
		"waiting:100:operator:op-investigate-the-example-app-x-8-17",
		"waiting:100:operator:operator-op-build",
	} {
		m := &beads.MemClient{}
		m.Seed(beads.Bead{ID: "p-ws.1", Status: "in_progress", Labels: []string{"kind:objective", label}})
		var sent []sentMail
		w := watcher(&beads.MockClient{}, nil, &sent, time.Unix(200, 0))
		w.beads = m
		w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
		for i := 0; i < 5; i++ {
			w.poll(context.Background())
		}
		if len(sent) != 1 {
			t.Fatalf("%s: want one mail over 5 ticks, got %d: %+v", label, len(sent), sent)
		}
		b, _ := m.Show(context.Background(), "/p", "p-ws.1")
		// The fired wait is gone; the step it alone held is the Manager's (dispatch), never unheld.
		if ws := beads.WaitsOf(b.Labels); slices.Contains(b.Labels, label) || len(ws) != 1 || ws[0].Ref != beads.ManagerDispatchWait {
			t.Errorf("%s: want only the Manager dispatch wait: %v", label, b.Labels)
		}
	}
}

// A wait that fires is cleared in the same write that names the next holder: the Manager dispatch
// wait when nothing else of the bead's own holds it; nothing is added beside another wait or a card.
func TestWakeClearKeepsAHolder(t *testing.T) {
	for name, extra := range map[string][]string{
		"only holder":     nil,
		"another wait":    {"waiting:150:review:reviewer-z"},
		"the user's card": {"needs-user"},
	} {
		t.Run(name, func(t *testing.T) {
			label := "waiting:100:operator:operator-op-build"
			m := &beads.MemClient{}
			m.Seed(beads.Bead{ID: "p-ws.1", Status: "open", Labels: append([]string{"kind:objective", label}, extra...)})
			calls := &beads.MockClient{}
			var sent []sentMail
			w := watcher(calls, nil, &sent, time.Unix(200, 0))
			w.beads = m
			w.running = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
			w.poll(context.Background())
			b, _ := m.Show(context.Background(), "/p", "p-ws.1")
			if slices.Contains(b.Labels, label) {
				t.Fatalf("fired wait stays: %v", b.Labels)
			}
			dispatch := 0
			for _, wt := range beads.WaitsOf(b.Labels) {
				if wt.Ref == beads.ManagerDispatchWait {
					dispatch++
				}
			}
			if want := map[bool]int{true: 1, false: 0}[extra == nil]; dispatch != want {
				t.Fatalf("dispatch waits %d, want %d: %v", dispatch, want, b.Labels)
			}
		})
	}
}

// A PR in another repo (pr:<owner/name>#N) is watched there, not in the project's repo.
func TestReviewWaitOnAPRInAnotherRepo(t *testing.T) {
	b := waitBead("in-review", "pr:acme/infrastructure#4003", "waiting:100:review:reviewer-z")
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(200, 0))
	var repos []string
	w.gh = func(_ context.Context, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "--repo" && i+1 < len(args) {
				repos = append(repos, args[i+1])
			}
		}
		return []byte(`{"state":"OPEN","reviews":[{"author":{"login":"reviewer-z"},"state":"APPROVED","submittedAt":"2030-01-01T00:00:00Z"}]}`), nil
	}
	w.poll(context.Background())
	if len(sent) != 1 || sent[0].subject != "[review] reviewer-z approved #4003" {
		t.Fatalf("mail: %+v", sent)
	}
	for _, r := range repos {
		if r != "acme/infrastructure" {
			t.Errorf("looked in %s, want acme/infrastructure (all: %v)", r, repos)
		}
	}
	if len(repos) == 0 {
		t.Error("gh was never asked about the PR's repo")
	}
}

func TestWaitPRStateFactsReuseExistingPoll(t *testing.T) {
	calls := 0
	w := &waitWatcher{gh: func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte(`{"state":"MERGED","reviews":[]}`), nil
	}}
	w.reviewVerdict(context.Background(), "owner/repo", 12, beads.Wait{})
	facts := w.PRStates()
	if calls != 1 || facts["owner/repo#12"] != "MERGED" {
		t.Fatalf("calls=%d facts=%v", calls, facts)
	}
	facts["owner/repo#12"] = "CLOSED"
	if w.PRStates()["owner/repo#12"] != "MERGED" || calls != 1 {
		t.Fatal("facts leaked mutable cache or polled")
	}
}

func TestOperatorWaitRechecksRunListAfterReadingNewWait(t *testing.T) {
	b := waitBead("waiting:100:operator:operator-op-build")
	var sent []sentMail
	w := watcher(b, nil, &sent, time.Unix(200, 0))
	calls := 0
	w.running = func(context.Context) (map[string]bool, error) {
		calls++
		return map[string]bool{"operator-op-build": calls > 1}, nil
	}
	w.poll(context.Background())
	if len(sent) != 0 {
		t.Fatalf("new Operator declared stopped: %+v", sent)
	}
	if calls != 2 {
		t.Fatalf("run reads = %d", calls)
	}
}

// m2: onlyHolder is the one predicate: a Manager handoff wait past its window holds nothing, so
// it never stops the fired wait's dispatch hand-on; a live one does.
func TestOnlyHolderReadsHandoffExpiryLikeTheBoard(t *testing.T) {
	label := "waiting:100:operator:operator-op-build"
	w := &waitWatcher{runningNow: map[string]bool{}}
	old := time.Now().Add(-3 * time.Hour)
	expired := beads.Wait{Kind: beads.WaitUntil, Since: old, Until: old.Add(time.Hour), Ref: beads.ManagerCloseWait}.Label()
	if !w.onlyHolder(beads.Bead{ID: "s", Status: "open", Labels: []string{label, expired}}, label) {
		t.Fatal("an expired handoff wait counted as a holder")
	}
	live := beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Hour), Ref: beads.ManagerCloseWait}.Label()
	if w.onlyHolder(beads.Bead{ID: "s", Status: "open", Labels: []string{label, live}}, label) {
		t.Fatal("a live handoff wait did not count as a holder")
	}
	w.runningNow = map[string]bool{"operator-x": true}
	if w.onlyHolder(beads.Bead{ID: "s", Status: "in_progress", Labels: []string{label, "owner:operator-x"}}, label) {
		t.Fatal("a running Operator owner did not count as a holder")
	}
}
