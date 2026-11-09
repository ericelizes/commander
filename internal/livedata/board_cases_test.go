package livedata

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Cases the app's own row tests held before the board moved here.

const caseNow = 10_000

func st(id string, f ...func(*SnapStep)) SnapStep {
	s := SnapStep{BeadID: id, Title: id, State: "not-started", Status: "open"}
	for _, x := range f {
		x(&s)
	}
	return s
}

func closedStep(s *SnapStep)         { s.State, s.Status = "done", "closed" }
func inProgress(s *SnapStep)         { s.State = "in-progress" }
func needsUser(s *SnapStep)          { s.State = "needs-user" }
func title(t string) func(*SnapStep) { return func(s *SnapStep) { s.Title = t } }
func waiting(w string) func(*SnapStep) {
	return func(s *SnapStep) { s.Waiting = w }
}
func blockedBy(ids ...string) func(*SnapStep) { return func(s *SnapStep) { s.BlockedBy = ids } }
func ownedBy(role, state string) func(*SnapStep) {
	return func(s *SnapStep) { s.Owner, s.OwnerState = role, state }
}
func gateOf(t string, approvers ...string) func(*SnapStep) {
	return func(s *SnapStep) { s.Title, s.Gate, s.Approvers = t, true, approvers }
}
func kids(c ...SnapStep) func(*SnapStep) { return func(s *SnapStep) { s.Children = c } }
func asTrack(s *SnapStep)                { s.Track = true }

var caseIdle = SnapAgent{State: "idle", Since: caseNow - 60}

// boardOf one workstream "W" (manager-w); needs lists bead ids NEEDS YOU holds.
func boardOne(steps []SnapStep, agent *SnapAgent, needs ...string) BoardRow {
	s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "W", Status: "open", ManagerRole: "manager-w", Steps: steps}}, Agents: map[string]SnapAgent{}}
	if agent != nil {
		s.Agents["manager-w"] = *agent
	}
	for _, id := range needs {
		s.NeedsYou = append(s.NeedsYou, NeedsYou{Project: "p", BeadID: id, Title: id, Workstream: "W", Rank: 1})
	}
	return Board(s, caseNow).Workstreams[0]
}

func expectLine(t *testing.T, r BoardRow, kind, text string) {
	t.Helper()
	if r.Kind != kind || r.Text != text {
		t.Fatalf("got %s %q, want %s %q", r.Kind, r.Text, kind, text)
	}
}

// pipelineSteps: spike, [Spec approved: user], build (3 units), [PR approval: two reviewers], merge.
func pipelineSteps(buildDone int, op string) []SnapStep {
	var units []SnapStep
	for i := range 3 {
		id := "u" + itoa(int64(i))
		switch {
		case i < buildDone:
			units = append(units, st(id, closedStep))
		case i == buildDone && op != "":
			units = append(units, st(id, inProgress, ownedBy(op, "")))
		default:
			units = append(units, st(id))
		}
	}
	build := st("build", blockedBy("spec-ok"), kids(units...))
	if buildDone == 3 {
		closedStep(&build)
	}
	return []SnapStep{
		st("spike", closedStep),
		st("spec-ok", gateOf("Spec approved", "user"), blockedBy("spike"), closedStep),
		build,
		st("prs-ok", gateOf("PR approval", "a-reviewer", "b-reviewer"), blockedBy("build")),
		st("merge", blockedBy("prs-ok")),
	}
}

