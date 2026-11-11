package beads

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/example-user/commander/internal/store"
)

const PlanPath = "/api/plan"

// PlanBasis is the tree version a Commander read before preparing a batch.
// Revisions are checked under the same graph lock and transaction as the edits.
type PlanBasis struct {
	RequestID            string           `json:"request_id,omitempty"`
	CoordinationID       string           `json:"coordination_id,omitempty"`
	JurisdictionRevision int64            `json:"jurisdiction_revision"`
	Beads                map[string]int64 `json:"beads"`
}

type planBasisKey struct{}

func WithPlanBasis(ctx context.Context, b *PlanBasis) context.Context {
	return context.WithValue(ctx, planBasisKey{}, b)
}
func planBasisOf(ctx context.Context) *PlanBasis {
	v, _ := ctx.Value(planBasisKey{}).(*PlanBasis)
	return v
}

// ApplyPlan always uses the service's existing approval/review/write checks.
// There is no offline or legacy fallback for a versioned planning transaction.
func ApplyPlan(ctx context.Context, dir string, ops [][]string, basis PlanBasis, intent *DoorIntent) (DoorResponse, error) {
	if !PostgresConfigured() {
		return DoorResponse{}, errors.New("versioned planning requires PostgreSQL")
	}
	if !OpsWritable(ops) {
		return DoorResponse{}, errors.New("plan requires a bounded list of write operations")
	}
	if basis.JurisdictionRevision < 1 {
		return DoorResponse{}, errors.New("plan requires a jurisdiction revision")
	}
	return viaDoor(ctx, dir, DoorRequest{Ops: ops, Atomic: true, NoWait: true, Plan: &basis, Intent: intent})
}

func pgCheckPlanBasis(ctx context.Context, q store.Querier, pid int64, ops [][]string) error {
	basis := planBasisOf(ctx)
	if basis == nil {
		return nil
	}
	if e := requirePlanningCaller(ctx); e != nil {
		return e
	}
	if basis.JurisdictionRevision < 1 || len(basis.Beads) > MaxOps*3 {
		return errors.New("invalid planning basis")
	}
	actor := pgCallerValue(ctx, "COMMANDER_ROLE")
	if actor != "" {
		var registered bool
		if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, actor).Scan(&registered); e != nil {
			return e
		}
		if !registered {
			return errors.New("only a registered planning Commander may submit a plan")
		}
	}
	var current int64
	if e := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM jurisdiction_projects WHERE project_id=?),1)`, pid).Scan(&current); e != nil {
		return e
	}
	if current != basis.JurisdictionRevision {
		return fmt.Errorf("planning conflict: jurisdiction changed from revision %d to %d", basis.JurisdictionRevision, current)
	}
	touched := map[string]bool{}
	declared := map[string]bool{}
	require := func(id string) error {
		if strings.HasPrefix(id, "$") {
			if !declared[strings.TrimPrefix(id, "$")] {
				return fmt.Errorf("unknown or forward planning reference %s", id)
			}
			return nil
		}
		touched[id] = true
		return nil
	}
	for _, op := range ops {
		if len(op) < 2 {
			return errors.New("incomplete planning operation")
		}
		if op[0] == "comments" {
			if len(op) != 4 || op[1] != "add" {
				return errors.New("invalid planning comment")
			}
			if e := require(op[2]); e != nil {
				return e
			}
			continue
		}
		pos, f, e := parsePGFlags(op[1:], "--title --description --acceptance --notes --status --priority --type --labels --add-label --remove-label --parent --assignee --reason --force --silent --no-inherit-labels --claim --ref")
		if e != nil {
			return e
		}
		if len(pos) == 0 {
			return errors.New("planning operation has no target")
		}
		switch op[0] {
		case "create":
		case "dep":
			if len(pos) != 3 {
				return errors.New("invalid planning dependency")
			}
			if e := require(pos[1]); e != nil {
				return e
			}
			if e := require(pos[2]); e != nil {
				return e
			}
		case "link":
			if len(pos) != 2 {
				return errors.New("invalid planning link")
			}
			if e := require(pos[0]); e != nil {
				return e
			}
			if e := require(pos[1]); e != nil {
				return e
			}
		default:
			if e := require(pos[0]); e != nil {
				return e
			}
		}
		if parent := f.one("--parent"); parent != "" {
			if e := require(parent); e != nil {
				return e
			}
		}
		if f.has("--ref") {
			name := f.one("--ref")
			if op[0] != "create" || len(f["--ref"]) != 1 || !planAliasName.MatchString(name) || declared[name] {
				return errors.New("invalid or duplicate creation alias")
			}
			declared[name] = true
		}
	}
	// Check every supplied read dependency as well as every mutation endpoint.
	// Stable order makes competing batches acquire row locks consistently.
	ids := make([]string, 0, len(basis.Beads))
	for id, revision := range basis.Beads {
		if id == "" || revision < 1 {
			return errors.New("invalid bead revision")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for id := range touched {
		if basis.Beads[id] < 1 {
			return fmt.Errorf("planning basis is missing bead %s", id)
		}
	}
	for _, id := range ids {
		var revision int64
		if e := q.QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, pid, id).Scan(&revision); e != nil {
			return fmt.Errorf("planning dependency %s unavailable: %w", id, e)
		}
		if revision != basis.Beads[id] {
			return fmt.Errorf("planning conflict: bead %s changed from revision %d to %d", id, basis.Beads[id], revision)
		}
	}
	return nil
}
