package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	pin     string // the runtime a person pinned ("" = unpinned): kept when it runs elsewhere
	leave   string // why a Codex run goes to Claude
}

// ManagerPin is the runtime a person chose for a Manager, "" when a share (or
// nothing) chose it: the runtime file a person wrote, or, while a Codex pin
// runs on Claude for want of Codex usage or hooks, the pin that fallback keeps
// (RuntimeFallbackFile).
func ManagerPin(home, role string) string {
	if !RuntimePinned(home, role) {
		return ""
	}
	if b, err := os.ReadFile(RuntimeFallbackFile(home, role)); err == nil && ValidRuntime(strings.TrimSpace(string(b))) {
		return strings.TrimSpace(string(b))
	}
	return ReadRuntime(home, role)
}

// RuntimeFallbackFile holds a pinned Manager's runtime while the start steer
// runs it elsewhere (decision d82); a person's runtime or account choice
// removes it.
func RuntimeFallbackFile(home, role string) string {
	return filepath.Join(home, "chat", role+".runtime-fallback")
}

// planManagerStart picks the runtime and account of a Manager's new process
// (decision d82: any account with usage left, never one that is out). A
// never-started Manager (fresh, no runtime file) draws its runtime from the
// Manager share (Codex only with hooks and usage left). A Codex pin runs on
// Codex while Codex has usage and hooks, else on Claude with the pin kept (it
// goes back once Codex can run it); a Claude pin never goes to Codex. An
// unpinned Codex Manager stays on Codex while it has usage, unless a share drew
// it there and the share is now 0 and Claude has usage. On Claude the role's
// intent (the account a usage move took it off) comes first once it has usage
// again, then the account it runs on, then the next logged-in account with
// usage; with every Claude account out an unpinned Manager goes to Codex if
// Codex has usage and hooks. Nothing left = ErrNoUsage. It writes nothing.
func planManagerStart(home, role string, fresh bool, usage map[string]Usage, now time.Time, hooksReady func() bool) (managerPlan, error) {
	codexOut := usage[UsageKey(RuntimeCodex, "")].Out(now)
	codexOK := func() bool { return !codexOut && hooksReady() }
	pin := ManagerPin(home, role)
	if fresh && !fileExists(RuntimeFile(home, role)) &&
		CodexByShare(strings.TrimPrefix(role, "manager-"), agentrt.CodexManagerShare()) && codexOK() {
		return managerPlan{runtime: RuntimeCodex}, nil
	}
	if pin == RuntimeCodex && codexOK() {
		return managerPlan{runtime: RuntimeCodex, pin: pin}, nil
	}
	onCodex := ReadRuntime(home, role) == RuntimeCodex
	drawn := onCodex && fileExists(RuntimeAutoFile(home, role)) && agentrt.CodexManagerShare() <= 0
	if pin == "" && onCodex && !codexOut && !drawn {
		return managerPlan{runtime: RuntimeCodex}, nil
	}
	leave := "out of usage"
	switch {
	case !codexOut && pin == RuntimeCodex:
		leave = "Codex does not run hooks"
	case !codexOut:
		leave = "the Manager share no longer draws Codex"
	}
	out := func(a string) bool { return usage[UsageKey(RuntimeClaude, a)].Out(now) }
	cur := ReadAccount(home, role)
	want := ReadAccountIntent(home, role)
	if want == "" {
		want = cur
	}
	switch {
	case want != cur && !out(want) && claudeacct.Get(home, want).LoggedIn:
		return managerPlan{runtime: RuntimeClaude, account: want, pin: pin, leave: leave}, nil
	case !out(cur):
		return managerPlan{runtime: RuntimeClaude, account: cur, intent: want, pin: pin, leave: leave}, nil
	}
	if next := NextAccountWith(home, want, usage, now); next != "" {
		return managerPlan{runtime: RuntimeClaude, account: next, intent: want, pin: pin, leave: leave}, nil
	}
	if pin == "" && codexOK() {
		return managerPlan{runtime: RuntimeCodex}, nil
	}
	codex := "Codex is out of usage"
	if !codexOut {
		codex = "Codex does not run hooks"
	}
	switch pin {
	case RuntimeClaude:
		codex = "the runtime is pinned to Claude"
	case RuntimeCodex:
		codex = "the runtime is pinned to Codex and " + codex
	}
	return managerPlan{}, fmt.Errorf("%w for %s: every logged-in Claude account is out and %s (cmdr account list)", ErrNoUsage, role, codex)
}

// writeManagerRuntime records the plan's runtime: a pinned role stays pinned
// (no auto marker) and, while it runs off its pin, keeps the pin in
// RuntimeFallbackFile; an unpinned one stays a share's choice.
func writeManagerRuntime(home, role string, plan managerPlan) error {
	if plan.pin == "" {
		return WriteAutoRuntime(home, role, plan.runtime)
	}
	if err := WriteEffectiveRuntime(home, role, plan.runtime); err != nil {
		return err
	}
	if plan.runtime == plan.pin {
		return removeFallback(home, role)
	}
	return os.WriteFile(RuntimeFallbackFile(home, role), []byte(plan.pin+"\n"), 0o600)
}

func removeFallback(home, role string) error {
	if err := os.Remove(RuntimeFallbackFile(home, role)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// steerManager applies planManagerStart before a Manager's new process and
// says what moved ("" when nothing did). Every Manager start the engine makes
// runs it: EnsureNative, Open, mail delivery, the app's Start, restarts.
func (e *Engine) steerManager(role string, fresh bool) (moved string, changed bool, err error) {
	home, now := e.opts.Home, e.opts.Now()
	plan, err := planChatStart(home, role, fresh, e.opts.Usage.All(now), now, e.hooksReady())
	if err != nil {
		return "", false, err
	}
	prevRT, prevAcct := ReadRuntime(home, role), ReadAccount(home, role)
	if plan.runtime != prevRT {
		if err := writeManagerRuntime(home, role, plan); err != nil {
			return "", false, err
		}
		changed = true
	}
	if plan.runtime == RuntimeCodex {
		switch {
		case prevRT == RuntimeCodex || fresh:
			return "", changed, nil
		case plan.pin == RuntimeCodex:
			return "moved back to Codex (its pinned runtime, usage left again)", true, nil
		}
		return "moved from Claude account " + prevAcct + " to Codex (no Claude account has usage left)", true, nil
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
	case prevRT == RuntimeCodex && plan.pin == RuntimeCodex:
		return "moved from Codex (" + plan.leave + ") to Claude account " + plan.account + "; its Codex pin stays and it goes back once Codex can run it", true, nil
	case prevRT == RuntimeCodex:
		return "moved from Codex (" + plan.leave + ") to Claude account " + plan.account, true, nil
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
	plan, err := planChatStart(e.opts.Home, c.role, false, e.opts.Usage.All(now), now, e.hooksReady())
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
