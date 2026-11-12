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
	if d.Workstreams[0].Text != fallback {
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
