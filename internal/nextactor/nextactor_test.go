package nextactor

import (
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

var now = time.Unix(1_800_000_000, 0)

func ctxWith(busy bool, idleFor time.Duration, beadsByID map[string]Bead) Context {
	c := Context{Now: now, Commander: func(string) (bool, int64) {
		if busy {
			return true, 0
		}
		return false, now.Add(-idleFor).Unix()
	}}
	if beadsByID != nil {
		c.Lookup = func(id string) (Bead, bool) { b, ok := beadsByID[id]; return b, ok }
	}
	return c
}

func TestDecideCoversEveryFactCombination(t *testing.T) {
	open := func(f func(*Bead)) Bead {
		b := Bead{ID: "b", Title: "Ship the guard", Status: "open", PlanningCommander: "commander"}
		f(&b)
		return b
	}
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	ci := beads.Wait{Kind: beads.WaitCI, PR: 42, Since: now.Add(-3 * time.Minute)}
	review := beads.Wait{Kind: beads.WaitReview, Ref: "alex"}
	future := beads.Wait{Kind: beads.WaitUntil, Ref: "rollout window", Until: now.Add(time.Hour)}
	past := beads.Wait{Kind: beads.WaitUntil, Ref: "rollout window", Until: now.Add(-time.Minute)}
	handoff := beads.Wait{Kind: beads.WaitUntil, Ref: beads.ManagerPlanWait, Until: now.Add(time.Hour)}
	cases := []struct {
		name          string
		bead          Bead
		busy          bool
		idle          time.Duration
		actor         Actor
		reason        string
		textHasPrefix string
	}{
		{"closed is done", open(func(b *Bead) { b.Status = "closed" }), false, 0, None, "done", "Done"},
		{"superseded", open(func(b *Bead) { b.Superseded = true }), false, 0, None, "withdrawn", "Withdrawn"},
		{"withdrawn gate", open(func(b *Bead) { b.Withdrawn = true; b.NeedsUser = true }), false, 0, None, "withdrawn", "Withdrawn"},
		{"a held step is an invalid state (4.1, Nov 20)", open(func(b *Bead) { b.Held = true }), false, 0, Stuck, "invalid:hold", "Invalid state:"},
		{"a hold no longer beats a user ask", open(func(b *Bead) { b.Held = true; b.NeedsUser = true }), false, 0, Stuck, "invalid:hold", "Invalid state:"},
		{"saved answer on held work goes to Commander", open(func(b *Bead) { b.Held = true; b.HumanAnswerPending = true }), true, 0, Agent, "apply_answer", "Applying your answer"},
		{"a parked step is an invalid state", open(func(b *Bead) { b.Status = beads.StatusDeferred }), false, 0, Stuck, "invalid:parked", "Invalid state:"},
		{"a parked container is paused", open(func(b *Bead) { b.Status = beads.StatusDeferred; b.Container = true }), false, 0, None, "paused", "Paused"},
		{"a park with a time is a wait cmdrd ends", open(func(b *Bead) { b.Status = beads.StatusDeferred; b.ParkedUntil = 1 }), false, 0, None, "paused", "Paused"},
		{"a step under a parked container is paused", open(func(b *Bead) { b.Status = beads.StatusDeferred; b.UnderParked = true }), false, 0, None, "paused", "Paused"},
		{"saved answer on parked work goes to Commander", open(func(b *Bead) { b.Status = beads.StatusDeferred; b.HumanAnswerPending = true }), true, 0, Agent, "apply_answer", "Applying your answer"},
		{"user decision", open(func(b *Bead) { b.NeedsUser = true; b.UserAsk = "approve the refund guard" }), false, 0, User, "decision", "Needs you: approve the refund guard"},
		{"user decision beats a running agent", open(func(b *Bead) { b.NeedsUser = true; b.Executing = true }), false, 0, User, "decision", "Needs you"},
		{"waiting on the user with a recorded ask", open(func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitUser}}; b.UserAsk = "pick the rollout day" }), false, 0, User, "waiting_user", "Needs you: pick the rollout day"},
		{"waiting on the user with nothing asked", open(func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitUser}} }), true, 0, Stuck, "invalid:no-card", "Invalid state:"},
		{"agent question, Commander busy", open(func(b *Bead) { b.PendingQuestions = 1 }), true, 0, Stuck, "agent_question", "The agent on"},
		{"agent question, Commander idle long", open(func(b *Bead) { b.QuestionWaiting = true }), false, time.Hour, Stuck, "agent_question", "The agent on"},
		{"launch failed", open(func(b *Bead) { b.LaunchFailed = true; b.LaunchError = "no worktree" }), false, 0, Stuck, "launch_failed", "Stuck: the agent could not start (no worktree)"},
		{"exited without report", open(func(b *Bead) { b.ExitedWithoutReport = true; b.ExitedAt = ago(time.Minute) }), false, 0, Stuck, "exited_without_report", "Stuck: the agent stopped"},
		{"agent crashed, Commander owes the next move", open(func(b *Bead) { b.StopReason = "Agent stopped: it crashed (exit code 1)"; b.ProgressAt = ago(time.Hour) }), true, 0, Stuck, "agent_stopped", "Agent stopped: it crashed (exit code 1)"},
		{"agent crashed, restart queued", open(func(b *Bead) { b.StopReason = "Agent stopped: it crashed; restarting it once"; b.ReadyQueued = true }), false, 0, Agent, "queued", "Agent stopped: it crashed; restarting it once"},
		{"restarted agent is working again", open(func(b *Bead) {
			b.StopReason = "Agent stopped: it crashed"
			b.Executing = true
			b.ExecSince = ago(time.Minute)
		}), false, 0, Agent, "executing", "Working on"},
		{"executing", open(func(b *Bead) { b.Executing = true; b.ExecRole = "operator-exec-1"; b.ExecSince = ago(5 * time.Minute) }), false, 0, Agent, "executing", "Working on"},
		{"executing with recent progress", open(func(b *Bead) { b.Executing = true; b.ExecSince = ago(time.Hour); b.ProgressAt = ago(time.Minute) }), false, 0, Agent, "executing", "Working on"},
		{"idle agent with no progress", open(func(b *Bead) { b.Executing = true; b.ExecSince = ago(time.Hour); b.ProgressAt = ago(30 * time.Minute) }), false, 0, Stuck, "no_progress", "Stuck: no progress for 30 minutes"},
		{"agent mid-turn on a long run", open(func(b *Bead) { b.Executing = true; b.ExecWorking = true; b.ProgressAt = ago(40 * time.Minute) }), false, 0, Agent, "executing", "Working on"},
		{"agent mid-turn far too long", open(func(b *Bead) { b.Executing = true; b.ExecWorking = true; b.ProgressAt = ago(2 * time.Hour) }), false, 0, Stuck, "no_progress", "Stuck: no progress for 2 hours"},
		{"claim just made", open(func(b *Bead) { b.Unverified = true; b.UnverifiedSince = ago(time.Minute) }), false, 0, Agent, "starting", "Starting an agent"},
		{"claim with no process", open(func(b *Bead) { b.Unverified = true; b.UnverifiedSince = ago(10 * time.Minute) }), false, 0, Stuck, "unverified", "Stuck: claimed"},
		{"result to review, Commander busy", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Minute) }), true, 0, Stuck, "agent_blocked", "The agent on"},
		{"result left unreviewed while Commander is busy elsewhere", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Hour) }), true, 0, Stuck, "agent_blocked", "The agent on"},
		{"result to review, Commander just idle", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Minute) }), false, time.Minute, Stuck, "agent_blocked", "The agent on"},
		{"result unreviewed for long", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Hour) }), false, time.Hour, Stuck, "agent_blocked", "The agent on"},
		{"waiting on CI", open(func(b *Bead) { b.Waits = []beads.Wait{ci} }), false, 0, External, "wait:ci", "Waiting on CI #42"},
		{"waiting on a reviewer", open(func(b *Bead) { b.Waits = []beads.Wait{review} }), false, 0, External, "wait:review", "Waiting on review by alex"},
		{"waiting for a date", open(func(b *Bead) { b.Waits = []beads.Wait{future} }), false, 0, External, "wait:until", "Waiting on rollout window"},
		{"a date that passed is owed", open(func(b *Bead) { b.Waits = []beads.Wait{past} }), true, 0, Agent, "wait_over", "Wait over"},
		{"a date long passed, Commander busy elsewhere", open(func(b *Bead) {
			b.Waits = []beads.Wait{{Kind: beads.WaitUntil, Ref: "rollout window", Until: now.Add(-time.Hour)}}
		}), true, 0, Agent, "wait_over", "Wait over"},
		{"legacy Manager handoff is Commander's", open(func(b *Bead) { b.Waits = []beads.Wait{handoff} }), true, 0, Agent, "wait_over", "Wait over"},
		{"user wait beats CI", open(func(b *Bead) { b.Waits = []beads.Wait{ci, {Kind: beads.WaitUser}}; b.UserAsk = "approve" }), false, 0, User, "waiting_user", "Needs you"},
		{"queued for capacity", open(func(b *Bead) { b.ReadyQueued = true }), false, 0, Agent, "queued", "Queued"},
		{"ready but nothing can run it", open(func(b *Bead) { b.ReadyQueued = true; b.NoCapableSlot = true }), false, 0, Stuck, "no_capable_slot", "Stuck: no agent slot"},
		{"parent carried by children", open(func(b *Bead) { b.HasOpenChildren = true }), false, 0, None, "children", "Carried"},
		{"parent with its own user ask", open(func(b *Bead) { b.HasOpenChildren = true; b.NeedsUser = true }), false, 0, User, "decision", "Needs you"},
		{"nothing recorded: queued for an agent", open(func(b *Bead) {}), true, 0, Agent, "ready", "Queued for an agent"},
		{"clean final report closes itself", open(func(b *Bead) { b.ResultReady = true; b.ResultClean = true }), true, 0, Agent, "result_accepting", "Finished"},
		{"a review step is a review agent's", open(func(b *Bead) { b.ReviewStep = true }), true, 0, Agent, "ready", "Queued for a review agent"},
		{"no brief is invalid", open(func(b *Bead) { b.NoBrief = true }), true, 0, Stuck, "invalid:no-brief", "Invalid state:"},
		{"a track with nothing open is invalid", open(func(b *Bead) { b.Container = true }), true, 0, Stuck, "invalid:no-open-step", "Invalid state:"},
		{"Eric's step with no card is invalid", open(func(b *Bead) { b.Lane = "you" }), true, 0, Stuck, "invalid:no-card", "Invalid state:"},
		{"an answered gate is the Commander's to structure", open(func(b *Bead) { b.Gate, b.Answered = true, true }), true, 0, Commander, "structure:answer", "Commander: turn your answer"},
		{"a gate with no card is invalid, never an agent's", open(func(b *Bead) { b.Gate = true }), true, 0, Stuck, "invalid:no-card", "Invalid state:"},
		{"nothing recorded for an hour, Commander busy elsewhere", open(func(b *Bead) { b.ProgressAt = ago(time.Hour) }), true, 0, Agent, "ready", "Queued for an agent"},
		{"nothing recorded, Commander idle long", open(func(b *Bead) {}), false, time.Hour, Agent, "ready", "Queued for an agent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Decide(c.bead, ctxWith(c.busy, c.idle, nil))
			if r.Actor != c.actor || r.Reason != c.reason || !strings.HasPrefix(r.Text, c.textHasPrefix) {
				t.Fatalf("got %+v, want actor=%s reason=%s text^=%q", r, c.actor, c.reason, c.textHasPrefix)
			}
		})
	}
}