func TestBoardGateLines(t *testing.T) {
	idle := caseIdle
	expectLine(t, boardOne(pipelineSteps(1, "operator-op-x"), &idle), "working", "Working on u1 · then: PR approval")
	expectLine(t, boardOne(pipelineSteps(1, ""), &idle), "stalled", "Stalled")
	expectLine(t, boardOne(pipelineSteps(3, ""), &idle), "waiting", "Waiting for PR approval")
	if r := boardOne(pipelineSteps(1, ""), &idle); r.Progress != (Progress{Done: 2, Total: 5}) {
		t.Fatalf("progress %+v", r.Progress)
	}
	// Open work behind the user's own open gate: Needs you, not Stalled.
	expectLine(t, boardOne([]SnapStep{st("wf", gateOf("Workflow approved", "user")), st("a", blockedBy("wf")), st("b", blockedBy("wf"))}, &idle, "wf"), "needs", "Needs you: wf")
	expectLine(t, boardOne([]SnapStep{st("a", closedStep), st("g", gateOf("Merge approval", "user"), blockedBy("a"))}, &idle, "g"), "needs", "Needs you · merge approval")
	g := st("g", gateOf("PR approval", "a-reviewer"), blockedBy("ci"))
	expectLine(t, boardOne([]SnapStep{st("ci", inProgress, waiting("CI #4101 · CI #4107")), g}, &idle), "waiting", "Waiting for CI on 2 PRs")
	expectLine(t, boardOne([]SnapStep{st("ci", inProgress, waiting("main CI 349a7f4")), g}, &idle), "waiting", "Waiting for CI")
	expectLine(t, boardOne([]SnapStep{st("ci", inProgress, waiting("prod tag 349a7f4")), g}, &idle), "waiting", "Waiting for prod tag")
	expectLine(t, boardOne([]SnapStep{st("ci", waiting("review (a-reviewer)")), g}, &idle), "waiting", "Waiting for review")
	// A wait is timed from when it was recorded, and CI comes first.
	rv := st("rv", waiting("review (a-reviewer)"))
	rv.WaitSince = caseNow - 1980
	ci := st("ci", inProgress, waiting("CI #4101"))
	ci.WaitSince = caseNow - 180
	r := boardOne([]SnapStep{rv, ci, st("g", gateOf("PR approval", "a-reviewer"), blockedBy("rv", "ci"))}, &idle)
	if r.Text != "Waiting for CI" || r.Since != caseNow-180 {
		t.Fatalf("%q since %d", r.Text, r.Since)
	}
}

func TestBoardGateOrder(t *testing.T) {
	idle := caseIdle
	// An open gate with done children is still the next gate.
	r := boardOne([]SnapStep{
		st("obs", closedStep),
		st("pr-ok", gateOf("PR approval", "a-reviewer"), blockedBy("obs"), waiting("review (a-reviewer)"), kids(st("pr-ok.1", closedStep))),
		st("merge", gateOf("Merge the stack", "user"), blockedBy("pr-ok")),
	}, &idle)
	if r.Gate == nil || r.Gate.BeadID != "pr-ok" {
		t.Fatalf("gate %+v", r.Gate)
	}
	expectLine(t, r, "waiting", "Waiting for review (a-reviewer)")
	// A workflow gate that waits on nothing does not take over the headline.
	r = boardOne([]SnapStep{
		st("obs", closedStep),
		st("pr", gateOf("PR approval", "a-reviewer", "b-reviewer"), blockedBy("obs"), waiting("review (a-reviewer, b-reviewer)")),
		st("merge", gateOf("Merge the stack", "user"), blockedBy("pr")),
		st("flow", gateOf("Workflow approved", "user")),
	}, &idle)
	if r.Gate.BeadID != "pr" {
		t.Fatalf("gate %+v", r.Gate)
	}
	expectLine(t, r, "waiting", "Waiting for review (a-reviewer, b-reviewer)")
}

