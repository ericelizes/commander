package livedata

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateTree = flag.Bool("update-tree", false, "rewrite the app's tree fixture (app/test/fixtures/tree-board.json)")

// treeSnapshot is a small board with every kind of row and card the tree view draws.
func treeSnapshot() Snapshot {
	at := time.Unix(1_800_000_000, 0)
	now := at.Unix()
	ds := func(id, title, actor, kind, text string, after ...string) DiagramStep {
		lane := map[string]string{"you": "you", "operators": "agents", "manager": "agents", "reviewers": "elsewhere", "automatic": "elsewhere"}[actor]
		return DiagramStep{ID: id, Title: title, Actor: actor, Lane: lane, Kind: kind, Text: text, After: after}
	}
	working := ds("w1.1.2", "Build the cutover and open its PR", "operators", "working", "Working on “Build the cutover”")
	working.Runtime, working.Since = "claude", now-25*60
	asleep := ds("w1.2.1", "Live check: the next real notice goes through Notify", "automatic", "waiting", "Waiting on prod deploy with abc123")
	asleep.Since = now - 3*3600
	needs := ds("w1.2.2", "Load the new credentials", "you", "needs", "Needs you: Load the three credential values?")
	needs.Since = now - 50*60
	steps1 := []SnapStep{
		{BeadID: "w1.1", Title: "Phase 1: first notices (FOR-101)", Track: true, Status: "open", Criteria: "Failed restarts go through Notify\nYou see one in your inbox", Children: []SnapStep{
			{BeadID: "w1.1.1", Title: "Inventory every sender", Status: "closed"},
			{BeadID: "w1.1.2", Title: "Build the cutover and open its PR", Status: "in_progress", Children: []SnapStep{{BeadID: "w1.1.2.1", Title: "operator task", Task: true, Status: "open"}}},
			{BeadID: "w1.1.3", Title: "Review: the cutover PR is green", Status: "open", BlockedBy: []string{"w1.1.2"}},
			{BeadID: "w1.1.4", Title: "Merge the cutover PR", Status: "open", Gate: true, BlockedBy: []string{"w1.1.3"}},
			{BeadID: "w1.1.5", Title: "Confirm done", Status: "open", Gate: true, ClosingGate: true, BlockedBy: []string{"w1.1.4"}},
			{BeadID: "w1.1.6", Title: "Old plan", Status: "closed", Superseded: true},
		}},
		{BeadID: "w1.2", Title: "Phase 2: alerts", Track: true, Status: "open", Children: []SnapStep{
			{BeadID: "w1.2.1", Title: "Live check: the next real notice goes through Notify", Status: "open"},
			{BeadID: "w1.2.2", Title: "Load the new credentials", Status: "open", Gate: true},
		}},
		{BeadID: "w1.3", Title: "Phase 3: everything else", Track: true, Status: "open", BlockedBy: []string{"w1.1"}, Children: []SnapStep{
			{BeadID: "w1.3.1", Title: "Move the remaining senders", Status: "open"},
			{BeadID: "w1.3.2", Title: "Review: the remaining senders moved", Status: "open", BlockedBy: []string{"w1.3.1"}},
		}},
		{BeadID: "w1.4", Title: "Paused work", Track: true, Status: "open", Children: []SnapStep{
			{BeadID: "w1.4.1", Title: "Measure the slow page", Status: "open"},
		}},
	}
	steps2 := []SnapStep{
		{BeadID: "w2.1", Title: "Fix the deploy failure", Status: "closed"},
		{BeadID: "w2.2", Title: "The platform team reviews and merges PR 77", Status: "open", BlockedBy: []string{"w2.1"}},
		{BeadID: "w2.3", Title: "Ship it and watch prod", Status: "open", BlockedBy: []string{"w2.2"}},
	}
	steps3 := []SnapStep{{BeadID: "w3.1", Title: "Write the note", Status: "closed"}}
	person := ds("w2.2", "The platform team reviews and merges PR 77", "reviewers", "waiting", "Waiting for review: waiting on Platform team (#77)")
	person.Who = "Platform team"
	paused := ds("w1.4", "Paused work", "operators", "quiet", "Paused")
	paused.Track = true
	d1 := &Diagram{ID: "w1", Title: "Notify", Steps: []DiagramStep{
		{ID: "w1.1", Title: "Phase 1", Track: true, Kind: "working"}, {ID: "w1.2", Title: "Phase 2", Track: true, Kind: "needs"},
		{ID: "w1.3", Title: "Phase 3", Track: true, Kind: "idle"}, paused},
		Tracks: []Diagram{
			{ID: "w1.1", Steps: []DiagramStep{ds("w1.1.1", "Inventory every sender", "operators", "done", "Done"), working,
				ds("w1.1.3", "Review: the cutover PR is green", "operators", "idle", "Not started", "w1.1.2"),
				ds("w1.1.4", "Merge the cutover PR", "you", "idle", "Not reached", "w1.1.3")}},
			{ID: "w1.2", Steps: []DiagramStep{asleep, needs}},
			{ID: "w1.3", Steps: []DiagramStep{ds("w1.3.1", "Move the remaining senders", "operators", "idle", "Not started"),
				ds("w1.3.2", "Review: the remaining senders moved", "operators", "idle", "Not started", "w1.3.1")}},
			{ID: "w1.4", Steps: []DiagramStep{ds("w1.4.1", "Measure the slow page", "operators", "idle", "Parked")}},
		}}
	d2 := &Diagram{ID: "w2", Steps: []DiagramStep{ds("w2.1", "Fix the deploy failure", "operators", "done", "Done"), person,
		ds("w2.3", "Ship it and watch prod", "operators", "idle", "Not started", "w2.2")}}
	d3 := &Diagram{ID: "w3", Steps: []DiagramStep{ds("w3.1", "Write the note", "operators", "done", "Done")}}
	return Snapshot{Version: 42, GeneratedAt: at,
		Workstreams: []SnapWorkstream{
			{N: 1, Project: "app", BeadID: "w1", Title: "Notify: every notification goes through Notify", Tickets: []string{"FOR-100"}, Status: "in_progress", Criteria: "Every notification is sent through Notify", Steps: steps1},
			{N: 2, Project: "app", BeadID: "w2", Title: "Fix today's deploy", Status: "open", Steps: steps2},
			{N: 3, Project: "app", BeadID: "w3", Title: "Write the release note", Status: "open", Steps: steps3},
		},
		Display: &Display{Workstreams: []BoardRow{{Key: "app:w1", N: 1, Diagram: d1}, {Key: "app:w2", N: 2, Diagram: d2}, {Key: "app:w3", N: 3, Diagram: d3}},
			Summaries: map[string]string{"w1": "Phase 1 is in its build and phase 2 waits on your credentials; phase 3 follows phase 1."}},
	}
}

