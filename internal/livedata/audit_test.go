package livedata

import (
	"strings"
	"testing"
)

const nowSec = 1_000_000

func gateStep(id, title string, approvers []string, blockedBy ...string) SnapStep {
	return SnapStep{BeadID: id, Title: title, Status: "open", State: "not-started", Gate: true, Approvers: approvers, BlockedBy: blockedBy}
}

func leaf(id, owner, ownerState, waiting string) SnapStep {
	return SnapStep{BeadID: id, Title: "Task " + id, Status: "open", State: "in-progress", Owner: owner, OwnerState: ownerState, Waiting: waiting}
}

func snapOf(ws SnapWorkstream, agent SnapAgent, needs ...NeedsYou) Snapshot {
	ws.N, ws.Project, ws.ManagerRole = 1, "p", "manager-"+ws.BeadID
	if ws.Title == "" {
		ws.Title = "WS " + ws.BeadID
	}
	return Snapshot{
		Workstreams: []SnapWorkstream{ws},
		Agents:      map[string]SnapAgent{ws.ManagerRole: agent},
		NeedsYou:    needs,
	}
}

func rulesOf(sus []Suspect) []string {
	var out []string
	for _, s := range sus {
		out = append(out, s.Rule+":"+s.Bead)
	}
	return out
}

func hasRule(sus []Suspect, rule, bead string) bool {
	for _, s := range sus {
		if s.Rule == rule && s.Bead == bead {
			return true
		}
	}
	return false
}

var idle = SnapAgent{State: "idle", Since: nowSec - 600}

func TestRowsGateLine(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{
		leaf("w.1", "operator-a", "working", ""),
		gateStep("w.2", "PR approval", []string{"user"}, "w.1"),
	}}
	rows := Rows(snapOf(ws, idle), nowSec)
	r := rows[0]
	if r.Line.Text != "Working on task w.1 · then: PR approval" {
		t.Fatalf("line %q", r.Line.Text)
	}
	if r.Colour != "green" || r.Agents != 2 || r.NextGate != "PR approval" {
		t.Fatalf("row %+v", r)
	}
}

func TestRuleStalledWithLiveAgentOrWait(t *testing.T) {
	// t04.3 shape: a wait is noted, the row still reads Stalled.
	ws := SnapWorkstream{BeadID: "t04", Steps: []SnapStep{
		{BeadID: "t04.3", Title: "Pilot", Status: "open", State: "in-progress", Note: "waiting on the vendor to reply"},
		gateStep("t04.9", "Merge approval", []string{"user"}, "t04.3"),
	}}
	sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{})
	if !hasRule(sus, RuleStalledButLive, "t04") {
		t.Fatalf("want stalled-with-wait, got %v", rulesOf(sus))
	}
	if sus[0].ManagerRole != "manager-t04" {
		t.Fatalf("manager %q", sus[0].ManagerRole)
	}
	// A truly stalled row is not flagged.
	ws.Steps[0].Note = ""
	if sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{}); hasRule(sus, RuleStalledButLive, "t04") {
		t.Fatalf("false positive %v", rulesOf(sus))
	}
}

func TestRuleNextGateParkedTrackA(t *testing.T) {
	// #5 shape: track A is parked (deferred); the row's next gate is B's, never the parked one.
	parked := gateStep("t02.6.1", "Workflow approved", []string{"user"})
	parked.Status = "deferred"
	b := gateStep("t02.7.9", "Workflow approved B", []string{"user"})
	ws := SnapWorkstream{BeadID: "t02", Steps: []SnapStep{parked, b, leaf("t02.7.1", "operator-b", "working", "")}}
	if g := Rows(snapOf(ws, idle), nowSec)[0].NextGateID; g != "t02.7.9" {
		t.Fatalf("next gate %q", g)
	}
	if sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{}); hasRule(sus, RuleNextGate, "t02.6.1") {
		t.Fatalf("parked next gate %v", rulesOf(sus))
	}
}

func TestRuleNextGateClosedAndNotEarliest(t *testing.T) {
	first := gateStep("w.g1", "Spec approval", []string{"user"})
	second := gateStep("w.g2", "Merge approval", []string{"user"}, "w.g1")
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{first, second}}
	row := Rows(snapOf(ws, idle), nowSec)[0]
	if row.NextGateID != "w.g1" {
		t.Fatalf("the mirror should pick the earliest, got %s", row.NextGateID)
	}
	// Force the not-earliest shape: the helper sees w.g1 blocking w.g2.
	byBead := map[string]SnapStep{"w.g1": first, "w.g2": second}
	if e := earlierOpenGate(ws, "w.g2", byBead); e != "w.g1" {
		t.Fatalf("earlier = %q", e)
	}
	if e := earlierOpenGate(ws, "w.g1", byBead); e != "" {
		t.Fatalf("earliest flagged: %q", e)
	}
	first.Status = "closed"
	byBead["w.g1"] = first
	if e := earlierOpenGate(ws, "w.g2", byBead); e != "" {
		t.Fatalf("closed gate is not earlier: %q", e)
	}
}

func TestRuleFlagIffRed(t *testing.T) {
	// A flag on a green row.
	ws := SnapWorkstream{BeadID: "w", Title: "WS", Steps: []SnapStep{leaf("w.1", "operator-a", "working", "")}}
	s := snapOf(ws, idle, NeedsYou{Project: "p", BeadID: "w.g", Title: "Approve", Workstream: "WS"})
	if sus := AuditSnapshot(s, nowSec, AuditOptions{}); !hasRule(sus, RuleFlag, "w") {
		t.Fatalf("flag on green: %v", rulesOf(sus))
	}
	// Red without a flag: a step waits on "you" with no NEEDS YOU entry.
	ws2 := SnapWorkstream{BeadID: "x", Title: "X", Steps: []SnapStep{leaf("x.1", "", "", "you")}}
	if sus := AuditSnapshot(snapOf(ws2, idle), nowSec, AuditOptions{}); !hasRule(sus, RuleFlag, "x") {
		t.Fatalf("red no flag: %v", rulesOf(sus))
	}
}