func TestBoardWorkingAgents(t *testing.T) {
	onReview := SnapAgent{State: "waiting", Wait: "review (a-reviewer)", Since: caseNow}
	if r := boardOne(pipelineSteps(1, ""), &onReview); r.Working != 0 {
		t.Fatalf("a Manager waiting on a review is not working: %d", r.Working)
	}
	subAgents := SnapAgent{State: "waiting", Detail: "2 sub-agents", Since: caseNow}
	if r := boardOne(pipelineSteps(1, ""), &subAgents); r.Working != 1 {
		t.Fatalf("a Manager on its sub-agents is working: %d", r.Working)
	}
	idle := caseIdle
	if r := boardOne([]SnapStep{st("u", inProgress, ownedBy("operator-op-x", "died"))}, &idle); r.Working != 0 {
		t.Fatalf("a dead claim works: %d", r.Working)
	}
	if r := boardOne([]SnapStep{st("u", inProgress, ownedBy("operator-op-x", "working"))}, &idle); r.Working != 2 {
		t.Fatalf("a live Operator and its Manager: %d", r.Working)
	}
}

func TestBoardFlagsFromNeedsYou(t *testing.T) {
	idle := caseIdle
	gate := func(id string, f ...func(*SnapStep)) SnapStep {
		return st(id, append([]func(*SnapStep){gateOf("Workflow approved", "user")}, f...)...)
	}
	// A user gate NEEDS YOU does not list is not waiting on the user.
	expectLine(t, boardOne([]SnapStep{st("a", inProgress, waiting("review (a-reviewer)")), gate("g", blockedBy("a"))}, &idle), "waiting", "Waiting for review")
	expectLine(t, boardOne([]SnapStep{gate("g"), st("b", blockedBy("g"))}, &idle, "g"), "needs", "Needs you: g")
	// An open user ask is Needs you even when the next gate is not theirs.
	steps := []SnapStep{st("st1", kids(st("x"))), st("inv", gateOf("Root cause found", "operator"), blockedBy("st1")), st("spike", needsUser)}
	if r := boardOne(steps, &idle); r.Kind != "stalled" {
		t.Fatalf("unlisted ask: %+v", r)
	}
	expectLine(t, boardOne(steps, &idle, "spike"), "needs", "Needs you: spike")
}

func TestBoardManagerLine(t *testing.T) {
	SetTicketPrefix("TEAM")
	t.Cleanup(func() { SetTicketPrefix("") })
	idle := caseIdle
	working := SnapAgent{State: "working", Detail: "Bash: grep x", Since: caseNow}
	expectLine(t, boardOne([]SnapStep{st("b", title("t"), needsUser)}, &working), "needs", "Needs you: t")
	many := []SnapStep{st("a", title("t"), waiting("you")), st("c", title("t"), waiting("you")), st("d", title("t"), needsUser, waiting("you"))}
	expectLine(t, boardOne(many, &idle), "needs", "Needs you: t (+2 more)")
	expectLine(t, boardOne([]SnapStep{st("a", title("Explore the sample design (TEAM-101)"), inProgress)}, &working), "working", "Working on explore the sample design")
	if r := boardOne([]SnapStep{st("b", inProgress, ownedBy("operator-op-x-p-w-1", ""))}, &idle); r.Kind != "working" {
		t.Fatalf("an Operator on it: %+v", r)
	}
	expectLine(t, boardOne([]SnapStep{st("b", inProgress, waiting("review (a-reviewer, b-reviewer)"))}, &idle), "waiting", "Waiting for review")
	rv := st("b", inProgress)
	rv.Review = "4107"
	expectLine(t, boardOne([]SnapStep{rv}, &idle), "waiting", "Waiting for review")
	expectLine(t, boardOne([]SnapStep{st("b", inProgress)}, &idle), "stalled", "Stalled")
	expectLine(t, boardOne([]SnapStep{st("a", closedStep), st("c")}, &idle), "stalled", "Stalled")
	broken := st("b", inProgress)
	broken.Broken = true
	expectLine(t, boardOne([]SnapStep{broken}, &idle), "stalled", "State missing")
	expectLine(t, boardOne([]SnapStep{st("b", closedStep)}, &idle), "quiet", "Nothing to do")
	expectLine(t, boardOne([]SnapStep{st("b")}, nil), "none", "No Manager yet")
}

