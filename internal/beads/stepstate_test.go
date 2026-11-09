package beads

import "testing"

func TestStepState(t *testing.T) {
	cases := []struct {
		status string
		labels []string
		state  string
		owner  string
	}{
		{"open", nil, StateNotStarted, ""},
		{"in_progress", []string{"kind:objective"}, StateInProgress, ""},
		{"in_progress", []string{"blocked"}, StateBlocked, ""},
		{"open", []string{"blocked", "needs-user"}, StateNeedsUser, ""},
		{"closed", []string{"blocked", "owner:user"}, StateDone, "user"},
		{"open", []string{"owner:operator-x"}, StateNotStarted, "operator-x"},
	}
	for _, c := range cases {
		state, owner := StepState(c.status, c.labels)
		if state != c.state || owner != c.owner {
			t.Errorf("StepState(%q, %v) = %q, %q; want %q, %q", c.status, c.labels, state, owner, c.state, c.owner)
		}
	}
}

func TestVizOf(t *testing.T) {
	cases := map[string]int{"viz:1": 1, "viz:3": 3, "viz:0": 0, "viz:4": 0, "viz:12": 0, "viz:": 0}
	for l, want := range cases {
		if got := VizOf([]string{"kind:task", l}); got != want {
			t.Errorf("VizOf(%q) = %d, want %d", l, got, want)
		}
	}
	if VizOf(nil) != 0 {
		t.Error("no label should be 0")
	}
}
