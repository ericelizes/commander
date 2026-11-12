package spawn

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"unicode"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/store"
)

// ReasonClaudeServiceKeyword is the operator-runtime reason of a run forced
// onto Claude because its task names a local background service.
const ReasonClaudeServiceKeyword = chat.RuntimeClaude + "-service-keyword"

// ReasonAgentAccounts is the operator-runtime reason of a run placed on the
// first account in the agents' account order with usage left.
const ReasonAgentAccounts = "agent-accounts"

// ReasonClaudeHooksUnavailable records an effective Claude run while Codex intent remains pinned.
const ReasonClaudeHooksUnavailable = chat.RuntimeClaude + "-hooks-unavailable"

// serviceKeywords are the conservative markers of a task that starts or keeps
// a LOCAL long-lived background service. A Codex tool call tears down what it
// forked when it returns, so such tasks run on Claude. Generic words (daemon,
// VM, server, service) stay out: they match far too much work that is fine on Codex.
var serviceKeywords = []string{"podman", "docker", "vfkit", "gvproxy", "colima", "lima", "machine start", "dev server", "docker compose"}

// needsBackgroundService reports whether the task text names a local
// background service (case-insensitive, whole words).
func needsBackgroundService(text string) bool {
	words := " " + strings.Join(strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ") + " "
	for _, k := range serviceKeywords {
		if strings.Contains(words, " "+k+" ") {
			return true
		}
	}
	return false
}

// resolveOperatorRuntime picks the agent CLI a host Operator runs on: the
// request's explicit choice, else a pinned runtime on respawn with hooks ready,
// else Claude for a respawn,
// else for a task naming a local background service (needsBackgroundService),
// else the share split (chat.CodexByShare on the task bead; a share is 0 unless
// a person configured one), else Claude. A Manager's runtime never passes to its
// Operators. It records the choice as the role's runtime, so the app opens the
// Operator's chat on the same CLI and Codex thread. The second result is the
// operator-runtime event's reason. A spawn picks (pickOperatorRuntime) and
// writes (writeOperatorRuntime) apart, so a refused spawn writes nothing.
func resolveOperatorRuntime(req SpawnRequest, agentName string) (string, string) {
	rt, reason, pinned := pickOperatorRuntime(req, agentName)
	writeOperatorRuntime(req, agentName, rt, pinned)
	return rt, reason
}

// writeOperatorRuntime records rt as the role's runtime for req (the request
// as asked): an explicit choice becomes the pin, a new run drops the role's
// Codex thread, and the effective runtime is written as a person's (explicit
// or pinned) or a draw's.
func writeOperatorRuntime(req SpawnRequest, agentName, rt string, pinned bool) {
	home := agentrt.CommanderHome()
	if req.Runtime != "" {
		_ = chat.WriteRuntime(home, agentName, req.Runtime)
	}
	if !req.Resume {
		_ = os.Remove(chat.CodexThreadFile(home, agentName)) // a new run is a new thread
	}
	if req.Runtime != "" || pinned || req.ResumeRuntime != "" {
		_ = chat.WriteEffectiveRuntime(home, agentName, rt)
	} else {
		_ = chat.WriteAutoRuntime(home, agentName, rt)
	}
}

// pickOperatorRuntime is resolveOperatorRuntime's choice with nothing written
// (CheckUsage asks it before anything is created or ended); pinned says the
// role's pin decided it.
func pickOperatorRuntime(req SpawnRequest, agentName string) (rt, reason string, pinned bool) {
	home := agentrt.CommanderHome()
	rt = req.Runtime
	pinned = req.Resume && chat.ReadRuntimePin(home, agentName) != ""
	if rt == "" && req.Account != "" {
		rt = chat.RuntimeClaude // an account switch changes execution, not runtime intent
	}
	if rt == "" && pinned {
		rt = chat.ReadRuntimePin(home, agentName)
	}
	if rt == chat.RuntimeCodex && !codexHooksReady() {
		rt, reason = chat.RuntimeClaude, ReasonClaudeHooksUnavailable
	}
	if rt == "" && !req.Resume && needsBackgroundService(req.TaskText) {
		rt, reason = chat.RuntimeClaude, ReasonClaudeServiceKeyword
	}
	if rt == "" && (!req.Resume || req.UsageRecovery) {
		if p, ok := firstAgentAccount(req); ok {
			rt, reason = p.Runtime, ReasonAgentAccounts
		}
	}
	if rt != chat.RuntimeCodex {
		rt = chat.RuntimeClaude
	}
	if reason == "" {
		reason = rt
	}
	return rt, reason, pinned
}

// agentOrder is req's account order for a new execution agent, else the default.
func agentOrder(req SpawnRequest) []string {
	if len(req.AgentAccounts) > 0 {
		return req.AgentAccounts
	}
	return defaultAgentOrder()
}

// defaultAgentOrder is the order with nothing saved. Tests replace it.
var defaultAgentOrder = func() []string { return store.DefaultAgentAccounts[store.AgentKindAgent] }

// firstAgentAccount is the first account in the agents' order that can take a
// new run now.
func firstAgentAccount(req SpawnRequest) (chat.Provider, bool) {
	home, now := agentrt.CommanderHome(), usageNow()
	accounts := chat.Accounts(home, readUsage(home), now)
	for _, p := range chat.OrderAccounts(agentOrder(req), accounts) {
		if p.Runtime == chat.RuntimeCodex && needsBackgroundService(req.TaskText) {
			continue
		}
		if chat.AccountAvailable(p, accounts, now, codexHooksReady) {
			return p, true
		}
	}
	return chat.Provider{}, false
}

// offCodexNote notes on the bead a respawn or crash restart that moved an
// Operator off Codex (wasCodex: the role's runtime said Codex before the
// spawn); reason is the pick's, req the request as asked.
func (s *Spawner) offCodexNote(ctx context.Context, req SpawnRequest, agentName, rt, reason string, wasCodex bool) {
	if !wasCodex || rt != chat.RuntimeClaude {
		return
	}
	why := "Share-drawn runs return to Claude on restart."
	if reason == ReasonClaudeHooksUnavailable {
		why = "Codex guard hooks are unavailable; the explicit Codex choice is preserved for recovery."
	} else if req.Account != "" {
		why = "The account switch selects Claude execution; explicit runtime intent is preserved."
	}
	note := fmt.Sprintf("cmdrd: %s was on Codex; this restart moved it to Claude, on the same branch and worktree. %s Reason: %s.", agentName, why, reason)
	if err := s.beads.AddComment(ctx, req.ProjectPath, req.TaskBeadID, note); err != nil {
		log.Printf("spawn: note the move off Codex on %s: %v", req.TaskBeadID, err)
	}
}

// wasOnCodex reports whether a respawn with no runtime named leaves a role
// whose runtime file says Codex (read before anything is written).
func wasOnCodex(req SpawnRequest, agentName string) bool {
	return req.Resume && req.Runtime == "" && chat.ReadRuntime(agentrt.CommanderHome(), agentName) == chat.RuntimeCodex
}

// codexHooksReady gates the split: a Codex Operator is only safe with its
// guard hooks (build slots, root-wide searches). Tests replace it.
var codexHooksReady = chat.CodexHooksReady

// recordRuntime notes which CLI a run is on, for `cmdr runtime report`
// (a cost event at the run's spawn time; agent_runs has no runtime column).
func (s *Spawner) recordRuntime(ctx context.Context, req SpawnRequest, agentName string, spawnTime int64) {
	_, _ = s.store.InsertCostEvent(ctx, store.CostEvent{ProjectID: req.ProjectID, AgentName: agentName,
		TaskBeadID: sql.NullString{String: req.TaskBeadID, Valid: req.TaskBeadID != ""}, EventType: store.EventOperatorRuntime,
		Reason: sql.NullString{String: req.runtimeReason, Valid: true}, OccurredAt: spawnTime})
}

// resolveOperatorAccount picks the Claude account a host Operator runs on, like
// resolveOperatorRuntime: the request's, else (on a respawn) the role's
// recorded one, else its Manager's, else default. It records the choice, so the
// app opens the Operator's chat on the same account. An unknown or logged-out
// account is an error.
func resolveOperatorAccount(req SpawnRequest, agentName string) (string, error) {
	acct, err := pickOperatorAccount(req, agentName)
	if err != nil {
		return "", err
	}
	return acct, chat.WriteAccount(agentrt.CommanderHome(), agentName, acct)
}

// pickOperatorAccount is resolveOperatorAccount's choice with nothing written.
// On a respawn the account a usage move took the role off (its intent) comes
// before the one it ran on, so a chosen account is not lost to a move.
func pickOperatorAccount(req SpawnRequest, agentName string) (string, error) {
	home := agentrt.CommanderHome()
	acct := req.Account
	if acct == "" && req.Resume {
		acct = chat.ReadAccountIntent(home, agentName)
	}
	if acct == "" && req.Resume && fileExists(chat.AccountFile(home, agentName)) {
		acct = chat.ReadAccount(home, agentName)
	}
	if acct == "" && req.managerRole != "" {
		acct = chat.ReadAccount(home, req.managerRole)
	}
	if acct == "" {
		acct = claudeacct.Default
	}
	if !claudeacct.Exists(home, acct) {
		return "", fmt.Errorf("no Claude account %q (cmdr account list)", acct)
	}
	// default is the user's normal login, as before accounts: never refused here.
	if acct != claudeacct.Default && req.Runtime != chat.RuntimeCodex && !claudeacct.Get(home, acct).LoggedIn {
		return "", fmt.Errorf("Claude account %s is not logged in: run %s", acct, claudeacct.LoginCommand(home, acct))
	}
	return acct, nil
}

// claudeCanResume reports whether a resumed Claude Operator has a transcript
// to --resume on its account, copying it there from the account it last ran
// on. A session that only ran on Codex has none: it starts fresh under the
// same id, with the resume prompt.
func claudeCanResume(req SpawnRequest, sessionID string) bool {
	home := agentrt.CommanderHome()
	path, err := claudeacct.CarryTranscript(home, claudeacct.ProjectsDir(home, claudeacct.Default), sessionID, req.Account)
	return err == nil && path != ""
}

// codexOperatorArgs runs a host Operator on Codex the way `claude -p` runs it:
// one turn on the bootstrap (or resume) prompt, Claude stream-json on stdout for
// the ceilings watchdog, and Claude's --dangerously-skip-permissions as approval
// "never" with full access. A respawn resumes the role's Codex thread. Hooks
// the build-slot guard, no mail (a one-shot takes no message after its prompt).
func codexOperatorArgs(req SpawnRequest, agentName, sessionID, prompt string) []string {
	cfg := chat.CodexBridgeConfig{
		Model:           chat.CodexModel(req.Model),
		PromptPath:      agentrt.ClaudePromptPath(agentName),
		ThreadFile:      chat.CodexThreadFile(agentrt.CommanderHome(), agentName),
		SessionID:       sessionID,
		SkipPermissions: true,
		Prompt:          prompt,
		HookRole:        agentName,
		GuardOnly:       true,
	}
	return append([]string{"cmdr"}, cfg.Args()...)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
