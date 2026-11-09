package managerguard

import "testing"

func TestWritesWaitLabel(t *testing.T) {
	deny := []string{
		"br label add p-1 waiting:1700000000:until:1700003600:user-spec-review-first",
		"br update p-1 --add-label w:tneb9u:un:tnebi0:repair-launch-check",
		"br update p-1 --add-label=waiting:1:review:reviewer-a",
		"br -C /repo update p-1 --add-label star,waiting:1:user",
		"br create t --parent p -l kind:objective,waiting:1:ci:3",
		"bd label add p-1 w:tniypv:op:op-build",
		"bash -c 'br label add p-1 waiting:1:user'",
		"br show p-1 && br label add p-1 waiting:1:user",
	}
	allow := []string{
		"br label remove p-1 waiting:1:user",
		"br update p-1 --remove-label waiting:1:user",
		"br label add p-1 star",
		"br show p-1",
		"cmdr internal-set-objective --project p --bead p-1 --waiting until:2026-10-27T14:00:00Z:the window opens",
		"br update p-1 --add-label until:merged",
		"echo waiting:1:user",
	}
	for _, c := range deny {
		if WritesWaitLabel(c) == "" {
			t.Errorf("want refused: %q", c)
		}
		if v := Classify(c, ""); !v.Deny {
			t.Errorf("Classify allowed %q", c)
		}
	}
	for _, c := range allow {
		if why := WritesWaitLabel(c); why != "" {
			t.Errorf("want allowed: %q (%s)", c, why)
		}
	}
}
