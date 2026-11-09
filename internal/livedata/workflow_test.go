package livedata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The workflow fixtures: a snapshot and its clock (testdata/workflow/*.json),
// every row's derived workflow (*.want.json, by row key) and its text
// (*.lines.txt). UPDATE_BOARD=1 rewrites both; review the diff.
func TestWorkflowFixtures(t *testing.T) {
	SetBoardZone(time.UTC)
	defer SetBoardZone(nil)
	files, _ := filepath.Glob("testdata/workflow/*.json")
	if len(files) == 0 {
		t.Fatal("no workflow fixtures")
	}
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
			byRow := map[string]*Workflow{}
			var lines []string
			for _, w := range d.Workstreams {
				byRow[w.Key] = w.Workflow
				lines = append(lines, "#"+itoa(int64(w.N))+" "+w.Title)
				lines = append(lines, WorkflowLines(w.Workflow)...)
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
				t.Errorf("workflow lines differ:\n%s", text)
			}
			var want, have any
			_ = json.Unmarshal(wantRaw, &want)
			_ = json.Unmarshal(got, &have)
			if diff := jsonDiff("", want, have); diff != "" {
				t.Errorf("workflow differs:\n%s", diff)
			}
		})
	}
}

// The approval on the card and what it starts, in the order the work can begin.
func TestWorkflow_GateUnlocksThroughNextGate(t *testing.T) {
	w := fixtureWorkflow(t, "gates", "lab:lab-t06")
	if w.Current != "lab-t06.6" {
		t.Fatalf("current = %q, want the gate being decided", w.Current)
	}
	want := []string{"lab-t06.2.3", "lab-t06.3.3", "lab-t06.2.4", "lab-t06.7"}
	if strings.Join(w.Unlocks, ",") != strings.Join(want, ",") {
		t.Fatalf("unlocks = %v, want %v (stops at the next gate; the summary post waits on it)", w.Unlocks, want)
	}
	gate := findStep(w.Steps, "lab-t06.6")
	if gate == nil || gate.Kind != "needs" || gate.Who != "you" || strings.Join(gate.Unlocks, ",") != strings.Join(want, ",") {
		t.Fatalf("gate step = %+v", gate)
	}
	if findStep(w.Steps, "lab-t06.9") != nil {
		t.Fatal("a superseded step is not part of the workflow")
	}
	if s := findStep(w.Steps, "lab-t06.7"); s.Text != "Not reached" {
		t.Fatalf("next gate reads %q", s.Text)
	}
}

// A step reads as the board reads its state: who has to act.
func TestWorkflow_StepWords(t *testing.T) {
	w := fixtureWorkflow(t, "gates", "lab:lab-ops")
	for id, want := range map[string]string{
		"lab-ops.1": "working|Operator working",
		"lab-ops.2": "waiting|Waiting for review",
		"lab-ops.3": "idle|Not started",
		"lab-ops.4": "stalled|Agent stopped: the Manager's move",
	} {
		s := findStep(w.Steps, id)
		if s == nil || s.Kind+"|"+s.Text != want {
			t.Errorf("%s = %+v, want %s", id, s, want)
		}
	}
	if w.Current != "lab-ops.1" || len(w.Unlocks) != 0 {
		t.Fatalf("current = %q unlocks %v: work in progress, nothing to approve", w.Current, w.Unlocks)
	}
}

func fixtureWorkflow(t *testing.T, name, key string) *Workflow {
	t.Helper()
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile("testdata/workflow/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, r := range Board(fx.Snapshot, fx.Now).Workstreams {
		if r.Key == key {
			if r.Workflow == nil {
				t.Fatalf("%s has no workflow", key)
			}
			return r.Workflow
		}
	}
	t.Fatalf("no row %s", key)
	return nil
}

func findStep(steps []WorkflowStep, id string) *WorkflowStep { return findWorkflowStep(steps, id) }

// A track's workflow is its own steps and its own current step.
func TestWorkflow_Sub(t *testing.T) {
	w := fixtureWorkflow(t, "gates", "lab:lab-t06").Sub("lab-t06.3")
	if w == nil || len(w.Steps) != 4 || w.Current != "lab-t06.3.2" {
		t.Fatalf("track workflow = %+v", w)
	}
	if fixtureWorkflow(t, "gates", "lab:lab-t06").Sub("lab-t06.4") != nil {
		t.Fatal("a done step has no workflow of its own")
	}
}

// checkWorkflows holds every workflow to the board it sits in: a track reads
// as its sidebar row, and what it points at is an open step in it.
func checkWorkflows(t *testing.T, d Display) {
	t.Helper()
	for _, r := range d.Workstreams {
		w := r.Workflow
		if w == nil {
			continue
		}
		for _, tr := range r.Tracks {
			s := findStep(w.Steps, tr.BeadID)
			if s == nil || s.Kind != tr.Kind || s.Text != tr.Text {
				t.Errorf("%s track %s: workflow %+v, board %s %q", r.Key, tr.BeadID, s, tr.Kind, tr.Text)
			}
		}
		if w.Current != "" && findStep(w.Steps, w.Current) == nil {
			t.Errorf("%s: current %s is not a step", r.Key, w.Current)
		}
		if r.Gate != nil && r.Gate.At && r.Gate.ForYou && w.Current != r.Gate.BeadID {
			t.Errorf("%s: the row waits on you for %s, workflow current %s", r.Key, r.Gate.BeadID, w.Current)
		}
		for _, id := range w.Unlocks {
			if s := findStep(w.Steps, id); s == nil || s.Kind == "done" {
				t.Errorf("%s: unlocks %s, not an open step", r.Key, id)
			}
		}
	}
}