func TestDependencyChainsResolveToTheirRoot(t *testing.T) {
	graph := map[string]Bead{
		"gate":   {ID: "gate", Title: "Greenlight the repair batch", Status: "open", NeedsUser: true},
		"mac":    {ID: "mac", Title: "Mac execution", Status: "open", BlockedBy: []string{"gate"}},
		"ci":     {ID: "ci", Title: "Merge the dashboard", Status: "open", Waits: []beads.Wait{{Kind: beads.WaitCI, PR: 7}}},
		"done":   {ID: "done", Title: "Old step", Status: "closed"},
		"run":    {ID: "run", Title: "Build it", Status: "open", Executing: true, ExecSince: now.Unix()},
		"loop-a": {ID: "loop-a", Title: "A", Status: "open", BlockedBy: []string{"loop-b"}},
		"loop-b": {ID: "loop-b", Title: "B", Status: "open", BlockedBy: []string{"loop-a"}},
	}
	ctx := ctxWith(true, 0, graph)
	cases := []struct {
		name      string
		blockedBy []string
		actor     Actor
		root      string
		via       string
	}{
		{"two links to the user", []string{"mac"}, User, "gate", "Mac execution"},
		{"to CI", []string{"ci"}, External, "ci", "Merge the dashboard"},
		{"the user outranks CI", []string{"ci", "mac"}, User, "gate", "Mac execution"},
		{"finished blockers hold nothing", []string{"done"}, Agent, "dev", ""},
		{"behind running work", []string{"run"}, Agent, "run", "Build it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Decide(Bead{ID: "dev", Title: "Repair dev3", Status: "open", BlockedBy: c.blockedBy}, ctx)
			if r.Actor != c.actor || r.Bead != c.root || (c.via != "" && (len(r.Via) == 0 || r.Via[0] != c.via)) {
				t.Fatalf("got %+v, want actor=%s root=%s via=%s", r, c.actor, c.root, c.via)
			}
		})
	}
	parent := map[string]Bead{
		"mac":  {ID: "mac", Title: "Mac execution", Status: "open", HasOpenChildren: true, Children: []string{"run", "idle"}},
		"run":  {ID: "run", Title: "Run the guard", Status: "open", Executing: true, ExecSince: now.Add(-time.Hour).Unix(), ProgressAt: now.Add(-time.Hour).Unix()},
		"idle": {ID: "idle", Title: "Later step", Status: "open", Waits: []beads.Wait{{Kind: beads.WaitCI, PR: 3}}},
		"gate": {ID: "gate", Title: "Greenlight", Status: "open", NeedsUser: true, UserAsk: "approve it"},
		"mid":  {ID: "mid", Title: "Middle step", Status: "open", BlockedBy: []string{"gate"}},
	}
	pctx := ctxWith(true, 0, parent)
	if r := Decide(Bead{ID: "dev", Title: "Repair dev3", Status: "open", BlockedBy: []string{"mac"}}, pctx); r.Actor != Stuck || r.Bead != "run" || r.Reason != "blocked:no_progress" {
		t.Fatalf("a chain through a parent must reach its stuck child: %+v", r)
	}
	r := Decide(Bead{ID: "dev", Title: "Repair dev3", Status: "open", BlockedBy: []string{"mid"}}, pctx)
	if r.Actor != User || r.Reason != "blocked:decision" || r.Text != "Needs you: approve it (blocking “Middle step”)" || strings.Count(r.Text, "blocking") != 1 {
		t.Fatalf("chain text: %+v", r)
	}
	asked := map[string]Bead{
		"fixes":    {ID: "fixes", Title: "Prepare fixes", Status: "open", HasOpenChildren: true, Children: []string{"registry", "later"}, Waits: []beads.Wait{{Kind: beads.WaitUser}}},
		"registry": {ID: "registry", Title: "Restore registry access", Status: "open", NeedsUser: true, UserAsk: "can the owner restore access?"},
		"later":    {ID: "later", Title: "Validate", Status: "open", BlockedBy: []string{"registry"}},
		"quiet":    {ID: "quiet", Title: "Prepare rollout", Status: "open", HasOpenChildren: true, Children: []string{"later2"}, Waits: []beads.Wait{{Kind: beads.WaitUser}}},
		"later2":   {ID: "later2", Title: "Validate again", Status: "open", Waits: []beads.Wait{{Kind: beads.WaitCI, PR: 4}}},
	}
	actx := ctxWith(true, 0, asked)
	if r := Decide(asked["fixes"], actx); r.Actor != None || r.Reason != "children" {
		t.Fatalf("a parent whose child holds the question is carried by it: %+v", r)
	}
	if r := Decide(Bead{ID: "next", Title: "Review", Status: "open", BlockedBy: []string{"fixes"}}, actx); r.Actor != User || r.Bead != "registry" {
		t.Fatalf("behind a parent whose child holds the question: %+v", r)
	}
	if r := Decide(asked["quiet"], actx); r.Actor != Stuck || r.Reason != "invalid:no-card" || r.Bead != "quiet" {
		t.Fatalf("a parent waiting on the user with no question anywhere: %+v", r)
	}
	if r := Decide(graph["loop-a"], ctx); r.Actor == "" {
		t.Fatal("a dependency cycle produced no decision")
	}
	if r := Decide(Bead{ID: "x", Status: "open", BlockedBy: []string{"gate"}}, Context{Now: now}); r.Actor != None || r.Reason != "dependency" {
		t.Fatalf("without lookup a blocked bead is a dependency: %+v", r)
	}
}

