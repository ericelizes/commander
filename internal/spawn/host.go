package spawn

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/example-user/commander/internal/tmuxctl"
	"github.com/example-user/commander/internal/worktreelock"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

// EnvOperatorRuntime selects where code Operators run. "host" forces the host
// runtime; anything else uses Docker when `docker` is on PATH and falls back to
// the host runtime when it is not.
const EnvOperatorRuntime = "COMMANDER_OPERATOR_RUNTIME"

// hostRuntimeSelected reports whether code Operators run on the host, the
// default (a Claude process in its own worktree on the normal Claude login,
// like Managers and Commander). Docker is opt-in: operator_sandbox = "docker"
// in config (see Spawner.WithDockerSandbox) or COMMANDER_OPERATOR_RUNTIME=docker.
func hostRuntimeSelected() bool {
	switch os.Getenv(EnvOperatorRuntime) {
	case "docker":
		if _, err := exec.LookPath("docker"); err == nil {
			return false
		}
	}
	return true
}

// WithDockerSandbox opts code Operators into the Docker sandbox when on (and
// the docker binary exists); off keeps the host default.
func (s *Spawner) WithDockerSandbox(on bool) *Spawner {
	if on {
		if _, err := exec.LookPath("docker"); err == nil {
			s.useHost = func() bool { return false }
		}
	}
	return s
}

// hostSessionName is the tmux session a host code Operator runs in. tmux
// rewrites '.' and ':' in session names, which would break `-t <name>`.
func hostSessionName(project, flat string) string {
	name := knowledgeSessionName(project, flat)
	return strings.NewReplacer(".", "-", ":", "-").Replace(name)
}

