package livedata

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/example-user/commander/internal/store"
)

// The board fixtures: a snapshot and its clock, the display it derives
// (*.want.json) and its lines (*.lines.txt, which app/test/board-lines.test.ts
// holds the app's copy to). UPDATE_BOARD=1 rewrites both; review the diff.
func TestBoardFixtures(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York") // the zone the fixtures read their clock times in
	if err != nil {
		t.Fatal(err)
	}
	SetBoardZone(zone)
	defer SetBoardZone(nil)
	files, _ := filepath.Glob("testdata/board/*.json")
	for _, f := range files {
		if strings.HasSuffix(f, ".want.json") {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			var fx struct {
				Now      int64    `json:"now"`
				Snapshot Snapshot `json:"snapshot"`
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			d := Board(fx.Snapshot, fx.Now)
			checkWorkflows(t, d)
			checkDiagrams(t, d)
			// Workflows and diagrams have their own goldens (TestWorkflowFixtures, TestDiagramFixtures).
			for i := range d.Workstreams {
				d.Workstreams[i].Workflow, d.Workstreams[i].Diagram = nil, nil
			}
			got, _ := json.MarshalIndent(d, "", " ")
			lines := strings.Join(BoardLines(d, fx.Now, "commander"), "\n") + "\n"
			base := strings.TrimSuffix(f, ".json")
			if os.Getenv("UPDATE_BOARD") != "" {
				_ = os.WriteFile(base+".want.json", append(got, '\n'), 0o644)
				_ = os.WriteFile(base+".lines.txt", []byte(lines), 0o644)
			}
			wantRaw, err := os.ReadFile(base + ".want.json")
			if err != nil {
				t.Fatal(err)
			}
			if wantLines, _ := os.ReadFile(base + ".lines.txt"); string(wantLines) != lines {
				t.Errorf("board lines differ:\n%s", lines)
			}
			var want, have any
			_ = json.Unmarshal(wantRaw, &want)
			_ = json.Unmarshal(got, &have)
			if diff := jsonDiff("", want, have); diff != "" {
				t.Errorf("display differs from the app's:\n%s", diff)
			}
		})
	}
}