func TestBoardReviewFacts(t *testing.T) {
	idle := caseIdle
	fact := func(f ReviewFact) []SnapStep {
		s := st("p-1.1", title("Review signed off"), waiting("review (a-reviewer)"))
		s.State, s.ReviewFact = "blocked", &f
		return []SnapStep{s}
	}
	expectLine(t, boardOne(fact(ReviewFact{Acted: true, Text: "a-reviewer approved #4109"}), &idle), "stalled", "a-reviewer approved #4109: the Manager's move")
	expectLine(t, boardOne(fact(ReviewFact{Text: "waiting on a-reviewer (#4101)"}), &idle), "waiting", "Waiting for review: waiting on a-reviewer (#4101)")
	expectLine(t, boardOne(fact(ReviewFact{NoPR: true, Text: "review wait names no PR"}), &idle), "stalled", "Waiting for a review of no PR: nothing can clear it")
	expectLine(t, boardOne([]SnapStep{st("p-1.1", title("Run the audit"), inProgress, ownedBy("operator-x", "died"))}, &idle), "stalled", "Agent stopped on run the audit: the Manager's move")
	// The wait on the track, the sign-off step open under it: the approval still reads.
	track := st("p-1", title("Preview display names"), inProgress)
	track.ReviewFact = &ReviewFact{Acted: true, Text: "a-reviewer approved #4091"}
	track.Children = []SnapStep{st("p-1.7", title("Signed off by the reviewer"), inProgress)}
	expectLine(t, boardOne([]SnapStep{track}, &idle), "stalled", "a-reviewer approved #4091: the Manager's move")
}

func TestBoardPickingUp(t *testing.T) {
	s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "W", ManagerRole: "manager-w", Steps: []SnapStep{st("a")}}},
		Agents: map[string]SnapAgent{"manager-w": caseIdle}, Unread: map[string]int{}}
	if r := Board(s, caseNow).Workstreams[0]; r.Text != "Stalled" {
		t.Fatalf("%+v", r)
	}
	s.Unread["manager-w"] = 1 // mail to read: it is acting on it
	if r := Board(s, caseNow).Workstreams[0]; r.Kind != "working" || r.Text != "Manager picking it up" {
		t.Fatalf("%+v", r)
	}
}

