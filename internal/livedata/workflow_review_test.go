package livedata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

const reviewedPR = "https://github.com/o/r/pull/1234"

// A fake gh with one fixed answer per PR URL, counting the calls.
type fakeHeads struct {
	mu    sync.Mutex
	calls int
	state string
	head  string
	err   error
	cache *PRHeadCache
}

func (f *fakeHeads) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return json.Marshal(map[string]string{"headRefOid": f.head, "state": f.state})
}

// reviewFindings runs the whole display path on seeded beads, the way cmdrd
// does: rows, snapshot, heads, then the diagram check on the workstream.
func reviewFindings(t *testing.T, mem *beads.MemClient, gh *fakeHeads) []DiagramFinding {
	t.Helper()
	repoMu.Lock()
	repoCache["/p"] = "o/r"
	repoMu.Unlock()
	ctx := context.Background()
	project := store.Project{ID: 1, Name: "p", Path: "/p"}
	rows, err := projectObjectiveRows(ctx, mem, project)
	if err != nil {
		t.Fatal(err)
	}
	snap := BuildSnapshot(Data{Projects: []store.Project{project}, Objectives: rows, LoadedAt: time.Now()}, nil)
	refreshHeadsNow(t, &snap, gh)
	if len(snap.Workstreams) != 1 {
		t.Fatalf("workstreams: %+v", snap.Workstreams)
	}
	var out []DiagramFinding
	for _, f := range CheckDiagram(snap.Workstreams[0], snap.Workstreams[0].BeadID) {
		if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
			out = append(out, f)
		}
	}
	return out
}

func refreshHeadsNow(t *testing.T, snap *Snapshot, gh *fakeHeads) {
	t.Helper()
	if gh.cache == nil {
		gh.cache = NewPRHeadCache(gh.run, PRHeadTTL)
	}
	RefreshWorkflowHeads(snap, gh.cache.Lookup)
	gh.cache.wait()
	RefreshWorkflowHeads(snap, gh.cache.Lookup)
}

func seedStage(mem *beads.MemClient, stageLabels ...string) {
	mem.Seed(beads.Bead{ID: "w", Title: "Workstream", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.1", Title: "Build it", Labels: append([]string{"kind:objective"}, stageLabels...)})
}

// seedClosedBlock plugs a complete, closed pr-review block before target.
func seedClosedBlock(t *testing.T, mem *beads.MemClient, target, head string) {
	t.Helper()
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: "w", Before: target, PR: reviewedPR})
	if err != nil {
		t.Fatal(err)
	}
	mem.Seed(beads.Bead{ID: "w.9", Title: "Review gate", Status: "closed", Labels: append([]string{"kind:objective"}, inst.Labels...), Description: fmt.Sprintf("for %s\n\n%s", reviewedPR, inst.Snapshot)})
	mem.AddComment(context.Background(), "/p", "w.9", workflowtemplate.WriterBinding{Revision: "rev", PR: reviewedPR, Head: head, Tasks: []string{"writer"}}.Comment())
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: target, DependsOnID: "w.9", Type: "blocks"})
	for i, s := range tpl.Steps {
		id := fmt.Sprintf("w.9.%d", i+1)
		mem.Seed(beads.Bead{ID: id, Title: s.Title, Status: "closed", Labels: append([]string{"kind:objective"}, inst.Steps[i].Labels...)})
		if i > 0 {
			mem.Edges = append(mem.Edges, beads.Dep{IssueID: id, DependsOnID: fmt.Sprintf("w.9.%d", i), Type: "blocks"})
		}
		ev := workflowtemplate.Evidence{Binding: "rev", Instance: inst.Key, Step: s.ID, Verifier: s.Verifier, PR: reviewedPR, Head: head, Finding: "passed"}
		if err := mem.AddComment(context.Background(), "/p", id, ev.Comment()); err != nil {
			t.Fatal(err)
		}
	}
}

// #3: a finished block folds off the board, and its target must still read complete.
func TestWorkflowReview_ClosedCompleteBlockReadsComplete(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	seedClosedBlock(t, mem, "w.2", "head1")
	if got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"}); len(got) != 0 {
		t.Fatalf("complete closed block: %+v", got)
	}
	// A new push still shows, from the same folded steps.
	got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head2"})
	if len(got) != 1 || got[0].Check != "MERGE_REVIEW_INCOMPLETE" {
		t.Fatalf("new push: %+v", got)
	}
}

// A step downstream of another target's block.
func TestWorkflowReview_DownstreamOfAnotherTargetsBlockHasNoFinding(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	seedClosedBlock(t, mem, "w.2", "head1")
	mem.Seed(beads.Bead{ID: "w.3", Title: "Follow-up check", Labels: []string{"kind:objective"}})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "w.3", DependsOnID: "w.9", Type: "blocks"})
	if got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"}); len(got) != 0 {
		t.Fatalf("downstream step: %+v", got)
	}
}

