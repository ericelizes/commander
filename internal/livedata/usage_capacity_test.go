package livedata

import (
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
)

func TestUsageCapacityAppearsOnEveryAffectedRow(t *testing.T) {
	s := activeBoardFixture()
	s.Workstreams[0].Steps = nil
	cause := store.UsageCapacityError + "no connected account has usage left"
	for _, id := range []string{"one", "two"} {
		s.Workstreams = append(s.Workstreams, SnapWorkstream{N: len(s.Workstreams) + 1, ProjectID: 1, Project: "p", BeadID: id, Title: id, Status: "open", ExecutionMode: "active", ExecutionReady: true, ExecutionEligible: true, ExecutionCapable: true, ExecutionError: cause, PlanningCommander: "commander-focused"})
	}
	// The root also contains the affected descendants, as a canonical snapshot does.
	for _, w := range s.Workstreams[1:] {
		s.Workstreams[0].Steps = append(s.Workstreams[0].Steps, SnapStep{BeadID: w.BeadID, Title: w.Title, Status: "open", ExecutionMode: w.ExecutionMode, ExecutionReady: true, ExecutionEligible: true, ExecutionCapable: true, ExecutionError: cause})
	}
	for _, now := range []int64{1, 86400} {
		for _, w := range s.Workstreams {
			l := (board{s: s, now: now}).activeWorkstreamLine(w, 0)
			if l.kind != "stuck" || l.text != cause || l.since != 0 || strings.Contains(l.text, "Commander to") {
				t.Fatalf("affected %s hid cause: %+v", w.BeadID, l)
			}
		}
	}
	w := s.Workstreams[1]
	// A hold is never a quiet pause: it reads as an invalid state.
	w.Hold = true
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); l.kind != "stuck" || !strings.HasPrefix(l.text, "Invalid state:") {
		t.Fatal(l)
	}
	w.Hold = false
	w.QuestionWaiting = true
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); !strings.Contains(l.text, "waits on an answer to its question") {
		t.Fatal(l)
	}
	w.QuestionWaiting = false
	w.ExecutionError = ""
	w.ExecutionReady = false
	w.PlanningPending = true
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); strings.Contains(l.text, "usage exhausted") {
		t.Fatal("unaffected work got exhaustion", l)
	}
}

func TestPinnedPlannerUsageAppearsOnEveryDependentRow(t *testing.T) {
	// The Commander is not on any step's path: its usage limit never holds work.
	s := activeBoardFixture()
	s.PlanningUsage = map[string]string{"commander-focused": "Claude usage exhausted; planning runtime is explicitly pinned to Claude"}
	if l := (board{s: s, now: 86400}).activeWorkstreamLine(s.Workstreams[0], 0); strings.Contains(l.text, "usage exhausted") {
		t.Fatal("the Commander's usage limit held an agent step", l)
	}
}

func TestNestedPlannerUsageUsesItsActualJurisdiction(t *testing.T) {
	s := activeBoardFixture()
	s.PlanningUsage = map[string]string{"commander-focused": "Claude usage exhausted"}
	w := trackWorkstream(s.Workstreams[0], SnapStep{BeadID: "nested", Status: "open", PlanningCommander: "commander-other", PlanningPending: true})
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); strings.Contains(l.text, "usage") {
		t.Fatal("parent planner exhaustion leaked across jurisdiction", l)
	}
}
