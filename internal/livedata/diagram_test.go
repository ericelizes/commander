package livedata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The diagram fixtures: a snapshot and its clock (testdata/diagram/*.json),
// every row's derived diagram (*.want.json, by row key) and its text
// (*.lines.txt). UPDATE_BOARD=1 rewrites both; review the diff.
func TestDiagramFixtures(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	files, _ := filepath.Glob("testdata/diagram/*.json")
	if len(files) == 0 {
		t.Fatal("no diagram fixtures")
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".want.json") {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			d := fixtureBoard(t, f)
			checkDiagrams(t, d)
			byRow := map[string]*Diagram{}
			var lines []string
			for _, r := range d.Workstreams {
				byRow[r.Key] = r.Diagram
				lines = append(lines, "#"+itoa(int64(r.N))+" "+r.Title)
				lines = append(lines, DiagramLines(r.Diagram)...)
				lines = append(lines, "")
			}
			got, _ := json.MarshalIndent(byRow, "", " ")
			text := strings.Join(lines, "\n")
			base := strings.TrimSuffix(f, ".json")
			if os.Getenv("UPDATE_BOARD") != "" {
				_ = os.WriteFile(base+".want.json", append(got, '\n'), 0o644)
				_ = os.WriteFile(base+".lines.txt", []byte(text), 0o644)
			}
			wantRaw, err := os.ReadFile(base + ".want.json")
			if err != nil {
				t.Fatal(err)
			}
			if wantLines, _ := os.ReadFile(base + ".lines.txt"); string(wantLines) != text {
				t.Errorf("diagram lines differ:\n%s", text)
			}
			var want, have any
			_ = json.Unmarshal(wantRaw, &want)
			_ = json.Unmarshal(got, &have)
			if diff := jsonDiff("", want, have); diff != "" {
				t.Errorf("diagram differs:\n%s", diff)
			}
		})
	}
}

// Each step sits on the line of whoever has the next move, read from beads.
func TestDiagram_Lanes(t *testing.T) {
	bex := fixtureDiagram(t, "lab:lab-bex")
	ops := fixtureDiagram(t, "lab:lab-ops")
	nightly := bex.Sub("lab-bex.2")
	finance := bex.Sub("lab-bex.3")
	if nightly == nil || finance == nil {
		t.Fatalf("tracks = %+v", bex.Tracks)
	}
	for _, c := range []struct {
		d        *Diagram
		id, want string
	}{
		{bex, "lab-bex.1", "you|"},                                      // a passed user gate stays on the user's line
		{bex, "lab-bex.2", "automatic|CI, runs on each push to the PR"}, // the track reads as its CI wait
		{bex, "lab-bex.3", "you|"},                                      // the track's ready decision outranks the Operator at work
		{bex, "lab-bex.4", "|"},                                         // nothing holds it: nobody's line
		{bex, "lab-bex.5", "manager|"},                                  // only the Manager approves
		{bex, "lab-bex.6", "automatic|parked until Tue 11:20 AM, then the Manager picks it up"}, // parked until a time
		{bex, "lab-bex.8", "operators|"}, // a bad lane label is ignored: planned work
		{bex, "lab-bex.9", "manager|"},   // Manager to close
		{nightly, "lab-bex.2.2", "automatic|CI, runs on each push to the PR"},
		{nightly, "lab-bex.2.3", "reviewers|reviewer-a, reviewer-b"},
		{nightly, "lab-bex.2.4", "you|"},
		{nightly, "lab-bex.2.5", "operators|"},
		{finance, "lab-bex.3.1", "operators|"},
		{finance, "lab-bex.3.2", "reviewers|finance team"}, // the lane label, where nothing else says
		{finance, "lab-bex.3.4", "you|"},
		{ops, "lab-ops.1", "manager|"}, // the Operator died: the Manager's move
		{ops, "lab-ops.2", "reviewers|oncall-team"},
		{ops, "lab-ops.3", "you|"},
		{ops, "lab-ops.4", "manager|"},
		{ops, "lab-ops.5", "automatic|a timer, then the Manager confirms"},
	} {
		s := diagramStep(c.d, c.id)
		if s == nil || s.Actor+"|"+s.Who != c.want || s.Lane != laneOfActor(s.Actor) {
			t.Errorf("%s = %+v, want %s", c.id, s, c.want)
		}
	}
	if finance.Current != "lab-bex.3.4" || ops.Current != "lab-ops.3" {
		t.Errorf("current: finance %q, ops %q, want the ready decisions", finance.Current, ops.Current)
	}
	if g := diagramStep(nightly, "lab-bex.2.3"); g.Criteria == "" || !g.Gate || g.Approvers != "reviewer-a, reviewer-b" {
		t.Errorf("reviewer gate = %+v", g)
	}
}

// Steps read in work order: what a step waits on is drawn above it.
func TestDiagram_WorkOrder(t *testing.T) {
	bex := fixtureDiagram(t, "lab:lab-bex")
	nightly := bex.Sub("lab-bex.2")
	var ids []string
	for _, s := range nightly.Steps {
		ids = append(ids, s.ID)
	}
	if want := "lab-bex.2.1,lab-bex.2.2,lab-bex.2.3,lab-bex.2.4,lab-bex.2.5"; strings.Join(ids, ",") != want {
		t.Fatalf("order = %v, want %s", ids, want)
	}
	if s := diagramStep(bex, "lab-bex.5"); strings.Join(s.After, ",") != "lab-bex.2,lab-bex.3" {
		t.Fatalf("rollout gate after = %v", s.After)
	}
}

// The checks: certain ones for what breaks the diagram, advisory ones for titles.
func TestDiagram_Findings(t *testing.T) {
	bex := fixtureDiagram(t, "lab:lab-bex")
	var got []string
	for _, f := range bex.Findings {
		tag := f.Check + ":" + f.Bead
		if f.Advisory {
			tag += ":advisory"
		}
		if f.Why == "" || f.Fix == "" {
			t.Errorf("%s has no plain-English why or fix: %+v", tag, f)
		}
		got = append(got, tag)
	}
	slices.Sort(got)
	want := []string{
		"cycle:lab-bex.7",
		"gate-criteria:lab-bex.3.3",
		"gate-title:lab-bex.5:advisory",
		"lane-label:lab-bex.8",
		"unheld:lab-bex.4",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("findings = %v\nwant %v", got, want)
	}
	if fs := bex.Sub("lab-bex.3").Findings; len(fs) != 1 || fs[0].Bead != "lab-bex.3.3" {
		t.Fatalf("the track lists only its own findings: %+v", fs)
	}
	fs := fixtureDiagram(t, "lab:lab-ops").Findings
	if len(fs) != 2 || fs[0].Check != "gate-criteria" || fs[1].Check != "order" || !fs[1].Advisory {
		t.Fatalf("ops findings = %+v, want the missing criteria and the gate beside the stage it sits in", fs)
	}
}

func TestGateTitleProblem(t *testing.T) {
	user, rev := []string{"user"}, []string{"reviewer-a"}
	for _, c := range []struct {
		title     string
		approvers []string
		bad       bool
	}{
		{"Greenlight merging the export to main", user, false},
		{"Workflow approved", user, false},
		{"Spec approved", user, false},
		{"PR approved", rev, false},
		{"Fix deployed to production", rev, false},
		{"PR ready: CI green, no open threads", []string{"automatic"}, false},
		{"Greenlight merging", rev, true},
		{"Merge the PR", user, true},
		{"Approve the rollout?", user, true},
		{"Rollout plan", user, true},
	} {
		why := gateTitleProblem(SnapStep{Title: c.title, Gate: true, Approvers: c.approvers})
		if (why != "") != c.bad {
			t.Errorf("%q (%v): %q", c.title, c.approvers, why)
		}
	}
}

func fixtureBoard(t *testing.T, path string) Display {
	t.Helper()
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return Board(fx.Snapshot, fx.Now)
}

func fixtureDiagram(t *testing.T, key string) *Diagram {
	t.Helper()
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	for _, r := range fixtureBoard(t, "testdata/diagram/tracks.json").Workstreams {
		if r.Key == key {
			if r.Diagram == nil {
				t.Fatalf("%s has no diagram", key)
			}
			return r.Diagram
		}
	}
	t.Fatalf("no row %s", key)
	return nil
}

func diagramStep(d *Diagram, id string) *DiagramStep {
	for i := range d.Steps {
		if d.Steps[i].ID == id {
			return &d.Steps[i]
		}
	}
	return nil
}

// checkDiagrams holds every diagram to the workflow and board it sits in:
// each step reads as its workflow step, a blocker is drawn above what waits
// on it (done steps, the past, come first), current is a step, and every
// open track has its own diagram.
func checkDiagrams(t *testing.T, d Display) {
	t.Helper()
	for _, r := range d.Workstreams {
		if r.Diagram == nil {
			if r.Workflow != nil {
				t.Errorf("%s: a workflow but no diagram", r.Key)
			}
			continue
		}
		check := func(dg Diagram) {
			at := map[string]int{}
			for i, s := range dg.Steps {
				at[s.ID] = i
				if strings.Contains(s.ID, CrossWaitSuffix) {
					continue // a wait on another track, drawn from that step's own line
				}
				// A gate split around its wait: the wait reads as the gate does, the gate as not reached.
				split := i > 0 && dg.Steps[i-1].ID == s.ID+WaitStepSuffix
				w := findWorkflowStep(r.Workflow.Steps, strings.TrimSuffix(s.ID, WaitStepSuffix))
				if w == nil || (!split && (w.Kind != s.Kind || w.Text != s.Text)) {
					t.Errorf("%s %s: diagram %s %q, workflow %+v", r.Key, s.ID, s.Kind, s.Text, w)
				}
				if s.Kind != "done" && s.Actor == "" && !slices.ContainsFunc(dg.Findings, func(f DiagramFinding) bool { return f.Check == "unheld" || f.Check == "gate-approvers" }) {
					t.Errorf("%s %s: no lane and no finding says why", r.Key, s.ID)
				}
			}
			for _, s := range dg.Steps {
				for _, a := range s.After {
					if i, ok := at[a]; !ok {
						t.Errorf("%s %s: after %s, not a step of the diagram", r.Key, s.ID, a)
					} else if i > at[s.ID] && s.Kind != "done" && !slices.ContainsFunc(dg.Findings, func(f DiagramFinding) bool { return f.Check == "cycle" || f.Check == "order" }) {
						t.Errorf("%s %s: drawn above %s, which it waits on", r.Key, s.ID, a)
					}
				}
			}
			if _, ok := at[dg.Current]; dg.Current != "" && !ok && drawnID(dg.Steps, dg.Current) != dg.Current {
				t.Errorf("%s: current %s is not a step", r.Key, dg.Current)
			}
		}
		check(*r.Diagram)
		for _, tr := range r.Tracks {
			if sub := r.Diagram.Sub(tr.BeadID); sub != nil {
				check(*sub)
			} else if w := r.Workflow.Sub(tr.BeadID); w != nil {
				t.Errorf("%s: track %s has a workflow but no diagram", r.Key, tr.BeadID)
			}
		}
	}
}

// A gate the user approves that waits on someone else is two moves on two
// lines: theirs now (the reviewer, CI), then the user's greenlight.
func TestDiagram_GateWaitingOnOthersIsTwoMoves(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	var key, ci *Diagram
	for _, r := range fixtureBoard(t, "testdata/diagram/gate-waits.json").Workstreams {
		key, ci = r.Diagram.Sub("lab-qm.1"), r.Diagram.Sub("lab-qm.2")
	}
	for _, c := range []struct {
		d               *Diagram
		gate, wait, who string
	}{
		{key, "lab-qm.1.7", "reviewers|reviewer-a reviews PR #4116", "you"},
		{ci, "lab-qm.2.2", "automatic|CI passes on PR #77", "you"},
	} {
		w, g := diagramStep(c.d, c.gate+WaitStepSuffix), diagramStep(c.d, c.gate)
		if w == nil || g == nil {
			t.Fatalf("%s: want a wait step and the gate, got %+v", c.gate, c.d.Steps)
		}
		if w.Actor+"|"+w.Title != c.wait || w.Gate || w.Kind != "waiting" {
			t.Errorf("%s wait = %+v, want %s, waiting", c.gate, w, c.wait)
		}
		if g.Actor != c.who || !g.Gate || g.Text != "Not reached" || strings.Join(g.After, ",") != w.ID {
			t.Errorf("%s gate = %+v, want on the user's line after its wait", c.gate, g)
		}
		if c.d.Current != w.ID {
			t.Errorf("%s: current %q, want the wait", c.gate, c.d.Current)
		}
		if diagramStepIndex(c.d, w.ID)+1 != diagramStepIndex(c.d, g.ID) {
			t.Errorf("%s: the gate does not follow its wait", c.gate)
		}
	}
}

func diagramStepIndex(d *Diagram, id string) int {
	for i := range d.Steps {
		if d.Steps[i].ID == id {
			return i
		}
	}
	return -1
}

// nestedBoard is the nested fixture with the track flag dropped from the
// first step (so its steps are drawn under it, not in a track's own diagram)
// and mut applied to the step lab-hub.1.2.5.
func nestedBoard(t *testing.T, mut func(top, op *SnapStep)) *Diagram {
	t.Helper()
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile("testdata/diagram/nested.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	top := &fx.Snapshot.Workstreams[0].Steps[1]
	top.Track = false
	mut(top, &top.Children[0].Children[4])
	return Board(fx.Snapshot, fx.Now).Workstreams[0].Diagram
}

// Steps nested three deep are drawn: step, its children, theirs; deeper is cut.
func TestDiagram_ThreeLevelNesting(t *testing.T) {
	d := nestedBoard(t, func(top, op *SnapStep) {
		leaf := SnapStep{BeadID: "lab-hub.1.2.5.1", Title: "Open the PR", State: "in-progress", Status: "in_progress", Owner: "operator-lab-hub-1-2-5-1"}
		deep := SnapStep{BeadID: "lab-hub.1.2.5.1.1", Title: "Too deep", State: "in-progress", Status: "in_progress", Owner: "operator-x"}
		leaf.Children = []SnapStep{deep}
		op.Children = []SnapStep{leaf}
	})
	l1 := diagramStep(d, "lab-hub.1")
	if l1 == nil || len(l1.Children) == 0 {
		t.Fatalf("level 1 has no children: %+v", l1)
	}
	var l2 *DiagramStep
	for i := range l1.Children {
		if l1.Children[i].ID == "lab-hub.1.2" {
			l2 = &l1.Children[i]
		}
	}
	if l2 == nil {
		t.Fatalf("lab-hub.1.2 not nested under lab-hub.1: %+v", l1.Children)
	}
	var l3 *DiagramStep
	for i := range l2.Children {
		if l2.Children[i].ID == "lab-hub.1.2.5" {
			l3 = &l2.Children[i]
		}
	}
	if l3 == nil || len(l3.Children) != 1 || l3.Children[0].ID != "lab-hub.1.2.5.1" {
		t.Fatalf("lab-hub.1.2.5 should hold its child: %+v", l3)
	}
	if len(l3.Children[0].Children) != 0 {
		t.Errorf("depth is capped, got %+v", l3.Children[0].Children)
	}
	if got := strings.Join(DiagramLines(d), "\n"); !strings.Contains(got, "              [Operators] Open the PR") {
		t.Errorf("lines do not indent three levels:\n%s", got)
	}
}

// A cycle among a step's children does not hang or drop them, and the diagram says so.
func TestDiagram_CycleWithChildren(t *testing.T) {
	d := nestedBoard(t, func(top, op *SnapStep) {
		last := &top.Children[0].Children[len(top.Children[0].Children)-1]
		op.BlockedBy = append(op.BlockedBy, last.BeadID) // 1.2.5 waits on 1.2.9, which waits (through 1.2.8..1.2.6) on 1.2.5
	})
	l1 := diagramStep(d, "lab-hub.1")
	if l1 == nil || len(l1.Children) == 0 {
		t.Fatalf("children lost under a cycle: %+v", l1)
	}
	var l2 *DiagramStep
	for i := range l1.Children {
		if l1.Children[i].ID == "lab-hub.1.2" {
			l2 = &l1.Children[i]
		}
	}
	if l2 == nil || len(l2.Children) < 5 {
		t.Fatalf("children of lab-hub.1.2 lost under a cycle: %+v", l2)
	}
}

// A step that waits only on a step in another track draws that wait as its
// own card first, so it never reads "Not reached" with nothing before it open.
func TestDiagram_WaitOnAnotherTrackIsDrawn(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile("testdata/diagram/gate-waits.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	b := &fx.Snapshot.Workstreams[0].Steps[1]
	b.Children = append(b.Children, SnapStep{BeadID: "lab-qm.2.4", Title: "Released together", State: "not-started", Status: "open", BlockedBy: []string{"lab-qm.1.7"}, Viz: 3})
	d := Board(fx.Snapshot, fx.Now).Workstreams[0].Diagram.Sub("lab-qm.2")
	w := diagramStep(d, "lab-qm.2.4"+CrossWaitSuffix+":lab-qm.1.7")
	if w == nil {
		t.Fatalf("no wait card: %+v", d.Steps)
	}
	if !strings.HasPrefix(w.Title, "Waiting on workstream 1.1") || w.Kind == "idle" || w.Kind == "done" || w.Actor == "" {
		t.Errorf("wait card = %+v", w)
	}
	if s := diagramStep(d, "lab-qm.2.4"); s == nil || strings.Join(s.After, ",") != w.ID {
		t.Errorf("step after = %+v, want the wait", s)
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Ship importer (retry on Claude)":     "Ship importer",
		"Review PR (on Codex)":                "Review PR",
		"Build (resume on Claude) (on Codex)": "Build",
		"Importer (CI retry)":                 "Importer (CI retry)",
		"(retry on Claude)":                   "(retry on Claude)",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// blankTitles empties every step title, as a bead made without one.
func blankTitles(steps []SnapStep) {
	for i := range steps {
		steps[i].Title = ""
		blankTitles(steps[i].Children)
	}
}

func diagramTexts(steps []DiagramStep, out *[]string) {
	for _, s := range steps {
		*out = append(*out, s.Title, s.Text, s.Who, s.Progress, s.Criteria)
		diagramTexts(s.Children, out)
	}
}

// No bead id is ever visible text: an untitled step reads "Untitled step".
func TestNoBeadIDInVisibleText(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile("testdata/diagram/nested-tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for i := range fx.Snapshot.Workstreams {
		blankTitles(fx.Snapshot.Workstreams[i].Steps)
	}
	for i := range fx.Snapshot.NeedsYou {
		fx.Snapshot.NeedsYou[i].Title = ""
	}
	// a title that IS its own bead id reads as untitled too
	fx.Snapshot.Workstreams[0].Steps[1].Title = fx.Snapshot.Workstreams[0].Steps[1].BeadID
	checkin := CheckinReport(fx.Snapshot, fx.Now, AuditOptions{})
	id := regexp.MustCompile(`\blab-(hub|ops)[\w.-]*|\b[a-z]+-[a-z0-9]+\.\d+(\.\d+)*\b`)
	d := Board(fx.Snapshot, fx.Now)
	var texts []string
	for _, r := range d.Workstreams {
		texts = append(texts, r.Text)
		for _, tr := range r.Tracks {
			texts = append(texts, tr.Title, tr.Text)
		}
		if r.Diagram != nil {
			diagramTexts(r.Diagram.Steps, &texts)
			for _, tr := range r.Diagram.Tracks {
				diagramTexts(tr.Steps, &texts)
			}
		}
		if r.Workflow != nil {
			var walk func([]WorkflowStep)
			walk = func(ws []WorkflowStep) {
				for _, s := range ws {
					texts = append(texts, s.Title, s.Text)
					walk(s.Children)
				}
			}
			walk(r.Workflow.Steps)
		}
	}
	for _, a := range d.Asks {
		texts = append(texts, a.Text)
	}
	untitled := false
	for _, l := range strings.Split(checkin, "\n") {
		if strings.HasPrefix(l, "- ") {
			// check-in ask lines are "- <bead id> | <workstream> | <title>": the title is visible text
			if p := strings.Split(l, " | "); len(p) == 3 {
				texts = append(texts, p[2])
			}
		}
	}
	for _, s := range texts {
		if m := id.FindString(s); m != "" {
			t.Errorf("bead id %q in visible text %q", m, s)
		}
		untitled = untitled || strings.Contains(s, UntitledStep)
	}
	if !untitled {
		t.Error("no step read as Untitled step")
	}
}

// A step with ONE Operator task is one box (its own state, no task title, nothing nested);
// a step with several real children nests them.
func TestSingleOperatorTaskIsOneBox(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	d := fixtureBoard(t, "testdata/diagram/nested-tasks.json")
	var all []DiagramStep
	var flat func([]DiagramStep)
	flat = func(ss []DiagramStep) {
		for _, s := range ss {
			all = append(all, s)
			flat(s.Children)
		}
	}
	for _, r := range d.Workstreams {
		for _, tr := range r.Diagram.Tracks {
			flat(tr.Steps)
		}
	}
	var one, parent *DiagramStep
	for i := range all {
		switch all[i].Title {
		case "Importer PR open":
			one = &all[i]
		case "Importer ships":
			parent = &all[i]
		}
		if strings.Contains(all[i].Title, "Ship importer") {
			t.Errorf("task title shown: %q", all[i].Title)
		}
	}
	if one == nil || parent == nil {
		t.Fatalf("steps missing: %v %v", one, parent)
	}
	if len(one.Children) != 0 || !strings.Contains(one.Text, "Operator working") {
		t.Errorf("single task step = %+v, want a childless box reading Operator working", *one)
	}
	if len(parent.Children) < 2 {
		t.Errorf("parent has %d nested steps, want several", len(parent.Children))
	}
}

// taskBoard is a workstream with one step (mut shapes it) drawn as a box.
func taskBoard(t *testing.T, step SnapStep) (BoardRow, *DiagramStep) {
	t.Helper()
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	step.BeadID, step.Title = "lab-x.1", "Importer ships"
	s := Snapshot{
		Agents: map[string]SnapAgent{"manager-lab-x": {State: "idle"}},
		Workstreams: []SnapWorkstream{{N: 1, Project: "lab", BeadID: "lab-x", Title: "X", State: "in-progress", Status: "in_progress",
			ManagerRole: "manager-lab-x", Steps: []SnapStep{step}}},
	}
	for _, r := range Board(s, 1793000000).Workstreams {
		if r.Diagram != nil {
			if d := diagramStep(r.Diagram, "lab-x.1"); d != nil {
				return r, d
			}
		}
	}
	t.Fatal("step not drawn")
	return BoardRow{}, nil
}

func opTask(status, ownerState string) SnapStep {
	return SnapStep{BeadID: "lab-x.1.1", Title: "Ship importer", Task: true, Status: status, State: "in-progress", Owner: "operator-lab-x-1-1", OwnerState: ownerState}
}

func TestLiveTaskUnderStepReadsAsOperator(t *testing.T) {
	cases := map[string]struct {
		step      SnapStep
		operators bool
	}{
		"in-progress manager step": {SnapStep{State: "in-progress", Status: "in_progress", Owner: "manager-lab-x", Children: []SnapStep{opTask("in_progress", "")}}, true},
		"not started step":         {SnapStep{State: "open", Status: "open", Children: []SnapStep{opTask("in_progress", "")}}, true},
		"two tasks, one done":      {SnapStep{State: "in-progress", Status: "in_progress", Owner: "manager-lab-x", Children: []SnapStep{{BeadID: "lab-x.1.2", Task: true, Status: "closed", State: "done"}, opTask("in_progress", "")}}, true},
		"dead task":                {SnapStep{State: "in-progress", Status: "in_progress", Owner: "manager-lab-x", Children: []SnapStep{opTask("in_progress", "died")}}, false},
		"closed task":              {SnapStep{State: "in-progress", Status: "in_progress", Owner: "manager-lab-x", Children: []SnapStep{opTask("closed", "")}}, false},
	}
	for name, c := range cases {
		row, d := taskBoard(t, c.step)
		if c.operators {
			if !strings.Contains(d.Text, "Operator working") || d.Lane != "agents" || d.Actor != ActorOperators || d.Kind != "working" {
				t.Errorf("%s: box = %+v, want Operator working in the agents lane", name, *d)
			}
			if !strings.HasPrefix(row.Text, "Working on importer ships") {
				t.Errorf("%s: row = %q", name, row.Text)
			}
			if row.Workflow == nil || row.Workflow.Current != "lab-x.1" {
				t.Errorf("%s: current = %+v", name, row.Workflow)
			}
		} else if strings.Contains(d.Text, "Operator working") {
			t.Errorf("%s: box = %q, a dead/closed task is not working", name, d.Text)
		}
		if len(d.Children) != 0 {
			t.Errorf("%s: task drawn as a nested step", name)
		}
	}
}

func TestLiveTaskUnderGateStaysAGate(t *testing.T) {
	_, d := taskBoard(t, SnapStep{State: "open", Status: "open", Gate: true, Approvers: []string{"user"}, Criteria: "ok", Children: []SnapStep{opTask("in_progress", "")}})
	if !d.Gate || len(d.Children) != 0 || strings.Contains(d.Text, "Operator working") {
		t.Errorf("gate box = %+v", *d)
	}
}

func TestCleanTitleKeepsOrdinaryParentheses(t *testing.T) {
	for in, want := range map[string]string{
		"Tune prompts (work on Claude Code docs)": "Tune prompts (work on Claude Code docs)",
		"Build (attempt 3 on Codex)":              "Build",
		"Build (RETRY ON OpenCode)":               "Build",
		"Build (resume on claude)":                "Build",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmptyOrIDWorkstreamTitleIsUntitled(t *testing.T) {
	for _, title := range []string{"", "lab-x"} {
		s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "lab", BeadID: "lab-x", Title: title, State: "in-progress", ManagerRole: "manager-lab-x"}}}
		r := Board(s, 1793000000).Workstreams[0]
		if r.Title != UntitledStep || (r.Diagram != nil && r.Diagram.Title != UntitledStep) {
			t.Errorf("title %q: row %q diagram %v", title, r.Title, r.Diagram)
		}
	}
}

// needsYouLaneFixture: a step with an open card, as cmdrd would load it, and
// the board that reads it.
func needsYouLaneFixture(steps ...SnapStep) (SnapWorkstream, board) {
	ws := SnapWorkstream{BeadID: "w", Project: "p", Title: "W", ManagerRole: "manager-w", Steps: steps}
	b := board{needsIDs: map[string]bool{"w.1": true}, steps: map[string]SnapStep{}}
	for _, s := range flattenSteps(steps) {
		b.steps[s.BeadID] = s
	}
	return ws, b
}

func TestDiagram_NeedsYouLaneCatchesACardDrawnElsewhere(t *testing.T) {
	ask := SnapStep{BeadID: "w.1", Title: "Approve the sign-in", Status: "open", State: "needs-user"}
	ws, b := needsYouLaneFixture(ask)
	// The old behaviour: the card's step drawn as its Operator's work.
	agents := BoardRow{Kind: "working", Text: "Operator working", Diagram: &Diagram{ID: "w", Steps: []DiagramStep{
		{ID: "w.1", Lane: "agents", Actor: ActorOperators, Kind: "working", Text: "Operator working"}}}}
	got := b.needsYouLane(ws, agents)
	if len(got) != 1 || got[0].Check != CheckNeedsYouLane || got[0].Bead != "w.1" || got[0].Fix == "" ||
		!strings.Contains(got[0].Why, "lane agents") || !strings.Contains(got[0].Why, "its row reads") {
		t.Fatalf("findings %+v", got)
	}

	// Each half is enough on its own.
	rowOnly := BoardRow{Kind: "working", Text: "Operator working", Diagram: &Diagram{ID: "w", Steps: []DiagramStep{
		{ID: "w.1", Lane: "you", Actor: ActorYou, Kind: "needs", Text: "Needs you"}}}}
	if got := b.needsYouLane(ws, rowOnly); len(got) != 1 || strings.Contains(got[0].Why, "lane") {
		t.Fatalf("row only: %+v", got)
	}
	drawnOnly := BoardRow{Kind: "needs", Text: "Needs you", Diagram: agents.Diagram}
	if got := b.needsYouLane(ws, drawnOnly); len(got) != 1 || strings.Contains(got[0].Why, "row reads") {
		t.Fatalf("diagram only: %+v", got)
	}

	clean := BoardRow{Kind: "needs", Text: "Needs you", Diagram: rowOnly.Diagram}
	if got := b.needsYouLane(ws, clean); len(got) != 0 {
		t.Fatalf("clean: %+v", got)
	}
}

func TestDiagram_NeedsYouLaneInATrackAndNested(t *testing.T) {
	ask := SnapStep{BeadID: "w.1", Title: "Approve the sign-in", Status: "open", State: "needs-user"}
	track := SnapStep{BeadID: "w.t", Title: "T", Status: "open", Track: true, Children: []SnapStep{ask}}
	ws, b := needsYouLaneFixture(track)
	r := BoardRow{Kind: "needs", Text: "Needs you", Tracks: []BoardTrack{{BeadID: "w.t", Kind: "working", Text: "Operator working"}},
		Diagram: &Diagram{ID: "w", Tracks: []Diagram{{ID: "w.t", Steps: []DiagramStep{
			{ID: "w.0", Kind: "working", Children: []DiagramStep{{ID: "w.1", Lane: "agents", Kind: "working", Text: "Operator working"}}}}}}}}
	b.addNeedsYouLane(ws, &r)
	if len(r.Diagram.Findings) != 1 || len(r.Diagram.Tracks[0].Findings) != 1 {
		t.Fatalf("workstream %+v, track %+v", r.Diagram.Findings, r.Diagram.Tracks[0].Findings)
	}
	if why := r.Diagram.Findings[0].Why; !strings.Contains(why, "lane agents") || !strings.Contains(why, "workstream's row") {
		t.Fatalf("why %q", why)
	}
}

// The deliberate exceptions are not findings, whatever is drawn.
func TestDiagram_NeedsYouLaneLeavesDeliberateExceptions(t *testing.T) {
	working := BoardRow{Kind: "working", Text: "Operator working on the build", Diagram: &Diagram{ID: "w", Steps: []DiagramStep{
		{ID: "w.1", Lane: "agents", Actor: ActorOperators, Kind: "working", Text: "Operator working"}}}}

	// heldByLive: a live Operator is on the work the decision waits on.
	prereq := SnapStep{BeadID: "w.0", Title: "Build", Status: "open", State: "in-progress", Owner: "operator-a", OwnerState: "working"}
	held := SnapStep{BeadID: "w.1", Title: "Approve", Status: "open", State: "needs-user", BlockedBy: []string{"w.0"}}
	ws, b := needsYouLaneFixture(prereq, held)
	if got := b.needsYouLane(ws, working); len(got) != 0 {
		t.Fatalf("held by a live Operator: %+v", got)
	}
	// Prerequisite work with nobody live is no reason to hide the card.
	prereq.OwnerState, prereq.Owner = "", ""
	ws, b = needsYouLaneFixture(prereq, held)
	if got := b.needsYouLane(ws, working); len(got) != 1 {
		t.Fatalf("prerequisite with no live Operator: %+v", got)
	}

	// A gate behind an open blocker reads Not reached.
	blocker := SnapStep{BeadID: "w.0", Title: "Build", Status: "open", State: "not-started"}
	gate := gateStep("w.1", "Spec approved", []string{"user"}, "w.0")
	ws, b = needsYouLaneFixture(blocker, gate)
	if got := b.needsYouLane(ws, working); len(got) != 0 {
		t.Fatalf("gate not reached: %+v", got)
	}

	// No card, or a parked or closed step, is not the user's move.
	plain := SnapStep{BeadID: "w.2", Title: "Other", Status: "open", State: "needs-user"}
	ws, b = needsYouLaneFixture(plain)
	if got := b.needsYouLane(ws, working); len(got) != 0 {
		t.Fatalf("no card: %+v", got)
	}
	for _, status := range []string{"closed", statusDeferred} {
		ws, b = needsYouLaneFixture(SnapStep{BeadID: "w.1", Title: "Approve", Status: status, State: "needs-user"})
		if got := b.needsYouLane(ws, working); len(got) != 0 {
			t.Fatalf("%s: %+v", status, got)
		}
	}
}

// Through Board: today's derivation draws an open card with a live task under it clean.
func TestDiagram_NeedsYouLaneBoardIsClean(t *testing.T) {
	task := SnapStep{BeadID: "w.1.1", Title: "Task", Status: "open", State: "in-progress", Task: true, Owner: "operator-a", OwnerState: "working"}
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{{BeadID: "w.1", Title: "Approve the sign-in", Status: "open", State: "needs-user", Owner: "operator-a", OwnerState: "working", Children: []SnapStep{task}}}}
	s := snapOf(ws, idle, NeedsYou{BeadID: "w.1", Project: "p", Workstream: "WS w", Title: "Approve the sign-in"})
	r := Board(s, nowSec).Workstreams[0]
	if r.Diagram == nil {
		t.Fatalf("no diagram: row %q %q", r.Kind, r.Text)
	}
	for _, f := range r.Diagram.Findings {
		if f.Check == CheckNeedsYouLane {
			t.Fatalf("finding %+v", f)
		}
	}
	if r.Kind != "needs" {
		t.Fatalf("row %q %q", r.Kind, r.Text)
	}
	if st := r.Diagram.drawn("w.1"); st == nil || st.Lane != "you" {
		t.Fatalf("drawn %+v", st)
	}
}

// An open card the user answers puts the step in the You lane, whatever its approver label says.
func TestDiagram_OpenCardIsTheYouLaneWhoeverApproves(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC
	var d *Diagram
	for _, r := range fixtureBoard(t, "testdata/diagram/card-you-lane.json").Workstreams {
		d = r.Diagram
	}
	g := diagramStep(d, "lab-cy.2")
	if g == nil || g.Actor != ActorYou || g.Lane != "you" {
		t.Fatalf("a gate approved by a named person, with the user's card open, is %+v", g)
	}
}

func TestDiagram_PlainUserOwnedDecisionLane(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Project: "p", Steps: []SnapStep{{BeadID: "w.1", Title: "Choose a region", Status: "open", State: "needs-user", Owner: "user"}}}
	r := Board(snapOf(ws, idle, NeedsYou{BeadID: "w.1", Project: "p", Title: "Choose a region"}), nowSec).Workstreams[0]
	st := r.Diagram.drawn("w.1")
	if st == nil || st.Lane != "you" || st.Kind != "needs" {
		t.Fatalf("decision: %+v", st)
	}
	for _, f := range r.Diagram.Findings {
		if f.Check == CheckNeedsYouLane {
			t.Fatalf("finding: %+v", f)
		}
	}
}