// jsonDiff lists the paths where two decoded JSON values differ.
func jsonDiff(path string, want, got any) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			break
		}
		var b strings.Builder
		for k := range w {
			b.WriteString(jsonDiff(path+"."+k, w[k], g[k]))
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				b.WriteString(path + "." + k + ": unexpected\n")
			}
		}
		return b.String()
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			break
		}
		var b strings.Builder
		for i := range w {
			b.WriteString(jsonDiff(path+"["+itoa(int64(i))+"]", w[i], g[i]))
		}
		return b.String()
	default:
		if w, _ := json.Marshal(want); string(w) == mustJSON(got) {
			return ""
		}
	}
	return path + ": want " + mustJSON(want) + ", got " + mustJSON(got) + "\n"
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestBoardOpenCardWithLiveTask(t *testing.T) {
	raw, err := os.ReadFile("testdata/board/branches.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	d := Board(fx.Snapshot, fx.Now)
	row := d.Workstreams[len(d.Workstreams)-1]
	if row.Kind != "needs" || len(row.Tracks) != 2 || row.Tracks[0].Kind != "needs" {
		t.Fatalf("open login card must lead row and track: %+v", row)
	}
	step := row.Workflow.Steps[0].Children[0]
	if step.Kind != "needs" || step.Text != "Needs you" {
		t.Fatalf("workflow step: %+v", step)
	}
	box := row.Diagram.Tracks[0].Steps[0]
	if box.Text != "Needs you" || box.Lane != "you" {
		t.Fatalf("diagram step: %+v", box)
	}
	if len(d.Asks) != len(fx.Snapshot.NeedsYou) {
		t.Fatal("card disappeared from NEEDS YOU")
	}
	checkWorkflows(t, d)
	checkDiagrams(t, d)
}

// A live Operator off the next gate's blocks path holds the line, so the row and each track read the
// same whether the Manager is idle or in a turn (it used to flip with the Manager's chat state).
func TestBoardOperatorOffGatePathIgnoresManagerState(t *testing.T) {
	raw, err := os.ReadFile("testdata/board/operator-off-gate-path.json")
	if err != nil {
		t.Fatal(err)
	}
	derive := func(state string) []BoardRow {
		var fx struct {
			Now      int64    `json:"now"`
			Snapshot Snapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatal(err)
		}
		for role, a := range fx.Snapshot.Agents {
			a.State = state
			fx.Snapshot.Agents[role] = a
		}
		return Board(fx.Snapshot, fx.Now).Workstreams
	}
	idle, busy := derive(store.AgentIdle), derive(store.AgentWorking)
	if len(idle) != 4 || len(idle) != len(busy) {
		t.Fatalf("rows: idle %d, busy %d", len(idle), len(busy))
	}
	// Row 3: its live track leads its second track's CI wait. Row 4: a broken step beside the live Operator
	// means no track leads on live work, so the wait does, whatever the Manager is doing.
	wantKind := []string{"working", "working", "working", "waiting"}
	for i := range idle {
		a, b := idle[i], busy[i]
		if a.Kind != wantKind[i] || a.Kind != b.Kind || a.Text != b.Text || a.Since != b.Since || a.AgeAs != b.AgeAs || a.Why != b.Why {
			t.Errorf("row %d: idle %s %q %d %q %q, busy %s %q %d %q %q", a.N, a.Kind, a.Text, a.Since, a.AgeAs, a.Why, b.Kind, b.Text, b.Since, b.AgeAs, b.Why)
		}
		if len(a.Tracks) != len(b.Tracks) {
			t.Fatalf("row %d tracks: idle %d, busy %d", a.N, len(a.Tracks), len(b.Tracks))
		}
		for j := range a.Tracks {
			x, y := a.Tracks[j], b.Tracks[j]
			if a.N == 4 && x.Letter == ".1" {
				// The broken track's own row reads as every stalled row does: a busy Manager is picking it up.
				if x.Text != "State missing" || y.Text != "State missing" {
					t.Errorf("track 4.1: idle %q, busy %q", x.Text, y.Text)
				}
				continue
			}
			if x.Kind != y.Kind || x.Text != y.Text || x.Since != y.Since {
				t.Errorf("track %s: idle %s %q %d, busy %s %q %d", x.Letter, x.Kind, x.Text, x.Since, y.Kind, y.Text, y.Since)
			}
		}
	}
}

// boardFixture derives a board fixture with every Manager in the given state.
func boardFixture(t *testing.T, name, state string) Display {
	t.Helper()
	raw, err := os.ReadFile("testdata/board/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for role, a := range fx.Snapshot.Agents {
		a.State = state
		fx.Snapshot.Agents[role] = a
	}
	return Board(fx.Snapshot, fx.Now)
}

// The Working line names the live Operator's step first, never a dead or waiting one ahead of it.
func TestBoardWorkingLineNamesTheLiveOperatorsStep(t *testing.T) {
	want := "Working on land the polish · then: release approved"
	wantRow := []string{want, want, want, "Working on land the polish +1 more · then: release approved", want,
		"Working on land the polish", "Working on land the polish", "Working on land the polish"}
	for _, state := range []string{store.AgentIdle, store.AgentWorking} {
		for i, r := range boardFixture(t, "operator-names-live-step", state).Workstreams {
			if r.Kind != "working" || r.Text != wantRow[i] {
				t.Errorf("%s row %d: %s %q, want %q", state, r.N, r.Kind, r.Text, wantRow[i])
			}
			if r.Since != 0 {
				t.Errorf("%s row %d: since %d, want no Manager age", state, r.N, r.Since)
			}
		}
	}
}

// A broken step beside a live Operator reads State missing, and the why keeps the live work. A busy Manager reads as
// picking it up, as on every stalled row; the why still names the step.
func TestBoardBrokenStepBesideLiveOperatorStaysVisible(t *testing.T) {
	wantWhy := map[int]string{
		1: "Working on land the polish; nothing holds forgotten step",
		2: "Working on land the polish; nothing holds forgotten step",
		3: "Working on land the polish; nothing holds dead step",
		4: "Working on land the polish; nothing holds forgotten step +1 more",
	}
	for _, c := range []struct{ state, kind, text string }{
		{store.AgentIdle, "stalled", "State missing"},
		{store.AgentWorking, "stalled", "State missing"},
	} {
		d := boardFixture(t, "operator-beside-broken", c.state)
		for _, r := range d.Workstreams[:4] {
			if r.Kind != c.kind || r.Text != c.text || !strings.HasPrefix(r.Why, wantWhy[r.N]) {
				t.Errorf("%s row %d: %s %q why %q", c.state, r.N, r.Kind, r.Text, r.Why)
			}
		}
		// Tracks: the broken one reads so on its own line; the row keeps mostUrgent's order.
		tracked := d.Workstreams[4]
		if a, b := tracked.Tracks[0], tracked.Tracks[1]; a.Text != "Working on land the polish" || b.Text != c.text {
			t.Errorf("%s tracks: %q, %q", c.state, a.Text, b.Text)
		}
	}
	if r := boardFixture(t, "operator-beside-broken", store.AgentIdle).Workstreams[5]; r.Text != "State missing" {
		t.Errorf("no Operator, Manager idle: %q", r.Text)
	}
	// The unheld step is not counted as live work.
	if r := boardFixture(t, "operator-beside-broken", store.AgentIdle).Workstreams[6]; !strings.HasPrefix(r.Why, "Working on land the polish; nothing holds unheld task") {
		t.Errorf("in-progress broken step: why %q", r.Why)
	}
}

// Missing custody keeps its age through Manager turns, with or without a live Operator.
func TestBoardBrokenRowBesideOperatorKeepsAgeThroughManagerTurns(t *testing.T) {
	var tr sinceTracker
	fill := func(state string, now int64) Display {
		d := boardFixture(t, "operator-beside-broken", state)
		if err := tr.fill(context.Background(), nil, &d, now, false); err != nil {
			t.Fatal(err)
		}
		return d
	}
	fill(store.AgentIdle, 1000)
	working := fill(store.AgentWorking, 1100)
	if r := working.Workstreams[5]; r.Kind != "stalled" || r.Text != "State missing" || r.Since != 1000 {
		t.Fatalf("Manager activity reset missing custody: %+v", r)
	}
	d := fill(store.AgentIdle, 1200)
	if r := d.Workstreams[0]; r.Since != 1000 {
		t.Errorf("beside an Operator: since %d, want 1000", r.Since)
	}
	if tk := d.Workstreams[4].Tracks[1]; tk.Since != 1000 {
		t.Errorf("broken track: since %d, want 1000", tk.Since)
	}
	if r := d.Workstreams[5]; r.Since != 1000 {
		t.Errorf("no Operator: since %d, want 1000", r.Since)
	}
}

// With no gates too, a task bead as the current step names the step it serves.
func TestBoardNoGatesWorkingLineNamesStepOfCurrentTask(t *testing.T) {
	s := Snapshot{Agents: map[string]SnapAgent{"manager-w": {State: store.AgentIdle}}, Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "w", Title: "w", Status: "open", ManagerRole: "manager-w", NowStep: "w.1.1", Steps: []SnapStep{
		{BeadID: "w.1", Title: "Land the polish", Status: "in_progress", State: "in-progress", Children: []SnapStep{
			{BeadID: "w.1.1", Title: "Operator task", Task: true, Status: "in_progress", State: "in-progress", Owner: "operator-x", OwnerState: "working"},
		}},
	}}}}
	if r := Board(s, 1793214500).Workstreams[0]; r.Text != "Working on land the polish" {
		t.Errorf("%s %q", r.Kind, r.Text)
	}
}

