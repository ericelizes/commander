package spawn

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

// ReasonClaudeCodexOut records a run that would have gone to Codex (a pin or a
// share draw) and runs on Claude because Codex reports no usage left.
const ReasonClaudeCodexOut = chat.RuntimeClaude + "-codex-out"
const ReasonCodexClaudeOut = chat.RuntimeCodex + "-claude-out"

// readUsage and usageNow are what a spawn knows of plan usage: the file cmdrd
// keeps from the rate-limit reports its chats and bridges see (the same one
// `cmdr account list` reads). Tests replace them.
var (
	readUsage = chat.ReadUsage
	usageNow  = time.Now
)

// providerPick is what resolveProvider chose before usage steering, kept so
// applyProvider can write it once the spawn can no longer refuse.
type providerPick struct {
	asked    SpawnRequest // the request as asked (explicit runtime/account)
	runtime  string
	reason   string
	account  string
	pinned   bool
	wasCodex bool
}

// resolveProvider picks what a host Operator runs on: the runtime
// (pickOperatorRuntime), the Claude account (pickOperatorAccount), then
// steerByUsage, so no Operator starts on a runtime or account that reports no
// usage left. A request that named the runtime or account itself is refused
// with the way out; automatic choices move to a guarded provider with usage. It
// writes nothing: applyProvider records the choice and any move once the
// spawn's own refusals (lock, branch, stop) are behind it, so a refused spawn
// leaves the role's runtime, account and intent as they were.
func (s *Spawner) resolveProvider(ctx context.Context, req SpawnRequest, agentName string) (SpawnRequest, error) {
	if (!req.Resume || req.UsageRecovery) && len(req.AgentAccounts) > 0 && req.Runtime == "" && req.Account == "" {
		if _, ok := firstAgentAccount(req); !ok {
			return req, fmt.Errorf("spawn: %w among connected accounts for this execution", chat.ErrNoUsage)
		}
	}
	asked := req
	pick := &providerPick{asked: asked, wasCodex: wasOnCodex(req, agentName)}
	selection := req
	// A checkpoint binds this continuation, independently of persistent user intent.
	if req.ResumeRuntime != "" && !req.UsageRecovery {
		selection.Runtime = req.ResumeRuntime
	}
	pick.runtime, pick.reason, pick.pinned = pickOperatorRuntime(selection, agentName)
	if req.UsageRecovery && asked.Runtime != "" && pick.runtime != asked.Runtime {
		return req, fmt.Errorf("%w: pinned runtime %s is unavailable", chat.ErrNoUsage, asked.Runtime)
	}
	req.Runtime, req.runtimeReason = pick.runtime, pick.reason
	if pick.reason == ReasonAgentAccounts && req.Account == "" {
		if p, ok := firstAgentAccount(req); ok && p.Account != "" && p.Runtime == chat.RuntimeClaude && (len(req.AgentAccounts) > 0 || req.UsageRecovery || slices.Contains(agentOrder(req), p.Account)) {
			req.Account = p.Account
		}
	}
	acct, err := pickOperatorAccount(req, agentName)
	if err != nil {
		return req, fmt.Errorf("spawn: %w", err)
	}
	pick.account = acct
	req.Account = acct
	req.pick = pick
	home := agentrt.CommanderHome()
	mv, err := steerByUsage(home, readUsage(home), usageNow(), req.Runtime, req.Account, usageRuntimeIntent(selection, agentName, pick.reason), asked.Account)
	if err != nil {
		return req, fmt.Errorf("spawn: %s: %w: %w", agentName, chat.ErrNoUsage, err)
	}
	if mv.note == "" {
		return req, nil
	}
	mv.fromRuntime, mv.fromAccount = req.Runtime, req.Account
	if mv.runtime != req.Runtime {
		req.Runtime, req.runtimeReason = mv.runtime, ReasonClaudeCodexOut
		if mv.runtime == chat.RuntimeCodex {
			req.runtimeReason = ReasonCodexClaudeOut
		}
	}
	req.Account = mv.account
	req.moved = mv.note
	req.move = &mv
	return req, nil
}

