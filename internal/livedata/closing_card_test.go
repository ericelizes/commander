package livedata

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestOpenConfirmDoneCardLeadsWorkstreamAndParent(t *testing.T) {
	s := activeBoardFixture()
	s.Workstreams[0].Steps = []SnapStep{
		{BeadID: "philip", Title: "Philip", Status: "open", Track: true, Children: []SnapStep{
			{BeadID: "finished", Status: "closed"},
			{BeadID: "confirm", Title: "Confirm done", Status: "open", ClosingGate: true, Gate: true, State: "needs-user", Approvers: []string{"user"}},
		}},
		{BeadID: "other", Title: "Other workstream", Status: "open", Track: true, Children: []SnapStep{{BeadID: "running", Title: "Other work", Status: "open"}}},
	}
	s.NeedsYou = []NeedsYou{{Project: "p", BeadID: "confirm", TrackID: "philip", Title: "Confirm done", Ask: "Is it done?", Decision: &store.Decision{Question: "Is it done?"}}}
	s.Executions = []SnapExecution{{ProjectID: 1, BeadID: "running", Role: "exec-other", State: store.AgentWorking}}
	row := Board(s, 100).Workstreams[0]
	if row.Kind != "needs" || !strings.Contains(row.Text, "Is it done?") {
		t.Errorf("parent hid Confirm done behind other work: kind=%s text=%q", row.Kind, row.Text)
	}
	if len(row.Tracks) != 2 || row.Tracks[0].Kind != "needs" || !strings.Contains(row.Tracks[0].Text, "Is it done?") {
		t.Errorf("workstream with an open card read Complete: %+v", row.Tracks)
	}
	// The same gate directly under a top-level workstream must also ask.
	s.Workstreams[0].Steps = s.Workstreams[0].Steps[0].Children
	row = Board(s, 100).Workstreams[0]
	if row.Kind != "needs" || !strings.Contains(row.Text, "Is it done?") {
		t.Fatalf("top-level: %+v", row)
	}
}

// Exercise the actual PostgreSQL answer projection through the card list and
// board. Generation 1 is deliberately left journaled, as in the live failure.
func TestConfirmDoneSavedAnswerGenerationScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/confirm-done-card.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Ask            string
		OldGeneration  string `json:"old_generation"`
		LiveGeneration int    `json:"live_generation"`
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "p", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'confirm','Confirm done','["needs-user"]','now','now')`, pid); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordPitch(ctx, pid, "confirm", "Previous confirmation", (&store.Decision{Question: "Previous confirmation"}).JSON(), nil); err != nil {
		t.Fatal(err)
	}
	saved := store.NeedsResolution{ProjectID: pid, BeadID: "confirm", Generation: sc.OldGeneration, AnswerID: "refused-old-answer", Action: "decide", Choice: "Yes"}
	if err = st.SaveNeedsResolution(ctx, saved, "Yes"); err != nil {
		t.Fatal(err)
	}
	for generation := 2; generation <= sc.LiveGeneration; generation++ {
		if err = st.RecordPitch(ctx, pid, "confirm", sc.Ask, (&store.Decision{Question: sc.Ask, Context: strings.Repeat("!", generation)}).JSON(), nil); err != nil {
			t.Fatal(err)
		}
	}
	row := ObjectiveRow{ProjectID: pid, ProjectName: "p", BeadID: "confirm", Title: "Confirm done", Status: "open", State: "needs-user", Note: "Please confirm the result", Ask: sc.Ask, ClosingGate: true}
	rows := []ObjectiveRow{row}
	if _, err = attachExecutionState(ctx, st, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].HumanAnswerPending || rows[0].State != "needs-user" {
		t.Fatalf("older answer hid newer card: %+v", rows[0])
	}
	project := store.Project{ID: pid, Name: "p"}
	data := Data{Projects: []store.Project{project}, Objectives: rows, workstreams: []Workstream{{Project: project, Root: rows[0]}}}
	asks := NeedsYouList(data)
	if len(asks) != 1 || asks[0].Ask != sc.Ask {
		t.Fatalf("new card missing: %+v", asks)
	}
	snapshot := activeBoardFixture()
	snapshot.Workstreams[0].Steps = []SnapStep{{BeadID: "finished", Status: "closed"}, {BeadID: "confirm", Title: "Confirm done", Status: "open", ClosingGate: true, HumanAnswerPending: rows[0].HumanAnswerPending}}
	snapshot.NeedsYou = asks
	if got := Board(snapshot, 100).Workstreams[0]; got.Kind != "needs" || !strings.Contains(got.Text, sc.Ask) {
		t.Fatalf("new card hidden on board: %+v", got)
	}
	entries, err := st.ListNeedsResolutions(ctx, pid, "confirm")
	if err != nil || len(entries) != 1 || entries[0].Generation != sc.OldGeneration || entries[0].Applied {
		t.Fatalf("old unapplied answer was not retained: %+v %v", entries, err)
	}
	// Answering the current generation must still suppress a duplicate ask.
	saved.Generation, err = st.NeedsGeneration(ctx, pid, "confirm")
	if err != nil || saved.Generation != "4" {
		t.Fatalf("expected live generation 4: %q %v", saved.Generation, err)
	}
	saved.AnswerID = "current-answer"
	if err = st.SaveNeedsResolution(ctx, saved, "Yes"); err != nil {
		t.Fatal(err)
	}
	if _, err = attachExecutionState(ctx, st, rows); err != nil {
		t.Fatal(err)
	}
	if !rows[0].HumanAnswerPending || rows[0].State != "review" {
		t.Fatalf("current answer lost: %+v", rows[0])
	}
	data.workstreams[0].Root = rows[0]
	if asks := NeedsYouList(data); len(asks) != 0 {
		t.Fatalf("current answer asked again: %+v", asks)
	}
}