func TestBoardTracks(t *testing.T) {
	flow := func(id string) SnapStep { return st(id, gateOf("Workflow approved", "user")) }
	tracked := func() []SnapStep {
		return []SnapStep{
			st("w.1", title("A. Script audits"), asTrack, kids(flow("w.1.1"), st("w.1.2", blockedBy("w.1.1")))),
			st("w.2", title("B. Config audits"), asTrack, kids(flow("w.2.1"), st("w.2.2", inProgress, ownedBy("operator-b", ""), blockedBy("w.2.1")))),
		}
	}
	idle := caseIdle
	s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "Audits", ManagerRole: "manager-w", Steps: tracked()}},
		Agents:   map[string]SnapAgent{"manager-w": idle},
		NeedsYou: []NeedsYou{{Project: "p", BeadID: "w.1.1", Title: "Workflow approved", Workstream: "Audits", Track: "A. Script audits", TrackID: "w.1", Rank: 1}}}
	d := Board(s, caseNow)
	r := d.Workstreams[0]
	if len(r.Tracks) != 2 || r.Tracks[0].Letter != ".1" || r.Tracks[0].Title != "Script audits" || r.Tracks[1].Title != "Config audits" {
		t.Fatalf("tracks %+v", r.Tracks)
	}
	if r.Tracks[0].Kind != "needs" || r.Tracks[1].Kind == "needs" || r.Kind != "needs" {
		t.Fatalf("row %s, tracks %s %s", r.Kind, r.Tracks[0].Kind, r.Tracks[1].Kind)
	}
	if d.Asks[0].TrackLetter != ".1" || d.Asks[0].Track != "A. Script audits" {
		t.Fatalf("ask %+v", d.Asks[0])
	}
	// Without the ask A has nobody on it and B is worked: the row reads as what moves.
	s.NeedsYou = nil
	r = Board(s, caseNow).Workstreams[0]
	if r.Kind != "working" || r.Tracks[0].Kind != "stalled" || r.Tracks[1].Working != 1 || r.Tracks[0].Working != 0 {
		t.Fatalf("row %+v", r)
	}
	if r.Tracks[1].Text != "Working on w.2.2 · then: workflow approved" {
		t.Fatalf("track B %q", r.Tracks[1].Text)
	}
	// Finished and parked tracks are not sub-rows; without tracks there are none.
	steps := append(tracked(), st("w.3", asTrack, closedStep), st("w.4", asTrack, func(s *SnapStep) { s.Status = statusDeferred }))
	s.Workstreams[0].Steps = steps
	if r := Board(s, caseNow).Workstreams[0]; len(r.Tracks) != 2 {
		t.Fatalf("tracks %+v", r.Tracks)
	}
	plain := tracked()
	for i := range plain {
		plain[i].Track = false
	}
	s.Workstreams[0].Steps = plain
	if r := Board(s, caseNow).Workstreams[0]; len(r.Tracks) != 0 {
		t.Fatalf("tracks %+v", r.Tracks)
	}
	// One track beside other open work still shows; alone it is the workstream.
	s.Workstreams[0].Steps = []SnapStep{tracked()[0], st("w.5", title("Switch the flag"), needsUser)}
	if r := Board(s, caseNow).Workstreams[0]; len(r.Tracks) != 1 || r.Tracks[0].BeadID != "w.1" {
		t.Fatalf("lone track beside work %+v", r.Tracks)
	}
	s.Workstreams[0].Steps = tracked()[:1]
	if r := Board(s, caseNow).Workstreams[0]; len(r.Tracks) != 0 {
		t.Fatalf("lone track %+v", r.Tracks)
	}
}

func TestMostUrgent(t *testing.T) {
	if l, _ := mostUrgent([]line{{kind: "working"}, {kind: "waiting"}, {kind: "needs"}}); l.kind != "needs" {
		t.Fatal(l)
	}
	if l, _ := mostUrgent([]line{{kind: "stalled", text: "State missing"}, {kind: "working"}}); l.kind != "working" {
		t.Fatal(l)
	}
}

func TestBoardParked(t *testing.T) {
	idle := caseIdle
	park := func(s *SnapStep) { s.Status = statusDeferred }
	t02 := []SnapStep{
		st("a", title("A. Script audits"), park, kids(
			st("a-flow", gateOf("Workflow approved (A)", "user"), park),
			st("a1", blockedBy("a-flow"), ownedBy("operator-a1", ""), inProgress, waiting("you"), park))),
		st("b", title("B"), kids(st("b-flow", gateOf("Workflow approved (B)", "user")), st("b1", inProgress, ownedBy("operator-b1", ""), blockedBy("b-flow")))),
	}
	r := boardOne(t02, &idle)
	if r.Progress != (Progress{Done: 0, Total: 1}) || r.Working != 2 || r.Gate == nil || r.Gate.Title != "Workflow approved (B)" {
		t.Fatalf("parked work counts: %+v", r)
	}
	expectLine(t, boardOne([]SnapStep{st("a1", ownedBy("operator-a1", ""), needsUser, waiting("you"), park)}, &idle), "quiet", "Nothing to do")
}

