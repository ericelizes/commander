package workflowtemplate

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func completeBlock(t *testing.T) []Node {
	t.Helper()
	tpl, err := Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := Instantiate(tpl, Params{Project: "p", Parent: "B", Before: "M", PR: testURL})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []Node{{Bead: beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Status: "open", Labels: []string{"gate"}}, Waits: []string{"R"}, Head: "abc"}, {Bead: beads.Bead{ID: "R", ParentID: "B", Status: "closed", Labels: inst.Labels, Description: fmt.Sprintf("for %s\n\n%s", testURL, inst.Snapshot)}}}
	nodes[1].Comments = []beads.Comment{{Text: WriterBinding{Revision: "rev", PR: testURL, Head: "abc", Tasks: []string{"writer"}}.Comment()}}
	for i, s := range tpl.Steps {
		id := fmt.Sprintf("S%d", i)
		n := Node{Bead: beads.Bead{ID: id, ParentID: "R", Status: "closed", Labels: inst.Steps[i].Labels}, Comments: []beads.Comment{{Text: Evidence{Binding: "rev", Instance: inst.Key, Step: s.ID, Verifier: s.Verifier, PR: testURL, Head: "abc", Finding: "passed"}.Comment()}}}
		if i > 0 {
			n.Waits = []string{fmt.Sprintf("S%d", i-1)}
		}
		nodes = append(nodes, n)
	}
	return nodes
}
func TestMergeRule(t *testing.T) {
	tests := []struct {
		name, want string
		edit       func([]Node) []Node
	}{
		{"complete", "", func(n []Node) []Node { return n }},
		{"missing", "MERGE_REVIEW_REQUIRED", func(n []Node) []Node { n[0].Waits = nil; return n }},
		{"open", "MERGE_REVIEW_INCOMPLETE", func(n []Node) []Node { n[2].Bead.Status = "open"; return n }},
		{"stale head", "MERGE_REVIEW_INCOMPLETE", func(n []Node) []Node { n[0].Head = "new"; return n }},
		{"missing evidence", "MERGE_REVIEW_INCOMPLETE", func(n []Node) []Node { n[2].Comments = nil; return n }},
		{"missing child", "MERGE_REVIEW_INCOMPLETE", func(n []Node) []Node { return n[:len(n)-1] }},
		{"missing edge", "MERGE_REVIEW_INCOMPLETE", func(n []Node) []Node { n[3].Waits = nil; return n }},
		{"closed exempt", "", func(n []Node) []Node { n[0].Waits = nil; n[0].Bead.Status = "closed"; return n }},
		{"merged exempt", "", func(n []Node) []Node {
			n[0].Waits = nil
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:7")
			n[0].PRStates = map[string]string{testURL: "MERGED"}
			return n
		}},
		{"block's PR merged", "", func(n []Node) []Node {
			n[1].Bead.Status = "open"
			n[0].PRStates = map[string]string{testURL: "MERGED"}
			return n
		}},
		// F6: free text on the target names no PR.
		{"merged URL in the description", "MERGE_REVIEW_REQUIRED", func(n []Node) []Node {
			n[0].Waits = nil
			n[0].Bead.Description = "follows https://github.com/o/r/pull/1"
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/1": "MERGED"}
			return n
		}},
		// F6: another merged PR does not settle the block's open one.
		{"merged label beside an open block", "MERGE_REVIEW_INCOMPLETE", func(n []Node) []Node {
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:1")
			n[1].Bead.Status = "open"
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/1": "MERGED", testURL: "OPEN"}
			return n
		}},
		// F7: every pr: label is judged.
		{"merged and open labels, no block", "MERGE_REVIEW_REQUIRED", func(n []Node) []Node {
			n[0].Waits = nil
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:1", "pr:8")
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/1": "MERGED", "https://github.com/o/r/pull/8": "OPEN"}
			return n
		}},
		{"open label beside a complete block", "MERGE_REVIEW_REQUIRED", func(n []Node) []Node {
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:8")
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/8": "OPEN", testURL: "OPEN"}
			return n
		}},
		// F7: an abandoned PR does not refuse the live PR's complete block...
		{"abandoned label beside a complete block", "", func(n []Node) []Node {
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:1")
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/1": "CLOSED", testURL: "OPEN"}
			return n
		}},
		// ...and alone it is no merge.
		{"only an abandoned label", "MERGE_REVIEW_REQUIRED", func(n []Node) []Node {
			n[0].Waits = nil
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:1")
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/1": "CLOSED"}
			return n
		}},
		{"abandoned and merged labels", "", func(n []Node) []Node {
			n[0].Waits = nil
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:1", "pr:2")
			n[0].PRStates = map[string]string{"https://github.com/o/r/pull/1": "CLOSED", "https://github.com/o/r/pull/2": "MERGED"}
			return n
		}},
		{"unknown state counts as open", "MERGE_REVIEW_REQUIRED", func(n []Node) []Node {
			n[0].Waits = nil
			n[0].Bead.Labels = append(n[0].Bead.Labels, "pr:1")
			return n
		}},
		{"wrong scope", "", func(n []Node) []Node { return n }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := tt.edit(completeBlock(t))
			scope := map[string]bool{"M": true}
			if tt.name == "wrong scope" {
				scope = map[string]bool{"X": true}
			}
			got := MergeRule("p", "o/r", n, scope)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatal(got)
				}
				return
			}
			if len(got) != 1 || got[0].Check != tt.want {
				t.Fatal(got)
			}
		})
	}
}
func TestMergeTargetWordsAndPR(t *testing.T) {
	for _, b := range []beads.Bead{{Title: "merge PR", Labels: []string{"gate"}}, {Labels: []string{"gate-name:merged"}}} {
		if !MergeGate(Node{Bead: b}) || !MergeTarget(Node{Bead: b}) {
			t.Fatal(b)
		}
	}
	for _, title := range []string{"merger approved", "pre-merge checks", "merge-queue set up"} {
		if MergeTarget(Node{Bead: beads.Bead{Title: title, Labels: []string{"gate"}}}) {
			t.Fatalf("%q reads as the merge", title)
		}
	}
	// pr: alone tracks a PR; with a merge title it is a merge target.
	plain := Node{Bead: beads.Bead{Title: "Worker retry", Labels: []string{"pr:7"}}}
	if MergeGate(plain) || MergeTarget(plain) {
		t.Fatal("plain pr: step is a target")
	}
	titled := Node{Bead: beads.Bead{Title: "Widget service PR 7 merged", Labels: []string{"pr:7"}}}
	if MergeGate(titled) || !MergeTarget(titled) {
		t.Fatal("merge step")
	}
	for _, label := range []string{"superseded", "retired"} {
		if MergeTarget(Node{Bead: beads.Bead{Title: "merge PR", Labels: []string{"gate", "gate-name:merged", label}}}) {
			t.Fatal(label)
		}
	}
}