// spawnCodeHost is the code-type spawn for machines without Docker: the same
// worktree and bookkeeping as spawnCode, launched the way spawnKnowledge does,
// as a headless `claude -p` process in a detached tmux session whose cwd is
// the worktree and whose stdout is piped to a fifo for the ceilings watchdog.
//
// Auth: the process inherits cmdr's environment untouched and signs in with
// the user's normal Claude login (on macOS the Keychain, so no token needs
// forwarding). Neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is
// forwarded explicitly, and ANTHROPIC_API_KEY is unset for the child (even if
// the tmux server has it) so an Operator can never bill the API instead of the
// user's plan. The host runtime is Claude-only, whatever agentrt picks for the
// other tiers.
func (s *Spawner) spawnCodeHost(ctx context.Context, req SpawnRequest) (*SpawnResult, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	req.managerRole = ManagerRole(ctx, s.beads, req.ProjectPath, req.TaskBeadID)

	flat := req.slug()
	agentName := "operator-" + flat
	worktreePath := filepath.Join(req.ProjectPath, ".worktrees", flat)
	operatorsDir := filepath.Join(req.ProjectPath, ".commander", "operators")
	sessionFile := filepath.Join(operatorsDir, agentrt.SessionFileName(flat, agentrt.Claude))
	fifoPath := filepath.Join(operatorsDir, flat+".fifo")
	sessionName := hostSessionName(req.ProjectName, flat)
	req, err := s.resolveProvider(ctx, req, agentName)
	if err != nil {
		return nil, err
	}

	var cleanups cleanups
	bail := func(err error) (*SpawnResult, error) {
		_ = cleanups.runReverse(ctx)
		return nil, err
	}

	lock, err := acquireSpawnLock(req.ProjectName, flat)
	if err != nil {
		return nil, fmt.Errorf("spawn: acquire lock: %w", err)
	}
	cleanups.push(func() error { return lock.release() })

	// A follow-up Operator on a branch a finished Operator left checked out
	// takes over that worktree (git checks a branch out in one place only),
	// keeping whatever it left there.
	prev := branchWorktree(ctx, req.repoRoot(), req.Branch)
	// Only an Operator worktree: an Operator in the main checkout would have it
	// write-protected when it ends, with every worktree under it.
	if prev != "" && !isOperatorWorktree(req.ProjectPath, prev) {
		return bail(fmt.Errorf("spawn: branch %s is checked out at %s, outside the project's worktree folders (.worktrees, .worktrees.noindex): an Operator cannot take that checkout over, spawn on a new branch", req.Branch, prev))
	}
	// Under its old name too: that worktree is the one the settle freezes.
	for _, dir := range []string{worktreePath, prev} {
		if dir == "" {
			continue
		}
		if err := s.refuseUnsweptStop(ctx, req, dir); err != nil {
			return bail(err)
		}
	}
	// From before its lock is cleared until the row names it, a claim keeps the
	// freeze of a run that ended there off this worktree.
	claimed := worktreePath
	if prev != "" {
		claimed = prev
	}
	release, err := claimWorktree(ctx, claimed)
	if err != nil {
		return bail(fmt.Errorf("spawn: %w", err))
	}
	defer release()
	if prev != "" && prev != worktreePath {
		if sessionLive(ctx, hostSessionName(req.ProjectName, filepath.Base(prev))) {
			return bail(fmt.Errorf("spawn: branch %s is checked out at %s by an Operator that is still running: wait for it to finish, or spawn on a new branch", req.Branch, prev))
		}
		if err := worktreelock.Unlock(prev); err != nil {
			return bail(fmt.Errorf("spawn: unlock %s: %w", prev, err))
		}
		worktreePath = prev
	} else {
		if err := gitWorktreeAdd(ctx, req.repoRoot(), worktreePath, req.Branch); err != nil {
			return bail(fmt.Errorf("spawn: git worktree add: %w", err))
		}
		cleanups.push(func() error {
			return gitWorktreeRemove(context.Background(), req.repoRoot(), worktreePath)
		})
	}

	// The spawn no longer refuses: record the provider and any usage move
	// before the launcher reads them (runtime, account, Codex thread).
	if err := s.applyProvider(ctx, req, agentName); err != nil {
		return bail(err)
	}
	if err := appendGitExcludes(req.repoRoot()); err != nil {
		return bail(fmt.Errorf("spawn: update git exclude: %w", err))
	}
	if err := os.MkdirAll(operatorsDir, 0o755); err != nil {
		return bail(fmt.Errorf("spawn: create operators dir: %w", err))
	}
	if err := promptassets.EnsureAgentFor(agentrt.Claude, promptassets.RoleOperatorCode, agentName); err != nil {
		return bail(fmt.Errorf("spawn: ensure operator agent prompt: %w", err))
	}

	if err := createPaneOutput(fifoPath); err != nil {
		return bail(fmt.Errorf("spawn: create %s: %w", fifoPath, err))
	}
	cleanups.push(func() error { return os.Remove(fifoPath) })

	// The session starts in the worktree, so the Operator's cwd is its branch.
	// Its pane never runs the user's shell: startup files can prompt (the
	// dotfiles /evolve check) and swallow the first typed keys. A placeholder
	// holds the pane until pipe-pane is wired, then the launcher replaces it.
	if err := s.tmux.NewSessionCommand(ctx, sessionName, worktreePath, "cat"); err != nil {
		return bail(fmt.Errorf("spawn: tmux new-session %s: %w", sessionName, err))
	}
	cleanups.push(func() error {
		killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.tmux.KillPane(killCtx, sessionName)
	})
	if err := s.tmux.PipePane(ctx, sessionName, fifoPath); err != nil {
		return bail(fmt.Errorf("spawn: tmux pipe-pane: %w", err))
	}
	// Claude takes a caller-chosen --session-id, so the id is minted here instead
	// of captured from the pane's output (which timed out after 30s).
	sessionID := agentrt.NewClaudeSessionID()
	resuming := false
	if req.Resume {
		if prev := readSessionFile(sessionFile); prev != "" {
			sessionID, resuming = prev, true
		}
	}
	// The full command is too long for a tmux argument list line, so it goes in a
	// launcher script.
	launcher := filepath.Join(operatorsDir, agentName+".sh")
	// Not exec'd: when the Operator exits the launcher ends its own session, so a
	// finished Operator never lingers as a session that looks alive. It first
	// leaves an exit marker, so the reaper can tell a finish from a kill.
	script := "#!/bin/sh\nunset TMUX TMUX_PANE\n" + hostOperatorCommand(req, agentName, sessionID, resuming) + "\n" +
		"st=$?\nmkdir -p " + shellQuote(ExitMarkerDir()) + " && echo $st > " + shellQuote(ExitMarker(sessionName)) + "\n" +
		hostSessionCleanup(sessionName) + "\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		return bail(fmt.Errorf("spawn: write launcher %s: %w", launcher, err))
	}
	if !req.Resume {
		s.markRunStart(agentName)
	}
	if err := s.tmux.RespawnPane(ctx, sessionName, worktreePath, "sh", launcher); err != nil {
		return bail(fmt.Errorf("spawn: tmux respawn-pane: %w", err))
	}

	if err := writeSessionFile(sessionFile, sessionID); err != nil {
		return bail(fmt.Errorf("spawn: persist session id: %w", err))
	}
	if !resuming {
		cleanups.push(func() error { return os.Remove(sessionFile) })
	}

	if err := s.am.Register(ctx, req.ProjectName, "claude-code", modelShort(req.Model), agentName); err != nil {
		return bail(fmt.Errorf("spawn: am register %s: %w", agentName, err))
	}
	if err := s.beads.SetState(ctx, req.ProjectPath, req.TaskBeadID, "cmdr", "brainstorming", "operator spawned"); err != nil {
		return bail(fmt.Errorf("spawn: bd set-state %s: %w", req.TaskBeadID, err))
	}

	// TmuxPaneID carries the session name; ContainerID stays NULL. That is how
	// the watchdog and the reaper tell a host Operator from a Docker one.
	spawnTime := s.now()
	runID, err := s.store.InsertAgentRun(ctx, store.AgentRun{
		ProjectID:         req.ProjectID,
		TaskBeadID:        req.TaskBeadID,
		AgentName:         agentName,
		OpencodeSessionID: sessionID,
		TmuxPaneID:        sql.NullString{String: sessionName, Valid: true},
		SpawnTime:         spawnTime.Unix(),
		Status:            "running",
		WorktreePath:      sql.NullString{String: worktreePath, Valid: true},
	})
	if err != nil {
		return bail(fmt.Errorf("spawn: insert agent_runs: %w", err))
	}
	s.recordRuntime(ctx, req, agentName, spawnTime.Unix())

	return &SpawnResult{
		RunID:        runID,
		SessionID:    sessionID,
		AgentName:    agentName,
		WorktreePath: worktreePath,
		SpawnTime:    spawnTime,
		Runtime:      req.Runtime,
		Account:      req.Account,
		Moved:        req.moved,
	}, nil
}