func TestAgentLine(t *testing.T) {
	for _, c := range []struct {
		a    SnapAgent
		kind string
		text string
	}{
		{SnapAgent{State: "idle"}, "idle", "idle"},
		{SnapAgent{State: "working", Detail: "Bash: grep"}, "working", "working"}, // the tool is not a state
		{SnapAgent{State: "waiting", Detail: "3 sub-agents"}, "waiting", "waiting on 3 sub-agents"},
		{SnapAgent{State: "waiting", Detail: "CI #4101", Wait: "CI #4101"}, "waiting", "waiting · CI #4101"},
		{SnapAgent{State: "waiting"}, "waiting", "waiting on background tasks"},
		{SnapAgent{State: "stuck", Detail: "input box has text"}, "stuck", "stuck: input box has text"},
	} {
		if l := agentLine(c.a, true); l.kind != c.kind || l.text != c.text {
			t.Errorf("%+v: got %s %q", c.a, l.kind, l.text)
		}
	}
	if l := agentLine(SnapAgent{}, false); l.kind != "none" || l.text != "no agent yet" {
		t.Errorf("none: %+v", l)
	}
}

// A recorded external wait wins over the Manager merely being busy; a busy Manager or an Operator on the work reads Working on.
func TestBoardRecordedWaitBeatsBusyManager(t *testing.T) {
	idle := caseIdle
	busy := SnapAgent{State: "working", Since: caseNow - 30}
	g := st("g", gateOf("PR approval", "a-reviewer"), blockedBy("rv"))
	withWait := func(w string, f ...func(*SnapStep)) []SnapStep {
		return []SnapStep{st("rv", append([]func(*SnapStep){waiting(w)}, f...)...), g}
	}
	// wait + busy Manager and wait + idle Manager read the same.
	expectLine(t, boardOne(withWait("review (a-reviewer)"), &busy), "waiting", "Waiting for review")
	expectLine(t, boardOne(withWait("review (a-reviewer)"), &idle), "waiting", "Waiting for review")
	expectLine(t, boardOne(withWait("CI #4101"), &busy), "waiting", "Waiting for CI")
	// a time wait still in the future holds; one past its time does not
	future := func(s *SnapStep) { s.WaitUntils = map[string]int64{"recheck": caseNow + 600} }
	if r := boardOne(withWait("recheck", future), &busy); r.Kind != "waiting" {
		t.Fatalf("future until + busy: %s %q", r.Kind, r.Text)
	}
	expired := func(s *SnapStep) { s.WaitUntils = map[string]int64{"recheck": caseNow - 600} }
	expectLine(t, boardOne(withWait("recheck", expired), &busy), "working", "Working · then: PR approval")
	// no wait + busy Manager: Working on the step
	expectLine(t, boardOne([]SnapStep{st("rv", inProgress), g}, &busy), "working", "Working on rv · then: PR approval")
	// an Operator on the step: Working on, even with a wait recorded
	expectLine(t, boardOne([]SnapStep{st("rv", inProgress, ownedBy("operator-op-x", ""), waiting("review (a-reviewer)")), g}, &idle), "working", "Working on rv · then: PR approval")
}