func TestClosingGateAsksOnlyWithItsCard(t *testing.T) {
	ctx := Context{Now: time.Unix(1_800_000_000, 0)}
	gate := Bead{ID: "ws.9", Title: "Confirm done", Status: "open", ClosingGate: true}
	if r := Decide(gate, ctx); r.Actor != None || r.Reason != "closing" {
		t.Fatalf("closing gate before its card: %+v", r)
	}
	gate.ReadyToClose = true
	if r := Decide(gate, ctx); r.Actor != Done || r.Text != "Complete" {
		t.Fatalf("closing gate once the rest is finished: %+v", r)
	}
	gate.NeedsUser, gate.UserAsk = true, "Is it done?"
	if r := Decide(gate, ctx); r.Actor != Done || r.Text != "Complete" {
		t.Fatalf("a card on a Confirm done step replaced Complete: %+v", r)
	}
	gate.ReadyToClose = false
	if r := Decide(gate, ctx); r.Actor != None || r.Reason != "closing" {
		t.Fatalf("a card on a Confirm done step asked before the rest was finished: %+v", r)
	}
	if row := Rollup([]Result{{Actor: Done, Reason: "ready_to_close"}, {Actor: Agent, Reason: "executing", Who: "a"}}); row.Lead.Actor != Agent {
		t.Fatalf("open work leads over Complete: %+v", row)
	}
	row := Rollup([]Result{{Actor: None, Reason: "closing"}, {Actor: Agent, Reason: "executing", Who: "a"}})
	if row.Lead.Actor != Agent || row.Counts[None] != 0 {
		t.Fatalf("a quiet closing gate must not count: %+v", row)
	}
}

