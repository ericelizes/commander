package spawn

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/worktreelock"
	promptassets "github.com/example-user/commander/prompts"
)

// branchRE constrains acceptable git branch names. Anchored, conservative;
// rejects anything that could break shell argv or filesystem paths.
var branchRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// operatorImage is the Docker image tag commander spawns. U10 builds + tags
// it via `just operator-image`; U9 just consumes the tag.
const operatorImage = "commander/operator:latest"

// spawnCode runs the full code-type operator spawn flow. On any step failure
// the reverse-order rollback unwinds prior mutations and the function returns
// the underlying error. On success returns a fully populated *SpawnResult.
//
// spawnCode is safe to call concurrently for different (project, branch)
// pairs; concurrent calls for the same (project, branch) serialize on the
// flock. Callers reach this via the public Spawn dispatch in spawn.go.
func (s *Spawner) spawnCode(ctx context.Context, req SpawnRequest) (*SpawnResult, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}

	flat := req.slug()
	agentName := "operator-" + flat
	worktreePath := filepath.Join(req.ProjectPath, ".worktrees", flat)
	commanderDir := filepath.Join(req.ProjectPath, ".commander")
	operatorsDir := filepath.Join(commanderDir, "operators")
	kind := agentrt.For(agentrt.TierOperator)
	sessionFile := filepath.Join(operatorsDir, agentrt.SessionFileName(flat, kind))
	containerName := containerName(req.ProjectName, flat, s.shortID())

	var cleanups cleanups
	// runReverseOnError is called from every error-return below.
	bail := func(err error) (*SpawnResult, error) {
		_ = cleanups.runReverse(ctx)
		return nil, err
	}

	// Step a: flock.
	lock, err := acquireSpawnLock(req.ProjectName, flat)
	if err != nil {
		return nil, fmt.Errorf("spawn: acquire lock: %w", err)
	}
	cleanups.push(func() error { return lock.release() })

	// Until the row names the worktree, keep a closed run's freeze off it.
	release, err := claimWorktree(ctx, worktreePath)
	if err != nil {
		return bail(fmt.Errorf("spawn: %w", err))
	}
	defer release()

	// Step b: git worktree add. -B force-creates / overrides the branch so
	// re-spawn after a crashed prior attempt is idempotent.
	if err := gitWorktreeAdd(ctx, req.repoRoot(), worktreePath, req.Branch); err != nil {
		return bail(fmt.Errorf("spawn: git worktree add: %w", err))
	}
	cleanups.push(func() error {
		return gitWorktreeRemove(context.Background(), req.repoRoot(), worktreePath)
	})

	// Step c: append .commander/ + .worktrees/ to .git/info/exclude (idempotent).
	if err := appendGitExcludes(req.repoRoot()); err != nil {
		return bail(fmt.Errorf("spawn: update git exclude: %w", err))
	}
	// No cleanup for exclude — entries are inert if commander state is gone.

	// Ensure operators dir exists for session-id file (and pre-creates the
	// directory the volume mount expects).
	if err := os.MkdirAll(operatorsDir, 0o755); err != nil {
		return bail(fmt.Errorf("spawn: create operators dir: %w", err))
	}
	if err := promptassets.EnsureAgentFor(kind, req.promptRole(promptassets.RoleOperatorCode), agentName); err != nil {
		return bail(fmt.Errorf("spawn: ensure operator agent prompt: %w", err))
	}

	// Step d: docker run.
	spawnOpts := buildSpawnOptions(req, worktreePath, containerName, s.commanderRepo, agentName)
	if kind == agentrt.Claude {
		if err := checkClaudeContainerAuth(); err != nil {
			return bail(err)
		}
		useClaudeInContainer(&spawnOpts, req, agentName)
	}
	containerID, err := s.docker.Spawn(ctx, spawnOpts)
	if err != nil {
		return bail(fmt.Errorf("spawn: docker run: %w", err))
	}
	cleanups.push(func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.docker.Stop(stopCtx, containerID)
	})

	// Step e: capture sessionID from container stdout (nd-JSON).
	sessionID, err := s.captureSession(ctx, containerID, s.captureTimeout)
	if err != nil {
		return bail(fmt.Errorf("spawn: capture session: %w", err))
	}

	// Step f: persist session-id.
	if err := writeSessionFile(sessionFile, sessionID); err != nil {
		return bail(fmt.Errorf("spawn: persist session id: %w", err))
	}
	cleanups.push(func() error {
		return os.Remove(sessionFile)
	})

	// Step g: am register (idempotent per am contract).
	if err := s.am.Register(ctx, req.ProjectName, "opencode-ai", modelShort(req.Model), agentName); err != nil {
		return bail(fmt.Errorf("spawn: am register %s: %w", agentName, err))
	}
	// am register has no inverse — re-registering same name is a no-op.

	// Step h: bd set-state cmdr=brainstorming.
	if err := s.beads.SetState(ctx, req.ProjectPath, req.TaskBeadID, "cmdr", "brainstorming", "operator spawned"); err != nil {
		return bail(fmt.Errorf("spawn: bd set-state %s: %w", req.TaskBeadID, err))
	}
	// bd set-state has no clean inverse — leaving cmdr:brainstorming on a
	// rolled-back spawn is recoverable: the next spawn will overwrite it.

	// Step i: insert agent_runs row.
	spawnTime := s.now()
	runID, err := s.store.InsertAgentRun(ctx, store.AgentRun{
		ProjectID:         req.ProjectID,
		TaskBeadID:        req.TaskBeadID,
		AgentName:         agentName,
		OpencodeSessionID: sessionID,
		ContainerID:       sql.NullString{String: containerID, Valid: true},
		SpawnTime:         spawnTime.Unix(),
		Status:            "running",
		WorktreePath:      sql.NullString{String: worktreePath, Valid: true},
	})
	if err != nil {
		return bail(fmt.Errorf("spawn: insert agent_runs: %w", err))
	}

	return &SpawnResult{
		RunID:        runID,
		SessionID:    sessionID,
		ContainerID:  containerID,
		AgentName:    agentName,
		WorktreePath: worktreePath,
		SpawnTime:    spawnTime,
	}, nil
}

