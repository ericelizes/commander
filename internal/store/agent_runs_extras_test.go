package store

import (
	"context"
	"database/sql"
	"testing"
)

func insertRun(t *testing.T, s *Store, pID int64, taskID, sess, status string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.InsertAgentRun(ctx, AgentRun{
		ProjectID:         pID,
		TaskBeadID:        taskID,
		AgentName:         "operator-x",
		OpencodeSessionID: sess,
		ContainerID:       sql.NullString{String: "c-" + sess, Valid: true},
		SpawnTime:         1000,
		Status:            status,
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return id
}

func TestAgentRun_ListRunningRuns(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	r1 := insertRun(t, s, pID, "bd-1", "ses_A", "running")
	insertRun(t, s, pID, "bd-2", "ses_B", "running")
	exited := insertRun(t, s, pID, "bd-3", "ses_C", "running")
	if err := s.MarkClosed(ctx, exited, "exited"); err != nil {
		t.Fatalf("mark closed: %v", err)
	}

	got, err := s.ListRunningRuns(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 running, got %d", len(got))
	}
	if got[0].ID != r1 {
		t.Fatalf("want oldest first, got id=%d", got[0].ID)
	}
}

func TestAgentRun_CountRunsByProject(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	insertRun(t, s, pID, "bd-1", "ses_A", "running")
	insertRun(t, s, pID, "bd-2", "ses_B", "running")
	closed := insertRun(t, s, pID, "bd-3", "ses_C", "running")
	if err := s.MarkClosed(ctx, closed, "closed"); err != nil {
		t.Fatalf("mark: %v", err)
	}

	running, err := s.CountRunsByProject(ctx, pID, "running")
	if err != nil {
		t.Fatalf("count running: %v", err)
	}
	if running != 2 {
		t.Fatalf("running: want 2 got %d", running)
	}

	all, err := s.CountRunsByProject(ctx, pID, "")
	if err != nil {
		t.Fatalf("count all: %v", err)
	}
	if all != 3 {
		t.Fatalf("all: want 3 got %d", all)
	}

	other, err := s.CountRunsByProject(ctx, pID+999, "running")
	if err != nil {
		t.Fatalf("count other: %v", err)
	}
	if other != 0 {
		t.Fatalf("other project: want 0 got %d", other)
	}
}

func TestAgentRun_GetAgentRunByTaskBeadID(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	// Insert two runs against the same task — one closed, one running.
	closed := insertRun(t, s, pID, "bd-task-1", "ses_OLD", "running")
	if err := s.MarkClosed(ctx, closed, "closed"); err != nil {
		t.Fatalf("mark closed: %v", err)
	}
	running := insertRun(t, s, pID, "bd-task-1", "ses_NEW", "running")

	got, err := s.GetAgentRunByTaskBeadID(ctx, "bd-task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("want a run, got nil")
	}
	if got.ID != running {
		t.Fatalf("want running run id %d, got %d", running, got.ID)
	}
	if got.OpencodeSessionID != "ses_NEW" {
		t.Fatalf("want ses_NEW, got %q", got.OpencodeSessionID)
	}

	// Missing bead id returns (nil, nil).
	missing, err := s.GetAgentRunByTaskBeadID(ctx, "bd-does-not-exist")
	if err != nil {
		t.Fatalf("missing: unexpected err: %v", err)
	}
	if missing != nil {
		t.Fatalf("want nil for missing, got %+v", missing)
	}
}

func TestAgentRun_ListAgentRunsByTaskBeadID(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID1, _ := seedProjectAndObjective(t, s)
	pID2, err := s.InsertProject(ctx, Project{
		Name: "p2", Type: "code", Path: "/tmp/p2", ActivatedAt: 1, Status: "active",
	})
	if err != nil {
		t.Fatalf("insert p2: %v", err)
	}

	insertRun(t, s, pID1, "bd-task-1", "ses_A", "running")
	closed := insertRun(t, s, pID2, "bd-task-1", "ses_B", "running")
	if err := s.MarkClosed(ctx, closed, "closed"); err != nil {
		t.Fatalf("mark closed: %v", err)
	}

	running, err := s.ListAgentRunsByTaskBeadID(ctx, "bd-task-1", "running")
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 1 || running[0].ProjectID != pID1 {
		t.Fatalf("running: got %+v", running)
	}

	all, err := s.ListAgentRunsByTaskBeadID(ctx, "bd-task-1", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all: want 2 got %d", len(all))
	}
}

func TestAgentRun_CountAllRuns(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	insertRun(t, s, pID, "bd-1", "ses_A", "running")
	insertRun(t, s, pID, "bd-2", "ses_B", "running")
	closed := insertRun(t, s, pID, "bd-3", "ses_C", "running")
	if err := s.MarkClosed(ctx, closed, "killed-ceiling"); err != nil {
		t.Fatalf("mark: %v", err)
	}

	running, err := s.CountAllRuns(ctx, "running")
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if running != 2 {
		t.Fatalf("running: want 2 got %d", running)
	}

	killed, err := s.CountAllRuns(ctx, "killed-ceiling")
	if err != nil {
		t.Fatalf("killed: %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed: want 1 got %d", killed)
	}

	all, err := s.CountAllRuns(ctx, "")
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all != 3 {
		t.Fatalf("all: want 3 got %d", all)
	}
}

func TestAgentRun_GetAgentRunByAgentNameDotsAsDashes(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	if _, err := s.InsertAgentRun(ctx, AgentRun{
		ProjectID:  pID,
		TaskBeadID: "example-app-t01.17.10",
		AgentName:  "operator-op-triage-example-app-t01.17.10",
		SpawnTime:  1000,
		Status:     "running",
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	for _, name := range []string{"operator-op-triage-example-app-t01.17.10", "operator-op-triage-example-app-t01-17-10"} {
		got, err := s.GetAgentRunByAgentName(ctx, name)
		if err != nil || got == nil || got.TaskBeadID != "example-app-t01.17.10" {
			t.Fatalf("lookup %q = %+v, %v", name, got, err)
		}
	}
	if got, err := s.GetAgentRunByAgentName(ctx, "operator-other"); err != nil || got != nil {
		t.Fatalf("unknown name = %+v, %v; want nil", got, err)
	}
}

// A run's worktree survives the round trip; NULL stays NULL for older rows.
func TestAgentRunWorktreePath(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	p, _ := seedProjectAndObjective(t, s)
	if _, err := s.InsertAgentRun(ctx, AgentRun{ProjectID: p, TaskBeadID: "a", AgentName: "operator-a", WorktreePath: sql.NullString{String: "/wt/.worktrees/b", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertAgentRun(ctx, AgentRun{ProjectID: p, TaskBeadID: "c", AgentName: "operator-c"}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRunningRuns(ctx)
	if err != nil || len(runs) != 2 {
		t.Fatalf("%+v %v", runs, err)
	}
	if runs[0].WorktreePath.String != "/wt/.worktrees/b" || runs[1].WorktreePath.Valid {
		t.Fatalf("%+v", runs)
	}
}