func TestRollupLeadsWithTheUserAndCountsTheRest(t *testing.T) {
	row := Rollup([]Result{
		{Actor: Agent, Reason: "executing", Who: "a", Since: 10},
		{Actor: Agent, Reason: "executing", Who: "b", Since: 20},
		{Actor: External, Reason: "wait:ci", Since: 5},
		{Actor: User, Reason: "decision", Text: "Needs you: approve", Since: 30},
		{Actor: None, Reason: "children"},
	})
	if row.Lead.Actor != User || row.Agents != 2 || row.Counts[External] != 1 || row.Counts[None] != 0 {
		t.Fatalf("row %+v", row)
	}
	if got := row.Summary(); got != "Needs you: approve · 2 agents working · 1 waiting outside" {
		t.Fatalf("summary %q", got)
	}
	stuck := Rollup([]Result{{Actor: Agent, Reason: "executing", Who: "a"}, {Actor: Stuck, Reason: "exited_without_report", Text: "Stuck: x"}})
	if stuck.Lead.Actor != Stuck {
		t.Fatalf("stuck work must lead over working: %+v", stuck)
	}
	if empty := Rollup(nil); empty.Lead.Actor != None {
		t.Fatalf("empty %+v", empty)
	}
}

func TestEveryDecisionHasAnActorAndText(t *testing.T) {
	// Property: whatever facts are set, the decision is never blank.
	flags := []func(*Bead){
		func(b *Bead) { b.Status = beads.StatusDeferred }, func(b *Bead) { b.NeedsUser = true }, func(b *Bead) { b.Executing = true },
		func(b *Bead) { b.ResultReady = true }, func(b *Bead) { b.ReadyQueued = true }, func(b *Bead) { b.HasOpenChildren = true },
		func(b *Bead) { b.Unverified = true }, func(b *Bead) { b.LaunchFailed = true }, func(b *Bead) { b.PendingQuestions = 1 },
		func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitCI, PR: 1}} }, func(b *Bead) { b.ExitedWithoutReport = true },
	}
	for mask := 0; mask < 1<<len(flags); mask++ {
		b := Bead{ID: "b", Title: "T", Status: "open"}
		for i, f := range flags {
			if mask&(1<<i) != 0 {
				f(&b)
			}
		}
		for _, busy := range []bool{true, false} {
			r := Decide(b, ctxWith(busy, time.Hour, nil))
			if r.Actor == "" || strings.TrimSpace(r.Text) == "" || r.Reason == "" {
				t.Fatalf("mask %b busy %v: blank decision %+v", mask, busy, r)
			}
		}
	}
}