// validateMountReads checks every path in req.MountReads exists on the host
// and is a directory, and that no two paths share a basename (basenames map
// to /mounts/<basename> targets in the container; collisions would mask one
// another). Returns nil for an empty slice. Errors are user-facing.
func validateMountReads(reads []string) error {
	seen := make(map[string]string, len(reads))
	for _, p := range reads {
		if p == "" {
			return errors.New("spawn: MountReads entry is empty")
		}
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("spawn: MountReads %q: %w", p, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("spawn: MountReads %q is not a directory", p)
		}
		base := filepath.Base(p)
		if base == "" || base == "/" || base == "." {
			return fmt.Errorf("spawn: MountReads %q has invalid basename %q", p, base)
		}
		if other, ok := seen[base]; ok {
			return fmt.Errorf("spawn: MountReads basename collision: %q and %q both map to /mounts/%s", other, p, base)
		}
		seen[base] = p
	}
	return nil
}

// validateRequest checks the SpawnRequest fields. Errors are user-facing.
func validateRequest(req SpawnRequest) error {
	if req.ProjectType != "code" {
		return fmt.Errorf("spawn: ProjectType %q: spawnCode requires \"code\"", req.ProjectType)
	}
	if req.ProjectName == "" {
		return errors.New("spawn: ProjectName required")
	}
	if req.TaskBeadID == "" {
		return errors.New("spawn: TaskBeadID required")
	}
	if req.Branch == "" {
		return errors.New("spawn: Branch required")
	}
	if !branchRE.MatchString(req.Branch) {
		return fmt.Errorf("spawn: branch %q must match [a-zA-Z0-9._/-]+", req.Branch)
	}
	if req.Model == "" {
		return errors.New("spawn: Model required")
	}
	if !filepath.IsAbs(req.ProjectPath) {
		return fmt.Errorf("spawn: ProjectPath %q must be absolute", req.ProjectPath)
	}
	info, err := os.Stat(req.ProjectPath)
	if err != nil {
		return fmt.Errorf("spawn: ProjectPath %q: %w", req.ProjectPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("spawn: ProjectPath %q is not a directory", req.ProjectPath)
	}
	root := req.repoRoot()
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		if req.RepoPath != "" {
			return fmt.Errorf("spawn: RepoPath %q is not a git repo (.git missing); fix it with `cmdr project set-repo %s <path-to-clone>`", root, req.ProjectName)
		}
		return fmt.Errorf("spawn: project path %q is not a git repo (.git missing); clone the code somewhere and register it with `cmdr project set-repo %s <path-to-clone>`", root, req.ProjectName)
	}
	if err := validateMountReads(req.MountReads); err != nil {
		return err
	}
	return nil
}

