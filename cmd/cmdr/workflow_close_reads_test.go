package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// A close's reads are bounded by what it is judged on: these hold however large
// the project is. The template has six steps, so its block is seven beads.
const (
	maxCloseShownBeads  = 16
	maxCloseListedBeads = 16
)

// countingReads counts what a close reads and can fail one kind of read.
type countingReads struct {
	*beads.MemClient
	lists, listed, shown, dependents int
	failList                         func(beads.ListOptions) bool
	failDependents, failDetails      bool
	// reads counts every read of any kind; failRead fails the read at that position (1 = the first).
	reads, failRead int
	// leaveOut drops ids from a list's answer, exit 0, as a backend answering short does.
	leaveOut func(beads.ListOptions, string) bool
	// idLists and labelLists record what each filtered list answered.
	idLists, labelLists [][]string
}

func (c *countingReads) nextRead(what string) error {
	c.reads++
	if c.reads == c.failRead {
		return fmt.Errorf("%s: busy", what)
	}
	return nil
}

func (c *countingReads) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	if err := c.nextRead("br list"); err != nil {
		return nil, err
	}
	if c.failList != nil && c.failList(o) {
		return nil, errors.New("br list: busy")
	}
	bs, err := c.MemClient.List(ctx, dir, o)
	c.lists++
	c.listed += len(bs)
	if c.leaveOut != nil {
		bs = slices.DeleteFunc(bs, func(b beads.Bead) bool { return c.leaveOut(o, b.ID) })
	}
	answered := []string{}
	for _, b := range bs {
		answered = append(answered, b.ID)
	}
	if len(o.IDs) > 0 {
		c.idLists = append(c.idLists, answered)
	} else if len(o.Labels) > 0 {
		c.labelLists = append(c.labelLists, answered)
	}
	return bs, err
}

func (c *countingReads) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	if err := c.nextRead("br show"); err != nil {
		return nil, err
	}
	return c.MemClient.Show(ctx, dir, id)
}

func (c *countingReads) Deps(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	if err := c.nextRead("br dep list"); err != nil {
		return nil, err
	}
	return c.MemClient.Deps(ctx, dir, id)
}

func (c *countingReads) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if err := c.nextRead("br show"); err != nil {
		return nil, err
	}
	if c.failDetails {
		return nil, errors.New("br show: busy")
	}
	c.shown += len(ids)
	return c.MemClient.Details(ctx, dir, ids)
}

func (c *countingReads) Dependents(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	if err := c.nextRead("br dep list"); err != nil {
		return nil, err
	}
	c.dependents++
	if c.failDependents {
		return nil, errors.New("br dep list: busy")
	}
	return c.MemClient.Dependents(ctx, dir, id)
}

func (c *countingReads) reset() { c.lists, c.listed, c.shown, c.dependents = 0, 0, 0, 0 }

func (c *countingReads) assertBounded(t *testing.T, what string) {
	t.Helper()
	if c.shown > maxCloseShownBeads || c.listed > maxCloseListedBeads {
		t.Fatalf("%s: showed %d beads and listed %d (limits %d, %d): the read grew with the project", what, c.shown, c.listed, maxCloseShownBeads, maxCloseListedBeads)
	}
}

// seedLargeProject adds n unrelated beads with labels, parents and waits of their own.
func seedLargeProject(mem *beads.MemClient, n int) {
	ctx := context.Background()
	mem.Seed(beads.Bead{ID: "far", Status: "open"})
	for i := range n {
		id := fmt.Sprintf("far-%d", i)
		mem.Seed(beads.Bead{ID: id, Status: "open", Title: "Other work", Labels: []string{"kind:objective", fmt.Sprintf("pr:%d", i+1)}})
		mem.Link(ctx, "/d", id, "far", "parent-child")
		if i > 0 {
			mem.DependOn(ctx, "/d", id, fmt.Sprintf("far-%d", i-1))
		}
	}
}

// finishReviewBlock closes every step of the block with current evidence.
func finishReviewBlock(t *testing.T, mem *beads.MemClient, res *workflowPlugResult) {
	t.Helper()
	ctx := context.Background()
	for _, r := range []string{"reviewer-one", "reviewer-two"} {
		mem.Seed(beads.Bead{ID: r, Status: "closed", Labels: []string{"owner:operator-" + r}})
		mem.SetParent(ctx, "/d", r, "B")
		mem.AddComment(ctx, "/d", r, "Head: head1\nno findings\nCLEAN")
	}
	for i, s := range res.Remaining {
		if i > 0 && i < 5 {
			review := "reviewer-one"
			if i >= 3 {
				review = "reviewer-two"
			}
			raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: review, Head: "head1", NoFindings: true})
			mem.AddComment(ctx, "/d", s.BeadID, workflowtemplate.ProofPrefix+string(raw))
		}
		if err := checkWorkflowClose(ctx, mem, "/d", "p", s.BeadID); err != nil {
			t.Fatalf("step %s: %v", s.Step, err)
		}
		mem.Close(ctx, "/d", s.BeadID)
	}
}

