package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

func TestApprovalRetainsCustodyWithStoppedManagerAndMailFailure(t *testing.T) {
	bc, st, pid, _ := resolveFailureFixture(t)
	ctx := context.Background()
	bc.ListResult = append(bc.ListResult, beads.Bead{ID: "ms-1.2", Title: "Repair dev3", Status: "open", DependencyCount: 2, Labels: []string{"kind:objective"}})
	bc.DepsResult = map[string][]beads.Dep{"ms-1.2": {{IssueID: "ms-1.2", DependsOnID: "ms-1.1", Type: "blocks"}}}
	originalMail := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		return nil, nil, "", nil, errors.New("mail offline")
	}
	t.Cleanup(func() { mailDeps = originalMail })
	if resolveFailureRun(t, false) == 0 {
		t.Fatal("mail failure must surface")
	}
	gate, _ := bc.Show(ctx, "", "ms-1.1")
	if gate.Status != "closed" {
		t.Fatal("approval not applied")
	}
	next, _ := bc.Show(ctx, "", "ms-1.2")
	if !slices.Contains(next.Labels, "owner:manager-ms-1") {
		t.Fatal(next.Labels)
	}
	waits := beads.WaitsOf(next.Labels)
	if len(waits) != 1 || waits[0].Ref != beads.ManagerDispatchWait {
		t.Fatal(waits)
	}
	if next.Status != "open" {
		t.Fatal("queued work pretended to start")
	}
	snapshot := livedata.BuildSnapshot(livedata.Load(ctx, livedata.Deps{Store: st, Beads: bc}), nil)
	if len(snapshot.Broken) > 0 {
		t.Fatalf("approval orphaned work: %+v", snapshot.Broken)
	}
	for _, now := range []int64{time.Now().Unix(), time.Now().Add(10 * time.Minute).Unix()} {
		row := livedata.Board(snapshot, now).Workstreams[0]
		if row.Text == "State missing" || !strings.Contains(row.Text, "Manager") || row.Working != 0 {
			t.Fatalf("misleading row: %+v", row)
		}
	}
	r, _ := st.GetNeedsResolution(ctx, pid, "ms-1.1")
	if r == nil || r.Mailed {
		t.Fatal(r)
	}
	before := strings.Join(next.Labels, ",")
	mailDeps = originalMail
	if resolveFailureRun(t, true) != 0 {
		t.Fatal("mail retry failed")
	}
	next, _ = bc.Show(ctx, "", "ms-1.2")
	if strings.Join(next.Labels, ",") != before {
		t.Fatal("retry renewed or replaced custody")
	}
}

func TestApprovalHandoffPreservesOtherBlockersAndExistingCustody(t *testing.T) {
	for _, mode := range []string{"another blocker", "operator", "wait", "read failure", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			bc, _, _, _ := resolveFailureFixture(t)
			bc.ListResult = append(bc.ListResult, beads.Bead{ID: "ms-1.2", Status: "open", DependencyCount: 1, Labels: []string{"kind:objective"}})
			bc.DepsResult = map[string][]beads.Dep{"ms-1.2": {{IssueID: "ms-1.2", DependsOnID: "ms-1.1", Type: "blocks"}}}
			switch mode {
			case "another blocker":
				bc.ListResult = append(bc.ListResult, beads.Bead{ID: "other", Status: "open"})
				bc.DepsResult["ms-1.2"] = append(bc.DepsResult["ms-1.2"], beads.Dep{IssueID: "ms-1.2", DependsOnID: "other", Type: "blocks"})
			case "operator":
				bc.ListResult[2].Labels = append(bc.ListResult[2].Labels, "owner:operator-real")
			case "wait":
				bc.ListResult[2].Labels = append(bc.ListResult[2].Labels, "waiting:1:review:someone")
			case "read failure":
				bc.ParentsErr = errors.New("details unavailable")
			case "write failure":
				bc.writeErr = errors.New("cannot save custody")
			}
			err := holdApprovedWork(context.Background(), bc, "/fixture", "ms-1.1")
			if strings.Contains(mode, "failure") {
				if err == nil {
					t.Fatal("failure ignored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := bc.Show(context.Background(), "", "ms-1.2")
			for _, w := range beads.WaitsOf(b.Labels) {
				if w.Ref == beads.ManagerDispatchWait {
					t.Fatal("stole already held work", b)
				}
			}
		})
	}
}

func TestApprovalCustodyOnlyClearsForRealSuccessor(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		bc, _, _, _ := resolveFailureFixture(t)
		wait := beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Minute), Ref: beads.ManagerDispatchWait}.Label()
		bc.ListResult[1].Labels = append(bc.ListResult[1].Labels, wait, "handoff-notified:fixture")
		if err := clearSettleMarkers(context.Background(), bc, "/fixture", "ms-1.1", accepted); err != nil {
			t.Fatal(err)
		}
		b, _ := bc.Show(context.Background(), "", "ms-1.1")
		if slices.Contains(b.Labels, wait) == accepted {
			t.Fatalf("accepted=%v labels=%v", accepted, b.Labels)
		}
	}
}

type checkedApprovalClose struct {
	*resolveFailureClient
	fail    bool
	checked bool
}

