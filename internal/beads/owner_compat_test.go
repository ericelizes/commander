package beads

import "testing"

func TestLegacyOwnerVocabulary(t *testing.T) {
	state, owner := StepState("open", []string{"needs-user", "owner:user"})
	if state != StateNeedsUser || owner != "user" {
		t.Fatalf("%s %s", state, owner)
	}
	w, err := ParseWait("user")
	if err != nil || w.Kind != WaitUser {
		t.Fatalf("%+v %v", w, err)
	}
	for _, label := range []string{"waiting:1:user", "w:1:er"} {
		w, ok := ParseWaitLabel(label)
		if !ok || w.Kind != WaitUser || w.Label() != "waiting:1:user" {
			t.Fatalf("%s: %+v", label, w)
		}
	}
}