// #15: internal-gate's merged gates are not steps, and still reach the rule.
func TestWorkflowReview_StandaloneMergedGateReachesTheRule(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem, "pr:1234")
	mem.Seed(beads.Bead{ID: "g1", Title: "w.1: gate merged", Labels: []string{"kind:gate", "gate-of:" + beads.EscapeLabel("w.1"), "gate-name:merged", "verifier:user"}})
	got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
	if len(got) != 1 || got[0].Check != "MERGE_REVIEW_REQUIRED" || got[0].Bead != "g1" {
		t.Fatalf("open PR: %+v", got)
	}
	if !strings.Contains(got[0].Fix, "--parent w.1 --before g1 --pr "+reviewedPR) {
		t.Fatalf("fix names the stage and its PR: %q", got[0].Fix)
	}
	// The stage's PR is merged: the gate is history.
	if got := reviewFindings(t, mem, &fakeHeads{state: "MERGED", head: "head1"}); len(got) != 0 {
		t.Fatalf("merged PR: %+v", got)
	}
}

// #2: a pr: label alone is not a merge target; a step that says it is the merge is.
func TestWorkflowReview_PlainPRStepHasNoFinding(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "Worker retry with backoff", Labels: []string{"kind:objective", "in-review", "pr:1234"}})
	if got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"}); len(got) != 0 {
		t.Fatalf("plain pr: step: %+v", got)
	}
	mem.Seed(beads.Bead{ID: "w.3", Title: "Widget service PR 1234 merged", Labels: []string{"kind:objective", "pr:1234"}})
	got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
	if len(got) != 1 || got[0].Bead != "w.3" || got[0].Check != "MERGE_REVIEW_REQUIRED" {
		t.Fatalf("merge step: %+v", got)
	}
}

// #16: out-of-play beads are not findings, whatever they look like.
func TestWorkflowReview_ParkedAndSupersededHaveNoFinding(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR 1234 merged", Status: beads.StatusDeferred, Labels: []string{"kind:objective", "pr:1234"}})
	mem.Seed(beads.Bead{ID: "w.3", Title: "PR 1235 merged", Labels: []string{"kind:objective", "pr:1235", "superseded"}})
	mem.Seed(beads.Bead{ID: "w.4", Title: "Merge it", Status: beads.StatusDeferred, Labels: []string{"kind:objective", "gate", "approver:user"}})
	mem.Seed(beads.Bead{ID: "g1", Title: "w.1: gate merged", Status: beads.StatusDeferred, Labels: []string{"kind:gate", "gate-of:" + beads.EscapeLabel("w.1"), "gate-name:merged", "verifier:user"}})
	if got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"}); len(got) != 0 {
		t.Fatalf("out of play: %+v", got)
	}
}

// #5: the display resolves the labels commander writes, and the merged PR exempts.
func TestWorkflowReview_RealLabelsResolveThroughTheProjectRepo(t *testing.T) {
	for _, label := range []string{"pr:1234", "pr:o/r#1234"} {
		mem := &beads.MemClient{}
		seedStage(mem)
		mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", label}})
		if got := reviewFindings(t, mem, &fakeHeads{state: "MERGED", head: "head1"}); len(got) != 0 {
			t.Fatalf("%s merged: %+v", label, got)
		}
		got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
		if len(got) != 1 || !strings.Contains(got[0].Fix, reviewedPR) {
			t.Fatalf("%s open: %+v", label, got)
		}
	}
}

// #4: 300 snapshots, the API's one-per-second poll for five minutes, are one gh process.
func TestWorkflowReview_SnapshotsDoNotSpawnGHPerPoll(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	gh := &fakeHeads{state: "OPEN", head: "head1"}
	for range 300 {
		reviewFindings(t, mem, gh)
	}
	gh.mu.Lock()
	defer gh.mu.Unlock()
	if gh.calls > 1 {
		t.Fatalf("gh ran %d times for 300 snapshots", gh.calls)
	}
}