func TestRuleNeedsYouNotUserGate(t *testing.T) {
	plain := SnapStep{BeadID: "w.1", Title: "Decide X", Status: "open", State: "needs-user"}
	rev := gateStep("w.2", "Review", []string{"reviewer:reviewer-a"})
	ws := SnapWorkstream{BeadID: "w", Title: "WS", Steps: []SnapStep{plain, rev}}
	s := snapOf(ws, idle,
		NeedsYou{Project: "p", BeadID: "w.1", Workstream: "WS", Title: "Decide X"},
		NeedsYou{Project: "p", BeadID: "w.2", Workstream: "WS", Title: "Review"},
		NeedsYou{Project: "p", BeadID: "w", Workstream: "WS", Title: "WS"})
	sus := AuditSnapshot(s, nowSec, AuditOptions{})
	for _, id := range []string{"w.1", "w.2", "w"} {
		if !hasRule(sus, RuleNeedsYou, id) {
			t.Fatalf("missing %s in %v", id, rulesOf(sus))
		}
	}
	ok := gateStep("w.3", "Merge approval", []string{"user"})
	ws.Steps = []SnapStep{ok}
	s = snapOf(ws, idle, NeedsYou{Project: "p", BeadID: "w.3", Workstream: "WS", Title: "Merge approval"})
	for _, x := range AuditSnapshot(s, nowSec, AuditOptions{}) {
		if x.Rule == RuleNeedsYou {
			t.Fatalf("false positive %+v", x)
		}
	}
}

func TestRuleStaleReviewerGate(t *testing.T) {
	g := gateStep("w.g", "Reviewed", []string{"reviewer:reviewer-a"})
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{g}, GateNodes: []SnapGateNode{
		{Kind: "gate", BeadID: "w.g", Name: "reviewed", Verifier: "reviewer:reviewer-a", Status: "open"},
	}}
	if sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{}); hasRule(sus, RuleStaleReviewer, "w.g") {
		t.Fatalf("no approval yet: %v", rulesOf(sus))
	}
	opt := AuditOptions{Approved: func(bead, login string) bool { return bead == "w.g" && login == "reviewer-a" }}
	if sus := AuditSnapshot(snapOf(ws, idle), nowSec, opt); !hasRule(sus, RuleStaleReviewer, "w.g") {
		t.Fatalf("hook: %v", rulesOf(sus))
	}
	ws.GateNodes[0].Evidence = "approved by reviewer-a at 5"
	if sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{}); !hasRule(sus, RuleStaleReviewer, "w.g") {
		t.Fatalf("evidence: %v", rulesOf(sus))
	}
}

func TestRuleOpenBeadNoOwner(t *testing.T) {
	orphan := SnapStep{BeadID: "w.1", Title: "Orphan", Status: "open", State: "not-started"}
	parked := SnapStep{BeadID: "w.2", Title: "Parked", Status: "deferred"}
	waits := leaf("w.3", "", "", "CI #5")
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{orphan, parked, waits}}
	sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{})
	if !hasRule(sus, RuleOpenBead, "w.1") || hasRule(sus, RuleOpenBead, "w.2") || hasRule(sus, RuleOpenBead, "w.3") {
		t.Fatalf("got %v", rulesOf(sus))
	}
}

func TestRuleRollupAsState(t *testing.T) {
	st := leaf("w.1", "operator-a", "working", "")
	st.State = "done" // derived from children while its own bead is open
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{st}}
	if sus := AuditSnapshot(snapOf(ws, idle), nowSec, AuditOptions{}); !hasRule(sus, RuleRollupAsState, "w.1") {
		t.Fatalf("got %v", rulesOf(sus))
	}
}

func TestCheckinReportShape(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Title: "Alpha", Steps: []SnapStep{
		leaf("w.1", "operator-a", "working", ""),
		gateStep("w.2", "PR approval", []string{"user"}, "w.1"),
	}}
	out := CheckinReport(snapOf(ws, idle), nowSec, AuditOptions{})
	for _, want := range []string{"1 | Alpha | [green] Working on task w.1", "| PR approval | 2 | -", "NEEDS YOU (0)", "Sanity audit: 0 suspects"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestAudit_NeedsYouLane(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{{BeadID: "w.1", Title: "Approve", Status: "open", State: "needs-user"}}}
	card := NeedsYou{BeadID: "w.1", Project: "p", Workstream: "WS w", Title: "Approve"}
	if sus := AuditSnapshot(snapOf(ws, idle, card), nowSec, AuditOptions{}); hasRule(sus, RuleNeedsYouLane, "w.1") {
		t.Fatalf("clean board: %v", rulesOf(sus))
	}
	// A display that drew the card elsewhere, as cmdrd would send it.
	s := snapOf(ws, idle, card)
	d := Board(s, nowSec)
	d.Workstreams[0].Diagram.Findings = append(d.Workstreams[0].Diagram.Findings, DiagramFinding{Check: CheckNeedsYouLane, Bead: "w.1", Why: "drawn elsewhere"})
	s.Display = &d
	sus := AuditSnapshot(s, nowSec, AuditOptions{})
	if !hasRule(sus, RuleNeedsYouLane, "w.1") {
		t.Fatalf("not listed: %v", rulesOf(sus))
	}
	if !strings.Contains(CheckinReport(s, nowSec, AuditOptions{}), "drawn elsewhere") {
		t.Fatal("check-in does not carry it")
	}
}
