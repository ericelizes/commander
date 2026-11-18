package beads

import "testing"

// 6.1 on Nov 17: "Propose the forge-bench test cases" went straight to Eric's
// "Approve the Temporal test cases" gate; the Commander's review of the
// proposal was hidden. An explicit Commander check between them is the shape.
func TestShapeHiddenReview(t *testing.T) {
	propose := step("W.1.2", "W.1", "Propose the forge-bench test cases for Temporal")
	approve := step("W.1.3", "W.1", "Approve the Temporal test cases", "gate", "approver:user")
	approve.Blockers = []string{"W.1.2"}
	vs := ShapeCheck([]ShapeNode{ws("W"), track("W.1", "W", "Context control: Temporal"), propose, approve})
	if rulesOf(vs)["hidden-review"] != 1 {
		t.Fatalf("want hidden-review: %+v", vs)
	}
	check := step("W.1.8", "W.1", "Commander checks the Temporal test cases", "lane:commander")
	check.Blockers = []string{"W.1.2"}
	approve.Blockers = []string{"W.1.8"}
	if vs := ShapeCheck([]ShapeNode{ws("W"), track("W.1", "W", "Context control: Temporal"), propose, check, approve}); rulesOf(vs)["hidden-review"] != 0 {
		t.Fatalf("explicit check still flagged: %+v", vs)
	}
}

func TestEveryTemplateHasNoHiddenReview(t *testing.T) {
	for _, name := range PlanTemplateNames() {
		for _, ownerGo := range []bool{false, true} {
			steps := planTemplates[name]("X", ownerGo)
			byRef := map[string]planStep{}
			for _, s := range steps {
				byRef[s.ref] = s
			}
			for _, s := range steps {
				if !contains(s.labels, "approver:user") || contains(s.labels, "closing-gate") {
					continue
				}
				for _, a := range s.after {
					if contains(byRef[a].labels, "lane:elsewhere:the team") {
						continue // the team on GitHub, not agent work
					}
					if !contains(byRef[a].labels, "review") || contains(byRef[a].labels, "lane:commander") {
						t.Fatalf("%s (ownerGo=%v): user gate %q follows %q, not an independent review step", name, ownerGo, s.title, byRef[a].title)
					}
				}
			}
		}
	}
}

func contains(labels, l string) bool {
	for _, x := range splitComma(labels) {
		if x == l {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

func TestPRTemplatesHaveNoConfirmDoneAndBothVariants(t *testing.T) {
	for _, name := range PlanTemplateNames() {
		if PlanTemplateTitles[name] == "" {
			t.Errorf("%s has no display name", name)
		}
		for _, s := range planTemplates[name]("X", true) {
			if contains(s.labels, "closing-gate") || s.title == "Confirm done" {
				t.Errorf("%s still has Confirm done", name)
			}
		}
	}
	slow := planTemplates["pr-babysit-slow"]("X", false)
	team := false
	for _, s := range slow {
		if contains(s.labels, "lane:elsewhere:the team") {
			team = true
		}
	}
	if !team || len(planTemplates["pr-babysit-fast"]("X", false)) != 5 {
		t.Fatal("PR variants wrong")
	}
}

func TestTeachAgentsTemplateShape(t *testing.T) {
	steps := planTemplates["teach-agents"]("X", false)
	want := []string{"ux", "bench", "guide", "run", "check", "live", "soak", "check2", "eric"}
	if len(steps) != len(want) {
		t.Fatalf("got %d steps", len(steps))
	}
	for i, s := range steps {
		if s.ref != want[i] {
			t.Errorf("step %d is %s, want %s", i, s.ref, want[i])
		}
	}
	if !contains(steps[8].labels, "approver:user") || !contains(steps[4].labels, "review") || !contains(steps[7].labels, "review") {
		t.Fatal("gate or review labels wrong")
	}
	for _, s := range steps[:8] {
		if contains(s.labels, "approver:user") {
			t.Errorf("%s is a human step; only eric is", s.ref)
		}
	}
	if _, err := PlanTemplateOps("teach-agents", "P", "X", false); err != nil {
		t.Fatal(err)
	}
}
