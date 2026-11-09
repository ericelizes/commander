// Package spawn implements U9: the code-type operator spawn flow. The Spawner
// orchestrates the full pipeline that turns a Lead-view "Enter on Task" into a
// running Docker container with an opencode operator agent attached:
//
//  1. Acquire an exclusive flock to serialize concurrent spawns of the same
//     (project, branch) pair.
//  2. Add a git worktree at <project>/.worktrees/<flat-branch>.
//  3. Update .git/info/exclude (idempotent).
//  4. docker run with per-OS network flags + commander.* labels.
//  5. Capture the opencode sessionID from the container's nd-JSON stdout.
//  6. Persist the session ID to <project>/.commander/operators/<branch>.session-id.
//  7. Register operator-<flat-branch> in the project's am-project.
//  8. Set the Task bead to cmdr=brainstorming.
//  9. Insert an agent_runs row.
//
// On any step failure, the rollback unwinds in reverse order. The orphan reaper
// (Reaper) handles the chicken-and-egg case where step 4 succeeds but step 9
// crashes before recording the row.
package spawn

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// AgentRunStore is the narrow slice of *store.Store this package needs. Defined
// as an interface so tests substitute a fake without spinning up SQLite.
//
// *store.Store satisfies this interface in production.
type AgentRunStore interface {
	InsertAgentRun(ctx context.Context, r store.AgentRun) (int64, error)
	GetAgentRunByContainer(ctx context.Context, containerID string) (*store.AgentRun, error)
	MarkClosed(ctx context.Context, runID int64, status string) error
	InsertCostEvent(ctx context.Context, e store.CostEvent) (int64, error)
	ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
}

// Spawner is the entry point for operator spawn. Construct via New. Dispatches
// on SpawnRequest.ProjectType: "code" → spawnCode (Docker + worktree),
// "knowledge" → spawnKnowledge (tmux pane, no Docker, commits to current branch).
type Spawner struct {
	store         AgentRunStore
	opencode      opencode.Client
	beads         beads.Client
	am            agentmail.Client
	docker        docker.Client
	tmux          tmuxctl.Client
	commanderRepo string

	// now is overridable for deterministic tests; defaults to time.Now.
	now func() time.Time

	// captureTimeout bounds how long Spawn waits for the container to emit a
	// sessionID via nd-JSON on stdout. Defaults to 30s. Same bound applies to
	// fifo-based capture in spawnKnowledge.
	captureTimeout time.Duration

	// captureSession overrides the session-capture path in tests. Production
	// uses captureSessionFromLogs (docker logs -f).
	captureSession func(ctx context.Context, containerID string, timeout time.Duration) (string, error)

	// captureKnowledgeSession overrides the fifo-based capture path used by
	// spawnKnowledge. Production uses captureSessionFromFifo.
	captureKnowledgeSession func(ctx context.Context, fifoPath string, timeout time.Duration) (string, error)

	// useHost picks the host runtime for code-type spawns (no Docker). Tests
	// override it; production uses hostRuntimeSelected.
	useHost func() bool

	// shortID generates the 8-char container-name suffix. Overridable for tests.
	shortID func() string
}

// SpawnRequest is the user-supplied intent.
type SpawnRequest struct {
	ProjectID   int64
	ProjectName string
	ProjectPath string // host abs path
	ProjectType string // "code" or "knowledge"
	// RepoPath is the git checkout Operator worktrees come from when the
	// project path (the beads root) is not itself a repo; "" = ProjectPath.
	RepoPath   string
	TaskBeadID string
	Branch     string // git branch name (code-type only; e.g. "feat/auth")
	// Name is the Operator's role/worktree slug (role operator-<Name>), derived
	// from the task bead by the caller (op-<slug>-<bead id>). Empty falls back to
	// the flattened branch, so the branch alone never has to name the role.
	Name  string
	Model string // e.g. "anthropic/claude-sonnet-4-5"

	// MountReads is an optional list of additional host paths the operator
	// may read. Empty in most cases — populated only when the Task bead body
	// declares operator.reads in its YAML config (parsed by Lead before
	// spawn). See U19.
	//
	//   - Code-type: appended as `-v <path>:/mounts/<basename>:ro` Docker args.
	//     The container sees readable paths at `/mounts/<basename>/`.
	//   - Knowledge-type: passed as a `COMMANDER_READS` env var (colon-separated
	//     host paths) prepended to the `opencode run` invocation in the tmux
	//     session. Knowledge operators read host paths directly via this var.
	//
	// Each path must exist on the host and be a directory; spawn fails with
	// a validation error otherwise.
	MountReads []string

	// Resume restarts a host Operator whose process was cut off: when its
	// session file holds an id, claude resumes that session (same worktree and
	// branch) instead of starting a fresh one.
	Resume bool
	// ResumeNote is why a resumed Operator was cut off, for its first message
	// ("" = a restart).
	ResumeNote string

	// Runtime is the agent CLI of a host Operator: "claude", "codex", or "" for
	// the default (resolveOperatorRuntime: respawn, service keyword, share split, Claude).
	Runtime string

	// TaskText is the task bead's title and brief; a Codex draw is overridden to
	// Claude when it names a local background service (needsBackgroundService).
	TaskText string

	// Account is the Claude account (claudeacct) of a host Operator; "" = its
	// Manager's (resolveOperatorAccount).
	Account string

	// ProdGate / ProdGateScope: a closed user-approved gate bead and its
	// description (the exact production action allowed). "" = production stays
	// forbidden.
	ProdGate      string
	ProdGateScope string

	// managerRole is the owning workstream's Manager, resolved once per spawn
	// for the Operator's env and brief.
	managerRole string
	// runtimeReason is the operator-runtime event's reason (resolveOperatorRuntime).
	runtimeReason string
	// moved says why the run left the runtime/account it was headed for
	// (steerByUsage); "" = it did not.
	moved string
	// move is the usage move resolveProvider decided, recorded by
	// applyProvider once the spawn can no longer refuse; nil = none.
	move *usageMove
	// pick is what resolveProvider chose before steering, written by
	// applyProvider; nil = nothing to write (a request never resolved).
	pick *providerPick
}

