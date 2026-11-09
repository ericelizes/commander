package store

import (
	"context"
	"testing"
)

func TestChatCostsIdempotentAndAggregated(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	for _, c := range []ChatCost{
		{Role: "manager-a", SessionID: "s1", TotalUSD: 1, DeltaUSD: 1, OccurredAt: 100},
		{Role: "manager-a", SessionID: "s1", TotalUSD: 3, DeltaUSD: 2, OccurredAt: 5000},
		{Role: "manager-a", SessionID: "s2", TotalUSD: 0.5, DeltaUSD: 0.5, OccurredAt: 6000},
	} {
		if added, err := s.InsertChatCost(ctx, c); err != nil || !added {
			t.Fatalf("insert %+v: %v %v", c, added, err)
		}
	}
	if added, _ := s.InsertChatCost(ctx, ChatCost{Role: "manager-a", SessionID: "s1", TotalUSD: 3, DeltaUSD: 2, OccurredAt: 7000}); added {
		t.Fatal("duplicate turn was added")
	}
	rows, err := s.ChatCostsByRole(ctx, 4000)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if r := rows[0]; r.TotalUSD != 3.5 || r.TodayUSD != 2.5 || r.SessionUSD != 0.5 {
		t.Fatalf("role cost = %+v", r)
	}
	if v, _ := s.SumChatUSDSince(ctx, 4000); v != 2.5 {
		t.Fatalf("since = %v", v)
	}
	hours, _ := s.ChatUSDByHour(ctx, 7000, 2)
	if len(hours) != 2 || hours[1] != 2.5 {
		t.Fatalf("by hour = %v", hours)
	}
}
