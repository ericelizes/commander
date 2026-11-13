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
	past := beads.Wait{Kind: beads.WaitUntil, Ref: "rollout window", Until: now.Add(-time.Hour)}
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
		{"held", open(func(b *Bead) { b.Held = true; b.HoldReason = "until the batch lands" }), false, 0, None, "paused", "Paused: until the batch lands"},
		{"deferred", open(func(b *Bead) { b.Status = beads.StatusDeferred }), false, 0, None, "paused", "Paused"},
		{"held beats a user ask", open(func(b *Bead) { b.Held = true; b.NeedsUser = true }), false, 0, None, "paused", "Paused"},
		{"saved answer on held work goes to Commander", open(func(b *Bead) { b.Held = true; b.HumanAnswerPending = true }), true, 0, Commander, "apply_answer", "Commander: apply"},
		{"user decision", open(func(b *Bead) { b.NeedsUser = true; b.UserAsk = "approve the refund guard" }), false, 0, User, "decision", "Needs you: approve the refund guard"},
		{"user decision beats a running agent", open(func(b *Bead) { b.NeedsUser = true; b.Executing = true }), false, 0, User, "decision", "Needs you"},
		{"waiting on the user with a recorded ask", open(func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitUser}}; b.UserAsk = "pick the rollout day" }), false, 0, User, "waiting_user", "Needs you: pick the rollout day"},
		{"waiting on the user with nothing asked", open(func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitUser}} }), true, 0, Commander, "record_question", "Commander: write down what"},
		{"agent question, Commander busy", open(func(b *Bead) { b.PendingQuestions = 1 }), true, 0, Commander, "answer_question", "Commander: answer"},
		{"agent question, Commander idle long", open(func(b *Bead) { b.QuestionWaiting = true }), false, time.Hour, Stuck, "commander_idle:answer_question", "Stuck: Commander"},
		{"launch failed", open(func(b *Bead) { b.LaunchFailed = true; b.LaunchError = "no worktree" }), false, 0, Stuck, "launch_failed", "Stuck: the agent could not start (no worktree)"},
		{"exited without report", open(func(b *Bead) { b.ExitedWithoutReport = true; b.ExitedAt = ago(time.Minute) }), false, 0, Stuck, "exited_without_report", "Stuck: the agent stopped"},
		{"executing", open(func(b *Bead) { b.Executing = true; b.ExecRole = "operator-exec-1"; b.ExecSince = ago(5 * time.Minute) }), false, 0, Agent, "executing", "Working on"},
		{"executing with recent progress", open(func(b *Bead) { b.Executing = true; b.ExecSince = ago(time.Hour); b.ProgressAt = ago(time.Minute) }), false, 0, Agent, "executing", "Working on"},
		{"idle agent with no progress", open(func(b *Bead) { b.Executing = true; b.ExecSince = ago(time.Hour); b.ProgressAt = ago(30 * time.Minute) }), false, 0, Stuck, "no_progress", "Stuck: no progress for 30 minutes"},
		{"agent mid-turn on a long run", open(func(b *Bead) { b.Executing = true; b.ExecWorking = true; b.ProgressAt = ago(40 * time.Minute) }), false, 0, Agent, "executing", "Working on"},
		{"agent mid-turn far too long", open(func(b *Bead) { b.Executing = true; b.ExecWorking = true; b.ProgressAt = ago(2 * time.Hour) }), false, 0, Stuck, "no_progress", "Stuck: no progress for 2 hours"},
		{"claim just made", open(func(b *Bead) { b.Unverified = true; b.UnverifiedSince = ago(time.Minute) }), false, 0, Agent, "starting", "Starting an agent"},
		{"claim with no process", open(func(b *Bead) { b.Unverified = true; b.UnverifiedSince = ago(10 * time.Minute) }), false, 0, Stuck, "unverified", "Stuck: claimed"},
		{"result to review, Commander busy", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Minute) }), true, 0, Commander, "review_result", "Commander: review"},
		{"result to review, Commander just idle", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Minute) }), false, time.Minute, Commander, "review_result", "Commander to review"},
		{"result unreviewed for long", open(func(b *Bead) { b.ResultReady = true; b.ResultSince = ago(time.Hour) }), false, time.Hour, Stuck, "commander_idle:review_result", "Stuck: Commander"},
		{"waiting on CI", open(func(b *Bead) { b.Waits = []beads.Wait{ci} }), false, 0, External, "wait:ci", "Waiting on CI #42"},
		{"waiting on a reviewer", open(func(b *Bead) { b.Waits = []beads.Wait{review} }), false, 0, External, "wait:review", "Waiting on review by alex"},
		{"waiting for a date", open(func(b *Bead) { b.Waits = []beads.Wait{future} }), false, 0, External, "wait:until", "Waiting on rollout window"},
		{"a date that passed is owed", open(func(b *Bead) { b.Waits = []beads.Wait{past} }), true, 0, Commander, "wait_over", "Commander: act on"},
		{"legacy Manager handoff is Commander's", open(func(b *Bead) { b.Waits = []beads.Wait{handoff} }), true, 0, Commander, "wait_over", "Commander: act on"},
		{"user wait beats CI", open(func(b *Bead) { b.Waits = []beads.Wait{ci, {Kind: beads.WaitUser}}; b.UserAsk = "approve" }), false, 0, User, "waiting_user", "Needs you"},
		{"queued for capacity", open(func(b *Bead) { b.ReadyQueued = true }), false, 0, Agent, "queued", "Queued"},
		{"ready but nothing can run it", open(func(b *Bead) { b.ReadyQueued = true; b.NoCapableSlot = true }), false, 0, Stuck, "no_capable_slot", "Stuck: no agent slot"},
		{"parent carried by children", open(func(b *Bead) { b.HasOpenChildren = true }), false, 0, None, "children", "Carried"},
		{"parent with its own user ask", open(func(b *Bead) { b.HasOpenChildren = true; b.NeedsUser = true }), false, 0, User, "decision", "Needs you"},
		{"nothing recorded, Commander busy", open(func(b *Bead) {}), true, 0, Commander, "plan", "Commander: plan"},
		{"nothing recorded, Commander idle long", open(func(b *Bead) {}), false, time.Hour, Stuck, "commander_idle:plan", "Stuck: Commander has not started to plan"},
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
		{"finished blockers hold nothing", []string{"done"}, Commander, "dev", ""},
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
	if r := Decide(asked["quiet"], actx); r.Actor != Commander || r.Reason != "record_question" || r.Bead != "quiet" {
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
		func(b *Bead) { b.Held = true }, func(b *Bead) { b.NeedsUser = true }, func(b *Bead) { b.Executing = true },
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
		{"paused", func(b *Bead) { b.Held = true }, None, "paused"},
		{"user decision", func(b *Bead) { b.NeedsUser = true }, User, "decision"},
		{"external wait", func(b *Bead) { b.Waits = []beads.Wait{{Kind: beads.WaitCI, PR: 42}} }, External, "wait:ci"},
		{"running", func(b *Bead) { b.Executing = true; b.ProgressAt = now.Unix() }, Agent, "executing"},
		{"result review", func(b *Bead) { b.ResultReady = true }, Commander, "review_result"},
		{"saved answer", func(b *Bead) { b.HumanAnswerPending = true }, Commander, "apply_answer"},
		{"question", func(b *Bead) { b.QuestionWaiting = true }, Commander, "answer_question"},
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