func TestBuildTree(t *testing.T) {
	snap := treeSnapshot()
	tr := BuildTree(snap, snap.GeneratedAt.Unix())
	rows := map[string]TreeRow{}
	var labels []string
	for _, r := range tr.Rows {
		rows[r.Label] = r
		labels = append(labels, r.Label)
	}
	if got := strings.Join(labels, " "); got != "1 1.1 1.2 1.3 1.4 2 3" {
		t.Fatalf("rows: %s", got)
	}
	steps := map[string]TreeStep{}
	for _, s := range tr.Steps {
		steps[s.ID] = s
	}
	for _, gone := range []string{"w1.1.5", "w1.1.6", "w1.1.2.1"} {
		if _, ok := steps[gone]; ok {
			t.Errorf("%s is drawn: Confirm done, superseded and task beads are not steps", gone)
		}
	}
	if tr.Total != 3 || tr.Done != 1 || tr.NeedsYou != 1 || tr.Version != 42 {
		t.Errorf("header: %d of %d done, %d need you, v%d", tr.Done, tr.Total, tr.NeedsYou, tr.Version)
	}
	r1 := rows["1"]
	if r1.Title != "Notify: every notification goes through Notify" || r1.Line != (TreeLine{"sum", snap.Display.Summaries["w1"]}) || r1.Of != "0 of 4 workstreams" || len(r1.Criteria) != 1 || r1.NeedsYou != 1 {
		t.Errorf("row 1: %+v", r1)
	}
	if r := rows["1.1"]; r.Title != "Phase 1: first notices" || strings.Join(r.Tickets, ",") != "FOR-101" || len(r.Criteria) != 2 || r.Of != "1 of 4 steps" || r.Progress != 25 {
		t.Errorf("row 1.1: %+v", r)
	}
	if r := rows["1.4"]; r.Note != "Paused" || r.Now != "Paused" || r.State != "Paused" {
		t.Errorf("paused row: %+v", r)
	}
	if r := rows["3"]; !r.Done || r.Now != "✓ Completed" || r.State != "done" {
		t.Errorf("done row: %+v", r)
	}
	check := func(id, state, own, wait string, col int) {
		t.Helper()
		s := steps[id]
		if s.State != state || s.Own != own || s.Wait != wait || s.Col != col {
			t.Errorf("%s: state %q own %q wait %q col %d; want %q %q %q %d", id, s.State, s.Own, s.Wait, s.Col, state, own, wait, col)
		}
	}
	check("w1.1.1", "done", "✓ Implementer agent", "", -1)
	check("w1.1.2", "active", "Implementer agent · Claude", "", 0)
	check("w1.1.3", "notstarted", "Review agent", "", 1)
	check("w1.1.4", "notstarted", "You", "", 2)
	check("w1.2.1", "active", "Implementer agent · asleep", "Asleep until prod deploy with abc123", 0)
	check("w1.2.2", "active", "You", "Load the three credential values?", 0)
	check("w1.3.1", "notstarted", "Implementer agent", "After 1.1", 3)
	check("w1.4.1", "notstarted", "Implementer agent", "Parked", 1)
	check("w2.2", "active", "Platform team", "Waiting for review: waiting on Platform team (#77)", 0)
	check("w2.3", "notstarted", "Implementer agent", "", 1)
	if s := steps["w1.2.2"]; s.Ask != "Load the three credential values?" || s.Since == 0 {
		t.Errorf("your card: %+v", s)
	}
	if r := rows["1.2"]; r.Status[0] != (TreeLine{"you", "Needs you"}) || r.State != "Needs you" {
		t.Errorf("row 1.2: %+v", r)
	}
	if r := rows["1.3"]; r.Status[0] != (TreeLine{"none", "Blocked by 1.1"}) {
		t.Errorf("row 1.3: %+v", r.Status)
	}
	if r := rows["2"]; r.Line != (TreeLine{"person", "Waiting on Platform team: the platform team reviews and merges PR 77"}) {
		t.Errorf("row 2 line: %+v", r.Line)
	}
	edges := map[[2]string]bool{}
	for _, e := range tr.Edges {
		edges[e] = true
	}
	// 1.3 waits on 1.1: its first step waits on 1.1's last step (Confirm done is not a step).
	for _, e := range [][2]string{{"w1.1.2", "w1.1.3"}, {"w1.1.3", "w1.1.4"}, {"w1.1.4", "w1.3.1"}, {"w2.2", "w2.3"}} {
		if !edges[e] {
			t.Errorf("missing edge %v in %v", e, tr.Edges)
		}
	}
}

