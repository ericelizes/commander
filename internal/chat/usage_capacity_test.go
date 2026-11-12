package chat

import (
	"context"
	"testing"
	"time"
)

func TestUnknownUsageResetRemainsUnknownAndAvailableAccountsClearCause(t *testing.T) {
	h := accountHarness(t, "work")
	h.role("operator-unknown-limit")
	h.start("operator-unknown-limit")
	c, _ := h.e.chat("operator-unknown-limit")
	c.mu.Lock()
	p := c.proc
	c.turnLimit = &UsageLimit{Text: "usage limit reached", fromText: true}
	c.mu.Unlock()
	h.e.settleLimit(c, p, Event{Kind: KindResult, Result: &Result{IsError: true}})
	u, ok := h.e.Usage().Get(UsageKey(RuntimeClaude, "default"))
	if !ok || u.OutUntil != 0 || !u.Out(time.Now().Add(2*time.Hour)) {
		t.Fatalf("invented reset: %+v", u)
	}
	now := time.Now()
	h.e.Usage().MarkOut(RuntimeClaude, "work", "fixture", 0, now)
	h.e.Usage().MarkOut(RuntimeCodex, "", "fixture", 0, now)
	if cause := UsageCapacityCause(h.home, now); cause == "" {
		t.Fatal("shared exhaustion hidden")
	}
	h.e.Usage().MarkOut(RuntimeClaude, "work", "fixture", now.Add(-time.Second).Unix(), now)
	if cause := UsageCapacityCause(h.home, now); cause != "" {
		t.Fatal("available account still reported exhausted", cause)
	}
	_ = h.e.Stop(context.Background(), "operator-unknown-limit")
}

func TestPinnedPlanningUsageDoesNotBorrowAnotherRuntime(t *testing.T) {
	h := accountHarness(t, "work")
	now := time.Now()
	if err := WriteRuntime(h.home, "commander", RuntimeClaude); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"default", "work"} {
		h.e.Usage().MarkOut(RuntimeClaude, name, "fixture", 0, now)
	}
	if cause := UsageCapacityCause(h.home, now); cause != "" {
		t.Fatal("available execution Codex hidden", cause)
	}
	if cause := PlanningUsageCause(h.home, "commander", now); cause == "" {
		t.Fatal("pinned planner exhaustion hidden by Codex capacity")
	}
}

func TestDelayedUsageReceiptCannotOverwriteNewerCapacityEvidence(t *testing.T) {
	now := time.Now()
	s := OpenUsage(t.TempDir())
	s.MarkOut(RuntimeClaude, "default", "newer", now.Add(-time.Second).Unix(), now)
	s.MarkOut(RuntimeClaude, "default", "delayed-exit", 0, now.Add(-time.Minute))
	u, _ := s.Get(UsageKey(RuntimeClaude, "default"))
	if u.Out(now) || u.Role != "newer" {
		t.Fatal("delayed receipt poisoned available capacity", u)
	}
}
