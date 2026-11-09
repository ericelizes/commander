package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestCostEvent_InsertGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	id, err := s.InsertCostEvent(ctx, CostEvent{
		ProjectID:   pID,
		TaskBeadID:  sql.NullString{String: "bd-1", Valid: true},
		AgentName:   "operator-foo",
		EventType:   "spawn",
		Reason:      sql.NullString{String: "manual", Valid: true},
		DeltaUSD:    0.05,
		DeltaTokens: 500,
		OccurredAt:  1700000000,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetCostEvent(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
	if got.EventType != "spawn" || got.DeltaUSD != 0.05 || got.DeltaTokens != 500 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestCostEvent_ListByProject_NewestFirst(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	for i, ts := range []int64{100, 300, 200} {
		if _, err := s.InsertCostEvent(ctx, CostEvent{
			ProjectID: pID, AgentName: "a", EventType: "tick",
			DeltaUSD: float64(i), DeltaTokens: int64(i), OccurredAt: ts,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	list, err := s.ListCostEventsByProject(ctx, pID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3, got %d", len(list))
	}
	// occurred_at DESC: 300, 200, 100
	if list[0].OccurredAt != 300 || list[1].OccurredAt != 200 || list[2].OccurredAt != 100 {
		t.Errorf("ordering: %+v", list)
	}
}

func TestCostEvent_ListLimit(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	for i := 0; i < 5; i++ {
		if _, err := s.InsertCostEvent(ctx, CostEvent{
			ProjectID: pID, AgentName: "a", EventType: "tick", OccurredAt: int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListCostEventsByProject(ctx, pID, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("limit: got %d, want 2", len(list))
	}
}

func TestCostEvent_SumUSDByWindow(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)

	// "now" = 10000; events at 9000 (in 1h window? no — window is 1h = 3600s,
	// so threshold is 10000-3600 = 6400; both 9000 and 7000 are in window).
	for _, e := range []CostEvent{
		{ProjectID: pID, AgentName: "a", EventType: "tick", DeltaUSD: 1.0, OccurredAt: 9000},
		{ProjectID: pID, AgentName: "a", EventType: "tick", DeltaUSD: 0.5, OccurredAt: 7000},
		{ProjectID: pID, AgentName: "a", EventType: "tick", DeltaUSD: 2.0, OccurredAt: 100}, // out of window
	} {
		if _, err := s.InsertCostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := s.SumUSDByWindow(ctx, 10000, 1)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if sum < 1.499 || sum > 1.501 {
		t.Errorf("sum: got %f, want 1.5", sum)
	}
}

func TestCostEvent_SumUSDByWindow_RejectsZeroHours(t *testing.T) {
	s := openMem(t)
	_, err := s.SumUSDByWindow(context.Background(), 0, 0)
	if err == nil {
		t.Error("expected error for hours=0")
	}
}

func TestCostEvent_GetMissing(t *testing.T) {
	s := openMem(t)
	got, err := s.GetCostEvent(context.Background(), 999)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestSumUSDByHour_BucketsOldestFirstCurrentHourLast(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "p", Type: "code", Path: "/p", ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_000_000)
	for _, ev := range []struct {
		ago int64
		usd float64
	}{{10, 1.0}, {20, 0.5}, {3600 + 5, 2.0}, {5 * 3600, 4.0}, {30 * 3600, 99}} {
		if _, err := s.InsertCostEvent(ctx, CostEvent{ProjectID: pid, AgentName: "a", EventType: "tick", DeltaUSD: ev.usd, OccurredAt: now - ev.ago}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SumUSDByHour(ctx, now, 24)
	if err != nil || len(got) != 24 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	if got[23] != 1.5 || got[22] != 2.0 || got[18] != 4.0 {
		t.Fatalf("buckets wrong: last=%v prev=%v fifth=%v", got[23], got[22], got[18])
	}
	var sum float64
	for _, v := range got {
		sum += v
	}
	if sum != 7.5 {
		t.Fatalf("events older than the window must be ignored, sum = %v", sum)
	}
}

func TestCostEvent_ListSince_FiltersByTypeAndTime(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	pID, _ := seedProjectAndObjective(t, s)
	for _, e := range []CostEvent{
		{EventType: EventOperatorExited, AgentName: "a", OccurredAt: 100},
		{EventType: EventOperatorDied, AgentName: "b", OccurredAt: 300},
		{EventType: EventOperatorExited, AgentName: "c", OccurredAt: 200},
		{EventType: "tick", AgentName: "d", OccurredAt: 400},
	} {
		e.ProjectID = pID
		if _, err := s.InsertCostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListCostEventsSince(ctx, 150, EventOperatorExited, EventOperatorDied)
	if err != nil || len(got) != 2 || got[0].AgentName != "c" || got[1].AgentName != "b" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}
