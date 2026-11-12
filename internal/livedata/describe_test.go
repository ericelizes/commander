package livedata

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/nextactor"
	"github.com/example-user/commander/internal/store"
)

func TestDescriptionsReplaceTheDeterministicLineOnlyWhenReady(t *testing.T) {
	s := activeBoardFixture()
	s.Agents["commander-focused"] = SnapAgent{State: store.AgentWorking, Detail: "reviewing results"}
	s.Workstreams[0].Steps[0].PlanningPending = false
	s.Workstreams[0].Steps[0].Activity = &StatusEvidence{Kind: "working", Text: "running the parity suite"}
	s.Executions = []SnapExecution{{ProjectID: 1, BeadID: "leaf", Role: "exec-a", State: store.AgentWorking}}
	d := Board(s, 100)
	fallback := d.Workstreams[0].Text
	var got struct{ key, actor, reason, opener, facts string }
	ready := false
	describe := func(key, actor, reason, opener, facts string) (string, string, bool) {
		got.key, got.actor, got.reason, got.opener, got.facts = key, actor, reason, opener, facts
		if !ready {
			return "", "", false
		}
		return opener + " two agents run the parity suite", "", true
	}
	applyDescriptions(describe, &s, &d)
	if d.Workstreams[0].Text != placeholder(fallback) {
		t.Fatal("the deterministic line was replaced before a description was ready")
	}
	if got.actor != "agent" || got.opener != "Working:" || !strings.Contains(got.facts, "running the parity suite") || strings.Contains(got.facts, "minute") {
		t.Fatalf("describer inputs: %+v", got)
	}
	ready = true
	applyDescriptions(describe, &s, &d)
	if d.Workstreams[0].Text != "Working: two agents run the parity suite" {
		t.Fatalf("description not applied: %q", d.Workstreams[0].Text)
	}
}

func TestPausedChainGivesTheDescriberItsHoldReason(t *testing.T) {
	row := nextactor.Row{Lead: nextactor.Result{Actor: nextactor.None, Reason: "blocked:paused", Text: "After “Send the note”: paused: wait for the user to greenlight 6.2", Via: []string{"Send the note"}}}
	facts := rowFacts(Snapshot{}, "Claims", "", nil, row)
	if !strings.Contains(facts, "wait for the user to greenlight 6.2") {
		t.Fatalf("hold reason missing from facts:\n%s", facts)
	}
	stuck := nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Stuck, Reason: "no_progress", Text: "Stuck: no progress for 25 minutes"}}
	if strings.Contains(rowFacts(Snapshot{}, "W", "", nil, stuck), "25 minutes") {
		t.Fatal("a ticking duration reached the describer's facts")
	}
}

func TestEveryTrackGetsItsOwnLine(t *testing.T) {
	lead := nextactor.Result{Actor: nextactor.User, Reason: "decision", Bead: "gate"}
	s := activeBoardFixture()
	d := Display{Workstreams: []BoardRow{{Key: "p:root", N: 1, Actor: &nextactor.Row{Lead: lead}, Tracks: []BoardTrack{
		{Key: "p:t1", BeadID: "t1", Actor: &nextactor.Row{Lead: lead}},
	}}}}
	describe := func(key, actor, reason, opener, facts string) (string, string, bool) {
		return opener + " written for " + key, "", true
	}
	applyDescriptions(describe, &s, &d)
	if got := d.Workstreams[0].Tracks[0].Text; got != "Needs you: written for p:t1" {
		t.Fatalf("a track on its row's decision lost its line: %q", got)
	}
}

func TestFactsSayABlockedLineWaitsInsteadOfWorking(t *testing.T) {
	row := nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Agent, Reason: "blocked:executing", Bead: "run", Via: []string{"Merge and install the batch"}}}
	facts := rowFacts(activeBoardFixture(), "Commander", "Agents on the Mac", nil, row)
	if !strings.Contains(facts, `Nothing runs for this line itself: it waits for "Merge and install the batch" to finish.`) {
		t.Fatalf("facts:\n%s", facts)
	}
	row.Lead.Actor, row.Lead.Reason = nextactor.User, "blocked:decision"
	if strings.Contains(rowFacts(activeBoardFixture(), "Commander", "", nil, row), "Nothing runs") {
		t.Fatal("a question for the user is still asked directly")
	}
}

func TestPlaceholderForABlockedLineSaysItWaits(t *testing.T) {
	row := nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Agent, Reason: "blocked:executing", Via: []string{"Merge and install the batch"}}}
	notReady := func(key, actor, reason, opener, facts string) (string, string, bool) { return "", "", false }
	if got := describeRow(notReady, "p:t", row, "facts", "After “Merge and install the batch”: working on “Merge and install the batch”"); got != "Waiting for: Merge and install the batch" {
		t.Fatalf("%q", got)
	}
}

func TestPlaceholderTrimsLongTitles(t *testing.T) {
	row := nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Agent, Reason: "blocked:executing", Via: []string{"Rebuild the QA Hub reporting-fix release on current prod and count errors"}}}
	notReady := func(key, actor, reason, opener, facts string) (string, string, bool) { return "", "", false }
	if got := describeRow(notReady, "p:t", row, "facts", ""); got != "Waiting for: Rebuild the QA Hub reporting-fix release on current prod and count errors" {
		t.Fatalf("%q", got)
	}
	if got := placeholder("Working on “Verify every Nov 9 wrong-state case is impossible on the installed Commander”"); got != "Agent: Verify every Nov 9 wrong-state case is impossible on the installed Commander" {
		t.Fatalf("%q", got)
	}
}

