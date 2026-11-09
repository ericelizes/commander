package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestObjective_InsertGetClose(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	id, err := s.InsertObjective(ctx, Objective{
		Title:           "Ship U3",
		DescriptionPath: sql.NullString{String: "/x.md", Valid: true},
		Status:          "open",
		CreatedAt:       100,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.GetObjective(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
	if got.Status != "open" || got.Title != "Ship U3" {
		t.Errorf("unexpected: %+v", got)
	}

	if err := s.CloseObjective(ctx, id, 200); err != nil {
		t.Fatalf("close: %v", err)
	}
	got2, err := s.GetObjective(ctx, id)
	if err != nil {
		t.Fatalf("get after close: %v", err)
	}
	if got2.Status != "closed" {
		t.Errorf("status after close: got %q, want %q", got2.Status, "closed")
	}
	if !got2.ClosedAt.Valid || got2.ClosedAt.Int64 != 200 {
		t.Errorf("closed_at: %+v", got2.ClosedAt)
	}
}

func TestObjective_ListByStatus(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if _, err := s.InsertObjective(ctx, Objective{Title: "open1", Status: "open", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertObjective(ctx, Objective{Title: "closed1", Status: "closed", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListObjectives(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v %d", err, len(all))
	}
	open, err := s.ListObjectives(ctx, "open")
	if err != nil || len(open) != 1 || open[0].Title != "open1" {
		t.Fatalf("list open: %v %+v", err, open)
	}
}

func TestObjective_GetMissingReturnsNil(t *testing.T) {
	s := openMem(t)
	o, err := s.GetObjective(context.Background(), 999)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o != nil {
		t.Errorf("got %+v, want nil", o)
	}
}