// A pr: step is the merge only when its title means merging the PR.
func TestMergeStepTitles(t *testing.T) {
	for _, c := range []struct {
		title string
		merge bool
	}{
		{"Merge PR", true},
		{"Merge PR 12 for the API", true},
		{"merge the pull request", true},
		{"Merge #12", true},
		{"Merged", true},
		{"Merge it", true},
		{"merge to main", true},
		{"API fix merged into master", true},
		{"PR merged", true},
		{"Widget service PR 7 merged", true},
		{"PR #7 is merged", true},
		// review commander-il9.135.5.6: merge steps the old allow-list missed
		{"Squash and merge PR 12", true},
		{"Review and merge PR 12", true},
		{"Merge the API PR", true},
		{"Merge this PR", true},
		{"Merge widget PR #7", true},
		{"Merge PRs", true},
		{"Squash-merge PR 12", true},
		{"Approve and merge", true},
		{"Merging the fix", true},
		{"Merge feature into main", true},
		{"Fix merge conflicts and merge PR 12", true},
		{"Rebase, then merge", true},
		{"Fix merge conflicts", false},
		{"Fix merge conflicts on the API branch", false},
		{"Merge main into feature", false},
		{"merge queue setup", false},
		{"Merge queue work", false},
		{"pre-merge checks", false},
		{"merger approved", false},
		{"Merge master into the API branch", false},
		{"Merge origin/main into feature", false},
		{"Resolve merge conflict", false},
		{"post-merge cleanup", false},
		{"Squash the merge commits", false},
		{"Open a merge request", false},
		{"Set up merge queues", false},
		{"Mergeable check", false},
		{"Worker retry", false},
	} {
		n := Node{Bead: beads.Bead{Title: c.title, Labels: []string{"pr:12"}}}
		if got := MergeTarget(n); got != c.merge {
			t.Errorf("%q: MergeTarget = %v, want %v", c.title, got, c.merge)
		}
	}
}

