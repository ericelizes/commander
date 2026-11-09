package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func costStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func roleCosts(t *testing.T, st *store.Store, role string) store.RoleCost {
	t.Helper()
	rows, err := st.ChatCostsByRole(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Role == role {
			return r
		}
	}
	return store.RoleCost{}
}

// A turn is counted once when it finishes, and adopting the host after a cmdrd
// restart (which replays the old lines silently) does not count it again.
func TestChatCostRecordedOnceAcrossAdoption(t *testing.T) {
	st := costStore(t)
	h := newHarness(t)
	h.opts.Costs = StoreCosts{Store: st}
	h.e.Close()
	h.e = New(h.opts)
	h.role("manager-c1")
	h.start("manager-c1")
	sub := h.subscribe("manager-c1")
	_ = h.e.Send(context.Background(), "manager-c1", "one")
	waitFor(t, sub, isKind(KindResult))

	got := roleCosts(t, st, "manager-c1")
	if got.TotalUSD < 0.0099 || got.TotalUSD > 0.0101 || got.InputTokens != 3 || got.OutputTokens != 7 {
		t.Fatalf("after turn 1 = %+v", got)
	}

	e2 := h.restart()
	waitUntil(t, "adopted", func() bool { return e2.Running("manager-c1") })
	if again := roleCosts(t, st, "manager-c1"); again != got {
		t.Fatalf("adoption changed the cost: %+v -> %+v", got, again)
	}

	_, sub2, _ := e2.Subscribe("manager-c1", 0)
	defer sub2.Close()
	_ = e2.Send(context.Background(), "manager-c1", "two")
	waitFor(t, sub2, func(e Event) bool { return e.Kind == KindResult && e.Result.TotalCostUSD > 0.015 })
	if end := roleCosts(t, st, "manager-c1"); end.TotalUSD < 0.0199 || end.TotalUSD > 0.0201 {
		t.Fatalf("after turn 2 = %+v", end)
	}
}

// Backfill reads the logs once and is safe to repeat, also over turns the
// engine already recorded live.
func TestBackfillCostsFromLogsIsIdempotent(t *testing.T) {
	st := costStore(t)
	home := t.TempDir()
	dir := filepath.Join(home, "chat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"seq":1,"session_id":"s1","kind":"user","time":"2026-10-01T10:00:00Z","source":"live","text":"turn_result"}
{"seq":2,"session_id":"s1","kind":"turn_result","time":"2026-10-01T10:01:00Z","source":"live","result":{"cost_delta_usd":0.5,"total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30}}}
{"seq":3,"session_id":"s1","kind":"turn_result","time":"2026-10-01T10:02:00Z","source":"live","result":{"cost_delta_usd":0.25,"total_cost_usd":0.75}}
{"seq":4,"session_id":"s1","kind":"turn_result","time":"2026-10-01T10:02:00Z","source":"live","result":{"cost_delta_usd":0,"total_cost_usd":0.75}}
{"seq":5,"session_id":"s0","kind":"turn_result","time":"2026-10-01T09:00:00Z","source":"transcript","result":{"cost_delta_usd":9,"total_cost_usd":9}}
`
	if err := os.WriteFile(filepath.Join(dir, "commander.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(Options{Home: home, Costs: StoreCosts{Store: st}})
	for i := 0; i < 2; i++ {
		if _, err := e.BackfillCosts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := roleCosts(t, st, "commander")
	if got.TotalUSD != 0.75 || got.InputTokens != 10 || got.OutputTokens != 20 || got.SessionUSD != 0.75 {
		t.Fatalf("backfilled = %+v", got)
	}
}
