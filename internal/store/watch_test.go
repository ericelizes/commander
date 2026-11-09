package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWatchFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pid, err := s.InsertProject(ctx, Project{Name: "p", Path: "/p", Type: "code", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "p-1.2", AgentName: "operator-op-x", OpencodeSessionID: "sid", SpawnTime: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunWatch(ctx, id, RunWatch{ActiveAt: 500, QuietTold: 400}); err != nil {
		t.Fatal(err)
	}
	ws, err := s.RunWatches(ctx)
	if err != nil || ws[id] != (RunWatch{ActiveAt: 500, QuietTold: 400}) {
		t.Fatalf("run watches = %v, %v", ws, err)
	}
	if err := s.SetCheckin(ctx, "manager-p-1", Checkin{Due: 900, Checked: 600}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCheckin(ctx, "manager-p-1", Checkin{Due: 1000, Checked: 660}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Checkins(ctx)
	if err != nil || cs["manager-p-1"] != (Checkin{Due: 1000, Checked: 660}) {
		t.Fatalf("checkins = %v, %v", cs, err)
	}
	if err := s.ClearCheckin(ctx, "manager-p-1"); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Checkins(ctx); len(cs) != 0 {
		t.Fatalf("checkins after clear = %v", cs)
	}

	issues := []WatchIssue{{Key: "w:stalled", WS: "w", Text: "stalled", Since: 10, Active: true}, {Key: "w:ci", WS: "w", Text: "CI", Since: 20, Told: true}}
	if err := s.SetWatchIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWatchIssues(ctx, issues[1:]); err != nil {
		t.Fatal(err)
	}
	got, err := s.WatchIssues(ctx)
	if err != nil || len(got) != 1 || got[0] != issues[1] {
		t.Fatalf("watch issues = %+v, %v", got, err)
	}
}
