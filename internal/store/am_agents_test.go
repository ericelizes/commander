package store

import (
	"context"
	"testing"
)

func TestAmAgents_UpsertAndGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	key := "/tmp/example/code/commander"

	// missing -> ("", nil)
	got, err := s.GetAmAgent(ctx, key, "manager-frontend")
	if err != nil {
		t.Fatalf("GetAmAgent (missing): %v", err)
	}
	if got != "" {
		t.Errorf("missing mapping = %q, want empty", got)
	}

	if err := s.UpsertAmAgent(ctx, key, "manager-frontend", "GoldTiger"); err != nil {
		t.Fatalf("UpsertAmAgent: %v", err)
	}
	got, err = s.GetAmAgent(ctx, key, "manager-frontend")
	if err != nil {
		t.Fatalf("GetAmAgent: %v", err)
	}
	if got != "GoldTiger" {
		t.Errorf("mapping = %q, want GoldTiger", got)
	}

	// idempotent update on the same (key, role)
	if err := s.UpsertAmAgent(ctx, key, "manager-frontend", "BlueLake"); err != nil {
		t.Fatalf("UpsertAmAgent (update): %v", err)
	}
	got, _ = s.GetAmAgent(ctx, key, "manager-frontend")
	if got != "BlueLake" {
		t.Errorf("updated mapping = %q, want BlueLake", got)
	}

	// distinct roles coexist
	if err := s.UpsertAmAgent(ctx, key, "lead-frontend", "BlueLake"); err != nil {
		t.Fatalf("UpsertAmAgent (alias): %v", err)
	}
	if got, _ := s.GetAmAgent(ctx, key, "lead-frontend"); got != "BlueLake" {
		t.Errorf("alias mapping = %q, want BlueLake", got)
	}
}

func TestAmAgents_Validation(t *testing.T) {
	s := openMem(t)
	if err := s.UpsertAmAgent(context.Background(), "", "role", "name"); err == nil {
		t.Error("expected error for empty project key")
	}
}