// The Commander's read (`cmdr workstream-statuses`, and the block on each of
// Eric's messages) is the tree's text: every row and open card the view draws,
// with the same words.
func TestTreeTextIsTheTree(t *testing.T) {
	snap := treeSnapshot()
	now := snap.GeneratedAt.Unix()
	tr := BuildTree(snap, now)
	text := tr.Text(now)
	if !strings.HasPrefix(text, "Board v42: 1 of 3 workstreams done · 1 need you") {
		t.Fatalf("header: %s", strings.SplitN(text, "\n", 2)[0])
	}
	done := map[string]bool{}
	for _, r := range tr.Rows {
		if !strings.Contains(text, r.Label+" "+r.Title+" · "+r.ID) {
			t.Errorf("row %s missing", r.Label)
		}
		if r.Done {
			done[r.ID] = true
			continue
		}
		for _, l := range r.Status {
			if !strings.Contains(text, l.Text) {
				t.Errorf("row %s status %q missing", r.Label, l.Text)
			}
		}
	}
	for _, s := range tr.Steps {
		line := ColumnName(s.Col) + ": [" + s.Own + "] " + s.Title
		if s.Wait != "" {
			line += " — " + s.Wait
		}
		if has := strings.Contains(text, line); has == (s.State == "done") && !done[s.Row] {
			t.Errorf("step %s: %q present=%v", s.ID, line, has)
		}
	}
	block := TreeBlock(tr, now)
	if !strings.HasPrefix(block, `<workstream-statuses version="42">`) || !strings.Contains(block, text) {
		t.Fatalf("block: %s", block)
	}
}