// wait returns once queued reads are done (tests only).
func (c *PRHeadCache) wait() {
	for {
		c.mu.Lock()
		idle := !c.reading
		c.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPRHeadCache_TTLLastGoodAndMergedIsFinal(t *testing.T) {
	gh := &fakeHeads{state: "OPEN", head: "head1"}
	c := NewPRHeadCache(gh.run, time.Minute)
	clock := time.Unix(1000, 0)
	c.now = func() time.Time { return clock }
	calls := func() int { gh.mu.Lock(); defer gh.mu.Unlock(); return gh.calls }

	if _, ok := c.Lookup(reviewedPR); ok {
		t.Fatal("known before any read")
	}
	c.wait()
	if h, ok := c.Lookup(reviewedPR); !ok || h.SHA != "head1" || calls() != 1 {
		t.Fatal(h, ok, calls())
	}
	// A failed read keeps the last good value and waits a TTL before trying again.
	clock = clock.Add(time.Minute)
	gh.mu.Lock()
	gh.err = errors.New("rate limited")
	gh.mu.Unlock()
	c.Lookup(reviewedPR)
	c.wait()
	if h, ok := c.Lookup(reviewedPR); !ok || h.SHA != "head1" || calls() != 2 {
		t.Fatal(h, ok, calls())
	}
	c.wait()
	if calls() != 2 {
		t.Fatalf("failed read retried inside the TTL: %d", calls())
	}
	// Back up: the new facts arrive, and a merged PR is never read again.
	clock = clock.Add(time.Minute)
	gh.mu.Lock()
	gh.err, gh.state, gh.head = nil, "MERGED", "head2"
	gh.mu.Unlock()
	c.Lookup(reviewedPR)
	c.wait()
	clock = clock.Add(time.Hour)
	if h, ok := c.Lookup(reviewedPR); !ok || h.State != "MERGED" || h.SHA != "head2" {
		t.Fatal(h, ok)
	}
	c.wait()
	if calls() != 3 {
		t.Fatalf("merged PR read again: %d", calls())
	}
}

func TestPRHeadCache_LookupNeverWaitsForGH(t *testing.T) {
	release := make(chan struct{})
	c := NewPRHeadCache(func(context.Context, ...string) ([]byte, error) {
		<-release
		return nil, errors.New("offline")
	}, time.Minute)
	done := make(chan struct{})
	go func() {
		for range 100 {
			c.Lookup(reviewedPR)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Lookup blocked on gh")
	}
	close(release)
	c.wait()
}

// A block plugged before an open target, under a parent that has since closed.
// w.3 is the closed parent; the block and its steps are folded off the board.
func seedFoldedBlock(t *testing.T, mem *beads.MemClient, target, head string, done int) {
	t.Helper()
	mem.Seed(beads.Bead{ID: "w.3", Title: "Finished stage", Status: "closed", ParentID: "w", Labels: []string{"kind:objective"}})
	seedOpenBlock(t, mem, "w.3", target, "w.3.1", head, done)
}

func closeFoldedBlock(t *testing.T, mem *beads.MemClient) {
	t.Helper()
	if err := mem.Close(context.Background(), "/p", "w.3.1"); err != nil {
		t.Fatal(err)
	}
}

func seedOpenGreenlight(mem *beads.MemClient) {
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "Greenlight merging the fix", ParentID: "w", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
}

const reviewSteps = 6

func TestWorkflowReview_FoldedBlockStillJudgesItsOpenTarget(t *testing.T) {
	complete := func(t *testing.T) *beads.MemClient {
		mem := &beads.MemClient{}
		seedOpenGreenlight(mem)
		seedFoldedBlock(t, mem, "w.2", "head1", reviewSteps)
		closeFoldedBlock(t, mem)
		return mem
	}
	t.Run("complete reads complete", func(t *testing.T) {
		if got := reviewFindings(t, complete(t), &fakeHeads{state: "OPEN", head: "head1"}); len(got) != 0 {
			t.Fatalf("complete block under a closed parent: %+v", got)
		}
	})
	t.Run("a new push reads incomplete", func(t *testing.T) {
		got := reviewFindings(t, complete(t), &fakeHeads{state: "OPEN", head: "head2"})
		if len(got) != 1 || got[0].Check != "MERGE_REVIEW_INCOMPLETE" || got[0].Bead != "w.2" {
			t.Fatalf("stale block: %+v", got)
		}
	})
	t.Run("an open block reads incomplete", func(t *testing.T) {
		mem := &beads.MemClient{}
		seedOpenGreenlight(mem)
		seedFoldedBlock(t, mem, "w.2", "head1", 2)
		got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
		if len(got) != 1 || got[0].Check != "MERGE_REVIEW_INCOMPLETE" {
			t.Fatalf("unfinished block: %+v", got)
		}
		if !strings.Contains(got[0].Why, "w.3.1.3") || strings.Contains(got[0].Why, "w.3.1.1,") {
			t.Fatalf("names only the unfinished steps: %s", got[0].Why)
		}
	})
	t.Run("a closed target stays out of the way", func(t *testing.T) {
		mem := complete(t)
		if err := mem.Close(context.Background(), "/p", "w.2"); err != nil {
			t.Fatal(err)
		}
		if got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head2"}); len(got) != 0 {
			t.Fatalf("closed target: %+v", got)
		}
	})
	t.Run("a merge gate guarding a stage", func(t *testing.T) {
		mem := &beads.MemClient{}
		seedStage(mem)
		mem.Seed(beads.Bead{ID: "g1", Title: "Merged", Labels: []string{"kind:gate", "gate-name:merged", "gate-of:w.1"}})
		seedFoldedBlock(t, mem, "g1", "head1", reviewSteps)
		closeFoldedBlock(t, mem)
		if got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"}); len(got) != 0 {
			t.Fatalf("gate with a folded block: %+v", got)
		}
	})
}

// A bead with no merge word is judged on its block alone, so its head comes from
// the block's PR, through the same cache: one read, however many polls.
func TestWorkflowReview_FoldedBlockAddsNoGHPerPoll(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "Land it", ParentID: "w", Labels: []string{"kind:objective"}})
	seedFoldedBlock(t, mem, "w.2", "head1", reviewSteps)
	closeFoldedBlock(t, mem)
	gh := &fakeHeads{state: "OPEN", head: "head1"}
	for range 50 {
		if got := reviewFindings(t, mem, gh); len(got) != 0 {
			t.Fatalf("label-free target: %+v", got)
		}
	}
	if gh.calls != 1 {
		t.Fatalf("gh calls: %d", gh.calls)
	}
}