func TestUsageCapacityPreservesNextActorGates(t *testing.T) {
	cause := "Account usage unavailable: exhausted"
	base := Bead{ID: "work", Title: "Work", Status: "open", ReadyQueued: true, UsageCapacity: cause, PlanningUsage: cause}
	cases := []struct {
		name   string
		change func(*Bead)
		actor  Actor
		reason string
	}{
		{"queued exhaustion", func(b *Bead) {}, Stuck, "usage_capacity"},
		{"paused", func(b *Bead) { b.Status = beads.StatusDeferred; b.Container = true }, None, "paused"},
		{"user decision", func(b *Bead) { b.NeedsUser = true }, User, "decision"},
		{"external wait", func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitCI, PR: 42}} }, External, "wait:ci"},
		{"running", func(b *Bead) { b.Executing = true; b.ProgressAt = now.Unix() }, Agent, "executing"},
		{"result review", func(b *Bead) { b.ResultReady = true }, Stuck, "agent_blocked"},
		{"saved answer", func(b *Bead) { b.HumanAnswerPending = true }, Agent, "apply_answer"},
		{"question", func(b *Bead) { b.QuestionWaiting = true }, Stuck, "agent_question"},
		{"dependency", func(b *Bead) { b.BlockedBy = []string{"gate"} }, User, "blocked:decision"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := base
			tt.change(&b)
			got := Decide(b, ctxWith(true, 0, map[string]Bead{"gate": {ID: "gate", Title: "Approve", Status: "open", NeedsUser: true}}))
			if got.Actor != tt.actor || got.Reason != tt.reason {
				t.Fatalf("got %+v, want %s/%s", got, tt.actor, tt.reason)
			}
		})
	}
}

// A card is the user's move only once what it waits on is done.
func TestCardWaitsOnItsBlocker(t *testing.T) {
	graph := map[string]Bead{
		"build": {ID: "build", Title: "Build the release", Status: "open", Executing: true, ExecSince: now.Unix()},
		"done":  {ID: "done", Title: "Old step", Status: "closed"},
	}
	ctx := ctxWith(true, 0, graph)
	card := Bead{ID: "gate", Title: "Approve the release", Status: "open", NeedsUser: true, UserAsk: "approve the release", BlockedBy: []string{"build"}}
	if r := Decide(card, ctx); r.Actor != Agent || r.Bead != "build" {
		t.Fatalf("a card behind running work: %+v", r)
	}
	card.BlockedBy = []string{"done"}
	if r := Decide(card, ctx); r.Actor != User || r.Reason != "decision" {
		t.Fatalf("a card whose blockers are done: %+v", r)
	}
	card.BlockedBy = []string{"build"}
	if r := Decide(card, Context{Now: now}); r.Actor != None || r.Reason != "dependency" {
		t.Fatalf("without lookup a blocked card waits on its dependency: %+v", r)
	}
}