// An Operator's task under a step with other open children still names that step.
func TestBoardWorkingLineNamesStepOfOperatorTask(t *testing.T) {
	s := Snapshot{Agents: map[string]SnapAgent{}, Workstreams: []SnapWorkstream{{N: 1, Project: "app", BeadID: "w", Title: "w", Status: "open", ManagerRole: "manager-w", Steps: []SnapStep{
		{BeadID: "w.1", Title: "Write the notes", Status: "open"},
		{BeadID: "w.2", Title: "Land the polish", Status: "in_progress", State: "in-progress", Children: []SnapStep{
			{BeadID: "w.2.1", Title: "Operator task", Task: true, Status: "in_progress", Owner: "operator-x", OwnerState: "working"},
			{BeadID: "w.2.2", Title: "Open sibling", Status: "open"},
		}},
		{BeadID: "w.3", Title: "Release approved", Status: "open", Gate: true, Approvers: []string{"reviewer-a"}, BlockedBy: []string{"w.1"}},
	}}}}
	if r := Board(s, 1793214500).Workstreams[0]; r.Text != "Working on land the polish · then: release approved" {
		t.Errorf("%s %q", r.Kind, r.Text)
	}
}

// The Manager's turn does not restart the age of a live Operator's row; with no Operator the turn is the work.
func TestBoardBusyManagerLeavesOperatorRowAge(t *testing.T) {
	d := boardFixture(t, "operator-busy-manager-age", store.AgentWorking)
	for _, r := range d.Workstreams[:3] {
		if r.Kind != "working" || r.Since != 0 {
			t.Errorf("row %d: %s since %d, want working with no Manager age", r.N, r.Kind, r.Since)
		}
	}
	if r := d.Workstreams[3]; r.Kind != "working" || r.Since != 1793170000 {
		t.Errorf("no Operator: %s since %d, want the Manager's turn", r.Kind, r.Since)
	}
}

