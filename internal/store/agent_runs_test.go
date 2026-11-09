package store

import (
	"context"
	"database/sql"
	"testing"
)

func newRun(pID int64, taskID, sessID string) AgentRun {
	return AgentRun{
		ProjectID:         pID,
		TaskBeadID:        taskID,
		AgentName:         "operator-x",
		OpencodeSessionID: sessID,
		ContainerID:       sql.NullString{String: "c-" + sessID, Valid: true},
		SpawnTime:         1000,
	}
}

func TestAgentRun_InsertDefaultsRunning(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	id, err := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_AAA"))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetAgentRun(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
	if got.Status != "running" {
		t.Errorf("default status: got %q want %q", got.Status, "running")
	}
	if got.TokensUsed != 0 || got.UsdUsed != 0 {
		t.Errorf("expected zero usage on insert, got tokens=%d usd=%f", got.TokensUsed, got.UsdUsed)
	}
}

func TestAgentRun_GetByContainer(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	if _, err := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_AAA")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgentRunByContainer(ctx, "c-ses_AAA")
	if err != nil || got == nil {
		t.Fatalf("get by container: %v %+v", err, got)
	}
	miss, err := s.GetAgentRunByContainer(ctx, "c-nonexistent")
	if err != nil {
		t.Fatalf("miss err: %v", err)
	}
	if miss != nil {
		t.Errorf("expected nil for unknown container, got %+v", miss)
	}
}

func TestAgentRun_UpdateUsage(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	id, err := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_AAA"))
	if err != nil {
		t.Fatal(err)
	}

	// Watchdog fires three step_finish events.
	if err := s.UpdateUsage(ctx, id, 1000, 0.10); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	if err := s.UpdateUsage(ctx, id, 2500, 0.27); err != nil {
		t.Fatalf("update 2: %v", err)
	}

	got, _ := s.GetAgentRun(ctx, id)
	if got.TokensUsed != 2500 || got.UsdUsed < 0.269 || got.UsdUsed > 0.271 {
		t.Errorf("usage: %+v", got)
	}
}

func TestAgentRun_UpdateUsage_MissingRunErrors(t *testing.T) {
	s := openMem(t)
	if err := s.UpdateUsage(context.Background(), 999, 1, 1); err == nil {
		t.Error("expected error for missing run")
	}
}

func TestAgentRun_IncrementFailures(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	id, err := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_AAA"))
	if err != nil {
		t.Fatal(err)
	}

	for i := int64(1); i <= 3; i++ {
		n, err := s.IncrementFailures(ctx, id)
		if err != nil {
			t.Fatalf("increment %d: %v", i, err)
		}
		if n != i {
			t.Errorf("count: got %d, want %d", n, i)
		}
	}
}

func TestAgentRun_MarkClosed(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	id, err := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_AAA"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.MarkClosed(ctx, id, "killed-ceiling"); err != nil {
		t.Fatalf("mark closed: %v", err)
	}
	got, _ := s.GetAgentRun(ctx, id)
	if got.Status != "killed-ceiling" {
		t.Errorf("status: got %q, want %q", got.Status, "killed-ceiling")
	}
}

func TestAgentRun_MarkClosed_RejectsRunning(t *testing.T) {
	s := openMem(t)
	if err := s.MarkClosed(context.Background(), 1, "running"); err == nil {
		t.Error("expected error when MarkClosed called with status='running'")
	}
}

func TestAgentRun_ListByProject_FilterByStatus(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	id1, _ := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_A"))
	id2, _ := s.InsertAgentRun(ctx, newRun(pID, "bd-2", "ses_B"))
	_, _ = s.InsertAgentRun(ctx, newRun(pID, "bd-3", "ses_C"))

	if err := s.MarkClosed(ctx, id1, "exited"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkClosed(ctx, id2, "killed-ceiling"); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListAgentRunsByProject(ctx, pID, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("all: %v %d", err, len(all))
	}
	running, err := s.ListAgentRunsByProject(ctx, pID, "running")
	if err != nil || len(running) != 1 {
		t.Fatalf("running: %v %+v", err, running)
	}
}

func TestAgentRun_CountByStatus(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	id1, _ := s.InsertAgentRun(ctx, newRun(pID, "bd-1", "ses_A"))
	_, _ = s.InsertAgentRun(ctx, newRun(pID, "bd-2", "ses_B"))
	if err := s.MarkClosed(ctx, id1, "exited"); err != nil {
		t.Fatal(err)
	}

	counts, err := s.CountAgentRunsByStatus(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["running"] != 1 || counts["exited"] != 1 {
		t.Errorf("counts: %+v", counts)
	}
}