func TestRollupLeadsWithTheRunningAgentOverAStepWaitingForIt(t *testing.T) {
	waiting := Result{Actor: Agent, Reason: "blocked:executing", Bead: "review", Since: 100, Via: []string{"Spec PR"}}
	running := Result{Actor: Agent, Reason: "executing", Bead: "spec", Since: 200, Who: "op-1"}
	for _, order := range [][]Result{{waiting, running}, {running, waiting}} {
		if got := Rollup(order).Lead; got.Bead != "spec" {
			t.Fatalf("the row is the agent at work, got lead %+v", got)
		}
	}
	// With no agent running in the row, the step waiting on another one leads.
	if got := Rollup([]Result{waiting}).Lead; got.Bead != "review" {
		t.Fatalf("got %+v", got)
	}
}

func TestCutoverReadsAsTheSwitchInProgress(t *testing.T) {
	before := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	running := func(mut func(b *Bead)) Bead {
		b := Bead{ID: "b", Title: "Make the read fast", Status: "open", PlanningCommander: "commander",
			Executing: true, ExecWorking: true, ExecRole: "operator-x", ExecSince: before(time.Minute), ProgressAt: before(time.Minute), RunRuntime: "codex", WantRuntime: "claude"}
		mut(&b)
		return b
	}
	cases := []struct {
		name   string
		bead   Bead
		actor  Actor
		reason string
		text   string
	}{
		{"switch asked", running(func(b *Bead) {
			b.Cutover, b.CutoverTo, b.CutoverFrom, b.CutoverSince = true, "claude", "codex", before(2*time.Minute)
		}),
			Agent, "cutover", "Switching to Claude: Codex agent saving its work (2 minutes)"},
		{"switch outlived the stop", running(func(b *Bead) {
			b.Cutover, b.CutoverTo, b.CutoverFrom, b.CutoverSince = true, "claude", "codex", before(20*time.Minute)
		}),
			Stuck, "cutover_stalled", "Stuck: the Codex agent asked to stop is still running (20 minutes)"},
		{"stop asked", running(func(b *Bead) { b.Cutover, b.CutoverFrom, b.CutoverSince = true, "codex", before(time.Minute) }),
			Agent, "cutover", "Stopping the agent: it is saving its work (1 minute)"},
		{"old agent gone, new one queued", Bead{ID: "b", Title: "Make the read fast", Status: "open", ReadyQueued: true, Cutover: true, CutoverTo: "claude", CutoverFrom: "codex", CutoverSince: before(3 * time.Minute)},
			Agent, "cutover_starting", "Switching to Claude: starting a Claude agent"},
		{"running on the other runtime", running(func(b *Bead) {}),
			Agent, "executing", "Working on \u201cMake the read fast\u201d on Codex, though the step asks for Claude"},
		{"running on the asked runtime", running(func(b *Bead) { b.RunRuntime = "claude" }),
			Agent, "executing", "Working on \u201cMake the read fast\u201d"},
		{"stopped, Commander re-plans", Bead{ID: "b", Title: "Make the read fast", Status: "open", Stopped: true, PlanningCommander: "commander"},
			Stuck, "agent_stopped", "Stopped: the agent on “Make the read fast” was stopped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Decide(c.bead, ctxWith(true, 0, nil))
			if r.Actor != c.actor || r.Reason != c.reason || r.Text != c.text {
				t.Fatalf("got %+v, want %s %s %q", r, c.actor, c.reason, c.text)
			}
		})
	}
}

func TestAFinishedAgentStepIsNeverTheCommanders(t *testing.T) {
	b := Bead{ID: "b", Title: "Ship the guard", Status: "open", PlanningCommander: "commander", ResultReady: true, ResultSince: now.Add(-time.Minute).Unix(), ResultClean: true}
	c := ctxWith(true, 0, nil)
	c.CommanderOn = func(who, bead string) bool { return true }
	if r := Decide(b, c); r.Actor != Agent || r.Reason != "result_accepting" || strings.Contains(r.Text, "Commander") {
		t.Fatalf("a clean result closes itself: %+v", r)
	}
}

