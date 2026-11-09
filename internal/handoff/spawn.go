package handoff

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// DerivedSpawnStore is the narrow store surface DerivedSpawner needs. The
// real *store.Store satisfies this; tests substitute a fake.
type DerivedSpawnStore interface {
	InsertObjectiveTask(ctx context.Context, t store.ObjectiveTask) (bool, error)
	CountRunsByProject(ctx context.Context, projectID int64, status string) (int, error)
}

// OperatorSpawner is the narrow Spawner surface DerivedSpawner needs.
// *spawn.Spawner satisfies this in production; tests pass a stub.
type OperatorSpawner interface {
	Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error)
}

// Limits configures DerivedSpawner's capacity-aware queueing.
type Limits struct {
	// MaxOperatorsPerProject caps the number of concurrently-running
	// operators per project. When the count of running runs equals or
	// exceeds this, additional derived Tasks are returned in
	// DerivedSpawnResult.Queued instead of being spawned. The caller
	// drains Queued FIFO as capacity opens (e.g., on operator close /
	// ceiling-kill events).
	//
	// Zero means "no cap" — every entry is spawned. Negative is treated
	// as zero (no cap) to keep behaviour conservative.
	MaxOperatorsPerProject int
}

// DerivedSpawner is Lead-mediated: it reads a producer Task's output.json,
// creates one derived Task bead per entry, persists the join row, and
// spawns operators up to the per-project cap. Entries beyond the cap are
// returned in Queued — caller's responsibility to drain (see U16-d).
type DerivedSpawner struct {
	beads   beads.Client
	spawner OperatorSpawner
	store   DerivedSpawnStore
	limits  Limits
	now     func() time.Time
}

// NewDerivedSpawner constructs a DerivedSpawner. All four dependencies are
// required.
func NewDerivedSpawner(b beads.Client, sp OperatorSpawner, s DerivedSpawnStore, lim Limits) *DerivedSpawner {
	return &DerivedSpawner{
		beads:   b,
		spawner: sp,
		store:   s,
		limits:  lim,
		now:     time.Now,
	}
}

// SpawnFromOutputRequest is the input to SpawnFromOutput.
type SpawnFromOutputRequest struct {
	// OutputPath is the absolute path to the producer's output.json
	// (typically `<worktree>/.commander/output.json` for code-type or
	// `<projectRoot>/.commander/output.json` for knowledge-type).
	OutputPath string
	// ParentTaskID is the bead ID of the producer Task — derived beads
	// link back via parent-child.
	ParentTaskID string
	// ProjectID, ProjectName, ProjectPath, ProjectType describe the
	// project the derived Tasks belong to. ProjectType selects the spawn
	// path ("code" → Docker+worktree; "knowledge" → tmux pane).
	ProjectID   int64
	ProjectName string
	ProjectPath string
	ProjectType string
	// ObjectiveID is the linked objective for the join row. Zero is
	// allowed for standalone Tasks not associated with an objective.
	ObjectiveID int64
	// Model is the LLM model the operator should boot with.
	Model string
}

// DerivedTask describes one derived Task produced by SpawnFromOutput. It is
// returned in either Spawned (operator already running) or Queued (waiting
// on capacity).
type DerivedTask struct {
	// BeadID is the derived Task's bead ID (returned from `bd create`).
	BeadID string
	// ParentID is the producer Task's bead ID.
	ParentID string
	// Title is the entry title (and the derived bead's title).
	Title string
	// Branch is the synthesized branch name for code-type derived Tasks
	// (empty for knowledge-type). Used by the spawner.
	Branch string
	// Prompt is the parameterized bootstrap prompt — the entry
	// description plus rendered context. Caller supplies this to the
	// operator at spawn or queue-drain time. (DerivedSpawner does not
	// itself thread the prompt through SpawnRequest because the existing
	// Spawner shape is fixed; the prompt rides on the derived bead body
	// and on this field for callers that wire it through.)
	Prompt string
}

// DerivedSpawnResult is what SpawnFromOutput returns.
type DerivedSpawnResult struct {
	// Spawned is the slice of derived Tasks for which an operator was
	// spawned successfully.
	Spawned []DerivedTask
	// Queued is the FIFO of derived Tasks that exceeded
	// MaxOperatorsPerProject. Caller drains as capacity opens.
	Queued []DerivedTask
}