func (c *checkedApprovalClose) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	if c.fail && id == "ms-1.2" {
		return errors.New("custody write failed")
	}
	return c.resolveFailureClient.SetLabel(ctx, dir, id, label, on)
}
func (c *checkedApprovalClose) Close(ctx context.Context, dir, id string) error {
	if id == "ms-1.1" {
		b, _ := c.Show(ctx, dir, "ms-1.2")
		if len(beads.WaitsOf(b.Labels)) != 1 {
			return errors.New("gate closed before successor custody")
		}
		c.checked = true
	}
	return c.resolveFailureClient.Close(ctx, dir, id)
}
func TestApprovalCannotReleaseWorkBeforeCustodyIsSaved(t *testing.T) {
	bc, _, _, _ := resolveFailureFixture(t)
	bc.ListResult = append(bc.ListResult, beads.Bead{ID: "ms-1.2", Status: "open", DependencyCount: 1, Labels: []string{"kind:objective"}})
	bc.DepsResult = map[string][]beads.Dep{"ms-1.2": {{IssueID: "ms-1.2", DependsOnID: "ms-1.1", Type: "blocks"}}}
	checked := &checkedApprovalClose{resolveFailureClient: bc, fail: true}
	previous := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return checked }
	t.Cleanup(func() { newBeadsClientFn = previous })
	if resolveFailureRun(t, false) == 0 {
		t.Fatal("custody failure hidden")
	}
	gate, _ := bc.Show(context.Background(), "", "ms-1.1")
	if gate.Status == "closed" {
		t.Fatal("released work after failed custody write")
	}
	checked.fail = false
	if resolveFailureRun(t, true) != 0 || !checked.checked {
		t.Fatal("retry did not write custody before releasing gate")
	}
}

// An approval that releases a step under a nested workstream hands it to that
// workstream's Manager; one still naming the root's Manager moves to the nested one.
func TestResolveNeeds_NestedStepDispatchedToNestedManager(t *testing.T) {
	ctx := context.Background()
	tree := beads.NestedTree()
	dir := t.TempDir()
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	tree.Seed(beads.Bead{ID: "demo-g", Labels: []string{"kind:objective", beads.GateLabel}})
	tree.Edges = append(tree.Edges, beads.Dep{IssueID: beads.NestedStep, DependsOnID: "demo-g", Type: "blocks"})
	for _, b := range tree.Beads {
		if b.ID == beads.NestedStep {
			b.DependencyCount = 2
			b.Labels = append(b.Labels, beads.OwnerLabelPrefix+"manager-"+beads.NestedRoot)
		}
	}
	if err := holdApprovedWork(ctx, tree, dir, "demo-g"); err != nil {
		t.Fatal(err)
	}
	b, _ := tree.Show(ctx, dir, beads.NestedStep)
	if !slices.Contains(b.Labels, beads.OwnerLabelPrefix+"manager-"+beads.NestedA1) || slices.Contains(b.Labels, beads.OwnerLabelPrefix+"manager-"+beads.NestedRoot) {
		t.Fatalf("labels %v, want the nested Manager as owner only", b.Labels)
	}
}

// Nested ids are dotted, owner: labels dashed: a step correctly owned by its
// nested Manager keeps custody, and a moved one is written dashed.
func TestResolveNeeds_DottedNestedOwnerLabel(t *testing.T) {
	ctx := context.Background()
	for _, start := range []string{"manager-demo-r-1", "manager-demo-r"} {
		dir := t.TempDir()
		if err := beads.SetNestedManagers(dir, true); err != nil {
			t.Fatal(err)
		}
		ws := []string{"kind:objective", beads.WorkstreamLabel}
		m := &beads.MemClient{EdgeParentsOnly: true}
		m.Seed(beads.Bead{ID: "demo-r", Labels: ws})
		m.Seed(beads.Bead{ID: "demo-r.1", Labels: ws})
		m.Seed(beads.Bead{ID: "demo-r.1.1", Labels: []string{"kind:objective", beads.OwnerLabelPrefix + start}, DependencyCount: 2})
		m.Seed(beads.Bead{ID: "demo-g", Labels: []string{"kind:objective", beads.GateLabel}})
		m.Edges = append(m.Edges,
			beads.Dep{IssueID: "demo-r.1", DependsOnID: "demo-r", Type: "parent-child"},
			beads.Dep{IssueID: "demo-r.1.1", DependsOnID: "demo-r.1", Type: "parent-child"},
			beads.Dep{IssueID: "demo-r.1.1", DependsOnID: "demo-g", Type: "blocks"})
		if err := holdApprovedWork(ctx, m, dir, "demo-g"); err != nil {
			t.Fatalf("%s: %v", start, err)
		}
		b, _ := m.Show(ctx, dir, "demo-r.1.1")
		var owners []string
		for _, l := range b.Labels {
			if o, ok := strings.CutPrefix(l, beads.OwnerLabelPrefix); ok {
				owners = append(owners, o)
			}
		}
		if !slices.Equal(owners, []string{"manager-demo-r-1"}) {
			t.Fatalf("%s: owner labels %v, want only the dashed nested Manager", start, owners)
		}
	}
}