// flatBranch turns a branch name into a filesystem/agent-name-safe slug.
// Slashes → dashes (so "feat/auth" → "feat-auth"). Dots, underscores, and
// other branch-legal characters are preserved.
func flatBranch(branch string) string {
	return strings.ReplaceAll(branch, "/", "-")
}

// containerName composes the Docker container name. Format mirrors the
// alt-name in share/contracts.md: cmdr-<project>-<flat-branch>-<short-id>.
func containerName(project, flat, short string) string {
	return "cmdr-" + project + "-" + flat + "-" + short
}

// modelShort maps an opencode-style "provider/model" into the short label am
// expects. Falls back to the raw value when no slash is present.
func modelShort(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		out := model[i+1:]
		// Drop common suffixes/version specifiers so "claude-sonnet-4-5" → "sonnet".
		switch {
		case strings.Contains(out, "sonnet"):
			return "sonnet"
		case strings.Contains(out, "opus"):
			return "opus"
		case strings.Contains(out, "haiku"):
			return "haiku"
		}
		return out
	}
	return model
}

// buildSpawnOptions assembles docker.SpawnOptions for the operator container.
//
// Per-Task additional read-only mounts (req.MountReads, U19) are appended
// after the standard volumes. Each path P appears in the container at
// `/mounts/<basename(P)>/` with :ro semantics. Validation in validateRequest
// guarantees every path exists, is a directory, and has a unique basename.
func buildSpawnOptions(req SpawnRequest, worktreePath, containerName, commanderRepo, agentName string) docker.SpawnOptions {
	bootstrap := bootstrapPrompt(req)
	volumes := []docker.VolumeMount{
		{Source: req.ProjectPath, Target: "/workspace"},
		{Source: worktreePath, Target: "/worktree"},
		{Source: filepath.Join(req.ProjectPath, ".beads"), Target: "/workspace/.beads"},
		{Source: filepath.Join(req.ProjectPath, ".commander"), Target: "/workspace/.commander"},
		{Source: filepath.Join(commanderRepo, ".opencode", "agents"), Target: "/workspace/.opencode/agents", ReadOnly: true},
		{Source: expandHome("~/.config/opencode"), Target: "/root/.config/opencode", ReadOnly: true},
	}
	for _, p := range req.MountReads {
		volumes = append(volumes, docker.VolumeMount{
			Source:   p,
			Target:   "/mounts/" + filepath.Base(p),
			ReadOnly: true,
		})
	}
	return docker.SpawnOptions{
		Image:   operatorImage,
		Name:    containerName,
		Volumes: volumes,
		Labels: map[string]string{
			"commander.project": req.ProjectName,
			"commander.task":    req.TaskBeadID,
		},
		Network:    docker.NetworkAuto,
		AutoRemove: true,
		Cmd: []string{
			"opencode", "run",
			"--format", "json",
			"--agent", agentName,
			"--model", req.Model,
			bootstrap,
		},
	}
}