func TestLabelPRsResolvesTheLabelsCommanderWrites(t *testing.T) {
	for _, c := range []struct {
		label, repo, want string
	}{
		{"pr:1234", "o/r", "https://github.com/o/r/pull/1234"},
		{"pr:other/name#7", "o/r", "https://github.com/other/name/pull/7"},
		{"pr:Other/Name#7", "", "https://github.com/other/name/pull/7"},
		{"pr:https://github.com/O/R/pull/9", "x/y", "https://github.com/o/r/pull/9"},
		{"pr:1234", "Acme/Web", "https://github.com/acme/web/pull/1234"},
		{"pr:1234", "", ""},
		{"pr:?", "o/r", ""},
	} {
		got := strings.Join(LabelPRs(beads.Bead{Labels: []string{c.label}}, c.repo), "")
		if got != c.want {
			t.Fatalf("%s repo %q: %q", c.label, c.repo, got)
		}
	}
	got := LabelPRs(beads.Bead{Labels: []string{"pr:7", "x", "pr:8"}}, "o/r")
	if len(got) != 2 || !strings.HasSuffix(got[1], "/8") {
		t.Fatalf("every label is read: %v", got)
	}
}

func TestBlockPRIsCanonical(t *testing.T) {
	got := BlockPR(beads.Bead{Description: "Workflow pr-review v1 (h) for https://github.com/Acme/Web/pull/7.\nSnapshot..."})
	if got != "https://github.com/acme/web/pull/7" {
		t.Fatal(got)
	}
	if got := BlockPR(beads.Bead{Description: "no link"}); got != "" {
		t.Fatal(got)
	}
}

func TestTargetPRs(t *testing.T) {
	nodes := completeBlock(t)
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.Bead.ID] = n
	}
	byID["s.1"] = Node{Bead: beads.Bead{ID: "s.1", Description: "follow-up to https://github.com/o/r/pull/1", Labels: []string{"pr:5"}}}
	gate := nodes[0]
	gate.Bead.Description = "after https://github.com/o/r/pull/2"
	gate.Bead.Labels = []string{"gate", "pr:7", "pr:6", "gate-of:" + beads.EscapeLabel("s.1")}
	want := []string{testURL, "https://github.com/o/r/pull/6", "https://github.com/o/r/pull/5"}
	if got := TargetPRs(gate, byID, "p", "o/r"); !slices.Equal(got, want) {
		t.Fatalf("block first, then labels, no free text, no repeats: %v", got)
	}
	if got := TargetPR(gate, byID, "p", "o/r"); got != testURL {
		t.Fatal(got)
	}
	// A review block not plugged for this target is judged on nothing, only named in the fix.
	loose := Node{Bead: beads.Bead{ID: "g", Labels: []string{"gate"}}, Waits: []string{"R"}}
	if got := TargetPRs(loose, byID, "p", "o/r"); len(got) != 0 {
		t.Fatal(got)
	}
	if got := TargetPR(loose, byID, "p", "o/r"); got != testURL {
		t.Fatal(got)
	}
}

func TestMergeReviewIncompletePrintsTheCommands(t *testing.T) {
	nodes := completeBlock(t)
	nodes[0].Head = "new"
	f := MergeRule("p", "o/r", nodes, map[string]bool{"M": true})
	if len(f) != 1 || f[0].Check != "MERGE_REVIEW_INCOMPLETE" {
		t.Fatal(f)
	}
	want := []string{"cmdr workflow bind-writers --project p --block R --writers <original-task,actual-fix-task> --head <current-full-sha>"}
	for i := range 6 {
		if i >= 1 && i <= 4 {
			want = append(want, fmt.Sprintf(`cmdr internal-comment --project p --bead S%d --text 'workflow-review-proof@v1 {"review_bead":"<separate-review-task>","head":"<full-sha>","fix_commits":["<full-fix-sha>"]}'`, i))
		}
		want = append(want, fmt.Sprintf("cmdr internal-close-objective --project p --bead S%d", i))
	}
	if got, w := f[0].Fix, strings.Join(want, " ; "); got != w {
		t.Fatalf("fix:\n%s\nwant:\n%s", got, w)
	}
	if !strings.Contains(f[0].Text, f[0].Fix) || strings.Contains(f[0].Text, "workflow plug") {
		t.Fatal(f[0].Text)
	}
	// F11: no finding is claimed for anyone; it is named as the alternative, in the text only.
	if strings.Contains(f[0].Fix, "no_findings") || !strings.Contains(f[0].Text, `"no_findings":true,"fix_commits":[]`) || !strings.Contains(f[0].Text, "closed separate review of new") {
		t.Fatal(f[0].Text)
	}

	nodes = completeBlock(t)
	nodes[1].Bead.Status = "open"
	nodes[0].Head = "abc"
	if f := MergeRule("p", "o/r", nodes, map[string]bool{"M": true}); len(f) != 1 || f[0].Fix != "cmdr internal-close-objective --project p --bead R" {
		t.Fatal(f)
	}

	nodes = completeBlock(t)[:7]
	f = MergeRule("p", "o/r", nodes, map[string]bool{"M": true})
	if len(f) != 1 || !strings.HasPrefix(f[0].Fix, "cmdr workflow plug pr-review") || slices.Contains(f[0].Stale, "") {
		t.Fatal(f)
	}
	// The step plug makes is closed too, after the ones before it.
	tpl, err := Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	last := "cmdr internal-close-objective --project p --bead <" + tpl.Steps[len(tpl.Steps)-1].ID + "-bead-from-plug>"
	if !strings.HasSuffix(f[0].Fix, last) {
		t.Fatalf("fix does not end with the missing step's close: %s", f[0].Fix)
	}
	nodes = completeBlock(t)
	nodes[3].Waits = nil
	f = MergeRule("p", "o/r", nodes, map[string]bool{"M": true})
	if len(f) != 1 || !strings.HasPrefix(f[0].Fix, "cmdr workflow plug pr-review") || !strings.Contains(f[0].Fix, "--bead S1") {
		t.Fatal(f)
	}
}