// SpawnFromOutput is the main entry point. Steps:
//  1. Parse + validate the output.json artifact.
//  2. For each entry, create a derived Task bead via `bd create --parent
//     --labels kind:task,kind:derived --silent`.
//  3. Insert an objective_tasks row with is_derived=1 and
//     derived_from_task_bead_id=ParentTaskID.
//  4. If the project's running operator count is below the cap, spawn an
//     operator for the derived Task; otherwise enqueue.
//
// Returns the spawn-or-queue partition. On any per-entry bd.Create or
// store.Insert failure, the function returns an error with the partial
// progress so far surfaced via the wrapped result (callers should treat
// this as "stop, fix, retry the rest manually" — we deliberately do not
// roll back partial bd.Create calls because beads are cheap and Lead can
// reconcile).
func (d *DerivedSpawner) SpawnFromOutput(ctx context.Context, req SpawnFromOutputRequest) (*DerivedSpawnResult, error) {
	out, err := ParseOutputFile(req.OutputPath)
	if err != nil {
		return nil, err
	}
	// ParseOutputFile already calls ValidateOutput, but call it again
	// defensively to make the contract explicit at this layer.
	if err := ValidateOutput(out); err != nil {
		return nil, err
	}

	result := &DerivedSpawnResult{}
	cap := req.cap(d.limits)

	// Snapshot running count once at the start. Each successful spawn
	// inside this loop conceptually consumes one slot, so we maintain a
	// local counter rather than re-querying after each spawn (which would
	// race with the watchdog anyway).
	running, err := d.store.CountRunsByProject(ctx, req.ProjectID, "running")
	if err != nil {
		return nil, fmt.Errorf("handoff: count running runs for project %d: %w", req.ProjectID, err)
	}

	for i, entry := range out.Entries {
		derivedID, err := d.beads.Create(ctx, req.ProjectPath, entry.Title, beads.CreateOptions{
			Parent: req.ParentTaskID,
			Labels: []string{"kind:task", "kind:derived"},
			Silent: true,
		})
		if err != nil {
			if derivedID != "" {
				_ = d.beads.Delete(ctx, req.ProjectPath, derivedID, "inherited labels could not be cleaned")
			}
			return result, fmt.Errorf("handoff: create derived bead for entries[%d] %q: %w", i, entry.Title, err)
		}

		parentRef := sql.NullString{String: req.ParentTaskID, Valid: req.ParentTaskID != ""}
		if _, err := d.store.InsertObjectiveTask(ctx, store.ObjectiveTask{
			ObjectiveID:           req.ObjectiveID,
			ProjectID:             req.ProjectID,
			TaskBeadID:            derivedID,
			IsDerived:             true,
			DerivedFromTaskBeadID: parentRef,
			CreatedAt:             d.now().Unix(),
		}); err != nil {
			return result, fmt.Errorf("handoff: insert objective_task for derived %s: %w", derivedID, err)
		}

		dt := DerivedTask{
			BeadID:   derivedID,
			ParentID: req.ParentTaskID,
			Title:    entry.Title,
			Branch:   deriveBranch(req.ProjectType, derivedID, entry.Title),
			Prompt:   buildPrompt(entry),
		}

		// Capacity check: cap == 0 means unlimited. Otherwise, only
		// spawn while running < cap.
		if cap > 0 && running >= cap {
			result.Queued = append(result.Queued, dt)
			continue
		}

		if _, err := d.spawner.Spawn(ctx, spawn.SpawnRequest{
			ProjectID:   req.ProjectID,
			ProjectName: req.ProjectName,
			ProjectPath: req.ProjectPath,
			ProjectType: req.ProjectType,
			TaskBeadID:  derivedID,
			Branch:      dt.Branch,
			Model:       req.Model,
		}); err != nil {
			// Spawn failed — keep the bead + objective_task row (Lead
			// can retry by draining queue), but surface the failure.
			return result, fmt.Errorf("handoff: spawn derived operator for %s: %w", derivedID, err)
		}
		running++
		result.Spawned = append(result.Spawned, dt)
	}

	return result, nil
}

// cap returns the effective per-project cap. Negative limits are clamped
// to zero ("no cap"), matching the doc on Limits.MaxOperatorsPerProject.
func (req SpawnFromOutputRequest) cap(lim Limits) int {
	if lim.MaxOperatorsPerProject < 0 {
		return 0
	}
	return lim.MaxOperatorsPerProject
}

// branchSlugRE matches characters safe in a git branch name.
var branchSlugRE = regexp.MustCompile(`[^a-zA-Z0-9._/-]+`)

// deriveBranch synthesizes a branch name for a code-type derived Task.
// Returns empty for knowledge-type (no branch needed). Format:
// `derived/<slug>-<bead-id>` so concurrent derived Tasks under the same
// parent never collide.
func deriveBranch(projectType, derivedID, title string) string {
	if projectType != "code" {
		return ""
	}
	slug := strings.ToLower(strings.TrimSpace(title))
	slug = branchSlugRE.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 32 {
		slug = slug[:32]
	}
	if slug == "" {
		return "derived/" + derivedID
	}
	return "derived/" + slug + "-" + derivedID
}

// buildPrompt renders the parameterized bootstrap prompt for a derived
// operator. Format is intentionally simple text — the operator's base
// system prompt already handles the agent loop; this just supplies the
// upstream context.
func buildPrompt(e OutputEntry) string {
	var b strings.Builder
	b.WriteString("# Upstream context\n\n")
	b.WriteString("Title: ")
	b.WriteString(e.Title)
	b.WriteString("\n")
	if e.Description != "" {
		b.WriteString("\nDescription:\n")
		b.WriteString(e.Description)
		b.WriteString("\n")
	}
	if len(e.Context) > 0 {
		b.WriteString("\nContext:\n")
		// Stable, human-readable formatting. We don't json-encode here
		// because the operator reads this as plain text in its prompt.
		for k, v := range e.Context {
			fmt.Fprintf(&b, "  - %s: %v\n", k, v)
		}
	}
	return b.String()
}