// Keeping the block readable draws nothing new: Board and diagram show the target
// exactly as they do with no block at all, and only the finding goes.
func TestWorkflowReview_FoldedBlockIsNotDrawn(t *testing.T) {
	mem := &beads.MemClient{}
	seedOpenGreenlight(mem)
	seedFoldedBlock(t, mem, "w.2", "head1", reviewSteps)
	closeFoldedBlock(t, mem)
	snap := reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
	var ids func([]SnapStep)
	ids = func(level []SnapStep) {
		for _, s := range level {
			if strings.HasPrefix(s.BeadID, "w.3.") {
				t.Errorf("folded bead has a step: %s", s.BeadID)
			}
			ids(s.Children)
		}
	}
	ids(snap.Workstreams[0].Steps)
	d := Board(snap, 1793000000)
	if len(d.Workstreams) != 1 || d.Workstreams[0].Diagram == nil || d.Workstreams[0].Workflow == nil {
		t.Fatalf("board: %+v", d.Workstreams)
	}
	for _, s := range d.Workstreams[0].Diagram.Steps {
		if strings.HasPrefix(s.ID, "w.3.") || s.Review != nil {
			t.Errorf("diagram draws the folded block: %+v", s)
		}
	}
	for _, f := range d.Workstreams[0].Diagram.Findings {
		if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
			t.Errorf("finding beside a complete block: %+v", f)
		}
	}
	for _, s := range d.Workstreams[0].Workflow.Steps {
		if strings.HasPrefix(s.ID, "w.3.") {
			t.Errorf("workflow lists the folded block: %+v", s)
		}
	}
}

// Another target's folded block: the fix line reads what the door reads, and
// never offers the plug that plug refuses.
func TestWorkflowReview_ALaterStepBesideAFoldedBlockIsToldWhatWorks(t *testing.T) {
	mem := &beads.MemClient{}
	seedOpenGreenlight(mem)
	seedFoldedBlock(t, mem, "w.2", "head1", reviewSteps)
	closeFoldedBlock(t, mem)
	mem.Seed(beads.Bead{ID: "w.4", Title: "Fix merged to main", ParentID: "w", Labels: []string{"kind:objective", "pr:1234"}})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "w.4", DependsOnID: "w.3.1", Type: "blocks"})
	got := reviewFindings(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
	if len(got) != 1 || got[0].Bead != "w.4" || got[0].Check != "MERGE_REVIEW_REQUIRED" {
		t.Fatalf("findings: %+v", got)
	}
	if strings.Contains(got[0].Fix, "workflow plug") || strings.Contains(got[0].Why, "workflow plug") {
		t.Fatalf("offers a refused plug: %+v", got[0])
	}
	if !strings.Contains(got[0].Fix, "--bead w.4 --review none") || !strings.Contains(got[0].Why, "w.3.1") || !strings.Contains(got[0].Why, "retitle it without the word merge") {
		t.Fatalf("fix: %+v", got[0])
	}
}

// GEN_FOLDED_BLOCK_FIXTURE=1 writes the eval's read-only snapshot from the display path above.
func TestWorkflowReview_GenerateFoldedBlockFixture(t *testing.T) {
	if os.Getenv("GEN_FOLDED_BLOCK_FIXTURE") == "" {
		t.Skip("set GEN_FOLDED_BLOCK_FIXTURE=1 to rewrite tests/evals/scenarios/folded-block.json")
	}
	mem := &beads.MemClient{}
	seedOpenGreenlight(mem)
	seedFoldedBlock(t, mem, "w.2", "head1", reviewSteps)
	closeFoldedBlock(t, mem)
	raw, err := json.MarshalIndent(reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"}), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../../tests/evals/scenarios/folded-block.json", append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