func TestMergeRulePlannedPRIsAdvisory(t *testing.T) {
	nodes := []Node{{Bead: beads.Bead{ID: "M", Title: "PR merged", Status: "open", Labels: []string{"gate"}}}}
	got := MergeRule("p", "o/r", nodes, nil)
	if len(got) != 1 || !got[0].Advisory || strings.Contains(got[0].Fix, "<URL>") {
		t.Fatalf("planned = %+v", got)
	}
	nodes[0].Bead.Labels = append(nodes[0].Bead.Labels, "pr:7")
	got = MergeRule("p", "o/r", nodes, nil)
	if len(got) != 1 || got[0].Advisory || !strings.Contains(got[0].Fix, testURL) {
		t.Fatalf("with PR = %+v", got)
	}
}

// Advisory only with no PR evidence at all. Evidence the PR readings cannot resolve
// still blocks, and its plug command names that PR.
func TestMergeRuleKnownPRIsNeverAdvisory(t *testing.T) {
	merge := func(labels ...string) Node {
		return Node{Bead: beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Status: "open", Labels: append([]string{"gate"}, labels...)}}
	}
	gateOf := "gate-of:" + beads.EscapeLabel("s.1")
	stage := func(labels ...string) Node {
		return Node{Bead: beads.Bead{ID: "s.1", Title: "Build", Status: "open", Labels: labels}}
	}
	for _, c := range []struct {
		name  string
		repo  string
		nodes []Node
		fix   string // empty = the advisory
	}{
		{name: "no evidence", repo: "o/r", nodes: []Node{merge()}},
		{name: "no evidence, no repo", nodes: []Node{merge()}},
		{name: "a stage with no pr label", repo: "o/r", nodes: []Node{merge(gateOf), stage()}},
		{name: "bare label, repo unknown", nodes: []Node{merge("pr:7")}, fix: "--pr <URL of pr:7>"},
		{name: "bare label on the stage, repo unknown", nodes: []Node{merge(gateOf), stage("pr:7")}, fix: "--pr <URL of pr:7>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := MergeRule("p", c.repo, c.nodes, map[string]bool{"M": true})
			if len(got) != 1 {
				t.Fatalf("findings = %+v", got)
			}
			if c.fix == "" {
				if !got[0].Advisory || !strings.Contains(got[0].Text, "no PR yet") {
					t.Fatalf("want the advisory: %+v", got[0])
				}
				return
			}
			if got[0].Advisory || got[0].Check != "MERGE_REVIEW_REQUIRED" || strings.Contains(got[0].Text, "no PR yet") ||
				!strings.HasPrefix(got[0].Fix, "cmdr workflow plug pr-review") || !strings.HasSuffix(got[0].Fix, c.fix) {
				t.Fatalf("want a blocker ending %q: %+v", c.fix, got[0])
			}
		})
	}
}