// The app's tree view test renders this fixture and checks it shows these
// fields; it must always be what BuildTree makes (go test -run TestTreeFixture -update-tree rewrites it).
func TestTreeFixture(t *testing.T) {
	snap := treeSnapshot()
	tr := BuildTree(snap, snap.GeneratedAt.Unix())
	want, _ := json.MarshalIndent(tr, "", "  ")
	want = append(want, '\n')
	path := filepath.Join("..", "..", "app", "test", "fixtures", "tree-board.json")
	if *updateTree {
		if e := os.WriteFile(path, want, 0o644); e != nil {
			t.Fatal(e)
		}
	}
	got, e := os.ReadFile(path)
	if e != nil || string(got) != string(want) {
		t.Fatalf("%s is not what BuildTree makes; rerun with -update-tree (%v)", path, e)
	}
}

// A workstream that holds workstreams gets the describer's summary, from the
// facts the board shows; a line naming the Commander is never shown.
func TestSummariesFromTheBoardsFacts(t *testing.T) {
	snap := treeSnapshot()
	d := snap.Display
	d.Summaries = nil
	var facts string
	reply := "Phase 1 is in its build; phase 2 waits on your credentials."
	applySummaries(func(key, actor, reason, opener, f string) (string, string, bool) {
		if key != "summary:app:w1" || actor != "summary" {
			t.Errorf("asked for %s (%s)", key, actor)
		}
		facts = f
		return reply, "", true
	}, &snap, d)
	if d.Summaries["w1"] != reply || len(d.Summaries) != 1 {
		t.Fatalf("summaries: %v", d.Summaries)
	}
	for _, want := range []string{"Acceptance criterion: Every notification is sent through Notify", "Inside: Phase 2: alerts (Needs you", "asks you: Load the three credential values?"} {
		if !strings.Contains(facts, want) {
			t.Errorf("facts lack %q:\n%s", want, facts)
		}
	}
	d.Summaries = nil
	applySummaries(func(string, string, string, string, string) (string, string, bool) {
		return "Commander to plan phase 3", "", true
	}, &snap, d)
	if len(d.Summaries) != 0 {
		t.Fatalf("a summary naming the Commander was kept: %v", d.Summaries)
	}
}

