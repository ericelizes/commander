package tui

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// OperatorLiveData is the side-panel snapshot the Operator view renders
// against once Services are wired. The chat content itself is driven
// independently by opencode.Run dispatches via operator_chat.go — this
// struct covers worktree state, container info, ceilings progress, and
// the title-bar Task title/phase.
//
// Partial loads are tolerated: any per-source error is captured in LoadErr
// and the renderer falls back to the fixture content for whichever source
// failed to populate.
type OperatorLiveData struct {
	Run          *store.AgentRun
	Project      *store.Project
	TaskTitle    string
	TaskPhase    string
	BranchName   string
	CommitsAhead int
	FilesDirty   int
	LoadErr      error
	LoadedAt     time.Time
}

// OperatorDataDeps is the narrow slice of dependencies LoadOperatorData
// needs. Pulled out so tests can inject mocks (a fake bd client, a stub
// git runner) without spinning up Services or hitting a real worktree.
type OperatorDataDeps struct {
	Store *store.Store
	Beads beads.Client
	// GitRun, when non-nil, replaces the os/exec git invocation used to
	// fetch worktree state. Tests inject a stub returning canned output.
	GitRun func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// LoadOperatorData hits the live backends and returns a snapshot for one
// operator. Errors from individual sources collapse into result.LoadErr;
// whatever loaded successfully is still returned so the renderer can show
// partial state.
func LoadOperatorData(ctx context.Context, deps OperatorDataDeps, taskBeadID string, projectID int64) OperatorLiveData {
	out := OperatorLiveData{LoadedAt: time.Now()}

	if deps.Store == nil {
		out.LoadErr = errOperatorDataNoStore
		return out
	}

	proj, err := deps.Store.GetProjectByID(ctx, projectID)
	if err != nil {
		out.LoadErr = err
		return out
	}
	if proj != nil {
		out.Project = proj
	}

	// Look up the running operator for this Task. If none, the side panel
	// renders zeros — do not treat as a fatal error.
	run, err := deps.Store.GetAgentRunByTaskBeadID(ctx, taskBeadID)
	if err != nil && out.LoadErr == nil {
		out.LoadErr = err
	}
	if run != nil {
		out.Run = run
	}

	// Resolve Task title + phase from bd show. Errors are non-fatal — the
	// renderer falls back to the bead ID when title is empty.
	bdClient := deps.Beads
	if bdClient == nil {
		bdClient = beads.New()
	}
	if proj != nil && proj.Path != "" {
		bead, bdErr := bdClient.Show(ctx, proj.Path, taskBeadID)
		if bdErr != nil && out.LoadErr == nil {
			out.LoadErr = bdErr
		}
		if bead != nil {
			out.TaskTitle = bead.Title
			out.TaskPhase = phaseFromLabels(bead.Labels)
		}
	}

	// Worktree state: only populated for code projects with a known run.
	if proj != nil && proj.Type == "code" && out.Run != nil {
		gitRun := deps.GitRun
		if gitRun == nil {
			gitRun = defaultGitRun
		}
		worktree := worktreePathForRun(proj.Path, out.Run)
		out.BranchName = readBranchName(ctx, gitRun, worktree)
		out.CommitsAhead = readCommitsAhead(ctx, gitRun, worktree)
		out.FilesDirty = readFilesDirty(ctx, gitRun, worktree)
	}

	return out
}

// worktreePathForRun derives the worktree directory the operator is
// editing inside. Commander's spawn flow places worktrees under
// <project>/.worktrees/<flat-branch>; the flat-branch label is the cmdr/
// fragment shoved into agent_runs at spawn time. When the run carries no
// useful identifier, we fall back to the project path itself so git still
// runs against *something* sensible.
func worktreePathForRun(projectPath string, run *store.AgentRun) string {
	if run == nil || projectPath == "" {
		return projectPath
	}
	// Operators key off task bead id in the absence of a stored worktree
	// path. The shape mirrors the spawn flow:
	//   <project>/.worktrees/cmdr-<task-bead-id>
	if run.TaskBeadID == "" {
		return projectPath
	}
	flat := strings.ReplaceAll(run.TaskBeadID, "/", "-")
	return filepath.Join(projectPath, ".worktrees", flat)
}

// defaultGitRun is the production GitRun: a thin os/exec wrapper that
// executes `git -C <dir> <args...>` with stdout captured. stderr is
// discarded — the caller treats any non-zero exit as a "no data"
// situation and renders zeros.
func defaultGitRun(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// readBranchName runs `git rev-parse --abbrev-ref HEAD` in the worktree.
// Returns "" on any error — the renderer treats empty as "unknown branch".
func readBranchName(ctx context.Context, run func(ctx context.Context, dir string, args ...string) ([]byte, error), dir string) string {
	if dir == "" {
		return ""
	}
	out, err := run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readCommitsAhead counts commits on HEAD that are not on main. Returns 0
// when the worktree can't be inspected (offline, no main branch yet, etc).
func readCommitsAhead(ctx context.Context, run func(ctx context.Context, dir string, args ...string) ([]byte, error), dir string) int {
	if dir == "" {
		return 0
	}
	out, err := run(ctx, dir, "rev-list", "--count", "main..HEAD")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// readFilesDirty counts modified+untracked entries via `git status --short`.
// One line per file; total lines = total dirty count.
func readFilesDirty(ctx context.Context, run func(ctx context.Context, dir string, args ...string) ([]byte, error), dir string) int {
	if dir == "" {
		return 0
	}
	out, err := run(ctx, dir, "status", "--short")
	if err != nil {
		return 0
	}
	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

// errOperatorDataNoStore signals that LoadOperatorData was called with no
// Store. Programmer error; surfaced distinctly so tests can sentinel-check.
var errOperatorDataNoStore = operatorDataError("operator_data: store dependency is nil")

// operatorDataError lets us mint sentinel errors without errors.New so
// they can sit in package-level var slots.
type operatorDataError string

func (e operatorDataError) Error() string { return string(e) }

// formatBranchLabel returns the branch name styled for the worktree
// section: "<branch>" when set, "(detached)" when empty. Pulled out so
// the renderer and tests share one string.
func formatBranchLabel(b string) string {
	if b == "" {
		return "(detached)"
	}
	return b
}