// Track 3.5 on Nov 16: the live check (14.6) finished with an unaccepted
// result, and the Commander answered it with a fix step (14.14) that now
// blocks it and has a running agent. The track read orange "Stuck: Commander
// has not got to review the finished result"; it is the fix being worked.
func TestRunOutcomeWithAPlannedFollowUpWaitsOnIt(t *testing.T) {
	fix := Bead{ID: "fix", Title: "Fix forge-lib notify for apps", Status: "open", PlanningCommander: "commander",
		Executing: true, ExecWorking: true, ExecRole: "operator-x", ExecSince: now.Add(-30 * time.Minute).Unix(), ProgressAt: now.Add(-time.Minute).Unix()}
	for _, outcome := range []func(*Bead){
		func(b *Bead) { b.ResultReady = true; b.ResultSince = now.Add(-31 * time.Minute).Unix() },
		func(b *Bead) {
			b.StopReason = "Agent stopped: it crashed (exit code 1)"
			b.ExitedAt = now.Add(-31 * time.Minute).Unix()
		},
		func(b *Bead) { b.ExitedWithoutReport = true; b.ExitedAt = now.Add(-31 * time.Minute).Unix() },
	} {
		check := Bead{ID: "check", Title: "Live check of the forge-lib notification functions", Status: "open", PlanningCommander: "commander", BlockedBy: []string{"fix"}}
		outcome(&check)
		ctx := ctxWith(true, 0, map[string]Bead{"fix": fix, "check": check})
		got := Decide(check, ctx)
		if got.Actor != Agent || !strings.HasPrefix(got.Reason, "blocked:") {
			t.Fatalf("check with a running follow-up = %s/%s %q, want it waiting on the fix's agent", got.Actor, got.Reason, got.Text)
		}
		row := Rollup([]Result{Decide(fix, ctx), got})
		if row.Lead.Actor != Agent || len(row.Lead.Via) != 0 || !strings.Contains(row.Lead.Text, "Fix forge-lib notify") {
			t.Fatalf("track lead = %s %q via %v, want the fix being worked", row.Lead.Actor, row.Lead.Text, row.Lead.Via)
		}
	}
	// With no open blocker the result is still the Commander's to review.
	alone := Bead{ID: "check", Title: "Live check", Status: "open", PlanningCommander: "commander", ResultReady: true, ResultSince: now.Add(-time.Minute).Unix()}
	if got := Decide(alone, ctxWith(true, 0, nil)); got.Reason != "agent_blocked" {
		t.Fatalf("result with no follow-up = %s, want agent_blocked", got.Reason)
	}
}

// Track 3.2 on Nov 16: its agent fixed review findings and watched CI for over
// an hour (tool calls every few minutes) without writing a progress note, and
// the row read "Stuck: no progress for 1 hour". The agent's own events decide.
func TestAnActiveAgentIsWorkingWhateverTheProgressClock(t *testing.T) {
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	run := func(f func(*Bead)) Result {
		b := Bead{ID: "b", Title: "Build Grafana alerting", Status: "open", PlanningCommander: "commander",
			Executing: true, ExecWorking: true, ExecRole: "operator-x", ExecSince: ago(80 * time.Minute), ProgressAt: ago(80 * time.Minute)}
		f(&b)
		return Decide(b, ctxWith(true, 0, nil))
	}
	if r := run(func(b *Bead) { b.ExecActiveAt = ago(30 * time.Second) }); r.Actor != Agent || r.Reason != "executing" {
		t.Fatalf("active agent = %s/%s %q, want working", r.Actor, r.Reason, r.Text)
	}
	if r := run(func(b *Bead) { b.ExecActiveAt = ago(25 * time.Minute) }); r.Actor != Stuck || !strings.Contains(r.Text, "done nothing for") {
		t.Fatalf("quiet agent = %s %q, want stuck naming its idle time", r.Actor, r.Text)
	}
	if r := run(func(b *Bead) {
		b.ExecActiveAt = ago(time.Minute)
		b.ExecSince, b.ProgressAt = ago(4*time.Hour), ago(4*time.Hour)
	}); r.Actor != Stuck || r.Reason != "looping" {
		t.Fatalf("busy 4h with no progress = %s/%s, want looping", r.Actor, r.Reason)
	}
	// Unknown activity keeps the old progress-clock rule.
	if r := run(func(b *Bead) {}); r.Actor != Stuck || r.Reason != "no_progress" {
		t.Fatalf("no activity clock, 80 min = %s/%s, want no_progress", r.Actor, r.Reason)
	}
}