func TestModelName(t *testing.T) {
	for _, c := range [][3]string{
		{"claude", "anthropic/claude-sonnet-5-5", "Sonnet 5.5"},
		{"claude", "claude-opus-5-5", "Opus 5.5"},
		{"codex", "gpt-5-codex", "GPT-5 Codex"},
		{"codex", "gpt-6.1-sol", "GPT-6.1 Sol"},
		{"codex", "codex:gpt-5-codex", "GPT-5 Codex"},
		{"claude", "", "Claude"},
		{"codex", "", "Codex"},
	} {
		if got := ModelName(c[0], c[1]); got != c[2] {
			t.Errorf("ModelName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// A running step's card names the model its run records; with none it names the runtime.
func TestTreeShowsTheRunsModel(t *testing.T) {
	snap := treeSnapshot()
	snap.Executions = []SnapExecution{{Role: "operator-x", State: "working", BeadID: "w1.1.2", TaskBeadID: "w1.1.2", Runtime: "claude", Model: "anthropic/claude-opus-5-5"}}
	tr := BuildTree(snap, snap.GeneratedAt.Unix())
	for _, s := range tr.Steps {
		if s.ID == "w1.1.2" && (s.Own != "Implementer agent · Opus 5.5" || s.ModelID != "anthropic/claude-opus-5-5") {
			t.Fatalf("card: %+v", s)
		}
	}
	if text := tr.Text(snap.GeneratedAt.Unix()); !strings.Contains(text, "[Implementer agent · Opus 5.5] Build the cutover and open its PR") {
		t.Fatalf("the Commander's read lacks the model:\n%s", text)
	}
}

// The first workstream shown is 1: open ones are numbered first, done ones after.
func TestTreeNumbersOpenWorkstreamsFirst(t *testing.T) {
	snap := treeSnapshot()
	done := snap.Workstreams[2]
	snap.Workstreams = append([]SnapWorkstream{done}, snap.Workstreams[:2]...)
	tr := BuildTree(snap, snap.GeneratedAt.Unix())
	var got []string
	for _, r := range tr.Rows {
		if r.Parent == "" {
			got = append(got, r.Label+" "+r.ID)
		}
	}
	if strings.Join(got, ", ") != "1 w1, 2 w2, 3 w3" {
		t.Fatalf("top-level numbers: %v", got)
	}
	if text := tr.Text(snap.GeneratedAt.Unix()); !strings.Contains(text, "\n1 Notify: every notification goes through Notify · w1") {
		t.Fatalf("the Commander's read numbers differently:\n%s", text)
	}
}

// 3.1 "Earlier fixes and groundwork" (Nov 18) read 100% but not done; 1.3.1 held
// a done workstream and open steps and read 100%. A row is done exactly when all
// its child workstreams and its own steps are, and its progress counts the same.
func TestTreeDoneAndProgressAgree(t *testing.T) {
	closedTrack := func(id string) SnapStep {
		return SnapStep{BeadID: id, Title: "Done track " + id, Track: true, Status: "closed"}
	}
	snap := Snapshot{Workstreams: []SnapWorkstream{
		{N: 1, Project: "app", BeadID: "a", Title: "Parent of done workstreams", Status: "open", Steps: []SnapStep{
			{BeadID: "a.1", Title: "Earlier fixes and groundwork", Track: true, Status: "open", Children: []SnapStep{closedTrack("a.1.1"), closedTrack("a.1.2"), closedTrack("a.1.3"), closedTrack("a.1.4"), closedTrack("a.1.5")}},
		}},
		{N: 2, Project: "app", BeadID: "m", Title: "Mixed", Status: "open", Steps: []SnapStep{
			{BeadID: "m.1", Title: "Teams delivery", Track: true, Status: "open", Children: []SnapStep{
				{BeadID: "m.1.1", Title: "Sealed review runtime", Track: true, Status: "open", Children: []SnapStep{{BeadID: "m.1.1.1", Title: "Done step", Status: "closed"}}},
				{BeadID: "m.1.2", Title: "Load the Teams key", Status: "open", Lane: "you"},
			}},
		}},
	}}
	tr := BuildTree(snap, 0)
	rows := map[string]TreeRow{}
	for _, r := range tr.Rows {
		rows[r.ID] = r
		if (r.Progress == 100) != r.Done {
			t.Errorf("%s %s: %d%% (%s) but done=%v", r.Label, r.ID, r.Progress, r.Of, r.Done)
		}
	}
	if r := rows["a.1"]; !r.Done || r.Of != "5 of 5 workstreams" {
		t.Errorf("parent of done workstreams: %+v", r)
	}
	if r := rows["a"]; !r.Done {
		t.Errorf("top-level parent of a done workstream: %+v", r)
	}
	if r := rows["m.1"]; r.Done || r.Progress != 50 || r.Of != "1 of 1 workstream and 0 of 1 step" {
		t.Errorf("mixed row: done=%v %d%% %q", r.Done, r.Progress, r.Of)
	}
}

// An Automatic step is a card of its own: no person or agent, "Waiting for" its
// event; the step after it is Next.
func TestTreeAutomaticStep(t *testing.T) {
	snap := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "a", Title: "Alerts", Status: "open", Steps: []SnapStep{
		{BeadID: "a.1", Title: "Waiting for prod deploy with 53c4be7", Status: "open", Automatic: true},
		{BeadID: "a.2", Title: "Check a real alert reaches the channel", Status: "open", Lane: "agents", BlockedBy: []string{"a.1"}},
	}}},
		Display: &Display{Workstreams: []BoardRow{{Key: "app:a", N: 1, Diagram: &Diagram{ID: "a", Steps: []DiagramStep{
			{ID: "a.1", Title: "Waiting for prod deploy with 53c4be7", Actor: "automatic", Kind: "waiting", Text: "Waiting on prod deploy with 53c4be7", Since: 1_800_000_000},
			{ID: "a.2", Title: "Check a real alert reaches the channel", Actor: "operators", Kind: "idle", Text: "Not started", After: []string{"a.1"}},
		}}}}}}
	tr := BuildTree(snap, 1_800_000_600)
	steps := map[string]TreeStep{}
	for _, s := range tr.Steps {
		steps[s.ID] = s
	}
	if s := steps["a.1"]; s.Type != "automatic" || s.Own != "Automatic" || s.State != "active" || s.Col != 0 || s.Wait != "" || s.Title != "Waiting for prod deploy with 53c4be7" || s.Run != "" {
		t.Fatalf("automatic card: %+v", s)
	}
	if s := steps["a.2"]; s.State != "notstarted" || s.Col != 1 {
		t.Fatalf("the step after it: %+v", s)
	}
	if r := tr.Rows[0]; r.Status[0] != (TreeLine{"automatic", "Waiting for prod deploy with 53c4be7"}) {
		t.Fatalf("row status: %+v", r.Status)
	}
}

// vibe-apps-zq9h.6.66.38 (Nov 18): its agent asked Eric a question and the card
// read "Implementer agent · Not started". It is his red card, with the question.
func TestTreeAgentQuestionIsYourCard(t *testing.T) {
	snap := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "a", Title: "Complete", Status: "open", Steps: []SnapStep{
		{BeadID: "a.1", Title: "Address the requested changes", Status: "open", Lane: "agents", State: "needs-user", Ask: "Keep the old endpoint for one release?"},
	}}},
		Display: &Display{Workstreams: []BoardRow{{Key: "app:a", N: 1, Diagram: &Diagram{ID: "a", Steps: []DiagramStep{
			{ID: "a.1", Title: "Address the requested changes", Actor: "operators", Kind: "idle", Text: "Not started"},
		}}}}}}
	tr := BuildTree(snap, 0)
	s := tr.Steps[0]
	if s.Type != "you" || s.State != "active" || s.Own != "You" || s.Wait != "Keep the old endpoint for one release?" || s.Col != 0 || tr.NeedsYou != 1 {
		t.Fatalf("card: %+v (needs you %d)", s, tr.NeedsYou)
	}
}

