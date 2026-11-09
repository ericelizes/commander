package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestTUIState_DefaultsWhenEmpty(t *testing.T) {
	s := openMem(t)
	got, err := s.GetTUIState(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.CurrentView != "commander" {
		t.Errorf("default state: %+v", got)
	}
	if got.SelectedProjectID.Valid || got.SelectedTaskBeadID.Valid {
		t.Errorf("expected NULL selections, got %+v", got)
	}
}

func TestTUIState_UpsertSingleRow(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	if err := s.SaveTUIState(ctx, TUIState{
		CurrentView:        "lead",
		SelectedProjectID:  sql.NullInt64{Int64: 7, Valid: true},
		SelectedTaskBeadID: sql.NullString{String: "bd-99", Valid: true},
		ScrollState:        sql.NullString{String: `{"col":1}`, Valid: true},
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.SaveTUIState(ctx, TUIState{
		CurrentView:        "operator",
		SelectedProjectID:  sql.NullInt64{Int64: 7, Valid: true},
		SelectedTaskBeadID: sql.NullString{String: "bd-99", Valid: true},
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := s.GetTUIState(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CurrentView != "operator" {
		t.Errorf("current_view after upsert: got %q want %q", got.CurrentView, "operator")
	}

	// Verify there's still exactly one row in the table.
	var n int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM tui_state").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 row, got %d", n)
	}
}

func TestTUIState_CheckConstraintBlocksOtherIDs(t *testing.T) {
	s := openMem(t)
	_, err := s.DB().Exec(`INSERT INTO tui_state(id, current_view) VALUES (2, 'lead')`)
	if err == nil {
		t.Fatal("expected CHECK(id=1) to block id=2 insert")
	}
	if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("expected CHECK violation, got %v", err)
	}
}