// The reads of a target, container or step close are the same at any project size.
func TestWorkflowCloseReadsDoNotGrowWithTheProject(t *testing.T) {
	ctx := context.Background()
	type counts struct{ lists, listed, shown, dependents int }
	run := func(t *testing.T, size int) map[string]counts {
		mem := &beads.MemClient{}
		res := seedReviewWorkflow(t, mem)
		pinWorkflowGH(t, workflowFakeGH)
		finishReviewBlock(t, mem, res)
		seedLargeProject(mem, size)
		c := &countingReads{MemClient: mem}
		got := map[string]counts{}
		check := func(what, id string) {
			t.Helper()
			c.reset()
			if err := checkWorkflowClose(ctx, c, "/d", "p", id); err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			c.assertBounded(t, what)
			got[what] = counts{c.lists, c.listed, c.shown, c.dependents}
		}
		check("renewing a step", res.Remaining[0].BeadID)
		check("container", res.Container)
		mem.Close(ctx, "/d", res.Container)
		check("target", "M")
		return got
	}
	small, large := run(t, 0), run(t, 5000)
	t.Logf("reads at 5000 beads: %+v", large)
	if !reflect.DeepEqual(small, large) {
		t.Fatalf("reads moved with the project size:\n   0: %+v\n5000: %+v", small, large)
	}
}

