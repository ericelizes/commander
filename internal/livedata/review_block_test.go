package livedata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// seedOpenBlock plugs an open pr-review block before target: the first done
// steps closed with evidence on head, the next one worked by an Operator.
func seedOpenBlock(t *testing.T, mem *beads.MemClient, parent, target, block, head string, done int) {
	t.Helper()
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: parent, Before: target, PR: reviewedPR})
	if err != nil {
		t.Fatal(err)
	}
	mem.Seed(beads.Bead{ID: block, Title: "Review gate", Status: "open", ParentID: parent, Labels: append([]string{"kind:objective"}, inst.Labels...), Description: fmt.Sprintf("for %s\n\n%s", reviewedPR, inst.Snapshot)})
	mem.AddComment(context.Background(), "/p", block, workflowtemplate.WriterBinding{Revision: "rev", PR: reviewedPR, Head: head, Tasks: []string{"writer"}}.Comment())
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: target, DependsOnID: block, Type: "blocks"})
	for i, s := range tpl.Steps {
		id := fmt.Sprintf("%s.%d", block, i+1)
		b := beads.Bead{ID: id, Title: s.Title, Status: "open", ParentID: block, Labels: append([]string{"kind:objective"}, inst.Steps[i].Labels...)}
		switch {
		case i < done:
			b.Status = "closed"
		case i == done:
			b.Status = "in_progress"
			b.Labels = append(b.Labels, "owner:operator-p-fix")
		}
		mem.Seed(b)
		if i > 0 {
			mem.Edges = append(mem.Edges, beads.Dep{IssueID: id, DependsOnID: fmt.Sprintf("%s.%d", block, i), Type: "blocks"})
		}
		if i < done {
			ev := workflowtemplate.Evidence{Binding: "rev", Instance: inst.Key, Step: s.ID, Verifier: s.Verifier, PR: reviewedPR, Head: head, Finding: s.Verifier + " passed"}
			if err := mem.AddComment(context.Background(), "/p", id, ev.Comment()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// reviewSnapshot is the display path cmdrd runs: rows, snapshot, PR heads.
func reviewSnapshot(t *testing.T, mem *beads.MemClient, gh *fakeHeads) Snapshot {
	t.Helper()
	repoMu.Lock()
	repoCache["/p"] = "o/r"
	repoMu.Unlock()
	project := store.Project{ID: 1, Name: "p", Path: "/p"}
	rows, err := projectObjectiveRows(context.Background(), mem, project)
	if err != nil {
		t.Fatal(err)
	}
	snap := BuildSnapshot(Data{Projects: []store.Project{project}, Objectives: rows, LoadedAt: time.Unix(1793000000, 0),
		Agents: map[string]store.AgentState{"operator-p-fix": {State: store.AgentWorking, Updated: 1793000000}}}, nil)
	snap.Live = &SnapLive{Operators: []string{"operator-p-fix"}}
	refreshHeadsNow(t, &snap, gh)
	if snap.Agents == nil {
		snap.Agents = map[string]SnapAgent{}
	}
	for _, ws := range snap.Workstreams {
		snap.Agents[ws.ManagerRole] = SnapAgent{State: "idle", Since: 1792990000}
	}
	return snap
}

func reviewStep(t *testing.T, d Display, id string) (*WorkflowStep, *DiagramStep) {
	t.Helper()
	if len(d.Workstreams) != 1 {
		t.Fatalf("rows: %+v", d.Workstreams)
	}
	r := d.Workstreams[0]
	var ds *DiagramStep
	var walk func([]DiagramStep)
	walk = func(ss []DiagramStep) {
		for i := range ss {
			if ss[i].ID == id {
				ds = &ss[i]
			}
			walk(ss[i].Children)
		}
	}
	walk(r.Diagram.Steps)
	return findWorkflowStep(r.Workflow.Steps, id), ds
}

// An open block is one step of its parent's level, read as its work reads,
// with each check's state and finding under it, never its steps as children.
func TestReviewBlock_OpenBlockIsOneStepWithItsChecks(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	seedOpenBlock(t, mem, "w", "w.2", "w.9", "head1", 3)
	d := Board(reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"}), 1793000000)
	w, ds := reviewStep(t, d, "w.9")
	if w == nil || ds == nil || w.Review == nil || ds.Review == nil {
		t.Fatalf("block: workflow %+v diagram %+v", w, ds)
	}
	if len(ds.Children) != 0 {
		t.Fatalf("the block's steps are drawn as children: %+v", ds.Children)
	}
	if ds.Kind != w.Kind || ds.Text != w.Text || w.Kind != "working" || !strings.HasPrefix(w.Text, "Working on adversarial review") {
		t.Fatalf("block line: workflow %s %q, diagram %s %q", w.Kind, w.Text, ds.Kind, ds.Text)
	}
	rb := ds.Review
	if rb.Total != 6 || rb.Done != 3 || rb.Stale != 0 || rb.Head != "head1" || ds.Progress != "3 of 6 checks done" {
		t.Fatalf("counts: %+v progress %q", rb, ds.Progress)
	}
	c := rb.Checks[0]
	if c.Step != "devin-comments" || c.Kind != "done" || c.Verifier != "devin-comments-resolved" || c.Head != "head1" || !strings.Contains(c.Finding, "passed") {
		t.Fatalf("first check: %+v", c)
	}
	if c := rb.Checks[3]; c.Kind != "working" || c.Text != "Agent working" || c.Finding != "" {
		t.Fatalf("worked check: %+v", c)
	}
	if c := rb.Checks[5]; c.Kind != "idle" || c.Text != "Not started" {
		t.Fatalf("last check: %+v", c)
	}
	// The work is on a check inside the block: the block is drawn as current.
	if r := d.Workstreams[0]; r.Diagram.Current != "w.9" {
		t.Fatalf("current %q", r.Diagram.Current)
	}
}

// A closed block reads done while every check stands on the PR head, and
// stale (the Manager's move) after a new push, from the same folded steps.
func TestReviewBlock_ClosedBlockDoneThenStaleAfterAPush(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	seedClosedBlock(t, mem, "w.2", "head1")
	d := Board(reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"}), 1793000000)
	w, ds := reviewStep(t, d, "w.9")
	if w == nil || w.Review == nil || w.Kind != "done" || ds.Review.Done != 6 || ds.Progress != "6 of 6 checks done" {
		t.Fatalf("complete block: %+v / %+v", w, ds)
	}
	d = Board(reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head2"}), 1793000000)
	w, ds = reviewStep(t, d, "w.9")
	if w.Kind != "stalled" || !strings.HasPrefix(w.Text, "Checks need renewal: 6 to rerun") || ds.Text != w.Text {
		t.Fatalf("stale block: %s %q", w.Kind, w.Text)
	}
	c := ds.Review.Checks[0]
	if !c.Stale || c.Kind != "stalled" || c.Text != "Stale: checked head1, the PR is now at head2" || c.Finding != "passed" {
		t.Fatalf("stale check: %+v", c)
	}
	// graph check agrees: the stale checks are the ones the rule lists.
	var rule *DiagramFinding
	for _, f := range d.Workstreams[0].Diagram.Findings {
		if f.Check == "MERGE_REVIEW_INCOMPLETE" {
			rule = &f
		}
	}
	if rule == nil {
		t.Fatalf("no MERGE_REVIEW_INCOMPLETE beside the stale block: %+v", d.Workstreams[0].Diagram.Findings)
	}
	for _, c := range ds.Review.Checks {
		if !strings.Contains(rule.Why, c.ID) {
			t.Errorf("stale check %s is not in the rule's list: %s", c.ID, rule.Why)
		}
	}
}

// Before the first PR read completion cannot be established.
func TestReviewBlock_UnknownHeadWaitsForFreshness(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	seedClosedBlock(t, mem, "w.2", "head1")
	snap := reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
	var clear func([]SnapStep)
	clear = func(ss []SnapStep) {
		for i := range ss {
			if ss[i].WorkflowCheck != nil {
				ss[i].WorkflowCheck.Head = ""
			}
			for g := range ss[i].GateChecks {
				ss[i].GateChecks[g].Head = ""
			}
			clear(ss[i].Children)
		}
	}
	clear(snap.Workstreams[0].Steps)
	w, _ := reviewStep(t, Board(snap, 1793000000), "w.9")
	if w.Kind != "stalled" || w.Review.Done != 0 || w.Review.Checks[0].Text != "Waiting for the current PR head" {
		t.Fatalf("unknown head: %s %q %+v", w.Kind, w.Text, w.Review)
	}
}

// GEN_REVIEW_FIXTURE=1 writes testdata/{diagram,workflow}/review-block.json and
// app/test/fixtures/snapshot-review-block.json
// from the display path above (then UPDATE_BOARD=1 renders their goldens).
func TestReviewBlock_GenerateFixture(t *testing.T) {
	if os.Getenv("GEN_REVIEW_FIXTURE") == "" {
		t.Skip("set GEN_REVIEW_FIXTURE=1 to rewrite the review-block fixture")
	}
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w", Title: "Checkout rewrite", Status: "in_progress", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.0", Title: "Workflow approved", Status: "closed", ParentID: "w", Labels: []string{"kind:objective", "gate", "approver:user"}, AcceptanceCriteria: "The user approved the stages."})
	mem.Seed(beads.Bead{ID: "w.1", Title: "Build the checkout", Status: "closed", ParentID: "w", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", ParentID: "w", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}, AcceptanceCriteria: "PR 1234 is merged on main."})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "w.2", DependsOnID: "w.1", Type: "blocks"})
	seedOpenBlock(t, mem, "w", "w.2", "w.9", "head1", 3)
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "w.9", DependsOnID: "w.1", Type: "blocks"})
	snap := reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
	raw, err := json.MarshalIndent(map[string]any{"now": 1793000000, "snapshot": snap}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"testdata/diagram", "testdata/workflow"} {
		if err := os.WriteFile(dir+"/review-block.json", append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The app's shots fixture carries the board, as cmdrd serves it.
	d := Board(snap, 1793000000)
	snap.Display = &d
	app, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../../app/test/fixtures/snapshot-review-block.json", append(app, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewBlock_BindingFreshnessAgreesWithChecker(t *testing.T) {
	for _, tc := range []struct{ name, revision, pr, head, text string }{
		{"unbound", "", "", "", "writer binding required"},
		{"rebound", "rev2", reviewedPR, "head1", "current writer binding"},
		{"reverted selection", "rev3", reviewedPR, "head1", "current writer binding"},
		{"foreign PR", "rev", "https://github.com/other/repo/pull/8", "head1", "writer binding differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			seedStage(mem)
			mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
			seedClosedBlock(t, mem, "w.2", "head1")
			snap := reviewSnapshot(t, mem, &fakeHeads{state: "OPEN", head: "head1"})
			var replace func([]SnapStep)
			replace = func(ss []SnapStep) {
				for i := range ss {
					if ss[i].BeadID == "w.9" {
						ss[i].WorkflowCheck.Comments = nil
						if tc.revision != "" {
							ss[i].WorkflowCheck.Comments = []beads.Comment{{Text: workflowtemplate.WriterBinding{Revision: tc.revision, PR: tc.pr, Head: tc.head, Tasks: []string{"writer"}}.Comment()}}
						}
					}
					replace(ss[i].Children)
				}
			}
			replace(snap.Workstreams[0].Steps)
			d := Board(snap, 1793000000)
			w, ds := reviewStep(t, d, "w.9")
			if w.Kind != "stalled" || ds.Review.Done != 0 || ds.Review.Stale != 6 || ds.Text != w.Text {
				t.Fatalf("display: %+v / %+v", w, ds)
			}
			lines := ReviewCheckLines(ds.Review, "", "")
			for i, c := range ds.Review.Checks {
				if !c.Stale || !strings.Contains(c.Text, tc.text) || !strings.Contains(lines[i], tc.text) || c.Finding != "passed" {
					t.Fatalf("check/history: %+v / %q", c, lines[i])
				}
				found := false
				for _, f := range d.Workstreams[0].Diagram.Findings {
					if f.Check == "MERGE_REVIEW_INCOMPLETE" && strings.Contains(f.Why, c.ID) {
						found = true
					}
				}
				if !found {
					t.Fatalf("checker did not list stale check %s", c.ID)
				}
			}
		})
	}
}

// Every row uses the real snapshot -> display and snapshot -> checker paths.
func TestReviewBlock_SettledAndInvalidDispositionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, state, head, revision           string
		unbound, invalid, reopened, extraLive bool
		targetClosed                          bool
		wantDone                              int
		wantFinding                           string
	}{
		{name: "open bound", state: "OPEN", head: "head1", wantDone: 6},
		{name: "merged bound", state: "MERGED", head: "head1", wantDone: 6},
		{name: "merged unbound", state: "MERGED", head: "head1", unbound: true, wantDone: 6},
		{name: "merged invalid exempt", state: "MERGED", head: "head1", invalid: true, wantDone: 6},
		{name: "closed unmerged unbound", state: "CLOSED", head: "head1", unbound: true, wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "open unbound", state: "OPEN", head: "head1", unbound: true, wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "changed binding", state: "OPEN", head: "head1", revision: "rev2", wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "reverted binding new revision", state: "OPEN", head: "head1", revision: "rev3", wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "new head", state: "OPEN", head: "head2", wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "missing head", state: "OPEN", wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "unknown state and head", wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "invalid bound snapshot", state: "OPEN", head: "head1", invalid: true, wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "reopened stage", state: "OPEN", head: "head1", reopened: true, wantDone: 5, wantFinding: "MERGE_REVIEW_INCOMPLETE"},
		{name: "merged PR beside live PR", state: "MERGED", head: "head1", unbound: true, extraLive: true, wantDone: 6},
		// A closed target gets no PR read at all (RefreshWorkflowHeads skips it),
		// so it reads as the merge rule treats it: exempt, whatever the PR did.
		{name: "closed target merged", state: "MERGED", head: "head1", targetClosed: true, wantDone: 6},
		{name: "closed target merged unbound", state: "MERGED", head: "head1", unbound: true, targetClosed: true, wantDone: 6},
		{name: "closed target merged invalid", state: "MERGED", head: "head1", invalid: true, targetClosed: true, wantDone: 6},
		{name: "closed target unmerged PR closed", state: "CLOSED", head: "head1", targetClosed: true, wantDone: 6},
		{name: "closed target unmerged PR open", state: "OPEN", head: "head1", targetClosed: true, wantDone: 6},
		{name: "closed target unmerged PR moved on", state: "OPEN", head: "head2", targetClosed: true, wantDone: 6},
		{name: "closed target unknown state and head", targetClosed: true, wantDone: 6},
		{name: "closed target reopened stage", state: "MERGED", head: "head1", reopened: true, targetClosed: true, wantDone: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			seedStage(mem)
			target := beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}}
			if tc.targetClosed {
				target.Status = "closed"
			}
			mem.Seed(target)
			seedClosedBlock(t, mem, "w.2", "head1")
			snap := reviewSnapshot(t, mem, &fakeHeads{state: tc.state, head: tc.head})
			var edit func([]SnapStep)
			edit = func(ss []SnapStep) {
				for i := range ss {
					s := &ss[i]
					if s.BeadID == "w.2" && tc.extraLive {
						s.WorkflowCheck.Bead.Labels = append(s.WorkflowCheck.Bead.Labels, "pr:8")
						s.WorkflowCheck.PRStates["https://github.com/o/r/pull/8"] = "OPEN"
					}
					if s.BeadID == "w.9" {
						b := s.WorkflowCheck
						if tc.unbound {
							b.Comments = nil
						}
						if tc.revision != "" {
							b.Comments = append(b.Comments, beads.Comment{Text: workflowtemplate.WriterBinding{Revision: tc.revision, PR: reviewedPR, Head: "head1", Tasks: []string{"writer"}}.Comment()})
						}
						if tc.invalid {
							b.Bead.Description = "for " + reviewedPR + "\n\n{invalid"
						}
						if tc.reopened {
							b.Steps[0].Bead.Status = "open"
						}
					}
					edit(s.Children)
				}
			}
			edit(snap.Workstreams[0].Steps)
			d := Board(snap, 1793000000)
			w, ds := reviewStep(t, d, "w.9")
			if w == nil || ds == nil || ds.Review == nil {
				t.Fatal("block missing")
			}
			if ds.Review.Done != tc.wantDone || w.Text != ds.Text {
				t.Fatalf("display: %+v / %+v", w, ds)
			}
			if tc.wantDone < 6 && w.Kind == "done" {
				t.Fatalf("refused block reads Done: %+v", w)
			}
			if tc.invalid && tc.state != "MERGED" && !strings.Contains(w.Text, "Invalid template snapshot") {
				t.Fatalf("invalid snapshot not actionable: %s", w.Text)
			}
			if tc.targetClosed {
				if ds.Review.Stale != 0 {
					t.Fatalf("receipts of a closed target read stale: %+v", ds.Review)
				}
				if tc.wantDone == 6 && (w.Kind != "done" || w.Text != "Done") {
					t.Fatalf("complete block of a closed target: %s %q", w.Kind, w.Text)
				}
			}
			found := ""
			for _, finding := range CheckDiagram(snap.Workstreams[0], "w.2") {
				if strings.HasPrefix(finding.Check, "MERGE_REVIEW_") {
					found = finding.Check
				}
			}
			if found != tc.wantFinding {
				t.Fatalf("checker %q want %q", found, tc.wantFinding)
			}
		})
	}
}

