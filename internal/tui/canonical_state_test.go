package tui

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui/widgets"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalNestedUsageShowsCauseInsteadOfLegacyHolder(t *testing.T) {
	cause := store.UsageCapacityError + "no connected account has usage left"
	frame := &livedata.Snapshot{Projects: []livedata.SnapProject{{ID: 1, Name: "p"}}, Display: &livedata.Display{Workstreams: []livedata.BoardRow{{Project: "p", BeadID: "root", Kind: "stalled", Text: cause, Tracks: []livedata.BoardTrack{{BeadID: "leaf", Kind: "stalled", Text: cause}}}}}}
	ws := workstream{project: store.Project{ID: 1, Name: "p"}, root: ObjectiveRow{ProjectID: 1, ProjectName: "p", BeadID: "root", Title: "Root", Status: "open"}, steps: []nestedObjective{{row: ObjectiveRow{ProjectID: 1, ProjectName: "p", BeadID: "leaf", Title: "Leaf", Status: "open", Owner: "manager-retired"}}}}
	found := false
	for _, row := range workstreamRows(1, ws, true, false, true, animState{truth: newCanonicalState(frame)}) {
		if row.row.BeadID == "leaf" && row.kind == "objective" {
			found = true
			if text := stripANSI(row.node.Title); !strings.Contains(text, cause) || strings.Contains(text, "manager-retired") {
				t.Fatal(text)
			}
		}
	}
	if !found {
		t.Fatal("nested row missing")
	}
}

func TestCanonicalTerminalCountsAndSpinnersIgnoreLegacyManagerActivity(t *testing.T) {
	frame := &livedata.Snapshot{Storage: "postgres", Projects: []livedata.SnapProject{{ID: 1, Name: "p"}}, Executions: []livedata.SnapExecution{}, Workstreams: []livedata.SnapWorkstream{{ProjectID: 1, Project: "p", BeadID: "root", Steps: []livedata.SnapStep{{BeadID: "leaf", State: "in-progress"}}}}, Display: &livedata.Display{Workstreams: []livedata.BoardRow{{Project: "p", BeadID: "root", Kind: "waiting", Text: "Result ready for Commander review"}}}}
	ws := workstream{project: store.Project{ID: 1, Name: "p"}, root: ObjectiveRow{ProjectID: 1, ProjectName: "p", BeadID: "root", Title: "Root", Status: "open"}, steps: []nestedObjective{{row: ObjectiveRow{ProjectID: 1, ProjectName: "p", BeadID: "leaf", Title: "Leaf", Status: "open", State: beads.StateInProgress}}}}
	render := func() []panelRow {
		return workstreamRows(1, ws, true, false, true, animState{frame: 3, on: true, motion: true, truth: newCanonicalState(frame), agents: map[string]store.AgentState{"manager-root": {State: store.AgentWorking, Since: time.Now().Unix(), Updated: time.Now().Unix()}}})
	}
	rows := render()
	found := false
	for _, r := range rows {
		if r.kind == "now" {
			found = strings.Contains(stripANSI(r.node.Title), "Result ready for Commander review")
		}
		if r.row.BeadID == "leaf" && strings.Contains(stripANSI(r.node.Marker), widgets.SpinnerFrames[3]) {
			t.Fatal("legacy activity made an unverified leaf spin")
		}
	}
	if !found {
		t.Fatal("terminal ignored canonical result wait")
	}
	for _, part := range rows[0].node.TagParts {
		if strings.Contains(part.Text, "●") {
			t.Fatal("in-progress bead was counted as an executing agent")
		}
	}

	frame.Executions = []livedata.SnapExecution{{ProjectID: 1, BeadID: "leaf", RunID: 9, SessionID: "current", Role: "operator-exec", State: store.AgentWorking}}
	frame.Workstreams[0].Steps[0].Working = true
	frame.Display.Workstreams[0].Working = 1
	frame.Display.Workstreams[0].Kind = "working"
	frame.Display.Workstreams[0].Text = "Executing the bounded task"
	rows = render()
	spinning := false
	for _, r := range rows {
		if r.row.BeadID == "leaf" && strings.Contains(stripANSI(r.node.Marker), widgets.SpinnerFrames[3]) {
			spinning = true
		}
	}
	if !spinning {
		t.Fatal("verified active execution did not drive the terminal spinner")
	}
	if len(rows[0].node.TagParts) == 0 || rows[0].node.TagParts[0].Text != "● 1" {
		t.Fatalf("header disagrees with execution count: %+v", rows[0].node.TagParts)
	}

}

func TestPostgresTerminalUsesCurrentFrameAndClearsItOnDatabaseFailure(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	pid, e := st.InsertProject(ctx, store.Project{Name: "terminal", Path: dir, Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'root','Recorded terminal result','["kind:objective","kind:workstream"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "root", AgentName: "operator-result", OpencodeSessionID: "private-session", SpawnTime: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.FinishNativeExecutionReport(ctx, "operator-result", rid, "private-session", "exited", "Prepared result awaiting review"); e != nil {
		t.Fatal(e)
	}
	deps := CommanderDataDeps{Store: st, Beads: beads.New()}
	good := LoadCommanderData(ctx, deps)
	if good.LoadErr != nil || good.CanonicalSnapshot == nil || good.CanonicalSnapshot.Display == nil || len(good.CanonicalSnapshot.Display.Workstreams) != 1 || good.CanonicalSnapshot.Display.Workstreams[0].Text != "Result ready for Commander review" || good.Metrics.AgentCount != 0 || good.Metrics.ActiveCount != 0 {
		t.Fatalf("terminal did not load canonical facts: %+v", good)
	}
	raw, e := json.Marshal(livedata.ToWire(good))
	if e != nil {
		t.Fatal(e)
	}
	var wire livedata.Wire
	if e = json.Unmarshal(raw, &wire); e != nil {
		t.Fatal(e)
	}
	if round := livedata.FromWire(wire); round.CanonicalSnapshot == nil || round.CanonicalSnapshot.Display.Workstreams[0].Text != "Result ready for Commander review" {
		t.Fatal("wire dropped canonical projection")
	}
	column := NewObjectivesColumn(nil)
	column.width = 100
	column.height = 35
	column.applyLiveData(good)
	if !strings.Contains(stripANSI(column.View()), "Recorded terminal result") {
		t.Fatal("live fixture did not render")
	}
	if _, e = st.DB().Exec(`ALTER TABLE run_observations RENAME TO run_observations_unavailable`); e != nil {
		t.Fatal(e)
	}
	failed := LoadCommanderData(ctx, deps)
	if failed.LoadErr == nil || failed.CanonicalSnapshot == nil || failed.CanonicalSnapshot.Degraded == nil || len(failed.Objectives) != 0 || failed.Metrics.ActiveCount != 0 {
		t.Fatal("failed current read returned a healthy saved frame")
	}
	column.applyLiveData(failed)
	view := stripANSI(column.View())
	if strings.Contains(view, "Recorded terminal result") || !strings.Contains(view, "state unavailable") {
		t.Fatalf("database failure kept a healthy sidebar:\n%s", view)
	}
}