func TestFinishedWorkstreamIsCompleteNotDescribed(t *testing.T) {
	steps := []SnapStep{{BeadID: "w.1", Status: "closed"}, {BeadID: "w.2", Status: "open", ClosingGate: true}}
	if got := readyClosingGate(steps); got != "w.2" {
		t.Fatalf("ready gate %q", got)
	}
	if got := readyClosingGate([]SnapStep{{BeadID: "w.2", Status: "open", ClosingGate: true}}); got != "" {
		t.Fatal("a workstream with nothing done yet is not complete")
	}
	if got := readyClosingGate(append(steps, SnapStep{BeadID: "w.3", Status: "open"})); got != "" {
		t.Fatal("open work: not complete")
	}
	called := false
	describe := func(key, actor, reason, opener, facts string) (string, string, bool) {
		called = true
		return "x", "", true
	}
	row := nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Done, Reason: "ready_to_close", Text: "Complete"}}
	if got := describeRow(describe, "p:w", row, "facts", "Complete"); got != "Complete" || called {
		t.Fatalf("Complete was described: %q", got)
	}
}

func TestPlaceholderHasNoLabel(t *testing.T) {
	if got := placeholder("Needs you: merge it? (blocking “Middle step”)"); got != "Merge it?" {
		t.Fatalf("%q", got)
	}
	if got := placeholder("Needs you"); got != "Needs you" {
		t.Fatalf("a bare label stays: %q", got)
	}
}

func TestPlaceholderDropsTheBlockingChain(t *testing.T) {
	cases := map[string]string{
		"After “Merge the batch”: working on “Prepare the Mac install”":     "Working on “Prepare the Mac install”",
		"Needs you: approve it (blocking “Middle step”)":                    "Needs you: approve it",
		"Needs you: approve it (blocking “Middle step”) · 2 agents working": "Needs you: approve it · 2 agents working",
		"Working on “Build it”":                                             "Working on “Build it”",
	}
	for in, want := range cases {
		if got := withoutChain(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestOpenerIsFixedByState(t *testing.T) {
	cases := map[string]nextactor.Result{
		"Needs you:":        {Actor: nextactor.User},
		"Stuck:":            {Actor: nextactor.Stuck},
		"Working:":          {Actor: nextactor.Agent},
		"Commander:":        {Actor: nextactor.Commander},
		"Waiting on CI #4:": {Actor: nextactor.External, Who: "CI #4"},
		"Paused:":           {Actor: nextactor.None, Reason: "paused"},
		"Withdrawn:":        {Actor: nextactor.None, Reason: "withdrawn"},
	}
	for want, r := range cases {
		if got := Opener(r); got != want {
			t.Errorf("%+v: %q, want %q", r, got, want)
		}
	}
}

func TestALineWaitingOnAnotherStepIsYellowAndSaysWaiting(t *testing.T) {
	lead := nextactor.Result{Actor: nextactor.Agent, Reason: "blocked:executing", Bead: "pr", Via: []string{"PR 3622: readable Notify messages"}}
	if got := actorLine(nextactor.Row{Lead: lead}); got.kind != "waiting" {
		t.Fatalf("kind %q", got.kind)
	}
	if got := actorLine(nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Agent, Reason: "executing"}}); got.kind != "working" {
		t.Fatalf("an agent on the step itself is working: %q", got.kind)
	}
	var asked string
	describe := func(key, actor, reason, opener, facts string) (string, string, bool) {
		asked = actor
		return "", "", false
	}
	describeRow(describe, "p:w", nextactor.Row{Lead: lead}, "facts", "")
	if asked != "waiting_on_step" {
		t.Fatalf("describer asked as %q", asked)
	}
}

func TestParentRowSaysWhatItCovers(t *testing.T) {
	lead := nextactor.Result{Actor: nextactor.Agent, Reason: "executing", Bead: "x"}
	s := activeBoardFixture()
	d := Display{Workstreams: []BoardRow{{Key: "p:root", N: 1, Actor: &nextactor.Row{Lead: lead}, Tracks: []BoardTrack{
		{Key: "p:t1", BeadID: "t1", Title: "Staging", Actor: &nextactor.Row{Lead: lead}, Children: []BoardTrack{{Key: "p:t1a", BeadID: "t1a", Title: "Staging checks", Actor: &nextactor.Row{Lead: lead}}}},
		{Key: "p:t2", BeadID: "t2", Title: "Prod", Actor: &nextactor.Row{Lead: lead}},
	}}}}
	var about string
	describe := func(key, actor, reason, opener, facts string) (string, string, bool) {
		if actor == "about" {
			about = facts
			return "Notify rollout: staging and prod", "", true
		}
		return "Agent working", "", true
	}
	applyDescriptions(describe, &s, &d)
	r := d.Workstreams[0]
	if r.Kind != "about" || r.Text != "Notify rollout: staging and prod" || !strings.Contains(about, "Staging; Prod") {
		t.Fatalf("parent row %q %q (facts %q)", r.Kind, r.Text, about)
	}
	if r.Tracks[0].Kind == "about" || r.Tracks[0].Text != "Agent working" {
		t.Fatalf("a nested row keeps its state, even with rows under it: %+v", r.Tracks[0])
	}
}
