package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// fakeGraph is a small stateful beads backend: creates, labels and edges persist,
// so a second plug sees what the first one wrote.
type fakeGraph struct {
	beads.Client
	beads    map[string]*beads.Bead
	order    []string
	deps     map[string][]beads.Dep
	n        int
	failAt   int // fail the Nth Create (1-based), once
	creates  int
	depCalls int
}

func newFakeGraph(ids ...string) *fakeGraph {
	g := &fakeGraph{beads: map[string]*beads.Bead{}, deps: map[string][]beads.Dep{}}
	for _, id := range ids {
		g.beads[id] = &beads.Bead{ID: id, Title: id, Status: "open"}
		g.order = append(g.order, id)
	}
	return g
}

func (g *fakeGraph) Show(_ context.Context, _, id string) (*beads.Bead, error) {
	return g.beads[id], nil
}

func (g *fakeGraph) List(_ context.Context, _ string, o beads.ListOptions) ([]beads.Bead, error) {
	var out []beads.Bead
	for _, id := range g.order {
		b := g.beads[id]
		ok := true
		for _, l := range o.Labels {
			if !hasLabel(b.Labels, l) {
				ok = false
			}
		}
		if ok {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (g *fakeGraph) Create(_ context.Context, _, title string, o beads.CreateOptions) (string, error) {
	g.creates++
	if g.failAt == g.creates {
		return "", errors.New("boom")
	}
	g.n++
	id := fmt.Sprintf("new-%d", g.n)
	g.beads[id] = &beads.Bead{ID: id, Title: title, Status: "open", Labels: o.Labels, ParentID: o.Parent, Description: o.Description}
	g.order = append(g.order, id)
	return id, nil
}

func (g *fakeGraph) Deps(_ context.Context, _, id string) ([]beads.Dep, error) {
	return g.deps[id], nil
}

func (g *fakeGraph) DependOn(_ context.Context, _, from, to string) error {
	g.depCalls++
	g.deps[from] = append(g.deps[from], beads.Dep{IssueID: from, DependsOnID: to, Type: "blocks"})
	return nil
}

func plugInstance(t *testing.T, pr string) *workflowtemplate.Instance {
	t.Helper()
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	in, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: "B", Before: "M", PR: pr})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

const testPR = "https://github.com/o/r/pull/7"

func TestPlugCreatesBlockAndEdges(t *testing.T) {
	g := newFakeGraph("B", "M", "X")
	g.deps["M"] = []beads.Dep{{IssueID: "M", DependsOnID: "X", Type: "blocks"}}
	res, err := plugWorkflow(context.Background(), g, "/d", plugInstance(t, testPR))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 7 || len(res.Remaining) != 6 {
		t.Fatalf("created %v remaining %v", res.Created, res.Remaining)
	}
	c := g.beads[res.Container]
	if c.Title != "Review gate" || c.ParentID != "B" || !hasLabel(c.Labels, "workflow-template:pr-review") || !hasLabel(c.Labels, "workflow-version:1") ||
		!hasLabel(c.Labels, "workflow-instance:"+res.Instance) || !strings.Contains(c.Description, `"schema":"workflow-template@v1"`) {
		t.Fatalf("container %+v", c)
	}
	// Each child sits under the block, labelled, and waits on the one before.
	prev := ""
	for _, s := range res.Remaining {
		b := g.beads[s.BeadID]
		if b.ParentID != res.Container || !hasLabel(b.Labels, "workflow-step:"+s.Step) || !hasLabel(b.Labels, "kind:objective") {
			t.Fatalf("step %s: %+v", s.Step, b)
		}
		d := g.deps[s.BeadID]
		if prev == "" && len(d) != 0 || prev != "" && (len(d) != 1 || d[0].DependsOnID != prev) {
			t.Fatalf("step %s deps %v, want %q", s.Step, d, prev)
		}
		prev = s.BeadID
	}
	// M waits on the block and keeps X.
	got := map[string]bool{}
	for _, d := range g.deps["M"] {
		got[d.DependsOnID] = true
	}
	if !got[res.Container] || !got["X"] || len(g.deps["M"]) != 2 {
		t.Fatalf("M deps %v", g.deps["M"])
	}
}

func TestPlugIsIdempotent(t *testing.T) {
	g := newFakeGraph("B", "M")
	in := plugInstance(t, testPR)
	first, err := plugWorkflow(context.Background(), g, "/d", in)
	if err != nil {
		t.Fatal(err)
	}
	beadsBefore, depsBefore := len(g.beads), g.depCalls
	second, err := plugWorkflow(context.Background(), g, "/d", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.beads) != beadsBefore || g.depCalls != depsBefore || len(second.Created) != 0 || second.Container != first.Container {
		t.Fatalf("second plug wrote: beads %d->%d deps %d->%d created %v", beadsBefore, len(g.beads), depsBefore, g.depCalls, second.Created)
	}
	if len(second.Remaining) != 6 {
		t.Fatalf("remaining %v", second.Remaining)
	}
}

func TestPlugResumesAfterPartialWrite(t *testing.T) {
	for failAt := 1; failAt <= 7; failAt++ {
		g := newFakeGraph("B", "M")
		g.failAt = failAt
		in := plugInstance(t, testPR)
		if _, err := plugWorkflow(context.Background(), g, "/d", in); err == nil || !strings.Contains(err.Error(), "rerun") {
			t.Fatalf("failAt %d: err=%v", failAt, err)
		}
		res, err := plugWorkflow(context.Background(), g, "/d", in)
		if err != nil {
			t.Fatalf("failAt %d resume: %v", failAt, err)
		}
		if len(g.beads) != 2+7 || len(res.Remaining) != 6 {
			t.Fatalf("failAt %d: %d beads, remaining %d (duplicates or gaps)", failAt, len(g.beads), len(res.Remaining))
		}
		if len(g.deps["M"]) != 1 || len(g.deps[res.Remaining[1].BeadID]) != 1 {
			t.Fatalf("failAt %d: deps %v", failAt, g.deps)
		}
	}
}

func TestPlugResumesAfterMissingEdges(t *testing.T) {
	g := newFakeGraph("B", "M")
	in := plugInstance(t, testPR)
	res, _ := plugWorkflow(context.Background(), g, "/d", in)
	g.deps = map[string][]beads.Dep{} // edges lost, beads remain
	if _, err := plugWorkflow(context.Background(), g, "/d", in); err != nil {
		t.Fatal(err)
	}
	if len(g.deps["M"]) != 1 || g.deps["M"][0].DependsOnID != res.Container || len(g.beads) != 9 {
		t.Fatalf("deps %v beads %d", g.deps["M"], len(g.beads))
	}
}

func TestPlugRefusesCycleBeforeAnyWrite(t *testing.T) {
	g := newFakeGraph("B", "M")
	g.deps["B"] = []beads.Dep{{IssueID: "B", DependsOnID: "M", Type: "blocks"}}
	_, err := plugWorkflow(context.Background(), g, "/d", plugInstance(t, testPR))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err=%v", err)
	}
	if g.creates != 0 || g.depCalls != 0 {
		t.Fatalf("wrote before refusing: creates %d deps %d", g.creates, g.depCalls)
	}
	g = newFakeGraph("B")
	in := plugInstance(t, testPR)
	in.Params.Before = "B"
	if _, err := plugWorkflow(context.Background(), g, "/d", in); err == nil || g.creates != 0 {
		t.Fatalf("parent == before: err=%v creates=%d", err, g.creates)
	}
}

func TestPlugRefusesSecondBlockForAnotherPR(t *testing.T) {
	g := newFakeGraph("B", "M")
	if _, err := plugWorkflow(context.Background(), g, "/d", plugInstance(t, testPR)); err != nil {
		t.Fatal(err)
	}
	n := len(g.beads)
	_, err := plugWorkflow(context.Background(), g, "/d", plugInstance(t, "https://github.com/o/r/pull/8"))
	if err == nil || !strings.Contains(err.Error(), "another PR") || len(g.beads) != n {
		t.Fatalf("err=%v beads %d->%d", err, n, len(g.beads))
	}
}

func TestPlugClosedStepsAreNotRemainingAndNothingCloses(t *testing.T) {
	g := newFakeGraph("B", "M")
	in := plugInstance(t, testPR)
	res, _ := plugWorkflow(context.Background(), g, "/d", in)
	g.beads[res.Remaining[0].BeadID].Status = "closed"
	res, err := plugWorkflow(context.Background(), g, "/d", in)
	if err != nil || len(res.Remaining) != 5 {
		t.Fatalf("err=%v remaining %d", err, len(res.Remaining))
	}
	closedTarget := newFakeGraph("B", "M")
	closedTarget.beads["M"].Status = "closed"
	if _, err := plugWorkflow(context.Background(), closedTarget, "/d", in); err == nil || closedTarget.creates != 0 {
		t.Fatalf("closed target: err=%v", err)
	}
}

func TestCanonicalPR(t *testing.T) {
	got, err := canonicalPR(" https://GitHub.com/o/r/pull/7/ ")
	if err != nil || got != "https://github.com/o/r/pull/7" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "7", "https://github.com/o/r/issues/7", "https://github.com/o/r/pull/x"} {
		if _, err := canonicalPR(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWorkflowPlugVerb(t *testing.T) {
	g := newFakeGraph("B", "M")
	setupObjectiveProject(t, g)
	pinWorkflowGH(t, workflowFakeGH)
	run := func() (int, string, string) {
		stdout, stderr, read := withTempOutputFiles(t)
		code := runWorkflow([]string{"plug", "pr-review", "--project", "manifest", "--parent", "B", "--before", "M", "--pr", testPR}, stdout, stderr)
		o, e := read()
		return code, o, e
	}
	code, out, errOut := run()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var res workflowPlugResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || len(res.Remaining) != 6 || res.Template != "pr-review" {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if code, _, _ = run(); code != 0 || len(g.beads) != 9 {
		t.Fatalf("rerun: code %d beads %d", code, len(g.beads))
	}
	stdout, stderr, read := withTempOutputFiles(t)
	if runWorkflow([]string{"plug", "nope", "--project", "manifest", "--parent", "B", "--before", "M", "--pr", testPR}, stdout, stderr) == 0 {
		t.Fatal("unknown template must fail")
	}
	if _, e := read(); !strings.Contains(e, "not found") {
		t.Fatalf("stderr %s", e)
	}
}

func TestWorkflowLabelsAreReserved(t *testing.T) {
	for _, l := range []string{"workflow-step:review", "workflow-template:pr-review", "workflow-version:1", "workflow-instance:abc", "workflow-hash:abc"} {
		if verb, ok := reservedLabel(l); !ok || !strings.Contains(verb, "workflow plug") {
			t.Errorf("%s not reserved (%q)", l, verb)
		}
	}
	// internal-label refuses them.
	g := newFakeGraph("B")
	setupObjectiveProject(t, g)
	stdout, stderr, read := withTempOutputFiles(t)
	if runInternalLabel([]string{"--project", "manifest", "--bead", "B", "add", "workflow-step:review"}, stdout, stderr) == 0 {
		t.Fatal("internal-label minted a workflow label")
	}
	if _, e := read(); !strings.Contains(e, "workflow plug") {
		t.Fatalf("stderr %s", e)
	}
	// internal-create-objective has no way to carry labels.
	stdout, stderr, _ = withTempOutputFiles(t)
	if runInternalCreateObjective([]string{"--project", "manifest", "--title", "x", "--labels", "workflow-step:review"}, stdout, stderr) == 0 {
		t.Fatal("create verb took a --labels flag")
	}
}

// The first step of a block has no predecessor: it is born with the Manager's dispatch wait.
func TestPlugFirstStepHeld(t *testing.T) {
	g := newFakeGraph("B", "M")
	res, err := plugWorkflow(context.Background(), g, "/d", plugInstance(t, testPR))
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range res.Remaining {
		var waits []beads.Wait
		for _, l := range g.beads[s.BeadID].Labels {
			if w, ok := beads.ParseWaitLabel(l); ok {
				waits = append(waits, w)
			}
		}
		if i == 0 && (len(waits) != 1 || waits[0].Ref != beads.ManagerDispatchWait) {
			t.Fatalf("first step waits %+v, want Manager dispatch", waits)
		}
		if i > 0 && len(waits) != 0 {
			t.Fatalf("step %d carries waits %+v", i, waits)
		}
	}
}