// applyProvider writes what resolveProvider chose, in one step once nothing
// in the spawn refuses any more and before the launcher starts: the role's
// runtime (pin, effective or drawn; a new run drops its Codex thread), the
// "was on Codex" note of a restart moved to Claude, the account, then any
// usage move (applyUsageMove).
func (s *Spawner) applyProvider(ctx context.Context, req SpawnRequest, agentName string) error {
	p := req.pick
	if p == nil {
		return nil
	}
	writeOperatorRuntime(p.asked, agentName, p.runtime, p.pinned)
	s.offCodexNote(ctx, p.asked, agentName, p.runtime, p.reason, p.wasCodex)
	if err := chat.WriteAccount(agentrt.CommanderHome(), agentName, p.account); err != nil {
		return fmt.Errorf("spawn: %w", err)
	}
	return s.applyUsageMove(ctx, req, agentName)
}

// applyUsageMove records the move resolveProvider decided (applyProvider
// calls it after the pick's own writes): the role's runtime (a pin stays for
// when Codex has usage again, like the hooks fallback) and account (the
// account it was headed for is kept as its intent, chat.WriteRunAccount), and
// a cmdrd: comment on the task bead.
func (s *Spawner) applyUsageMove(ctx context.Context, req SpawnRequest, agentName string) error {
	mv := req.move
	if mv == nil {
		return nil
	}
	home := agentrt.CommanderHome()
	if mv.runtime != mv.fromRuntime {
		if chat.ReadRuntimePin(home, agentName) != "" {
			_ = chat.WriteEffectiveRuntime(home, agentName, mv.runtime)
		} else {
			_ = chat.WriteAutoRuntime(home, agentName, mv.runtime)
		}
	}
	if mv.account != mv.fromAccount {
		if err := chat.WriteRunAccount(home, agentName, mv.account, mv.fromAccount); err != nil {
			return fmt.Errorf("spawn: %w", err)
		}
	}
	note := fmt.Sprintf("cmdrd: %s %s", agentName, mv.note)
	if err := s.beads.AddComment(ctx, req.ProjectPath, req.TaskBeadID, note); err != nil {
		log.Printf("spawn: note the usage move on %s: %v", req.TaskBeadID, err)
	}
	return nil
}

// CheckUsage refuses, before anything is created or ended, a spawn the
// spawner would refuse for usage: it picks the runtime and account exactly as
// resolveProvider does (the role's pin and account on a respawn, the Manager's
// account, the request's names) but writes nothing. managerRole is the owning
// workstream's Manager. A new run whose runtime is left to the share draw on a
// task bead not made yet is refused only when both runtimes would be.
func CheckUsage(req SpawnRequest, agentName, managerRole string) error {
	req.managerRole = managerRole
	if req.ResumeRuntime != "" && !req.UsageRecovery {
		req.Runtime = req.ResumeRuntime
	}
	home := agentrt.CommanderHome()
	usage, now := readUsage(home), usageNow()
	rt, reason, _ := pickOperatorRuntime(req, agentName)
	runtimes := []string{rt}
	if rt == chat.RuntimeClaude && req.Runtime == "" && req.Account == "" && !req.Resume && slices.Contains(agentOrder(req), chat.RuntimeCodex) && codexHooksReady() {
		runtimes = append(runtimes, chat.RuntimeCodex)
	}
	var err error
	for _, rt := range runtimes {
		r := req
		r.Runtime = rt
		acct, aerr := pickOperatorAccount(r, agentName)
		if aerr != nil {
			err = aerr
			continue
		}
		if _, err = steerByUsage(home, usage, now, rt, acct, usageRuntimeIntent(req, agentName, reason), req.Account); err == nil {
			return nil
		}
	}
	return err
}