// A live Operator's Working line beside a card elsewhere: the card stays in NEEDS YOU and on its own row.
func TestBoardCardInAnotherRowStaysInNeedsYou(t *testing.T) {
	for _, state := range []string{store.AgentIdle, store.AgentWorking} {
		d := boardFixture(t, "operator-beside-card", state)
		var asks []string
		for _, a := range d.Asks {
			asks = append(asks, a.Key)
		}
		if !slices.Contains(asks, "ask:app:a.1.1") || !slices.Contains(asks, "ask:app:b.1") {
			t.Fatalf("%s: NEEDS YOU lost a card: %v", state, asks)
		}
		sibling, own, dependent := d.Workstreams[0], d.Workstreams[1], d.Workstreams[2]
		if sibling.Kind != "needs" || sibling.Flag != 1 || sibling.Tracks[0].Kind != "needs" {
			t.Errorf("%s: row with the card in a track: %s flag %d, track %s", state, sibling.Kind, sibling.Flag, sibling.Tracks[0].Kind)
		}
		if tr := sibling.Tracks[1]; tr.Kind != "working" {
			t.Errorf("%s: dependent track: %s %q", state, tr.Kind, tr.Text)
		}
		if own.Kind != "needs" || own.Flag != 1 {
			t.Errorf("%s: row with the card: %s %q flag %d", state, own.Kind, own.Text, own.Flag)
		}
		if dependent.Kind != "working" || dependent.Flag != 0 {
			t.Errorf("%s: row blocked on another row's card: %s %q flag %d", state, dependent.Kind, dependent.Text, dependent.Flag)
		}
	}
}

