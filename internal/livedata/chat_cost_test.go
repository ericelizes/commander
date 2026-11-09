package livedata

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func TestFleetMetricsIncludeChatSpend(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	_, _ = st.InsertChatCost(ctx, store.ChatCost{Role: "commander", SessionID: "c", TotalUSD: 2, DeltaUSD: 2, OccurredAt: now.Unix() - 60})
	_, _ = st.InsertChatCost(ctx, store.ChatCost{Role: "manager-x", SessionID: "m", TotalUSD: 1, DeltaUSD: 1, OccurredAt: now.Unix() - 2*86400})

	m := fleetMetricsFromStore(ctx, st, nil, 1, now)
	if m.TotalCostUSD24h != 2 || m.TotalCostUSD7d != 3 || m.ChatCostUSD24h != 2 {
		t.Fatalf("metrics = %+v", m)
	}
	if len(m.RoleCosts) != 2 || m.RoleCosts[0].Role != "commander" || m.RoleCosts[0].TodayUSD != 2 || m.RoleCosts[1].TodayUSD != 0 || m.RoleCosts[1].TotalUSD != 1 {
		t.Fatalf("roles = %+v", m.RoleCosts)
	}
	snap := BuildSnapshot(Data{Metrics: m, LoadedAt: now}, nil)
	if snap.Fleet.ChatCostUSD24h != 2 || len(snap.Fleet.Roles) != 2 {
		t.Fatalf("snapshot fleet = %+v", snap.Fleet)
	}
}