// usageMove is steerByUsage's answer: where the run goes and, when that is not
// where it was headed, a note saying so ("" = unchanged).
type usageMove struct {
	runtime, account, note string
	// fromRuntime and fromAccount are where the run was headed.
	fromRuntime, fromAccount string
}

// steerByUsage checks the chosen runtime and Claude account against usage (as
// `cmdr account list` reports it) at now. Codex out: refused when the request
// named Codex, else moved to Claude. A Claude account out (or not logged in
// after such a move): refused when the request named it, else moved to the
// next logged-in account with usage, by chat.NextAccountWith's rules (never
// one sharing a login with an account that is out). With none left, an automatic
// runtime moves to guarded Codex when available; otherwise it refuses.
// An account with no report counts as having
// usage: nothing polls the providers.
func steerByUsage(home string, usage map[string]chat.Usage, now time.Time, rt, acct, askedRuntime, askedAccount string) (usageMove, error) {
	mv := usageMove{runtime: rt, account: acct}
	codex := usage[chat.UsageKey(chat.RuntimeCodex, "")]
	var why []string
	if rt == chat.RuntimeCodex {
		if !codex.Out(now) {
			return mv, nil
		}
		if askedRuntime == chat.RuntimeCodex {
			return mv, fmt.Errorf("Codex is out of usage%s (cmdr account list), so the Operator was not started on it; %s",
				outUntil(codex, now), claudeWayOut(home, usage, now, acct, askedAccount))
		}
		mv.runtime = chat.RuntimeClaude
		why = append(why, "Codex is out of usage"+outUntil(codex, now))
	}
	usable := func(name string) bool {
		return (name == claudeacct.Default || claudeacct.Get(home, name).LoggedIn) &&
			!usage[chat.UsageKey(chat.RuntimeClaude, name)].Out(now)
	}
	if usable(acct) {
		if len(why) > 0 {
			mv.note = fmt.Sprintf("was headed for Codex; %s, so it runs on Claude account %s instead.", why[0], acct)
		}
		return mv, nil
	}
	state := "is out of usage" + outUntil(usage[chat.UsageKey(chat.RuntimeClaude, acct)], now)
	if !usage[chat.UsageKey(chat.RuntimeClaude, acct)].Out(now) {
		state = "is not logged in"
	}
	if askedAccount != "" {
		return mv, fmt.Errorf("Claude account %s %s (cmdr account list), so the Operator was not started on it; %s",
			acct, state, wayOut(home, usage, now, acct, askedRuntime))
	}
	next := nextWithUsage(home, usage, now, acct)
	if next == "" {
		if askedRuntime == "" && !codex.Out(now) && codexHooksReady() {
			mv.runtime = chat.RuntimeCodex
			mv.note = fmt.Sprintf("was headed for Claude account %s; no logged-in Claude account has usage left, so it runs on Codex instead.", acct)
			return mv, nil
		}
		return mv, fmt.Errorf("Claude account %s %s and no other logged-in Claude account has usage left (cmdr account list)%s; nothing was started",
			acct, state, codexWayOut(codex, now, askedRuntime))
	}
	mv.account = next
	from := "Claude account " + acct
	if rt == chat.RuntimeCodex {
		from = "Codex (" + why[0] + "), then Claude account " + acct
	}
	mv.note = fmt.Sprintf("was headed for %s, which %s; it runs on Claude account %s instead.", from, state, next)
	return mv, nil
}

// outUntil says when an out account resets (" until Wed 15:04"), "" unknown.
func outUntil(u chat.Usage, now time.Time) string {
	if c := u.Current(now); c.OutUntil > 0 {
		return " until " + time.Unix(c.OutUntil, 0).In(now.Location()).Format("Mon 15:04")
	}
	return ""
}