// claudeContainerEnv is the one credential forwarded into an operator container:
// the OAuth token for the user's Claude (Max) login. Claude Code on macOS keeps
// its login in the Keychain, which a container cannot read, so
// `claude setup-token` must mint CLAUDE_CODE_OAUTH_TOKEN. ANTHROPIC_API_KEY is
// deliberately NOT forwarded: it would make the Operator bill the API
// separately instead of using the plan. Set COMMANDER_ALLOW_API_KEY=1 to opt in.
var claudeContainerEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN"}

// checkClaudeContainerAuth fails a Claude Operator spawn early, with the fix,
// when the sandbox would have no usable plan login.
func checkClaudeContainerAuth() error {
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		return nil
	}
	if os.Getenv("COMMANDER_ALLOW_API_KEY") == "1" && os.Getenv("ANTHROPIC_API_KEY") != "" {
		return nil
	}
	return errors.New("spawn: a Claude Operator in the Docker sandbox signs in with your Claude login via CLAUDE_CODE_OAUTH_TOKEN " +
		"(run `claude setup-token`, export it where cmdr runs). ANTHROPIC_API_KEY is not forwarded because it would bill separately")
}

// claudeContainerPromptDir is where the role prompts are mounted in the
// operator container.
const claudeContainerPromptDir = "/root/.commander/" + agentrt.ClaudePromptDirName

// useClaudeInContainer swaps the operator container from opencode to Claude
// Code: the role prompt dir replaces the opencode mounts, and the run uses
// --dangerously-skip-permissions, which Claude only allows as root when
// IS_SANDBOX=1 marks the container as a sandbox.
func useClaudeInContainer(opts *docker.SpawnOptions, req SpawnRequest, agentName string) {
	promptDir, _ := agentrt.ClaudePromptDir()
	volumes := opts.Volumes[:0]
	for _, v := range opts.Volumes {
		if strings.Contains(v.Target, "opencode") {
			continue
		}
		volumes = append(volumes, v)
	}
	opts.Volumes = append(volumes, docker.VolumeMount{Source: promptDir, Target: claudeContainerPromptDir, ReadOnly: true})
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	opts.Env["IS_SANDBOX"] = "1"
	for _, key := range claudeContainerEnv {
		if v := os.Getenv(key); v != "" {
			opts.Env[key] = v
		}
	}
	if os.Getenv("COMMANDER_ALLOW_API_KEY") == "1" && os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			opts.Env["ANTHROPIC_API_KEY"] = v
		}
	}
	opts.Cmd = agentrt.ClaudeHeadlessArgs(agentName, claudeContainerPromptDir+"/"+agentName+".md", req.Model, agentrt.NewClaudeSessionID(), bootstrapPrompt(req), true)
}

// bootstrapPrompt is the seed message the operator sees on first invocation.
// Kept terse — operator behavior lives in prompts/operator-code.md.
func bootstrapPrompt(req SpawnRequest) string {
	if req.ExecutionClaimToken != "" {
		return executionBootstrapPrompt(req, fmt.Sprintf("Your code workspace is mounted at /worktree on branch %s.", req.Branch))
	}
	return fmt.Sprintf(
		"You are the Operator for task %s in project %q. "+
			"Worktree mounted at /worktree, branch %s. "+
			"Read the task body via `bd show %s` and reply with a one-line acknowledgement.",
		req.TaskBeadID, req.ProjectName, req.Branch, req.TaskBeadID,
	)
}

// expandHome resolves a leading "~/" against $HOME. Other inputs are returned
// untouched. Used to mount the host's opencode config dir.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// DetachedHEAD is what git rev-parse --abbrev-ref HEAD reads in a detached
// worktree (review Operators often leave one): not a branch, so a respawn has
// nothing to pass to `worktree add -B`.
const DetachedHEAD = "HEAD"

const (
	worktreeAddBudget = 3 * time.Minute
	worktreeCrowded   = 300
)

