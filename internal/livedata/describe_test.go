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
