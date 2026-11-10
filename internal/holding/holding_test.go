package holding

import "testing"

type fakeWorld struct {
	beads map[string]Bead
	child map[string]bool
	live  map[string]string // nil = unknown
}

func (f fakeWorld) Bead(id string) (Bead, bool) { b, ok := f.beads[id]; return b, ok }
func (f fakeWorld) HasOpenChild(id string) bool { return f.child[id] }
func (f fakeWorld) OwnerLive(o string) (string, bool) {
	if f.live == nil {
		return "", false
	}
	return f.live[o], true
}

// TestHeldMatrix: every case of the report's section 2 against the one predicate.
func TestHeldMatrix(t *testing.T) {
	open := Bead{ID: "blk", Status: "open"}
	cases := []struct {
		name string
		b    Bead
		w    fakeWorld
		held bool
		kind Kind
		why  Why
	}{
		{"idle manager owner", Bead{ID: "x", Status: "in_progress", Owner: "manager-w"}, fakeWorld{live: map[string]string{"manager-w": "idle"}}, false, "", WhyManagerIdle},
		{"working manager owner", Bead{ID: "x", Status: "in_progress", Owner: "manager-w"}, fakeWorld{live: map[string]string{"manager-w": "working"}}, true, Operator, ""},
		{"owner with no live source", Bead{ID: "x", Status: "in_progress", Owner: "manager-w"}, fakeWorld{}, true, Operator, ""},
		{"dead operator with a wait", Bead{ID: "x", Status: "open", Owner: "operator-a", OwnerState: OwnerDied, Waits: 1}, fakeWorld{}, false, "", WhyOwnerGone},
		{"working operator", Bead{ID: "x", Status: "in_progress", Owner: "operator-a", OwnerState: OwnerWorking}, fakeWorld{}, true, Operator, ""},
		{"typed wait", Bead{ID: "x", Status: "open", Waits: 1}, fakeWorld{}, true, Wait, ""},
		{"untyped slug wait", Bead{ID: "x", Status: "open", Waits: 1, Untyped: "soak"}, fakeWorld{}, false, "", WhyUntypedWait},
		{"in progress behind a blocker", Bead{ID: "x", Status: "in_progress", BlockedBy: []string{"blk"}}, fakeWorld{beads: map[string]Bead{"blk": open}}, false, "", WhyNoHolder},
		{"open behind a blocker", Bead{ID: "x", Status: "open", BlockedBy: []string{"blk"}}, fakeWorld{beads: map[string]Bead{"blk": open}}, true, Blocker, ""},
		{"blocked only through its parent", Bead{ID: "x", Parent: "p", Status: "open"}, fakeWorld{beads: map[string]Bead{"blk": open, "p": {ID: "p", Status: "open", BlockedBy: []string{"blk"}}}}, true, Blocker, ""},
		{"superseded blocker", Bead{ID: "x", Status: "open", BlockedBy: []string{"blk"}}, fakeWorld{beads: map[string]Bead{"blk": {ID: "blk", Status: "open", Superseded: true}}}, false, "", WhyNoHolder},
		{"parked blocker", Bead{ID: "x", Status: "open", BlockedBy: []string{"blk"}}, fakeWorld{beads: map[string]Bead{"blk": {ID: "blk", Status: "deferred"}}}, false, "", WhyNoHolder},
		{"closed blocker", Bead{ID: "x", Status: "open", BlockedBy: []string{"blk"}}, fakeWorld{beads: map[string]Bead{"blk": {ID: "blk", Status: "closed"}}}, false, "", WhyNoHolder},
		{"unknown blocker", Bead{ID: "x", Status: "open", BlockedBy: []string{"nope"}}, fakeWorld{}, false, "", WhyNoHolder},
		{"open child", Bead{ID: "x", Status: "open"}, fakeWorld{child: map[string]bool{"x": true}}, true, Children, ""},
		{"empty scope", Bead{ID: "x", Status: "open"}, fakeWorld{}, false, "", WhyNoHolder},
		{"user card", Bead{ID: "x", Status: "open", NeedsUser: true}, fakeWorld{}, true, User, ""},
		{"gate", Bead{ID: "x", Status: "open", Gate: true}, fakeWorld{}, true, Gate, ""},
		{"parked", Bead{ID: "x", Status: "deferred"}, fakeWorld{}, true, Parked, ""},
		{"closed", Bead{ID: "x", Status: "closed"}, fakeWorld{}, false, "", WhyClosed},
	}
	for _, c := range cases {
		h, held, why := Held(c.b, c.w)
		if held != c.held || h.Kind != c.kind || why != c.why {
			t.Errorf("%s: Held = (%v, %v, %q), want (%v, %v, %q)", c.name, h.Kind, held, why, c.kind, c.held, c.why)
		}
	}
}

// A parent cycle must not hang the blocker walk.
func TestHeldParentCycleTerminates(t *testing.T) {
	w := fakeWorld{beads: map[string]Bead{"a": {ID: "a", Parent: "b"}, "b": {ID: "b", Parent: "a"}}}
	if _, held, _ := Held(Bead{ID: "a", Parent: "b", Status: "open"}, w); held {
		t.Fatal("a cycle with no blocker held a bead")
	}
}

// A dead Operator's step is a failure: a blocker on its parent chain does not excuse it.
func TestHeldDeadOwnerNotExcusedByAncestorBlocker(t *testing.T) {
	w := fakeWorld{beads: map[string]Bead{"blk": {ID: "blk", Status: "open"}, "p": {ID: "p", Status: "open", BlockedBy: []string{"blk"}}}}
	b := Bead{ID: "x", Parent: "p", Status: "open", Owner: "operator-a", OwnerState: OwnerDied}
	if _, held, why := Held(b, w); held || why != WhyOwnerGone {
		t.Fatalf("held=%v why=%q", held, why)
	}
	b.BlockedBy = []string{"blk"}
	if _, held, _ := Held(b, w); !held {
		t.Fatal("its own open blocker still holds it")
	}
}