// The same input read with a busy and with an idle Manager gives the same gate row,
// except where an Operator is on the work or the Manager is doing it with nothing recorded.
func TestBoardGateRowSameBusyAndIdle(t *testing.T) {
	idle := caseIdle
	busy := SnapAgent{State: "working", Since: caseNow - 30}
	g := st("g", gateOf("PR approval", "a-reviewer"), blockedBy("rv"))
	future := func(s *SnapStep) { s.WaitUntils = map[string]int64{"recheck": caseNow + 600} }
	expired := func(s *SnapStep) { s.WaitUntils = map[string]int64{"recheck": caseNow - 600} }
	died := st("rv2", func(s *SnapStep) { s.OwnerState = "died" })
	g = st("g", gateOf("PR approval", "a-reviewer"), blockedBy("rv", "rv2"))
	for name, steps := range map[string][]SnapStep{
		"user wait":    {st("rv", waiting("you")), g},
		"review":       {st("rv", waiting("review (a-reviewer)")), g},
		"CI":           {st("rv", waiting("CI #4101")), g},
		"future until": {st("rv", waiting("recheck"), future), g},
	} {
		b, i := boardOne(steps, &busy), boardOne(steps, &idle)
		if b.Kind != i.Kind || b.Text != i.Text {
			t.Errorf("%s: busy %s %q, idle %s %q", name, b.Kind, b.Text, i.Kind, i.Text)
		}
	}
	// an expired time wait holds nothing: a busy Manager is doing the work, an idle one is stalled
	if r := boardOne([]SnapStep{st("rv", waiting("recheck"), expired), g}, &busy); r.Text != "Working · then: PR approval" {
		t.Errorf("expired + busy: %q", r.Text)
	}
	if r := boardOne([]SnapStep{st("rv", waiting("recheck"), expired), g}, &idle); r.Text == "Waiting for recheck" {
		t.Errorf("expired + idle still waits: %q", r.Text)
	}
	if r := boardOne([]SnapStep{st("rv", waiting("you")), g}, &busy); r.Text != "Needs you" {
		t.Errorf("user wait: %q", r.Text)
	}
	// A stopped agent is a fact in both: the gate phase says so for either Manager state (a busy Manager
	// then reads as already picking it up, the existing stalled rule), never Readying and never the other wait.
	for _, steps := range [][]SnapStep{{st("rv", waiting("review (a-reviewer)")), died, g}, {st("rv", inProgress), died, g}} {
		if r := boardOne(steps, &idle); r.Kind != "stalled" || !strings.HasPrefix(r.Text, "Agent stopped") {
			t.Errorf("idle died unit: %s %q", r.Kind, r.Text)
		}
		if r := boardOne(steps, &busy); r.Text != "Manager picking it up" {
			t.Errorf("busy died unit: %s %q", r.Kind, r.Text)
		}
	}
}

// The row names the live step, the gate only as "then:"; several live steps add "+N more"; a gate bead with an Operator on it is readying.
func TestBoardWorkingNamesLiveStep(t *testing.T) {
	idle := caseIdle
	busy := SnapAgent{State: "working", Since: caseNow - 30}
	g := st("g", gateOf("PR approval", "a-reviewer"), blockedBy("a", "b"))
	// two live steps: the first is named
	two := []SnapStep{st("a", inProgress, ownedBy("operator-op-x", "")), st("b", inProgress, ownedBy("operator-op-y", "")), g}
	r := boardOne(two, &idle)
	if r.Kind != "working" || !strings.HasPrefix(r.Text, "Working on ") || !strings.Contains(r.Text, " +1 more · then: PR approval") {
		t.Fatalf("two live: %s %q", r.Kind, r.Text)
	}
	if strings.Contains(r.Text, "Readying") {
		t.Fatalf("Readying survives: %q", r.Text)
	}
	// no live step known: bare Working, no invented step
	if r := boardOne([]SnapStep{st("a"), g}, &busy); r.Kind != "working" || r.Text != "Working · then: PR approval" {
		t.Fatalf("no live step: %s %q", r.Kind, r.Text)
	}
	// a recorded wait still wins over the Manager being busy
	expectLine(t, boardOne([]SnapStep{st("a", waiting("CI #4101")), g}, &busy), "waiting", "Waiting for CI")
	// an Operator on the gate bead itself is readying that gate
	gate := st("g", gateOf("PR approval", "a-reviewer"), ownedBy("operator-op-x", ""))
	expectLine(t, boardOne([]SnapStep{gate}, &idle), "working", "Readying for PR approval")
}