// A task directly under a track has no step to lift to: its track names it, and the age stays when the other track ends.
func TestBoardDirectTaskIsNamedAndKeepsItsAge(t *testing.T) {
	d := boardFixture(t, "operator-direct-task", store.AgentIdle)
	tks := d.Workstreams[0].Tracks
	if len(tks) != 2 || tks[0].Text != "Working on task A" || tks[1].Text != "Working on task B" {
		t.Fatalf("tracks %+v", tks)
	}
	raw, err := os.ReadFile("testdata/board/operator-direct-task.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var tr sinceTracker
	fill := func(s Snapshot, now int64) Display {
		d := Board(s, fx.Now)
		if err := tr.fill(context.Background(), nil, &d, now, false); err != nil {
			t.Fatal(err)
		}
		return d
	}
	// A third track keeps the tracks listed once A ends (a lone track folds into its row).
	s := fx.Snapshot
	third := s.Workstreams[0].Steps[1]
	third.BeadID, third.Title = "t.3", "Track C"
	s.Workstreams[0].Steps = append(slices.Clone(s.Workstreams[0].Steps), third)
	fill(s, 1000)
	s.Workstreams[0].Steps = s.Workstreams[0].Steps[1:]
	got := fill(s, 2000).Workstreams[0]
	if len(got.Tracks) != 2 || got.Tracks[0].Text != "Working on task B" || got.Tracks[0].Since != 1000 {
		t.Errorf("track B after A ended: %+v", got.Tracks)
	}
}

// The live step's name: a task under a gate or superseded bead names itself, a parked subtree is out of play, and a
// stopped task's stage is no work under way.
func TestBoardLiveStepSkipsWhatIsNotWork(t *testing.T) {
	op := func(id, title string) SnapStep {
		return SnapStep{BeadID: id, Title: title, Task: true, Status: "in_progress", State: "in-progress", Owner: "operator-x", OwnerState: "working"}
	}
	stage := func(id, title string, kids ...SnapStep) SnapStep {
		return SnapStep{BeadID: id, Title: title, Status: "open", State: "in-progress", Children: kids}
	}
	gate := stage("w.1", "Release approved", op("w.1.1", "Gate task"))
	gate.Gate, gate.Approvers = true, []string{"reviewer-a"}
	gone := stage("w.1", "Old stage", op("w.1.1", "Stray task"))
	gone.Superseded = true
	shelved := stage("w.1", "Parked stage", op("w.1.1", "Shelved task"))
	shelved.Status = statusDeferred
	dead := op("w.1.1", "Dead task")
	dead.OwnerState = OwnerDied
	dead.Status = "open"
	kept := stage("w.2", "Land the polish", op("w.2.1", "Operator task"))
	child := SnapStep{BeadID: "w.1.1", Title: "Child of parked", Status: "in_progress", State: "in-progress", Owner: "manager-w"}
	shelvedKid := stage("w.1", "Parked stage", child)
	shelvedKid.Status = statusDeferred
	for _, c := range []struct {
		name, now string
		steps     []SnapStep
		want      string
	}{
		{"task under a gate", "w.1.1", []SnapStep{gate}, "Working on gate task"},
		{"task under a superseded bead", "w.1.1", []SnapStep{gone}, "Working on stray task"},
		{"task under a parked stage", "w.1.1", []SnapStep{shelved}, "Working"},
		{"child of a parked stage", "", []SnapStep{shelvedKid, kept}, "Working on land the polish"},
		{"a died task's stage", "w.1.1", []SnapStep{stage("w.1", "Abandoned stage", dead), kept}, "Working on land the polish"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := SnapWorkstream{N: 1, Project: "app", BeadID: "w", Title: "w", Status: "open", ManagerRole: "manager-w", NowStep: c.now, Steps: c.steps}
			got := workingText(board{}.liveSteps(ws))
			if got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
}

// A Manager-owned step is broken only between the Manager's turns, by the invariant itself: the row's age is the live
// Operator's through both states, and through the Manager claiming or clearing another step.
func TestBoardOperatorAgeThroughManagerOwnedStep(t *testing.T) {
	var tr sinceTracker
	claimed := SnapStep{BeadID: "w.2", Title: "Tidy the docs", Status: "in_progress", State: "in-progress", Owner: "manager-w"}
	operator := SnapStep{BeadID: "w.1", Title: "Land the polish", Status: "in_progress", State: "in-progress", Owner: "operator-x", OwnerState: "working"}
	fill := func(state string, now int64, steps ...SnapStep) BoardRow {
		s := Snapshot{Agents: map[string]SnapAgent{"manager-w": {State: state, Since: 500}}, Workstreams: []SnapWorkstream{
			{N: 1, Project: "app", BeadID: "w", Title: "w", Status: "open", ManagerRole: "manager-w", Steps: steps}}}
		markBroken(s.Workstreams, SnapshotViolations(s))
		d := Board(s, 1793214500)
		if err := tr.fill(context.Background(), nil, &d, now, false); err != nil {
			t.Fatal(err)
		}
		return d.Workstreams[0]
	}
	r := fill(store.AgentIdle, 1000, operator, claimed)
	if r.Text != "State missing" {
		t.Fatalf("idle Manager: %q", r.Text)
	}
	r = fill(store.AgentWorking, 1100, operator, claimed)
	if r.Text != "Working on land the polish +1 more" || r.Since != 1000 {
		t.Errorf("busy Manager: %q since %d, want 1000", r.Text, r.Since)
	}
	if r = fill(store.AgentIdle, 1200, operator, claimed); r.Since != 1000 {
		t.Errorf("idle again: since %d, want 1000", r.Since)
	}
	if r = fill(store.AgentWorking, 1300, operator); r.Text != "Working on land the polish" || r.Since != 1000 {
		t.Errorf("step cleared: %q since %d, want 1000", r.Text, r.Since)
	}
}