// gitWorktreeAdd runs `git -C <project> worktree add -B <branch> <path>`.
// -B force-creates the branch (resetting if it exists) so re-spawn after a
// crashed attempt is idempotent. The branch's start-point defaults to HEAD.
func gitWorktreeAdd(ctx context.Context, projectPath, worktreePath, branch string) error {
	// A closed run froze its worktree (settle.go); spawning on the bead again unfreezes it.
	if err := worktreelock.Unlock(worktreePath); err != nil {
		return fmt.Errorf("unlock worktree: %w", err)
	}
	// A crowded repo makes git slow: the add gets its own budget, not what is
	// left of the caller's, and prunes stale entries only past the threshold.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeAddBudget)
	defer cancel()
	if n := worktreelock.WorktreeCount(projectPath); n > worktreeCrowded {
		log.Printf("spawn: %d worktrees in %s: run cmdr worktrees gc", n, projectPath)
		_ = exec.CommandContext(ctx, "git", "-C", projectPath, "worktree", "prune").Run()
	}
	cmd := exec.CommandContext(ctx, "git", "-C", projectPath, "worktree", "add", "-B", branch, worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// If the worktree path already exists with the right branch, treat as success.
		// `git worktree add -B` should overwrite, but older git versions can complain.
		if strings.Contains(string(out), "already exists") {
			return nil
		}
		return fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitWorktreeRemove runs `git worktree remove --force <path>`. Idempotent: a
// missing worktree is treated as success.
func gitWorktreeRemove(ctx context.Context, projectPath, worktreePath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", projectPath, "worktree", "remove", "--force", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if strings.Contains(s, "is not a working tree") || strings.Contains(s, "not a valid path") {
			return nil
		}
		// Best-effort: also try removing the directory directly so a half-state
		// from a crashed mid-add doesn't trip the next spawn.
		_ = os.RemoveAll(worktreePath)
		return fmt.Errorf("git worktree remove: %w: %s", err, s)
	}
	return nil
}

// appendGitExcludes appends `.commander/`, `.worktrees`, `.worktrees/` and `.worktrees.noindex/` to .git/info/exclude
// when missing. No-op when the file does not exist.
func appendGitExcludes(projectPath string) error {
	excludePath := filepath.Join(projectPath, ".git", "info", "exclude")
	body, err := os.ReadFile(excludePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	want := []string{".commander/", ".worktrees", ".worktrees/", ".worktrees.noindex/"}
	existing := string(body)
	var toAdd []string
	for _, w := range want {
		if !containsLine(existing, w) {
			toAdd = append(toAdd, w)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(existing, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	for _, line := range toAdd {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func containsLine(body, needle string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}

// writeSessionFile persists the session id atomically via temp + rename.
func writeSessionFile(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-id-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.WriteString(tmp, id+"\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// captureSessionFromLogs tails `docker logs -f <containerID>` and returns the
// first non-empty sessionID it finds in the nd-JSON event stream. Returns
// an error if the timeout elapses before a sessionID arrives.
func captureSessionFromLogs(ctx context.Context, containerID string, timeout time.Duration) (string, error) {
	tCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(tCtx, "docker", "logs", "-f", containerID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("docker logs: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("docker logs: start: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	type result struct {
		id  string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		id, err := scanForSessionID(stdout)
		ch <- result{id: id, err: err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		if r.id == "" {
			return "", errors.New("docker logs closed before sessionID arrived")
		}
		return r.id, nil
	case <-tCtx.Done():
		return "", fmt.Errorf("session-capture timeout after %s", timeout)
	}
}

// scanForSessionID reads nd-JSON lines from r and returns the first non-empty
// sessionID it finds. Returns empty string with nil error when r closes
// without yielding any sessionID.
func scanForSessionID(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes_trimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if id := agentrt.EventSessionID(line); id != "" {
			return id, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

// bytes_trimSpace trims leading/trailing ASCII whitespace without allocating.
func bytes_trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// defaultShortID returns a random 8-char hex string for container naming.
func defaultShortID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// CanonicalRole is the comparison form of a role: lowercased with every
// character a role cannot carry (the dots of a bead id) turned into a dash.
// It is used only to match names written either way; the stored AgentName and
// the worktree/pane paths keep the slug as spawned.
func CanonicalRole(role string) string {
	return beads.OwnerLabelName(role)
}