func TestLiveTitleClipsAndCollapses(t *testing.T) {
	exact := strings.Repeat("a", needsTitleMax)
	cases := map[string]string{
		"long":    strings.Repeat("word ", 30),
		"newline": "first line\nsecond   line\tthird",
		"exact":   exact,
		"unicode": strings.Repeat("é", 80),
	}
	for name, raw := range cases {
		got := liveTitle(raw)
		if strings.ContainsAny(got, "\n\t") || strings.Contains(got, "  ") {
			t.Errorf("%s: whitespace left: %q", name, got)
		}
		if n := len([]rune(got)); n > needsTitleMax {
			t.Errorf("%s: %d runes: %q", name, n, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: invalid utf8", name)
		}
	}
	if got := liveTitle(exact); got != exact {
		t.Errorf("exact limit clipped: %q", got)
	}
	if got := liveTitle("first line\nsecond   line\tthird"); got != "first line second line third" {
		t.Errorf("collapse: %q", got)
	}
	if got := liveTitle(strings.Repeat("é", 80)); !strings.HasSuffix(got, "…") {
		t.Errorf("unicode not marked: %q", got)
	}
	// through the rows
	long := "Do\nthe " + strings.Repeat("very long ", 20) + "thing"
	r := boardOne([]SnapStep{st("a", title(long), inProgress, ownedBy("operator-op-x", ""))}, &caseIdle)
	if strings.Contains(r.Text, "\n") || len([]rune(r.Text)) > len("Working on ")+needsTitleMax {
		t.Errorf("row: %q", r.Text)
	}
}

func TestLiveTrackOutranksWaitingSibling(t *testing.T) {
	working := line{kind: "working", text: "Working on write the importer"}
	waiting := line{kind: "waiting", text: "Waiting for soak-period"}
	pitch := line{kind: "waiting", text: "Manager writing the pitch for cutover"}
	needs := line{kind: "needs", text: "Needs you: Cut over?"}
	pick := func(cands []line, live []bool) line {
		names := []string{"Build", "Cutover", "Docs"}[:len(cands)]
		if len(cands) == 3 {
			names = []string{"", "Build", "Cutover"} // the workstream's own line has no name
		}
		l, _ := mostUrgent(cands)
		return liveOverWaiting(l, cands, names, live)
	}
	if l := pick([]line{working, waiting}, []bool{true, false}); l.kind != "working" || l.text != "Working on write the importer · Cutover waiting for soak-period" || l.ageAs != "working|Working on write the importer" {
		t.Fatalf("live beside waiting: %+v", l)
	}
	if l := pick([]line{working, waiting}, []bool{false, false}); l != waiting {
		t.Fatalf("no live Operator: %+v", l)
	}
	if l := pick([]line{working, needs}, []bool{true, false}); l != needs {
		t.Fatalf("needs you still leads: %+v", l)
	}
	if l := pick([]line{waiting, working, pitch}, []bool{false, true, false}); l.text != "Working on write the importer · waiting for soak-period +1 more" {
		t.Fatalf("own wait and two waits: %+v", l)
	}
	if l := pick([]line{working, pitch}, []bool{true, false}); l.text != "Working on write the importer · Cutover: Manager writing the pitch for cutover" {
		t.Fatalf("non-wait text: %+v", l)
	}
}

// A track named beside the live work is cleaned as the row's other titles are.
func TestLiveTrackNamesWaitingSiblingCleanly(t *testing.T) {
	SetTicketPrefix("TEAM")
	t.Cleanup(func() { SetTicketPrefix("") })
	long := "Cutover the billing importer and every downstream consumer of it"
	for raw, want := range map[string]string{
		"Cutover (TEAM-13)":  "Cutover",
		"Cutover (on Codex)": "Cutover",
		"Cutover\n  today":   "Cutover today",
		"B. Cutover":         "Cutover",
		long:                 clip(long, needsTitleMax),
	} {
		t.Run(raw, func(t *testing.T) {
			r := boardOne([]SnapStep{
				st("t1", title("Build"), asTrack, inProgress, kids(st("a", title("Write the importer"), inProgress, ownedBy("operator-op-x", "working")))),
				st("t2", title(raw), asTrack, inProgress, kids(st("b", title("Soak"), inProgress, waiting("soak-period")))),
			}, &caseIdle)
			expectLine(t, r, "working", "Working on write the importer · "+want+" waiting for soak-period")
		})
	}
}
