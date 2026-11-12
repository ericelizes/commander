package execution

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestAStepCanAskForItsAgentsRuntime(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{Prefix: "p", EdgeParentsOnly: true}
	ws, _ := m.Create(ctx, "", "QA Hub speed", beads.CreateOptions{Labels: []string{"kind:workstream", "runtime:claude"}})
	step, _ := m.Create(ctx, "", "Ship the batch", beads.CreateOptions{Parent: ws})
	other, _ := m.Create(ctx, "", "Elsewhere", beads.CreateOptions{})
	if got := runtimePreference(ctx, m, "", step); got != "claude" {
		t.Fatalf("a step under a runtime:claude workstream: %q", got)
	}
	if got := runtimePreference(ctx, m, "", other); got != "" {
		t.Fatalf("no label: %q", got)
	}
	if got := preferRuntime([]string{"codex", "claude"}, "claude"); len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("order %v", got)
	}
}
