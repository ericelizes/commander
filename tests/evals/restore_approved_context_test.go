package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/example-user/commander/internal/approvedcontext"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// In-process interaction scenario. No CLI build or external agents needed.
func TestRestoreApprovedContextScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/restore-approved-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Project, Root, Gate string
		Decision            store.Decision
		AnswerID            string   `json:"answer_id"`
		Actions             []string `json:"action_contains"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.InsertProject(ctx, store.Project{Name: scenario.Project, Type: "code", Path: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordPitch(ctx, pid, scenario.Gate, "Continue?", scenario.Decision.JSON(), nil); err != nil {
		t.Fatal(err)
	}
	r := store.NeedsResolution{ProjectID: pid, BeadID: scenario.Gate, Generation: "1", AnswerID: scenario.AnswerID, Action: "approve"}
	if err = st.SaveNeedsResolution(ctx, r, "Approved"); err != nil {
		t.Fatal(err)
	}
	if err = st.AdvanceNeedsResolution(ctx, r, store.ResolutionMailed); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = store.OpenProvenance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bc := &beads.MemClient{EdgeParentsOnly: true}
	bc.Seed(beads.Bead{ID: scenario.Root, Labels: []string{"kind:objective"}})
	bc.Seed(beads.Bead{ID: scenario.Gate, Status: "closed", Labels: []string{"gate", "approver:user"}})
	bc.Edges = append(bc.Edges, beads.Dep{IssueID: scenario.Gate, DependsOnID: scenario.Root, Type: "parent-child"})
	p := store.Project{ID: pid, Path: "/fixture"}
	before, _ := st.ListNeedsResolutions(ctx, pid, scenario.Gate)
	restore := func(dry bool) (approvedcontext.Result, error) {
		return approvedcontext.Restore(ctx, st, bc, p, scenario.Gate, "manager-"+scenario.Root, 1, r.AnswerID, dry)
	}
	if _, err = restore(true); err != nil {
		t.Fatal(err)
	}
	b, _ := bc.Show(ctx, p.Path, scenario.Gate)
	if b.Description != "" || b.Status != "closed" {
		t.Fatal("dry run changed gate")
	}
	t.Log(scenario.Actions[0])
	result, err := restore(false)
	if err != nil || !result.Changed {
		t.Fatalf("%+v %v", result, err)
	}
	b, _ = bc.Show(ctx, p.Path, scenario.Gate)
	if b.Description != scenario.Decision.Context || b.Status != "closed" {
		t.Fatalf("%+v", b)
	}
	t.Log(scenario.Actions[1])
	result, err = restore(false)
	if err != nil || result.Changed {
		t.Fatalf("%+v %v", result, err)
	}
	t.Log(scenario.Actions[2])
	changed := "different"
	if err = bc.UpdateFields(ctx, p.Path, scenario.Gate, beads.Fields{Description: &changed}); err != nil {
		t.Fatal(err)
	}
	if _, err = restore(false); err == nil {
		t.Fatal("overwrote different description")
	}
	t.Log(scenario.Actions[3])
	after, _ := st.ListNeedsResolutions(ctx, pid, scenario.Gate)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed answer history")
	}
	t.Log(scenario.Actions[4])
}

// Exercise the real process client against bd's native omission shape. The
// restoration interaction above and TestRestoreExactBackend cover the write.
func TestRestoreNativeDescriptionScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/restore-native-description.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Show    json.RawMessage
		Version string
		Cases   []struct {
			Query json.RawMessage
			Want  string
			Deny  bool
		}
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{"show.json": s.Show, "version": []byte(s.Version)} {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "fake-bd")
	script := `#!/usr/bin/env python3
import sys,pathlib
args=sys.argv[1:]
if args==['show','sample-gate','--json']: print(pathlib.Path('show.json').read_text())
elif args==['--version']: print(pathlib.Path('version').read_text())
elif args==['--readonly','sql',"SELECT id, description FROM issues WHERE id = 'sample-gate'",'--json']: print(pathlib.Path('query.json').read_text())
else: raise Exception(args)
`
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", bin)
	reader := beads.New().(beads.ExactDescriptionReader)
	for _, tc := range s.Cases {
		if err = os.WriteFile(filepath.Join(dir, "query.json"), tc.Query, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := reader.ExactDescription(context.Background(), dir, "sample-gate")
		if (err != nil) != tc.Deny || (!tc.Deny && got != tc.Want) {
			t.Fatalf("query %s: got %q, %v", tc.Query, got, err)
		}
	}
}