// spawnKnowledgeHost is the knowledge-type spawn on the host runtime: no
// worktree and no git requirement (the project path is just the beads root and
// the Operator's cwd), a headless claude in a detached tmux session like
// spawnCodeHost.
func (s *Spawner) spawnKnowledgeHost(ctx context.Context, req SpawnRequest) (*SpawnResult, error) {
	if err := validateKnowledgeRequest(req); err != nil {
		return nil, err
	}
	req.managerRole = ManagerRole(ctx, s.beads, req.ProjectPath, req.TaskBeadID)

	flat := flatTask(req.TaskBeadID)
	agentName := "operator-" + flat
	operatorsDir := filepath.Join(req.ProjectPath, ".commander", "operators")
	sessionFile := filepath.Join(operatorsDir, agentrt.SessionFileName(flat, agentrt.Claude))
	fifoPath := filepath.Join(operatorsDir, flat+".fifo")
	sessionName := hostSessionName(req.ProjectName, flat)
	req, err := s.resolveProvider(ctx, req, agentName)
	if err != nil {
		return nil, err
	}

	var cleanups cleanups
	bail := func(err error) (*SpawnResult, error) {
		_ = cleanups.runReverse(ctx)
		return nil, err
	}

	lock, err := acquireSpawnLock(req.ProjectName, flat)
	if err != nil {
		return nil, fmt.Errorf("spawn: acquire lock: %w", err)
	}
	cleanups.push(func() error { return lock.release() })
	if err := s.applyProvider(ctx, req, agentName); err != nil {
		return bail(err)
	}

	if err := os.MkdirAll(operatorsDir, 0o755); err != nil {
		return bail(fmt.Errorf("spawn: create operators dir: %w", err))
	}
	if err := promptassets.EnsureAgentFor(agentrt.Claude, promptassets.RoleOperatorKnowledge, agentName); err != nil {
		return bail(fmt.Errorf("spawn: ensure operator agent prompt: %w", err))
	}

	if err := createPaneOutput(fifoPath); err != nil {
		return bail(fmt.Errorf("spawn: create %s: %w", fifoPath, err))
	}
	cleanups.push(func() error { return os.Remove(fifoPath) })

	if err := s.tmux.NewSessionCommand(ctx, sessionName, req.ProjectPath, "cat"); err != nil {
		return bail(fmt.Errorf("spawn: tmux new-session %s: %w", sessionName, err))
	}
	cleanups.push(func() error {
		killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.tmux.KillPane(killCtx, sessionName)
	})
	if err := s.tmux.PipePane(ctx, sessionName, fifoPath); err != nil {
		return bail(fmt.Errorf("spawn: tmux pipe-pane: %w", err))
	}

	sessionID := agentrt.NewClaudeSessionID()
	resuming := false
	if req.Resume {
		if prev := readSessionFile(sessionFile); prev != "" {
			sessionID, resuming = prev, true
		}
	}
	launcher := filepath.Join(operatorsDir, agentName+".sh")
	script := "#!/bin/sh\nunset TMUX TMUX_PANE\nexec " + hostOperatorCommand(req, agentName, sessionID, resuming) + "\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		return bail(fmt.Errorf("spawn: write launcher %s: %w", launcher, err))
	}
	if !req.Resume {
		s.markRunStart(agentName)
	}
	if err := s.tmux.RespawnPane(ctx, sessionName, req.ProjectPath, "sh", launcher); err != nil {
		return bail(fmt.Errorf("spawn: tmux respawn-pane: %w", err))
	}

	if err := writeSessionFile(sessionFile, sessionID); err != nil {
		return bail(fmt.Errorf("spawn: persist session id: %w", err))
	}
	if !resuming {
		cleanups.push(func() error { return os.Remove(sessionFile) })
	}

	if err := s.am.Register(ctx, req.ProjectName, "claude-code", modelShort(req.Model), agentName); err != nil {
		return bail(fmt.Errorf("spawn: am register %s: %w", agentName, err))
	}
	if err := s.beads.SetState(ctx, req.ProjectPath, req.TaskBeadID, "cmdr", "brainstorming", "operator spawned"); err != nil {
		return bail(fmt.Errorf("spawn: bd set-state %s: %w", req.TaskBeadID, err))
	}

	spawnTime := s.now()
	runID, err := s.store.InsertAgentRun(ctx, store.AgentRun{
		ProjectID:         req.ProjectID,
		TaskBeadID:        req.TaskBeadID,
		AgentName:         agentName,
		OpencodeSessionID: sessionID,
		TmuxPaneID:        sql.NullString{String: sessionName, Valid: true},
		SpawnTime:         spawnTime.Unix(),
		Status:            "running",
	})
	if err != nil {
		return bail(fmt.Errorf("spawn: insert agent_runs: %w", err))
	}
	s.recordRuntime(ctx, req, agentName, spawnTime.Unix())

	// WorktreePath stays empty: the Operator works in the project path.
	return &SpawnResult{RunID: runID, SessionID: sessionID, AgentName: agentName, SpawnTime: spawnTime,
		Runtime: req.Runtime, Account: req.Account, Moved: req.moved}, nil
}