// A block settle-block closed after its PR merged outside the review reads
// settled, not reviewed: the checks it closed say so, the ones checked before
// the merge keep their evidence, and nothing on it reads Done for the block.
func TestReviewBlock_SettledBlockReadsSettledNotReviewed(t *testing.T) {
	mem := &beads.MemClient{}
	seedStage(mem)
	mem.Seed(beads.Bead{ID: "w.2", Title: "PR merged", Labels: []string{"kind:objective", "gate", "approver:user", "pr:1234"}})
	seedClosedBlock(t, mem, "w.2", "head1")
	key := ""
	for _, b := range mem.Beads {
		if b.ID == "w.9" {
			key = workflowtemplate.Label(*b, workflowtemplate.LabelInstance)
		}
	}
	// Steps 3..6 were never checked: the settle is their newest evidence.
	for i := 3; i <= 6; i++ {
		id := fmt.Sprintf("w.9.%d", i)
		for _, b := range mem.Beads {
			if b.ID == id {
				step := workflowtemplate.Label(*b, workflowtemplate.LabelStep)
				ev := workflowtemplate.Evidence{Instance: key, Step: step, Verifier: workflowtemplate.SettledVerifier, PR: reviewedPR, Head: "head9", Finding: workflowtemplate.SettledFinding("head9", "merged before the review ran")}
				mem.AddComment(context.Background(), "/p", id, ev.Comment())
			}
		}
	}
	snap := reviewSnapshot(t, mem, &fakeHeads{state: "MERGED", head: "head9"})
	d := Board(snap, 1793000000)
	w, ds := reviewStep(t, d, "w.9")
	if w == nil || ds == nil || ds.Review == nil {
		t.Fatal("block missing")
	}
	rb := ds.Review
	if rb.Done != 2 || rb.Settled != 4 || rb.Stale != 0 {
		t.Fatalf("counts: %+v", rb)
	}
	if w.Kind != "done" || w.Text != "Settled: PR merged, review not completed" || ds.Text != w.Text {
		t.Fatalf("block line: %s %q / %q", w.Kind, w.Text, ds.Text)
	}
	if ds.Progress != "2 of 6 checks done, 4 settled: PR merged, not reviewed" {
		t.Fatalf("progress %q", ds.Progress)
	}
	if c := rb.Checks[0]; c.Settled || c.Text != "Done" || c.Finding != "passed" {
		t.Fatalf("checked before the merge: %+v", c)
	}
	if c := rb.Checks[5]; !c.Settled || c.Kind != "done" || c.Text != "Settled: PR merged, not reviewed" || !strings.Contains(c.Finding, "review not completed") {
		t.Fatalf("settled check: %+v", c)
	}
	for _, f := range CheckDiagram(snap.Workstreams[0], "w.2") {
		if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
			t.Fatalf("finding beside a settled block: %+v", f)
		}
	}
}
