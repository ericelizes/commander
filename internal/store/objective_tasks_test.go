package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// seedProjectAndObjective creates one project + one objective and returns
// their ids. Used by FK + join tests.
func seedProjectAndObjective(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	pID, err := s.InsertProject(ctx, Project{
		Name: "p1", Type: "code", Path: "/tmp/p1", ActivatedAt: 1, Status: "active",
	})
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	oID, err := s.InsertObjective(ctx, Objective{Title: "obj", Status: "open", CreatedAt: 1})
	if err != nil {
		t.Fatalf("insert objective: %v", err)
	}
	return pID, oID
}

func TestObjectiveTask_InsertAndGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, oID := seedProjectAndObjective(t, s)

	inserted, err := s.InsertObjectiveTask(ctx, ObjectiveTask{
		ObjectiveID: oID, ProjectID: pID, TaskBeadID: "bd-1", IsDerived: false, CreatedAt: 10,
	})
	if err != nil || !inserted {
		t.Fatalf("insert: err=%v inserted=%v", err, inserted)
	}

	got, err := s.GetObjectiveTask(ctx, oID, pID, "bd-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
	if got.TaskBeadID != "bd-1" || got.IsDerived {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestObjectiveTask_InsertOrIgnore_DuplicateNoOp(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, oID := seedProjectAndObjective(t, s)

	row := ObjectiveTask{ObjectiveID: oID, ProjectID: pID, TaskBeadID: "bd-1", CreatedAt: 10}
	if _, err := s.InsertObjectiveTask(ctx, row); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	inserted, err := s.InsertObjectiveTask(ctx, row)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if inserted {
		t.Errorf("expected duplicate insert to be a no-op, but inserted=true")
	}
}

func TestObjectiveTask_DerivedTracking(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, oID := seedProjectAndObjective(t, s)

	if _, err := s.InsertObjectiveTask(ctx, ObjectiveTask{
		ObjectiveID: oID, ProjectID: pID, TaskBeadID: "bd-2",
		IsDerived: true, DerivedFromTaskBeadID: sql.NullString{String: "bd-1", Valid: true},
		CreatedAt: 20,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetObjectiveTask(ctx, oID, pID, "bd-2")
	if err != nil || got == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
	if !got.IsDerived || !got.DerivedFromTaskBeadID.Valid || got.DerivedFromTaskBeadID.String != "bd-1" {
		t.Errorf("derived fields: %+v", got)
	}
}

func TestObjectiveTask_FKConstraint(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// No project, no objective — FK to both must fire.
	_, err := s.InsertObjectiveTask(ctx, ObjectiveTask{
		ObjectiveID: 999, ProjectID: 999, TaskBeadID: "bd-x", CreatedAt: 1,
	})
	if err == nil {
		t.Fatal("expected FK constraint error, got nil")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("expected FK error, got %v", err)
	}
}

func TestObjectiveTask_ListByObjectiveAndProject(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID1, oID := seedProjectAndObjective(t, s)
	pID2, err := s.InsertProject(ctx, Project{
		Name: "p2", Type: "code", Path: "/tmp/p2", ActivatedAt: 1, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range []ObjectiveTask{
		{ObjectiveID: oID, ProjectID: pID1, TaskBeadID: "bd-1", CreatedAt: 10},
		{ObjectiveID: oID, ProjectID: pID2, TaskBeadID: "bd-2", CreatedAt: 20},
		{ObjectiveID: oID, ProjectID: pID1, TaskBeadID: "bd-3", CreatedAt: 30},
	} {
		if _, err := s.InsertObjectiveTask(ctx, row); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	byObj, err := s.ListObjectiveTasksByObjective(ctx, oID)
	if err != nil || len(byObj) != 3 {
		t.Fatalf("by objective: %v %d", err, len(byObj))
	}
	byP1, err := s.ListObjectiveTasksByProject(ctx, pID1)
	if err != nil || len(byP1) != 2 {
		t.Fatalf("by project p1: %v %d", err, len(byP1))
	}
	byP2, err := s.ListObjectiveTasksByProject(ctx, pID2)
	if err != nil || len(byP2) != 1 || byP2[0].TaskBeadID != "bd-2" {
		t.Fatalf("by project p2: %v %+v", err, byP2)
	}
}

func TestObjectiveTask_ListByTaskBeadID(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID1, oID := seedProjectAndObjective(t, s)
	pID2, err := s.InsertProject(ctx, Project{
		Name: "p2", Type: "code", Path: "/tmp/p2", ActivatedAt: 1, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range []ObjectiveTask{
		{ObjectiveID: oID, ProjectID: pID1, TaskBeadID: "bd-1", CreatedAt: 10},
		{ObjectiveID: oID, ProjectID: pID2, TaskBeadID: "bd-1", CreatedAt: 20},
		{ObjectiveID: oID, ProjectID: pID2, TaskBeadID: "bd-2", CreatedAt: 30},
	} {
		if _, err := s.InsertObjectiveTask(ctx, row); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := s.ListObjectiveTasksByTaskBeadID(ctx, "bd-1")
	if err != nil {
		t.Fatalf("list by task: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows for bd-1, got %d", len(got))
	}
}