// createPaneOutput makes the file a host Operator's pane output is appended
// to (tmux pipe-pane). It keeps the .fifo name but is a regular file: a writer
// opening a fifo blocks until something reads it, and nothing does, so every
// writer outlived its Operator. Appending to a file never blocks, and the
// writer ends on EOF when the pane closes.
func createPaneOutput(path string) error {
	// A fifo left by an older build: opening it for writing would block.
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// The launcher has no TMUX; pin cleanup to the socket selected at spawn time.
func hostSessionCleanup(sessionName string) string {
	parts := []string{"tmux"}
	for _, arg := range tmuxctl.OpsArgs("kill-session", "-t", "="+sessionName) {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// hostOperatorCommand is the shell line the launcher script execs.
// `env -u ANTHROPIC_API_KEY` keeps the child off the API (see spawnCodeHost).
func hostOperatorCommand(req SpawnRequest, agentName, sessionID string, resuming bool) string {
	prompt := hostBootstrapPrompt(req)
	if req.ProjectType == "knowledge" {
		prompt = knowledgeHostBootstrapPrompt(req)
	}
	if resuming {
		prompt = hostResumePrompt(req)
	}
	args := agentrt.ClaudeHeadlessArgs(agentName, agentrt.ClaudePromptPath(agentName), req.Model, sessionID, prompt, true)
	if resuming && req.Runtime != chat.RuntimeCodex && claudeCanResume(req, sessionID) {
		for i, a := range args {
			if a == "--session-id" {
				args[i] = "--resume"
			}
		}
	}
	args = append(args, "--settings", agentrt.ClaudeHostOperatorSettings(agentName))
	if req.Runtime == chat.RuntimeCodex {
		args = codexOperatorArgs(req, agentName, sessionID, prompt)
	}
	// -p=2: Operators share the machine; full suites also go through the build
	// slot. -trimpath: Go keys its build and test caches on source paths, so
	// without it every new worktree recompiled and re-ran everything (measured:
	// 0 of 34 packages cached in a second fresh worktree, 31 of 34 with it).
	parts := []string{"env", "-u", "TMUX", "-u", "TMUX_PANE", "-u", "ANTHROPIC_API_KEY", "-u", "CLAUDE_CONFIG_DIR", "GOFLAGS='-trimpath -p=2'", "COMMANDER_ROLE=" + shellQuote(agentName)}
	for _, kv := range OperatorGitEnv(agentName) {
		parts = append(parts, shellQuote(kv))
	}
	if role := req.managerRole; role != "" {
		parts = append(parts, "COMMANDER_MANAGER_ROLE="+shellQuote(role))
	}
	if req.ProdGate != "" {
		parts = append(parts, "COMMANDER_PROD_GATE="+shellQuote(req.ProdGate))
	}
	if req.Runtime != chat.RuntimeCodex {
		for _, kv := range claudeacct.Env(agentrt.CommanderHome(), req.Account) {
			parts = append(parts, shellQuote(kv))
		}
	}
	if len(req.MountReads) > 0 {
		parts = append(parts, "COMMANDER_READS="+shellQuote(strings.Join(req.MountReads, ":")))
	}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// hostResumePrompt is the first message of a resumed Operator.
func hostResumePrompt(req SpawnRequest) string {
	why := "your process was cut off by a restart"
	if req.ResumeNote != "" {
		why = req.ResumeNote
	}
	return "[resume] " + why + "; continue where you left off. " +
		"Re-read your bead (`" + BrCommand(req.ProjectPath) + " show " + req.TaskBeadID + "`) for what is already done, and finish the task."
}

// readSessionFile returns the session id in path, or "" when there is none.
func readSessionFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// BrCommand is the bd invocation an Operator runs from any cwd (name kept from br days):
// a worktree's .beads is a stale copy, so point --db at
// the project root's database.
func BrCommand(projectRoot string) string {
	return "bd --db " + shellQuote(filepath.Join(projectRoot, ".beads", "beads.db"))
}

// hostBootstrapPrompt is the seed message for a host Operator: it works in its
// own worktree (the process cwd) but beads lives in the project root.
func hostBootstrapPrompt(req SpawnRequest) string {
	manager := req.managerRole
	return fmt.Sprintf(
		"You are the Operator for task %s in project %q. "+
			"Your working directory is your own git worktree on branch %s; the project root is %s. "+
			"The bead is your brief: read it via `%s show %s` (run bd only this way, not from the worktree whose .beads is stale; use the same `%s` prefix for `comments add` and other bd commands), work only in your worktree, write your final report onto the bead, mail your Manager, then stop. "+
			"Your Manager's mail role is exactly `%s` (also $COMMANDER_MANAGER_ROLE): report with `cmdr internal-mail --to %s`, never to commander.",
		req.TaskBeadID, req.ProjectName, req.Branch, req.ProjectPath, BrCommand(req.ProjectPath), req.TaskBeadID, BrCommand(req.ProjectPath), manager, manager,
	) + prodGatePrompt(req)
}

// prodGatePrompt is the production exception appended to the brief of an
// Operator spawned with --prod-gate; "" when the default (no production) holds.
func prodGatePrompt(req SpawnRequest) string {
	if req.ProdGate == "" {
		return ""
	}
	return fmt.Sprintf(" PRODUCTION GATE %s (closed, approved by the user, Manager-approved): the guardrail against touching production is lifted for exactly the action this gate covers, and nothing else. "+
		"Allowed scope, verbatim: %q. Do only that; post evidence of what you ran on your bead; never exceed it (no merges to protected branches, no other deploys, no other secrets or data). Unsure whether something is inside the scope: stop and mail your Manager.",
		req.ProdGate, req.ProdGateScope)
}

// knowledgeHostBootstrapPrompt is the seed for a host knowledge Operator: its
// cwd is the project path, which is also where beads lives.
func knowledgeHostBootstrapPrompt(req SpawnRequest) string {
	manager := req.managerRole
	return fmt.Sprintf(
		"You are the knowledge Operator for task %s in project %q. "+
			"Your working directory is the project root %s (not a git repo necessarily; do not run git init). "+
			"The bead is your brief: read it via `%s show %s` (run bd only this way, not from the worktree whose .beads is stale; use the same `%s` prefix for `comments add` and other bd commands), write your final report onto the bead, mail your Manager, then stop. "+
			"Your Manager's mail role is exactly `%s` (also $COMMANDER_MANAGER_ROLE): report with `cmdr internal-mail --to %s`, never to commander.",
		req.TaskBeadID, req.ProjectName, req.ProjectPath, BrCommand(req.ProjectPath), req.TaskBeadID, BrCommand(req.ProjectPath), manager, manager,
	)
}

// ManagerRole is the mail role of the Manager that owns a task bead: the
// Manager of its nearest open workstream (beads.Owner; the top of its real
// parent chain while the project's nested Managers are off). "" for an empty id.
func ManagerRole(ctx context.Context, c beads.Client, dir, taskBeadID string) string {
	if taskBeadID == "" {
		return ""
	}
	return store.WorkstreamManagerRole(beads.Owner(ctx, c, dir, nil, nil, taskBeadID))
}

// branchWorktree is the worktree that has branch checked out, "" when none does.
func branchWorktree(ctx context.Context, repo, branch string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil || branch == "" {
		return ""
	}
	path := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return path
		}
	}
	return ""
}

// sessionLive reports whether a tmux session with this name exists.
func sessionLive(ctx context.Context, name string) bool {
	out, err := tmuxctl.ListPanes(ctx, "#{session_name}")
	if err != nil {
		return true // unsure: do not take over a worktree that may be in use
	}
	for _, s := range strings.Split(out, "\n") {
		if strings.TrimSpace(s) == name {
			return true
		}
	}
	return false
}

// markRunStart bounds what this run's mail hook counts as news when the name has
// no read cursor: before its first tool call, so no hook reads an inbox without
// it. Best effort: with no stamp the whole retained inbox reads as unread. A Resume
// does not call it: it continues a run, and that run's unseen mail is still its own.
func (s *Spawner) markRunStart(agentName string) {
	if err := mailstate.MarkRunStart(agentName, s.now()); err != nil {
		log.Printf("spawn: mark run start %s: %v", agentName, err)
	}
}
