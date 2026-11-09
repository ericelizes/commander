package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/claudeacct"
)

// ErrNoUsage refuses a Manager start when no account it may run on has usage left.
var ErrNoUsage = errors.New("chat: no account has usage left")

// managerPlan is where a Manager's next process runs.
type managerPlan struct {
	runtime string // RuntimeClaude or RuntimeCodex
	account string // Claude account (RuntimeClaude only)
	intent  string // the account the role is headed for, when it runs elsewhere
}

// planManagerStart picks the runtime and account of a Manager's new process
// (decision d82: any account with usage left, never one that is out). A
// never-started Manager (fresh, no runtime file) draws its runtime from the
// Manager share (Codex only with hooks and usage left). A runtime a person
// pinned stays; an unpinned Codex Manager whose Codex is out runs on Claude.
// On Claude the role's intent (the account a usage move took it off) comes
// first once it has usage again, then the account it runs on, then the next
// logged-in account with usage; with every Claude account out an unpinned
// Manager goes to Codex if Codex has usage and hooks. Nothing left = ErrNoUsage.
// It writes nothing.
func planManagerStart(home, role string, fresh bool, usage map[string]Usage, now time.Time, hooksReady func() bool) (managerPlan, error) {
	codexOut := usage[UsageKey(RuntimeCodex, "")].Out(now)
	codexOK := func() bool { return !codexOut && hooksReady() }
	pinned := RuntimePinned(home, role)
	if fresh && !fileExists(RuntimeFile(home, role)) &&
		CodexByShare(strings.TrimPrefix(role, "manager-"), agentrt.CodexManagerShare()) && codexOK() {
		return managerPlan{runtime: RuntimeCodex}, nil
	}
	if ReadRuntime(home, role) == RuntimeCodex && (pinned || !codexOut) {
		return managerPlan{runtime: RuntimeCodex}, nil
	}
	out := func(a string) bool { return usage[UsageKey(RuntimeClaude, a)].Out(now) }
	cur := ReadAccount(home, role)
	want := ReadAccountIntent(home, role)
	if want == "" {
		want = cur
	}
	switch {
	case want != cur && !out(want) && claudeacct.Get(home, want).LoggedIn:
		return managerPlan{runtime: RuntimeClaude, account: want}, nil
	case !out(cur):
		return managerPlan{runtime: RuntimeClaude, account: cur, intent: want}, nil
	}
	if next := NextAccountWith(home, want, usage, now); next != "" {
		return managerPlan{runtime: RuntimeClaude, account: next, intent: want}, nil
	}
	if !pinned && codexOK() {
		return managerPlan{runtime: RuntimeCodex}, nil
	}
	codex := "Codex is out of usage"
	switch {
	case pinned:
		codex = "the runtime is pinned to Claude"
	case !codexOut:
		codex = "Codex does not run hooks"
	}
	return managerPlan{}, fmt.Errorf("%w for %s: every logged-in Claude account is out and %s (cmdr account list)", ErrNoUsage, role, codex)
}

// steerManager applies planManagerStart before a Manager's new process and
// says what moved ("" when nothing did). Every Manager start the engine makes
// runs it: EnsureNative, Open, mail delivery, the app's Start, restarts.
func (e *Engine) steerManager(role string, fresh bool) (moved string, changed bool, err error) {
	home, now := e.opts.Home, e.opts.Now()
	plan, err := planManagerStart(home, role, fresh, e.opts.Usage.All(now), now, e.hooksReady())
	if err != nil {
		return "", false, err
	}
	prevRT, prevAcct := ReadRuntime(home, role), ReadAccount(home, role)
	if plan.runtime != prevRT {
		if err := WriteAutoRuntime(home, role, plan.runtime); err != nil {
			return "", false, err
		}
		changed = true
	}
	if plan.runtime == RuntimeCodex {
		if prevRT != RuntimeCodex && !fresh {
			return "moved from Claude account " + prevAcct + " to Codex (no Claude account has usage left)", true, nil
		}
		return "", changed, nil
	}
	intent := plan.intent
	if intent == plan.account {
		intent = ""
	}
	if plan.account != prevAcct || intent != ReadAccountIntent(home, role) {
		if intent == "" {
			err = WriteAccount(home, role, plan.account)
		} else {
			err = WriteRunAccount(home, role, plan.account, intent)
		}
		if err != nil {
			return "", changed, err
		}
		changed = true
		if err := claudeacct.Sync(home, plan.account); err != nil {
			e.opts.Logf("chat: sync account %s: %v", plan.account, err)
		}
	}
	switch {
	case prevRT == RuntimeCodex:
		return "moved from Codex (out of usage) to Claude account " + plan.account, true, nil
	case plan.account == prevAcct:
		return "", changed, nil
	case intent == "":
		return "moved back to Claude account " + plan.account + " (usage left again)", true, nil
	}
	return "moved from Claude account " + prevAcct + " (out of usage) to " + plan.account, true, nil
}

func (e *Engine) hooksReady() func() bool {
	if e.opts.CodexHooksReady != nil {
		return e.opts.CodexHooksReady
	}
	return CodexHooksReady
}

// moveManager restarts a Manager that stopped at a usage limit, so its start
// steers it to a provider with usage left, and tells it to re-read its
// handover. A chat that is not running moves at its next start.
func (e *Engine) moveManager(c *chatRole) {
	notice := func(text string) {
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "usage_limit_switch", Text: text})
	}
	p, err := e.idleProcess(c.role, "account")
	if err != nil || p == nil {
		if err != nil {
			notice(fmt.Sprintf("Moving after the usage limit failed: %v", err))
		}
		return
	}
	now := e.opts.Now()
	plan, err := planManagerStart(e.opts.Home, c.role, false, e.opts.Usage.All(now), now, e.hooksReady())
	if err != nil {
		notice("Not moved: " + err.Error())
		return
	}
	if plan.runtime == p.runtime() && (plan.runtime == RuntimeCodex || plan.account == accountName(p.spec.Account)) {
		notice("Not moved: its runtime is pinned to " + plan.runtime + ".")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := e.restart(ctx, c.role, e.moveOptions(c.role)); err != nil {
		notice(fmt.Sprintf("Moving after the usage limit failed: %v", err))
	}
}

// startMoved reports a move steerManager made at a start: a notice in the
// chat and, through OnUsageMove, a note on the workstream bead.
func (e *Engine) startMoved(c *chatRole, moved string) {
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "usage_limit_switch", Text: "Usage: " + moved + "."})
	if f := e.opts.OnUsageMove; f != nil {
		go f(c.role, c.role+" "+moved+".")
	}
}
