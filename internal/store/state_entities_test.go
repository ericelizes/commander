package store

import (
	"slices"
	"testing"
)

func TestPostgresStateEntitiesSinceListsChangedTables(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	var start int64
	if e := s.DB().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM state_events`).Scan(&start); e != nil {
		t.Fatal(e)
	}
	if got, next, e := s.StateEntitiesSince(ctx, start); e != nil || len(got) != 0 || next != start {
		t.Fatalf("nothing changed: %v %d %v", got, next, e)
	}
	if _, e := s.DB().ExecContext(ctx, `UPDATE bead_tasks SET title=? WHERE project_id=? AND id=?`, "Renamed", pid, "leaf"); e != nil {
		t.Fatal(e)
	}
	got, next, e := s.StateEntitiesSince(ctx, start)
	if e != nil || !slices.Contains(got, "bead_tasks") || next <= start {
		t.Fatalf("bead change: %v %d %v", got, next, e)
	}
	if again, same, e := s.StateEntitiesSince(ctx, next); e != nil || len(again) != 0 || same != next {
		t.Fatalf("cursor did not advance past seen events: %v %d %v", again, same, e)
	}
}