// The same step as the store left it: a needs-user label, its result ready, no
// card or live question; it is still Eric's card, with what its agent said.
func TestTreeNeedsUserLabelIsYourCard(t *testing.T) {
	snap := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "a", Title: "Complete", Status: "open", Steps: []SnapStep{
		{BeadID: "a.1", Title: "Address the second review", Status: "open", Lane: "agents", State: "review", NeedsUser: true, ResultLine: "The question about whether to resume is open and blocking, so I've stopped."},
	}}}}
	s := BuildTree(snap, 0).Steps[0]
	if s.Type != "you" || s.State != "active" || !strings.Contains(s.Wait, "whether to resume") {
		t.Fatalf("card: %+v", s)
	}
}

// A review that sent its work back says so, never "Not started".
func TestTreeSentBackReview(t *testing.T) {
	snap := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "a", Title: "Chart open", Status: "open", Steps: []SnapStep{
		{BeadID: "a.1", Title: "Review: the chart-open-under-2s plan", Status: "open", Lane: "agents", ResultReadyAt: 1, ResultLine: "Final report: **SEND BACK**. The p95 target is measured on the wrong route."},
	}}}}
	s := BuildTree(snap, 0).Steps[0]
	if s.Wait != "Sent back: The p95 target is measured on the wrong route." {
		t.Fatalf("card: %+v", s)
	}
}

// An agent waiting on the Commander's answer to its question is still the
// agent's card, saying so; only a question asked --to user is Eric's.
func TestTreeQuestionToCommanderStaysAgentCard(t *testing.T) {
	snap := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "a", Title: "Jobs on Temporal", Status: "open", Steps: []SnapStep{
		{BeadID: "a.1", Title: "Turn on the safe jobs in dev", Status: "in_progress", Lane: "agents", State: "in-progress", QuestionWaiting: true},
	}}}}
	tr := BuildTree(snap, 0)
	s := tr.Steps[0]
	if s.Type != "agent" || s.Wait != "Waiting on the Commander's answer" || tr.NeedsYou != 0 {
		t.Fatalf("card: %+v (needs you %d)", s, tr.NeedsYou)
	}
}

// y5s on Nov 19: its only open step was on hold (deferred) and read Paused, yet
// the workstream read "Stalled: nothing active". Open work all on hold is Paused.
func TestTreeWorkstreamWithOnlyHeldStepsIsPaused(t *testing.T) {
	snap := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "rcm", BeadID: "y5s", Title: "Claims stuck after payer answered", Status: "open", Steps: []SnapStep{
		{BeadID: "y5s.1", Title: "Find the stuck claims", Status: "closed", Lane: "agents", State: "done"},
		{BeadID: "y5s.2", Title: "Re-submit the stuck claims", Status: "deferred", Lane: "agents"},
	}}}}
	tr := BuildTree(snap, 0)
	if got := tr.Rows[0].Status; len(got) != 1 || got[0].Text != "Paused" {
		t.Fatalf("lines %+v", got)
	}
}