// A merged gate on a stage: the stage's pr: label is judged beside the gate's own
// block, and clearing it leaves the gate judged on that block.
func TestMergeRuleStageLabelBesideAGatesBlock(t *testing.T) {
	nodes := completeBlock(t)
	nodes[0].Bead.Labels = []string{"gate-name:merged", "gate-of:STAGE"}
	stage := Node{Bead: beads.Bead{ID: "STAGE", Title: "Fix merged to main", Status: "open", Labels: []string{"pr:9"}}}
	nodes = append(nodes, stage)
	got := MergeRule("p", "o/r", nodes, map[string]bool{"M": true, "STAGE": true})
	if len(got) != 2 || !strings.Contains(got[0].Text, "pull/9") {
		t.Fatalf("label on: %+v", got)
	}
	nodes[len(nodes)-1].Bead.Labels = nil
	if got := MergeRule("p", "o/r", nodes, map[string]bool{"M": true, "STAGE": true}); len(got) != 0 {
		t.Fatalf("label cleared: %+v", got)
	}
	if prs := TargetPRs(nodes[0], nodeIndex(nodes), "p", "o/r"); !slices.Equal(prs, []string{testURL}) {
		t.Fatalf("the block's PR stays: %v", prs)
	}
}

func nodeIndex(nodes []Node) map[string]Node {
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.Bead.ID] = n
	}
	return byID
}

// plug refuses a target that waits on a block of another instance, so the fix line
// must not offer it; a plug that finishes the target's own block names that block's parent.
func TestMergeRuleNeverOffersARefusedPlug(t *testing.T) {
	t.Run("a second live PR beside the block, label on the stage", func(t *testing.T) {
		nodes := completeBlock(t)
		nodes[0].Bead.Labels = []string{"gate-name:merged", "gate-of:STAGE"}
		nodes = append(nodes, Node{Bead: beads.Bead{ID: "STAGE", Title: "Build", Status: "open", Labels: []string{"pr:9"}}})
		got := MergeRule("p", "o/r", nodes, map[string]bool{"M": true})
		if len(got) != 1 || got[0].Check != "MERGE_REVIEW_REQUIRED" {
			t.Fatalf("findings: %+v", got)
		}
		if strings.Contains(got[0].Fix, "workflow plug") || strings.Contains(got[0].Text, "workflow plug") {
			t.Fatalf("offers a refused plug: %+v", got[0])
		}
		if !strings.Contains(got[0].Fix, "--bead STAGE --review none") {
			t.Fatalf("clears the label where it sits: %s", got[0].Fix)
		}
	})
	t.Run("a merge gate with no label, on another target's block", func(t *testing.T) {
		nodes := completeBlock(t)
		nodes = append(nodes, Node{Bead: beads.Bead{ID: "G", Title: "Merged", Status: "open", Labels: []string{"gate-name:merged"}}, Waits: []string{"R"}})
		got := MergeRule("p", "o/r", nodes, map[string]bool{"G": true})
		if len(got) != 1 || got[0].Advisory || strings.Contains(got[0].Fix, "workflow plug") || strings.Contains(got[0].Fix, "--review none") || !strings.Contains(got[0].Text, testURL) || !strings.Contains(got[0].Fix, "G waits on R") {
			t.Fatalf("findings: %+v", got)
		}
	})
	t.Run("no other block: plug is still the fix", func(t *testing.T) {
		got := MergeRule("p", "o/r", []Node{{Bead: beads.Bead{ID: "G", Title: "Merged", Status: "open", Labels: []string{"gate-name:merged", "pr:9"}}}}, nil)
		if len(got) != 1 || !strings.HasPrefix(got[0].Fix, "cmdr workflow plug pr-review") {
			t.Fatalf("findings: %+v", got)
		}
	})
	t.Run("a missing step's plug names the block's own parent", func(t *testing.T) {
		tpl, err := Load("pr-review")
		if err != nil {
			t.Fatal(err)
		}
		inst, err := Instantiate(tpl, Params{Project: "p", Parent: "CLOSED", Before: "T", PR: testURL})
		if err != nil {
			t.Fatal(err)
		}
		nodes := []Node{
			{Bead: beads.Bead{ID: "T", ParentID: "B", Title: "Merged", Status: "open", Labels: []string{"gate-name:merged"}}, Waits: []string{"R"}, Head: "h"},
			{Bead: beads.Bead{ID: "R", ParentID: "CLOSED", Status: "open", Labels: inst.Labels, Description: "for " + testURL + "\n\n" + string(inst.Snapshot)}},
		}
		got := MergeRule("p", "o/r", nodes, map[string]bool{"T": true})
		if len(got) != 1 || !strings.Contains(got[0].Fix, "workflow plug pr-review --project p --parent CLOSED --before T") {
			t.Fatalf("findings: %+v", got)
		}
	})
}