// Track 4.7 on Nov 16: the memory step's agent finished a turn and sat idle,
// held off dev-6 for the release. Idle with a recorded wait reads that wait;
// idle with none is the Commander's to answer, not "no progress".
func TestAnIdleAgentReadsItsWaitOrTheCommandersMove(t *testing.T) {
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	b := Bead{ID: "b", Title: "Memory: cache charts in Postgres", Status: "open", PlanningCommander: "commander",
		Executing: true, ExecRole: "operator-x", ExecSince: ago(2 * time.Hour), ProgressAt: ago(90 * time.Minute), ExecActiveAt: ago(30 * time.Minute)}
	if r := Decide(b, ctxWith(true, 0, nil)); r.Actor != Stuck || r.Reason != "idle_agent" {
		t.Fatalf("idle agent, no wait = %s/%s %q, want the Commander's move", r.Actor, r.Reason, r.Text)
	}
	b.Waits = []beads.Wait{{Kind: beads.WaitUntil, Ref: "release 2a deployed", Until: now.Add(time.Hour)}}
	if r := Decide(b, ctxWith(true, 0, nil)); r.Actor != External || !strings.Contains(r.Text, "release 2a deployed") {
		t.Fatalf("idle agent with a wait = %s %q, want that wait", r.Actor, r.Text)
	}
	b.Waits, b.ExecActiveAt = nil, ago(5*time.Minute)
	if r := Decide(b, ctxWith(true, 0, nil)); r.Actor != Agent {
		t.Fatalf("agent idle 5 min = %s, want still its own", r.Actor)
	}
}

func TestAHeldMergeCardWaitsOnItsChecks(t *testing.T) {
	b := Bead{ID: "m", Title: "Merge PR 3772", Status: "open", PlanningCommander: "commander", MergeWait: "PR #3772's Integration tests (failure)"}
	if r := Decide(b, ctxWith(true, 0, nil)); r.Actor != External || r.Text != "Waiting on PR #3772's Integration tests (failure)" {
		t.Fatalf("held merge = %s %q, want waiting on its checks", r.Actor, r.Text)
	}
}

// Release 2b (vibe-apps-zq9h.6.76) on Nov 17: the Commander asked its agent to
// stop at 03:06, the agent's save-and-stop "Final report: STOPPED" landed as
// the step's result, cmdrd forced the stop at 03:16, and at 14:31 the step was
// re-planned into a six-step chain whose step 2 has a running agent. Tracks
// 4.2 and 4.5, blocked on it, read "commander to review the finished result"
// for 12 hours; they wait on the chain's live step.
func TestRePlannedStepIsCarriedByItsNewerChildren(t *testing.T) {
	result := now.Add(-12 * time.Hour).Unix()
	build := Bead{ID: "c1", Title: "2b chain 1/6: build the train", Status: "closed"}
	fixOrDrop := Bead{ID: "c2", Title: "2b chain 2/6: fix or drop the 03:17 validate failures", Status: "open", PlanningCommander: "commander",
		Executing: true, ExecWorking: true, ExecRole: "operator-y", ExecSince: now.Add(-30 * time.Minute).Unix(), ProgressAt: now.Add(-time.Minute).Unix()}
	gate := Bead{ID: "c3", Title: "2b chain 3/6 (gate): one full validate", Status: "open", PlanningCommander: "commander", BlockedBy: []string{"c2"}}
	release := Bead{ID: "rel", Title: "Release 2b", Status: "open", PlanningCommander: "commander",
		ResultReady: true, ResultSince: result, HasOpenChildren: true, Children: []string{"c2", "c3"}}
	chart := Bead{ID: "chart", Title: "Chart open", Status: "open", PlanningCommander: "commander", BlockedBy: []string{"rel"}}
	ctx := ctxWith(true, 0, map[string]Bead{"c1": build, "c2": fixOrDrop, "c3": gate, "rel": release, "chart": chart})

	got := Decide(chart, ctx)
	if got.Actor != Agent || !strings.Contains(got.Text, "2b chain 2/6") {
		t.Fatalf("blocked track = %s/%s %q, want it waiting on the chain's working step", got.Actor, got.Reason, got.Text)
	}
	if len(got.Via) == 0 || got.Via[0] != "Release 2b" {
		t.Fatalf("via = %v, want it to name Release 2b", got.Via)
	}

	// Children with nothing newer than the outcome do not supersede it.
	stale := fixOrDrop
	stale.Executing, stale.ExecWorking, stale.ExecSince, stale.ProgressAt = false, false, 0, result-60
	ctx2 := ctxWith(true, 0, map[string]Bead{"c2": stale, "c3": gate, "rel": release})
	if r := Decide(release, ctx2); !strings.HasSuffix(r.Reason, "agent_blocked") {
		t.Fatalf("old children = %s, want the result still owed a review", r.Reason)
	}
}

// The same 2b step: a result recorded by the agent the Commander stopped is its
// save-and-stop note. Re-planning is owed, never "review the finished result".
func TestCommanderStoppedStepOwesReplanNotReview(t *testing.T) {
	b := Bead{ID: "rel", Title: "Release 2b", Status: "open", PlanningCommander: "commander",
		ResultReady: true, ResultSince: now.Add(-time.Hour).Unix(), Stopped: true, ProgressAt: now.Add(-time.Hour).Unix()}
	got := Decide(b, ctxWith(true, 0, nil))
	if got.Reason != "agent_stopped" || strings.Contains(got.Text, "Commander") {
		t.Fatalf("stopped step = %s %q, want re-plan owed, not a review", got.Reason, got.Text)
	}
}