// wayOut names a provider with usage for a refused request.
func wayOut(home string, usage map[string]chat.Usage, now time.Time, acct, askedRuntime string) string {
	if next := nextWithUsage(home, usage, now, acct); next != "" {
		return "spawn again with --account " + next + ", which has usage left"
	}
	if s := codexWayOut(usage[chat.UsageKey(chat.RuntimeCodex, "")], now, askedRuntime); s != "" {
		return strings.TrimPrefix(s, "; ")
	}
	return "no other account has usage left (cmdr account list)"
}

// claudeWayOut is the way out of a refused Codex request: Claude on acct when
// it has usage, else the next account that does. With no account named the
// spawner moves the run to an account with usage itself, so --runtime claude
// alone is the way out whenever any account has it.
func claudeWayOut(home string, usage map[string]chat.Usage, now time.Time, acct, askedAccount string) string {
	logged := acct == claudeacct.Default || claudeacct.Get(home, acct).LoggedIn
	usable := logged && !usage[chat.UsageKey(chat.RuntimeClaude, acct)].Out(now)
	next := nextWithUsage(home, usage, now, acct)
	switch {
	case askedAccount == "" && (usable || next != ""):
		return "spawn again with --runtime claude (a Claude account has usage left)"
	case usable:
		return "spawn again with --runtime claude (Claude account " + acct + " has usage left)"
	case next != "":
		return "spawn again with --runtime claude --account " + next + ", which has usage left"
	}
	return "no Claude account has usage left either (cmdr account list)"
}

// codexWayOut offers Codex when it has usage, its guard hooks are ready and
// the request named neither runtime (naming Codex again would loop).
func codexWayOut(codex chat.Usage, now time.Time, askedRuntime string) string {
	if askedRuntime != "" || codex.Out(now) || !codexHooksReady() {
		return ""
	}
	return "; Codex has usage: spawn again with --runtime codex"
}

// RequestedUsage refuses, before anything is created or ended, a request that
// names a runtime or Claude account reporting no usage left (the spawner
// checks again; this keeps a refused respawn from ending the old run first).
// nil when the request names neither: the spawner moves such a run itself.
func RequestedUsage(runtime, account string) error {
	rt, acct := runtime, account
	if rt == "" {
		rt = chat.RuntimeClaude
	}
	if acct == "" {
		if rt != chat.RuntimeCodex {
			return nil
		}
		acct = claudeacct.Default
	}
	home := agentrt.CommanderHome()
	_, err := steerByUsage(home, readUsage(home), usageNow(), rt, acct, runtime, account)
	return err
}

// nextWithUsage is chat.NextAccountWith, falling back to default (the user's
// normal login, which a spawn never refuses as logged out) when it has usage
// and shares a login with no account that is out (nor with from).
func nextWithUsage(home string, usage map[string]chat.Usage, now time.Time, from string) string {
	if next := chat.NextAccountWith(home, from, usage, now); next != "" {
		return next
	}
	if from == claudeacct.Default || usage[chat.UsageKey(chat.RuntimeClaude, claudeacct.Default)].Out(now) {
		return ""
	}
	def := claudeacct.Get(home, claudeacct.Default)
	if claudeacct.SameIdentity(claudeacct.Get(home, from), def) {
		return ""
	}
	for _, a := range claudeacct.List(home) {
		if a.Name != claudeacct.Default && usage[chat.UsageKey(chat.RuntimeClaude, a.Name)].Out(now) && claudeacct.SameIdentity(a, def) {
			return ""
		}
	}
	return claudeacct.Default
}

// Explicit Claude intent and local background services cannot run on Codex.
func usageRuntimeIntent(req SpawnRequest, role, reason string) string {
	if req.Runtime != "" {
		return req.Runtime
	}
	if reason == ReasonClaudeServiceKeyword || needsBackgroundService(req.TaskText) {
		return chat.RuntimeClaude
	}
	if req.Resume && chat.ReadRuntimePin(agentrt.CommanderHome(), role) == chat.RuntimeClaude {
		return chat.RuntimeClaude
	}
	return ""
}
