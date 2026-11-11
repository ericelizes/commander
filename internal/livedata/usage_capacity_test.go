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
			if l.kind != "stalled" || l.text != cause || l.since != 0 || strings.Contains(l.text, "Commander to") {
				t.Fatalf("affected %s hid cause: %+v", w.BeadID, l)
			}
		}
	}
	w := s.Workstreams[1]
	w.Hold = true
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); l.text != "Paused by recorded direction" {
		t.Fatal(l)
	}
	w.Hold = false
	w.QuestionWaiting = true
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); !strings.Contains(l.text, "decision") {
		t.Fatal(l)
	}
	w.QuestionWaiting = false
	w.ExecutionError = ""
	w.ExecutionReady = false
	w.PlanningPending = true
	if l := (board{s: s, now: 1}).activeWorkstreamLine(w, 0); l.text != "Commander to plan the next action" {
		t.Fatal("unaffected work got exhaustion", l)
	}
}

func TestPinnedPlannerUsageAppearsOnEveryDependentRow(t *testing.T) {
	s := activeBoardFixture()
	s.PlanningUsage = map[string]string{"commander-focused": "Claude usage exhausted; planning runtime is explicitly pinned to Claude"}
	for _, w := range []SnapWorkstream{s.Workstreams[0], {BeadID: "other", Status: "open", PlanningCommander: "commander-focused", PlanningPending: true}} {
		if l := (board{s: s, now: 86400}).activeWorkstreamLine(w, 0); !strings.Contains(l.text, "usage exhausted") || l.since != 0 || l.kind != "stalled" {
			t.Fatal(l)
		}
	}
	w := s.Workstreams[0]
	w.PlanningPending = false
	w.Steps = nil
	w.ExecutionMode = "active"
	w.ExecutionReady = true
	w.ExecutionEligible = true
	w.ExecutionCapable = true
	if l := (board{s: s, now: 86400}).activeWorkstreamLine(w, 0); l.text != "Queued for available execution capacity" {
		t.Fatal("planner usage blocked independently authorized work", l)
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