// What blocked the user: recording a merged PR through the close verb itself.
func TestCloseVerbOfAMergedPRTargetReadsOnlyItsOwnBeads(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "G", Status: "open", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user", "pr:12"}})
	seedLargeProject(mem, 5000)
	c := &countingReads{MemClient: mem}
	setupObjectiveProject(t, c)
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"headRefOid":"head1","state":"MERGED"}`), nil
	})
	orig := closeSnapshotFn
	closeSnapshotFn = func() (*livedata.Snapshot, error) { return nil, errors.New("no board") }
	t.Cleanup(func() { closeSnapshotFn = orig })

	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "G"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if g, _ := mem.Show(context.Background(), "/d", "G"); g.Status != "closed" {
		t.Fatalf("not closed: %+v", g)
	}
	c.assertBounded(t, "close verb")
}

// plugBefore plugs a pr-review block for testPR in front of an existing bead under B.
func plugBefore(t *testing.T, mem *beads.MemClient, target string) *workflowPlugResult {
	t.Helper()
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	in, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: "B", Before: target, PR: testPR})
	if err != nil {
		t.Fatal(err)
	}
	res, err := plugWorkflow(context.Background(), mem, "/d", in)
	if err != nil {
		t.Fatal(err)
	}
	seedWriterBinding(mem, res.Container, "head1")
	return res
}

// closeKind is one way rule.go makes a bead subject to the merge rule, in a fresh
// project. seed returns the bead whose close is checked.
type closeKind struct {
	name string
	seed func(t *testing.T, mem *beads.MemClient) string
}

// closeKinds is every kind of target in internal/workflowtemplate/rule.go, plus
// the generated beads a door judges on the same reads.
func closeKinds() []closeKind {
	blockedTarget := func(b beads.Bead) func(*testing.T, *beads.MemClient) string {
		return func(t *testing.T, mem *beads.MemClient) string {
			pinOwnerRuns(t, true, true)
			mem.Seed(beads.Bead{ID: "B", Status: "open"})
			b.ParentID = "B"
			mem.Seed(b)
			plugBefore(t, mem, b.ID)
			return b.ID
		}
	}
	return []closeKind{
		{"merge gate by gate-name", blockedTarget(beads.Bead{ID: "T", Title: "Ship it", Labels: []string{"gate-name:merged"}})},
		{"merge gate by title", blockedTarget(beads.Bead{ID: "T", Title: "PR merged", Labels: []string{"gate"}})},
		{"merge step with a pr label", blockedTarget(beads.Bead{ID: "T", Title: "Squash-merge the PR", Labels: []string{"pr:" + testPR}})},
		{"ordinary bead with a plugged block", blockedTarget(beads.Bead{ID: "T", Title: "Roll out"})},
		{"gate judged on its stage", func(t *testing.T, mem *beads.MemClient) string {
			pinOwnerRuns(t, true, true)
			mem.Seed(beads.Bead{ID: "B", Status: "open"})
			mem.Seed(beads.Bead{ID: "S", Labels: []string{"pr:" + testPR}})
			mem.Seed(beads.Bead{ID: "T", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "gate-of:S"}})
			plugBefore(t, mem, "T")
			return "T"
		}},
		{"gate with no block yet", func(t *testing.T, mem *beads.MemClient) string {
			mem.Seed(beads.Bead{ID: "S", Labels: []string{"pr:" + testPR}})
			mem.Seed(beads.Bead{ID: "T", Title: "PR merged", Labels: []string{"gate", "gate-of:S"}})
			return "T"
		}},
		{"container", func(t *testing.T, mem *beads.MemClient) string { return seedReviewWorkflow(t, mem).Container }},
		{"generated step", func(t *testing.T, mem *beads.MemClient) string { return seedReviewWorkflow(t, mem).Remaining[0].BeadID }},
	}
}

// Every read of every kind of close, failed one at a time, refuses by that failure.
func TestWorkflowCloseRefusesWhenAnyReadFails(t *testing.T) {
	ctx := context.Background()
	for _, kind := range closeKinds() {
		t.Run(kind.name, func(t *testing.T) {
			pinWorkflowRepo(t, "o/r")
			pinWorkflowGH(t, workflowFakeGH)
			run := func(failRead int) (*countingReads, error) {
				mem := &beads.MemClient{}
				id := kind.seed(t, mem)
				c := &countingReads{MemClient: mem, failRead: failRead}
				return c, checkWorkflowClose(ctx, c, "/d", "p", id)
			}
			clean, _ := run(0)
			if clean.reads < 3 {
				t.Fatalf("only %d reads: the fixture never reached the narrow read", clean.reads)
			}
			for i := 1; i <= clean.reads; i++ {
				if _, err := run(i); err == nil || !strings.Contains(err.Error(), "busy") {
					t.Fatalf("read %d of %d failed and the close said: %v", i, clean.reads, err)
				}
			}
		})
	}
}

// Any id left out of any ID-filtered answer of any kind of close, for a bead that
// exists, refuses by that short read: the rule's own findings do not count.
func TestWorkflowCloseRefusesEveryShortIDRead(t *testing.T) {
	ctx := context.Background()
	for _, kind := range closeKinds() {
		t.Run(kind.name, func(t *testing.T) {
			pinWorkflowRepo(t, "o/r")
			pinWorkflowGH(t, workflowFakeGH)
			run := func(call int, drop string) (*countingReads, error) {
				mem := &beads.MemClient{}
				id := kind.seed(t, mem)
				c := &countingReads{MemClient: mem}
				c.leaveOut = func(o beads.ListOptions, got string) bool {
					return len(o.IDs) > 0 && len(c.idLists) == call && got == drop
				}
				return c, checkWorkflowClose(ctx, c, "/d", "p", id)
			}
			clean, _ := run(-1, "")
			if len(clean.idLists) == 0 {
				t.Fatal("no ID-filtered read to shorten")
			}
			for call, ids := range clean.idLists {
				for _, drop := range ids {
					_, err := run(call, drop)
					if err == nil || !strings.Contains(err.Error(), drop) || !(strings.Contains(err.Error(), "came back short") || strings.Contains(err.Error(), "missing from fresh list")) {
						t.Fatalf("list %d left out %s and the close said: %v", call, drop, err)
					}
				}
			}
		})
	}
}

// A step left out of its block's label read cannot pass a finished block: the rule
// sees the step missing. (The container is read through the target's waits.)
func TestWorkflowCloseRefusesAShortInstanceRead(t *testing.T) {
	ctx := context.Background()
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, workflowFakeGH)
	run := func(drop func(*workflowPlugResult) string) error {
		mem := &beads.MemClient{}
		res := seedReviewWorkflow(t, mem)
		finishReviewBlock(t, mem, res)
		mem.Close(ctx, "/d", res.Container)
		c := &countingReads{MemClient: mem, leaveOut: func(o beads.ListOptions, got string) bool {
			return len(o.Labels) > 0 && got == drop(res)
		}}
		return checkWorkflowClose(ctx, c, "/d", "p", "M")
	}
	if err := run(func(*workflowPlugResult) string { return "" }); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		err := run(func(r *workflowPlugResult) string { return r.Remaining[i].BeadID })
		if err == nil || !strings.Contains(err.Error(), "MERGE_REVIEW_INCOMPLETE") {
			t.Fatalf("step %d left out of the block's read: close said %v", i, err)
		}
	}
}

// The three closes the short read used to pass. Each is refused on a whole read
// (the fixture is a real refusal), and on the short read.
func TestShortIDReadNeverPassesAGuardedClose(t *testing.T) {
	ctx := context.Background()
	const otherPR = "https://github.com/o/r/pull/9"
	for _, tc := range []struct {
		name, close, whole string
		seed               func(t *testing.T, mem *beads.MemClient) string // returns the id the list leaves out
	}{
		{
			name: "ordinary bead whose unfinished block was left out", close: "T", whole: "MERGE_REVIEW_INCOMPLETE",
			seed: func(t *testing.T, mem *beads.MemClient) string {
				pinOwnerRuns(t, true, true)
				mem.Seed(beads.Bead{ID: "B", Status: "open"})
				mem.Seed(beads.Bead{ID: "T", ParentID: "B", Title: "Roll out"})
				mem.Seed(beads.Bead{ID: "zz-other", Title: "Sorts last"})
				mem.DependOn(ctx, "/d", "T", "zz-other")
				return plugBefore(t, mem, "T").Container
			},
		},
		{
			name: "target whose second live PR's block was left out", close: "M", whole: "MERGE_REVIEW_REQUIRED",
			seed: func(t *testing.T, mem *beads.MemClient) string {
				res := seedReviewWorkflow(t, mem)
				finishReviewBlock(t, mem, res)
				mem.Close(ctx, "/d", res.Container)
				key := workflowtemplate.Key(workflowtemplate.Params{Project: "p", Parent: "B", Before: "M", PR: otherPR})
				mem.Seed(beads.Bead{ID: "second", Status: "open", Description: otherPR, Labels: []string{workflowtemplate.LabelTemplate + "pr-review", workflowtemplate.LabelInstance + key}})
				mem.SetParent(ctx, "/d", "second", "B")
				mem.DependOn(ctx, "/d", "M", "second")
				return "second"
			},
		},
		{
			name: "gate whose stage's live PR was left out", close: "G", whole: "MERGE_REVIEW_REQUIRED",
			seed: func(t *testing.T, mem *beads.MemClient) string {
				mem.Seed(beads.Bead{ID: "S", Labels: []string{"pr:77"}})
				mem.Seed(beads.Bead{ID: "G", Title: "PR merged", Labels: []string{"gate", "gate-of:S", "pr:12"}})
				return "S"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinWorkflowRepo(t, "o/r")
			pinWorkflowGH(t, func(ctx context.Context, args ...string) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "/pull/12") {
					return []byte(`{"headRefOid":"head1","state":"MERGED"}`), nil
				}
				return workflowFakeGH(ctx, args...)
			})
			run := func(short bool) error {
				mem := &beads.MemClient{}
				drop := tc.seed(t, mem)
				c := &countingReads{MemClient: mem}
				if short {
					c.leaveOut = func(o beads.ListOptions, got string) bool { return len(o.IDs) > 0 && got == drop }
				}
				return checkWorkflowClose(ctx, c, "/d", "p", tc.close)
			}
			if err := run(false); err == nil || !strings.Contains(err.Error(), tc.whole) {
				t.Fatalf("whole read: %v, want %s", err, tc.whole)
			}
			if err := run(true); err == nil || !strings.Contains(err.Error(), "came back short") {
				t.Fatalf("short read: close said %v, want a refusal for the short read", err)
			}
		})
	}
}

// A bead that is really gone stays no node, as in a whole-project list: a wait or
// stage naming a deleted bead does not stop a close for good.
func TestWorkflowCloseToleratesABeadThatIsGone(t *testing.T) {
	ctx := context.Background()
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, workflowFakeGH)
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	finishReviewBlock(t, mem, res)
	mem.Close(ctx, "/d", res.Container)
	mem.DependOn(ctx, "/d", "M", "deleted-long-ago")
	mem.SetLabel(ctx, "/d", "M", "gate-of:deleted-stage", true)
	c := &countingReads{MemClient: mem}
	if err := checkWorkflowClose(ctx, c, "/d", "p", "M"); err != nil {
		t.Fatal(err)
	}
}

// A bead left out of the list whose own read then fails is not confirmed gone.
func TestShortIDReadWithAFailedConfirmationRefuses(t *testing.T) {
	ctx := context.Background()
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, workflowFakeGH)
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "S", Labels: []string{"pr:77"}})
	mem.Seed(beads.Bead{ID: "G", Title: "PR merged", Labels: []string{"gate", "gate-of:S"}})
	c := &countingReads{MemClient: mem, leaveOut: func(o beads.ListOptions, got string) bool { return got == "S" }}
	clean := &countingReads{MemClient: mem, leaveOut: c.leaveOut}
	_ = checkWorkflowClose(ctx, clean, "/d", "p", "G")
	c.failRead = clean.reads // the last read of the refusal: the confirmation itself
	if err := checkWorkflowClose(ctx, c, "/d", "p", "G"); err == nil || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "left out S") {
		t.Fatalf("close said %v", err)
	}
}

// The narrow read gives the rule the same answer the whole project gave, for every
// kind of target and every exemption, with bait on every relationship it skips.
func TestWorkflowCloseJudgesLikeAWholeProjectRead(t *testing.T) {
	ctx := context.Background()
	const otherPR = "https://github.com/o/r/pull/9"
	secondBlock := func(mem *beads.MemClient) {
		key := workflowtemplate.Key(workflowtemplate.Params{Project: "p", Parent: "B", Before: "M", PR: otherPR})
		mem.Seed(beads.Bead{ID: "second", Status: "open", Description: otherPR, Labels: []string{workflowtemplate.LabelTemplate + "pr-review", workflowtemplate.LabelInstance + key}})
		mem.SetParent(ctx, "/d", "second", "B")
		mem.DependOn(ctx, "/d", "M", "second")
	}
	label := func(mem *beads.MemClient, id string, labels ...string) {
		for _, l := range labels {
			mem.SetLabel(ctx, "/d", id, l, true)
		}
	}
	// a fresh target T with its own block, in place of M's.
	blocked := func(t *testing.T, mem *beads.MemClient, b beads.Bead) *workflowPlugResult {
		b.ID, b.ParentID = "T", "B"
		mem.Seed(b)
		return plugBefore(t, mem, "T")
	}
	for _, tc := range []struct {
		name, close string
		container   bool // close M's block, judged on what waits on it
		edit        func(t *testing.T, mem *beads.MemClient, res *workflowPlugResult)
		states      map[string]string // GitHub's answer, as the door gives the node
		head        string
		want        string
	}{
		{name: "target with an unfinished block", close: "M", want: "MERGE_REVIEW_INCOMPLETE"},
		{name: "target with a finished block", close: "M", head: "head1", edit: func(t *testing.T, mem *beads.MemClient, res *workflowPlugResult) {
			finishReviewBlock(t, mem, res)
			mem.Close(ctx, "/d", res.Container)
		}},
		{name: "target with a second block", close: "M", edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) { secondBlock(mem) }, want: "MERGE_REVIEW_REQUIRED"},
		{name: "container whose target has a second block", container: true, edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) { secondBlock(mem) }, want: "MERGE_REVIEW_REQUIRED"},
		{name: "gate judged on its stage's PR", close: "G", edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) {
			mem.Seed(beads.Bead{ID: "S", Status: "open", Labels: []string{"pr:77"}})
			mem.Seed(beads.Bead{ID: "G", Status: "open", Title: "PR merged", Labels: []string{"gate", "gate-of:S"}})
		}, want: "MERGE_REVIEW_REQUIRED"},
		{name: "container over a gate with a stage's PR", container: true, edit: func(_ *testing.T, mem *beads.MemClient, res *workflowPlugResult) {
			mem.Seed(beads.Bead{ID: "S", Status: "open", Labels: []string{"pr:77"}})
			mem.Seed(beads.Bead{ID: "G", Status: "open", Title: "PR merged", Labels: []string{"gate", "gate-of:S"}})
			mem.DependOn(ctx, "/d", "G", res.Container)
		}, want: "MERGE_REVIEW_REQUIRED"},
		{name: "merge gate by gate-name", close: "T", edit: func(t *testing.T, mem *beads.MemClient, _ *workflowPlugResult) {
			blocked(t, mem, beads.Bead{Title: "Ship it", Labels: []string{"gate-name:merged"}})
		}, want: "MERGE_REVIEW_INCOMPLETE"},
		{name: "merge gate with no block", close: "T", edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) {
			mem.Seed(beads.Bead{ID: "T", Title: "Ship it", Labels: []string{"gate-name:merged", "pr:12"}})
		}, want: "MERGE_REVIEW_REQUIRED"},
		{name: "merge step with a pr label", close: "T", edit: func(t *testing.T, mem *beads.MemClient, _ *workflowPlugResult) {
			blocked(t, mem, beads.Bead{Title: "Squash-merge the PR", Labels: []string{"pr:" + testPR}})
		}, want: "MERGE_REVIEW_INCOMPLETE"},
		{name: "ordinary bead with a plugged block", close: "T", edit: func(t *testing.T, mem *beads.MemClient, _ *workflowPlugResult) {
			blocked(t, mem, beads.Bead{Title: "Roll out"})
		}, want: "MERGE_REVIEW_INCOMPLETE"},
		{name: "parked merge gate is still judged", close: "T", edit: func(t *testing.T, mem *beads.MemClient, _ *workflowPlugResult) {
			blocked(t, mem, beads.Bead{Title: "PR merged", Status: "deferred", Labels: []string{"gate"}})
		}, want: "MERGE_REVIEW_INCOMPLETE"},
		{name: "settled: its PR merged", close: "M", states: map[string]string{testPR: "MERGED"}},
		{name: "settled, but a second PR is live", close: "M", states: map[string]string{testPR: "MERGED"}, edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) { secondBlock(mem) }, want: "MERGE_REVIEW_REQUIRED"},
		{name: "exempt: closed target", close: "M", edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) { mem.Close(ctx, "/d", "M") }},
		{name: "exempt: superseded target", close: "M", edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) { label(mem, "M", "superseded") }},
		{name: "exempt: retired target", close: "M", edit: func(_ *testing.T, mem *beads.MemClient, _ *workflowPlugResult) { label(mem, "M", "retired") }},
		{name: "downstream of another target's block", close: "D", edit: func(_ *testing.T, mem *beads.MemClient, res *workflowPlugResult) {
			mem.Seed(beads.Bead{ID: "D", Status: "open", Title: "After the merge"})
			mem.DependOn(ctx, "/d", "D", res.Container)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			res := seedReviewWorkflow(t, mem)
			pinWorkflowRepo(t, "o/r")
			pinWorkflowGH(t, workflowFakeGH)
			if tc.edit != nil {
				tc.edit(t, mem, res)
			}
			closing := tc.close
			if tc.container {
				closing = res.Container
			}
			seedRelationshipBait(t, mem, closing)
			seedLargeProject(mem, 50)

			narrow, err := workflowNodes(ctx, mem, "/d", closing)
			if err != nil {
				t.Fatal(err)
			}
			all, _ := mem.List(ctx, "/d", beads.ListOptions{Status: "all"})
			var ids []string
			for _, b := range all {
				ids = append(ids, b.ID)
			}
			details, _ := mem.Details(ctx, "/d", ids)
			var whole []workflowtemplate.Node
			for _, b := range all {
				whole = append(whole, workflowNode(b, details[b.ID]))
			}
			if len(narrow) > maxCloseShownBeads {
				t.Fatalf("read %d nodes", len(narrow))
			}
			// The scope the door derives, from each read: a container's waiters.
			scope := func(nodes []workflowtemplate.Node) map[string]bool {
				if !tc.container {
					return map[string]bool{closing: true}
				}
				in := map[string]bool{}
				for _, n := range nodes {
					if slices.Contains(n.Waits, closing) && !strings.HasPrefix(n.Bead.ID, "bait-") {
						in[n.Bead.ID] = true
					}
				}
				return in
			}
			in := scope(whole)
			if got := scope(narrow); !reflect.DeepEqual(got, in) {
				t.Fatalf("narrow scope %v, whole %v", got, in)
			}
			apply := func(nodes []workflowtemplate.Node) []workflowtemplate.Node {
				for i := range nodes {
					if in[nodes[i].Bead.ID] {
						nodes[i].Head, nodes[i].PRStates = tc.head, tc.states
					}
				}
				return sortedByIDForTest(nodes)
			}
			got := workflowtemplate.MergeRule("p", "o/r", apply(narrow), in)
			want := workflowtemplate.MergeRule("p", "o/r", apply(whole), in)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("narrow read judged differently:\n got %+v\nwant %+v", got, want)
			}
			if tc.want == "" && len(got) != 0 || tc.want != "" && (len(got) == 0 || got[0].Check != tc.want) {
				t.Fatalf("findings %+v, want %q", got, tc.want)
			}
		})
	}
}

func sortedByIDForTest(nodes []workflowtemplate.Node) []workflowtemplate.Node {
	m := map[string]workflowtemplate.Node{}
	for _, n := range nodes {
		m[n.Bead.ID] = n
	}
	return sortedWorkflowNodes(m)
}

// seedRelationshipBait hangs a bead carrying a live PR on every relationship
// workflowNodes does not follow (parent, child, a bead waiting on it, a gate guarding
// it, a sibling). Were MergeRule to start reading one, the whole read would see the
// PR and the narrow read would not, and the parity test fails.
func seedRelationshipBait(t *testing.T, mem *beads.MemClient, id string) {
	t.Helper()
	ctx := context.Background()
	self, err := mem.Show(ctx, "/d", id)
	if err != nil {
		t.Fatal(err)
	}
	parent := self.ParentID
	if parent == "" {
		parent = "bait-parent"
		mem.Seed(beads.Bead{ID: parent, Title: "PR merged", Labels: []string{"gate"}})
	}
	mem.SetParent(ctx, "/d", id, parent) // a real edge: Details reads the parent from it
	mem.SetLabel(ctx, "/d", parent, "pr:80", true)
	for i, rel := range []struct {
		name string
		link func(bait string)
	}{
		{"child", func(bait string) { mem.SetParent(ctx, "/d", bait, id) }},
		{"waiter", func(bait string) { mem.DependOn(ctx, "/d", bait, id) }},
		{"guard", func(bait string) { mem.SetLabel(ctx, "/d", bait, "gate-of:"+id, true) }},
		{"sibling", func(bait string) { mem.SetParent(ctx, "/d", bait, parent) }},
	} {
		bait := "bait-" + rel.name
		mem.Seed(beads.Bead{ID: bait, Title: "PR merged", Labels: []string{"gate", fmt.Sprintf("pr:%d", 81+i)}})
		rel.link(bait)
	}
}

// mergeRuleReads is every place the merge rule looks past the bead it judges. Each
// must be followed by workflowNodes. Adding one here means reading it there too.
var mergeRuleReads = map[string]string{
	"pluggedBlocks: byID[id]":               "waits of each judged bead",
	"TargetPRs: byID[stageOf(n.Bead)]":      "the stage a gate guards",
	"prLabel: byID[stageOf(n.Bead)]":        "the stage a gate guards",
	"TargetPR: byID[id]":                    "waits of each judged bead",
	"otherBlock: byID[id]":                  "waits of each judged bead",
	"refusedPlugFix: byID[stageOf(n.Bead)]": "the stage a gate guards",
	"MergeRule: byID[ids[s.ID]]":            "every instance among the beads read",
	"MergeRule: range steps":                "the instance's steps, picked from the beads read",
	"MergeRule: steps[id]":                  "the instance's steps, picked from the beads read",
	"MergeRule: steps[s.ID]":                "the instance's steps, picked from the beads read",
	"MergeRule: range nodes":                "every bead read (the outer loop stops at scope; the two inner loops match on instance)",
}

// A read MergeRule gains shows up here even when no fixture exercises it: this pins
// where it can reach, and the parity test proves workflowNodes follows each of them.
func TestWorkflowNodesFollowsEveryMergeRuleRead(t *testing.T) {
	files, err := filepath.Glob("../../internal/workflowtemplate/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v %v", files, err)
	}
	fset := token.NewFileSet()
	got := map[string]bool{}
	text := func(e ast.Expr) string {
		var b strings.Builder
		printer.Fprint(&b, fset, e)
		return b.String()
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Whatever holds the graph: a parameter or local of type map[string]Node or []Node.
			holders := map[string]bool{}
			isGraph := func(e ast.Expr) bool {
				s := text(e)
				return s == "map[string]Node" || s == "[]Node"
			}
			for _, p := range fn.Type.Params.List {
				if isGraph(p.Type) {
					for _, n := range p.Names {
						holders[n.Name] = true
					}
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok {
					for i, r := range as.Rhs {
						if lit, ok := r.(*ast.CompositeLit); ok && lit.Type != nil && isGraph(lit.Type) && i < len(as.Lhs) {
							holders[text(as.Lhs[i])] = true
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.IndexExpr:
					if id, ok := v.X.(*ast.Ident); ok && holders[id.Name] && !isWriteTarget(fn.Body, v) {
						got[fn.Name.Name+": "+text(v)] = true
					}
				case *ast.RangeStmt:
					if id, ok := v.X.(*ast.Ident); ok && holders[id.Name] {
						got[fn.Name.Name+": range "+id.Name] = true
					}
				}
				return true
			})
		}
	}
	for read := range got {
		if _, ok := mergeRuleReads[read]; !ok {
			t.Errorf("the merge rule reads something new, %q: make workflowNodes (cmd/cmdr/workflow_verify.go) read it, add it to the parity test, then list it in mergeRuleReads", read)
		}
	}
	for read := range mergeRuleReads {
		if !got[read] {
			t.Errorf("the merge rule no longer reads %q: if workflowNodes still does, drop that read; then remove it from mergeRuleReads", read)
		}
	}
}

// isWriteTarget is an index expression being assigned to, which reads nothing.
func isWriteTarget(body *ast.BlockStmt, e *ast.IndexExpr) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, l := range as.Lhs {
				if l == e {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// A read that fails refuses the close; it is never "nothing there".
func TestWorkflowCloseRefusesWhenANarrowReadFails(t *testing.T) {
	ctx := context.Background()
	const refused = "busy"
	for _, tc := range []struct {
		name string
		bead func(*workflowPlugResult) string
		fail func(*countingReads)
	}{
		{"who waits on a container", func(r *workflowPlugResult) string { return r.Container }, func(c *countingReads) { c.failDependents = true }},
		{"a target's own beads", func(*workflowPlugResult) string { return "M" }, func(c *countingReads) {
			c.failList = func(o beads.ListOptions) bool { return len(o.IDs) > 0 }
		}},
		{"a block's members", func(*workflowPlugResult) string { return "M" }, func(c *countingReads) {
			c.failList = func(o beads.ListOptions) bool { return len(o.Labels) > 0 }
		}},
		{"edges and comments", func(*workflowPlugResult) string { return "M" }, func(c *countingReads) { c.failDetails = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			res := seedReviewWorkflow(t, mem)
			pinWorkflowGH(t, workflowFakeGH)
			c := &countingReads{MemClient: mem}
			tc.fail(c)
			err := checkWorkflowClose(ctx, c, "/d", "p", tc.bead(res))
			if err == nil || !strings.Contains(err.Error(), refused) {
				t.Fatalf("close was not refused by the failed read: %v", err)
			}
		})
	}
}

// shortBDScript hands bd the id filter as repeated flags, whatever it was given, so
// the short answer below is real bd keeping only the last --id, with exit 0.
const shortBDScript = `#!/usr/bin/env bash
args=()
while [ $# -gt 0 ]; do
	if [ "$1" = --id ]; then
		IFS=, read -ra ids <<<"$2"
		for id in "${ids[@]}"; do args+=(--id "$id"); done
		shift 2
		continue
	fi
	args+=("$1")
	shift
done
exec "$REAL_BD" "${args[@]}"
`

// A guarded close against a real bd on a private temp server: the unfinished block's
// id sorts before the other wait, so a repeated --id drops the block and the close
// has nothing left to refuse on but the short read itself.
func TestGuardedCloseAgainstRealBD_BDIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	realBD, err := exec.LookPath("bd")
	if err != nil {
		t.Fatal(err)
	}
	standIn, err := filepath.Abs("../../scripts/br-bd")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("REAL_BD", realBD)
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := beads.New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(realBD, "-C", dir, "dolt", "stop").Run() })

	pinOwnerRuns(t, true, true)
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, workflowFakeGH)
	create := func(title string, labels ...string) string {
		t.Helper()
		id, err := c.Create(ctx, dir, title, beads.CreateOptions{Labels: labels, Silent: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	parent, target := create("Work"), create("Roll out")
	if err := c.SetParent(ctx, dir, target, parent); err != nil {
		t.Fatal(err)
	}
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	in, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: parent, Before: target, PR: testPR})
	if err != nil {
		t.Fatal(err)
	}
	res, err := plugWorkflow(ctx, c, dir, in)
	if err != nil {
		t.Fatal(err)
	}
	other := create("Other wait")
	for other <= res.Container {
		other = create("Other wait")
	}
	if err := c.DependOn(ctx, dir, target, other); err != nil {
		t.Fatal(err)
	}

	shortDir := t.TempDir()
	shortBD := filepath.Join(shortDir, "bd")
	if err := os.WriteFile(shortBD, []byte(shortBDScript), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(shortDir, "my-beads")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$REAL_BD\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, bin, want string }{
		{"bd", realBD, "MERGE_REVIEW_INCOMPLETE"},
		{"the br stand-in routing to bd", standIn, "MERGE_REVIEW_INCOMPLETE"},
		{"a wrapper with another name", wrapper, "MERGE_REVIEW_INCOMPLETE"},
		{"bd answering short", shortBD, "came back short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMMANDER_BEADS_BIN", tc.bin)
			err := checkWorkflowClose(ctx, c, dir, "p", target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("close said %v, want %q", err, tc.want)
			}
		})
	}

	// A really deleted bead is confirmed gone by bd's own answer: no node, no refusal.
	t.Run("a stage that is gone", func(t *testing.T) {
		t.Setenv("COMMANDER_BEADS_BIN", realBD)
		gate := create("PR merged", "gate", "gate-of:nobody-1")
		nodes, err := workflowNodes(ctx, c, dir, gate)
		if err != nil || len(nodes) != 1 {
			t.Fatalf("nodes %+v err %v", nodes, err)
		}
	})
}
