package livedata

import (
	"slices"

	"github.com/example-user/commander/internal/beads"
)

// StepPatch is what a write left on one step (the write door's Plan.After): its status and
// labels as the door's simulation read them once the write landed.
type StepPatch struct {
	Status string
	Labels []string
}

// Patched is s with each patch laid on its step (status, state, owner, waits), Broken derived
// again and the board rebuilt from the result (invariant I8: a board is never older than a write
// it answered). A row whose line did not change keeps its age; a changed one reads from now. The
// patched ids go to Pending until a load shows them. finish (nil: none) is the
// full build's finishing pass, so the rebuilt board reads like any other. Pure:
// s is not changed.
func Patched(s Snapshot, patches map[string]StepPatch, now int64, finish func(*Snapshot, *Display)) Snapshot {
	if len(patches) == 0 {
		return s
	}
	out := s
	out.Workstreams = make([]SnapWorkstream, len(s.Workstreams))
	hit := map[string]bool{}
	for i, ws := range s.Workstreams {
		ws.Steps = patchSteps(ws.Steps, patches, hit)
		out.Workstreams[i] = ws
	}
	if len(hit) == 0 {
		return s
	}
	for i := range out.Workstreams {
		clearBroken(out.Workstreams[i].Steps)
	}
	out.Broken = SnapshotViolations(out)
	markBroken(out.Workstreams, out.Broken)
	out.Pending = nil
	for id := range hit {
		out.Pending = append(out.Pending, id)
	}
	slices.Sort(out.Pending)
	if s.Display != nil {
		d := Board(out, now)
		if finish != nil {
			finish(&out, &d)
		}
		keepAges(&d, *s.Display, now)
		out.Display = &d
	}
	return out
}

func patchSteps(steps []SnapStep, patches map[string]StepPatch, hit map[string]bool) []SnapStep {
	if steps == nil {
		return nil
	}
	out := make([]SnapStep, len(steps))
	for i, st := range steps {
		st.Children = patchSteps(st.Children, patches, hit)
		if p, ok := patches[st.BeadID]; ok {
			hit[st.BeadID] = true
			state, owner := beads.StepState(p.Status, p.Labels)
			if owner != st.Owner {
				st.OwnerState, st.OwnerWhy, st.OwnerSince = "", "", 0
			}
			st.Status, st.State, st.Owner = p.Status, state, owner
			waits := beads.WaitsOf(p.Labels)
			st.Waiting, st.WaitSince, st.WaitUntil, st.WaitUntilAs = beads.WaitsText(waits), 0, 0, ""
			stepWaits(&st, waits)
			if p.Status == "closed" {
				st.State = beads.StateDone
			}
		}
		out[i] = st
	}
	return out
}

func clearBroken(steps []SnapStep) {
	for i := range steps {
		steps[i].Broken = false
		clearBroken(steps[i].Children)
	}
}

// keepAges carries each row's age over from the board it replaces when its line is unchanged.
func keepAges(d *Display, old Display, now int64) {
	prev := map[string]BoardRow{}
	for _, r := range old.Workstreams {
		prev[r.Key] = r
	}
	for i := range d.Workstreams {
		r := &d.Workstreams[i]
		if p, ok := prev[r.Key]; ok && p.Kind == r.Kind && p.Text == r.Text {
			r.Since = p.Since
		} else if r.Since == 0 {
			r.Since = now
		}
	}
	if old.Commander.Kind == d.Commander.Kind && old.Commander.Text == d.Commander.Text {
		d.Commander.Since = old.Commander.Since
	}
}
