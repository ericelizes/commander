package managerguard

import "testing"

func TestClassifyBD(t *testing.T) {
	// Beads writes are the Manager's own job from the project root; from an
	// Operator worktree every bd write is denied, reads pass (as for br).
	cwd := "/repo/.worktrees/op-x"
	for _, c := range []string{
		"bd show x-1", "bd list --json", "bd comments x-1", "bd dep list x-1",
		"bd --db /x/.beads/beads.db show x-1", "bd -C /repo list --json | head",
	} {
		if got := Classify(c, cwd); got.Deny {
			t.Errorf("%q: bd read denied: %+v", c, got)
		}
	}
	for _, c := range []string{
		"bd update x-1 --title t", "bd close x-1", "bd create t", "bd comments add x-1 hi",
		"bd -C /repo comments add x-1 hi", "bd --db /x/.beads/beads.db comments add x-1 hi",
		"bd label add x-1 a", "bd dep add a b", "bd sync", "bd set-state x-1 a=b",
	} {
		if got := Classify(c, cwd); !got.Deny {
			t.Errorf("%q: bd write allowed", c)
		}
	}
}