// slug is the role/worktree slug: the bead-derived Name, else the flat branch.
func (r SpawnRequest) slug() string {
	if r.Name != "" {
		return r.Name
	}
	return flatBranch(r.Branch)
}

// SpawnResult summarizes a successful spawn.
type SpawnResult struct {
	RunID        int64
	SessionID    string // ses_-prefixed, captured from opencode bootstrap
	ContainerID  string
	AgentName    string // operator-<flat-branch>
	WorktreePath string // <project>/.worktrees/<flat-branch>
	SpawnTime    time.Time
	// Runtime and Account are what a host Operator runs on; Moved says why
	// that is not where it was headed (a usage limit), "" = it is.
	Runtime, Account, Moved string
}

// DefaultCaptureTimeout is how long Spawn waits for the container to emit
// a sessionID before treating the spawn as failed and rolling back.
const DefaultCaptureTimeout = 30 * time.Second

// New constructs a Spawner. commanderRepo is the absolute path to the
// commander checkout — used to mount .opencode/agents/ into operator
// containers (read-only). The tmux client is required for knowledge-type
// spawns; pass tmuxctl.New() in production.
func New(
	s AgentRunStore,
	oc opencode.Client,
	b beads.Client,
	am agentmail.Client,
	d docker.Client,
	tx tmuxctl.Client,
	commanderRepo string,
) *Spawner {
	return &Spawner{
		store:                   s,
		opencode:                oc,
		beads:                   b,
		am:                      am,
		docker:                  d,
		tmux:                    tx,
		commanderRepo:           commanderRepo,
		now:                     time.Now,
		captureTimeout:          DefaultCaptureTimeout,
		captureSession:          captureSessionFromLogs,
		captureKnowledgeSession: captureSessionFromFifo,
		useHost:                 hostRuntimeSelected,
		shortID:                 defaultShortID,
	}
}

// Spawn dispatches on req.ProjectType to the right type-specific spawn flow.
// "code" runs spawnCode (Docker + worktree), or spawnCodeHost (worktree + a
// headless claude in a tmux pane) when Docker is unavailable or
// COMMANDER_OPERATOR_RUNTIME=host; "knowledge" runs spawnKnowledge (tmux pane,
// no Docker, commits to current branch).
func (s *Spawner) Spawn(ctx context.Context, req SpawnRequest) (result *SpawnResult, spawnErr error) {
	defer func() {
		if spawnErr == nil && result != nil && result.RunID > 0 {
			if err := s.acceptDispatchHandoff(ctx, req); err != nil {
				log.Printf("spawn: run %d started; dispatch receipt cleanup pending: %v", result.RunID, err)
			}
		}
	}()
	switch req.ProjectType {
	case "code":
		if s.useHost != nil && s.useHost() {
			return s.spawnCodeHost(ctx, req)
		}
		return s.spawnCode(ctx, req)
	case "knowledge":
		if s.useHost != nil && s.useHost() {
			return s.spawnKnowledgeHost(ctx, req)
		}
		return s.spawnKnowledge(ctx, req)
	default:
		return nil, fmt.Errorf("spawn: ProjectType %q: want \"code\" or \"knowledge\"", req.ProjectType)
	}
}

// repoRoot is the git repo code Operators branch from.
func (r SpawnRequest) repoRoot() string {
	if r.RepoPath != "" {
		return r.RepoPath
	}
	return r.ProjectPath
}

// SetCaptureTimeout overrides the session-capture timeout. Used by callers
// that want a tighter or looser bound than the default.
func (s *Spawner) SetCaptureTimeout(d time.Duration) {
	if d > 0 {
		s.captureTimeout = d
	}
}
