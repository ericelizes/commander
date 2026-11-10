package beads

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/store"
)

func pgActor(ctx context.Context) string {
	actor := os.Getenv("COMMANDER_ROLE")
	if env, ok := ctx.Value(doorEnvKey{}).([]string); ok {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "COMMANDER_ROLE="); ok {
				actor = v
			}
		}
	}
	return actor
}

func pgQualifiedEndpoint(ctx context.Context, q store.Querier, pid int64, id string) (*Bead, error) {
	var count int
	if e := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks b JOIN projects p ON p.id=b.project_id WHERE b.id=? AND b.deleted_at IS NULL`, id).Scan(&count); e != nil {
		return nil, qualifiedFailure("identity", id, e)
	}
	if count != 1 {
		return nil, qualifiedFailure("identity", id, errors.New("expected exactly one live ID across registered projects"))
	}
	b, e := scanPGBead(q.QueryRowContext(ctx, `SELECT `+pgBeadColumns+` FROM bead_tasks b WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL`, pid, id))
	if e != nil {
		return nil, qualifiedFailure("endpoint", id, e)
	}
	return &b, nil
}

func pgSourceAuthority(ctx context.Context, q store.Querier, pid int64, source *Bead) error {
	actor := pgActor(ctx)
	if actor == "" {
		return nil
	} // the user's own writes
	if strings.HasPrefix(actor, "operator-") {
		var active bool
		e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id WHERE b.project_id=? AND b.id=? AND r.project_id=b.project_id AND r.task_bead_id=b.id AND r.agent_name=? AND r.status='running' AND b.assigned_agent=?)`, pid, source.ID, actor, actor).Scan(&active)
		if e != nil {
			return e
		}
		if active {
			return nil
		}
		return qualifiedFailure("authority", source.ID, errors.New("Operator does not hold the current source run"))
	}
	current := source.ID
	seen := map[string]bool{}
	for len(seen) < 32 && current != "" && !seen[current] {
		seen[current] = true
		var manager string
		// Only the nearest registered Manager governs the source lineage.
		if e := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT am_role FROM workstream_managers WHERE project_id=? AND workstream_bead_id=?),'')`, pid, current).Scan(&manager); e != nil {
			return e
		}
		if manager != "" {
			if actor == manager {
				return nil
			}
			return qualifiedFailure("authority", source.ID, errors.New("source belongs to a different registered Manager"))
		}
		var parent string
		if e := q.QueryRowContext(ctx, `SELECT COALESCE(parent_id,'') FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL`, pid, current).Scan(&parent); e != nil {
			return e
		}
		current = parent
	}
	return qualifiedFailure("authority", source.ID, errors.New("source has no proven registered Manager lineage"))
}

func pgQualifiedDep(ctx context.Context, q store.Querier, pid int64, from, to, sub, kind string, f pgFlags) ([]byte, error) {
	if f.one("--source-project") == "" || f.one("--target-project") == "" {
		return nil, qualifiedFailure("endpoint", from, errors.New("explicit --source-project and --target-project paths are required"))
	}
	expected, e := projectID(ctx, q, f.one("--source-project"))
	if e != nil || expected != pid {
		return nil, qualifiedFailure("source", from, errors.New("source project does not match write directory"))
	}
	target, e := projectID(ctx, q, f.one("--target-project"))
	if e != nil {
		return nil, qualifiedFailure("target", to, e)
	}
	source, e := pgQualifiedEndpoint(ctx, q, pid, from)
	if e != nil {
		return nil, e
	}
	blocker, e := pgQualifiedEndpoint(ctx, q, target, to)
	if e != nil {
		return nil, e
	}
	for _, b := range []*Bead{source, blocker} {
		if slices.Contains(b.Labels, GateLabel) || slices.Contains(b.Labels, "kind:gate") || slices.Contains(b.Labels, TrackLabel) {
			return nil, qualifiedFailure("gate", b.ID, errors.New("gate and track edges require the existing internal-gate route"))
		}
	}
	if kind != "blocks" {
		return nil, qualifiedFailure("type", from, errors.New("qualified dependencies support blocks only"))
	}
	if e = pgSourceAuthority(ctx, q, pid, source); e != nil {
		return nil, e
	}
	if sub == "rm" || sub == "remove" {
		_, e = q.ExecContext(ctx, `DELETE FROM bead_dependencies WHERE project_id=? AND issue_id=? AND target_project_id=? AND depends_on_id=? AND type='blocks'`, pid, from, target, to)
		return nil, e
	}
	if sub != "add" {
		return nil, errors.New("unsupported qualified dependency operation")
	}
	v, e := loadQualifiedView(ctx, q, target, true)
	if e != nil {
		return nil, e
	}
	if e = qualifiedCycle(v.edges, qualifiedNode{pid, from}, qualifiedNode{target, to}, kind, qualifiedGraphLimit); e != nil {
		return nil, e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,target_project_id,depends_on_id,type) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, pid, from, target, to, kind)
	return nil, e
}

// QualifiedDependency writes only the source-owned dependency record. Expected
// registered project paths travel through the ordinary write door to pgWrite.
func QualifiedDependency(ctx context.Context, sourceDir, targetDir, op, from, to string) error {
	if !PostgresConfigured() {
		return errors.New("qualified dependencies require PostgreSQL")
	}
	_, _, err := runBr(ctx, sourceDir, beadsBinary(), "dep", op, from, to, "--source-project", sourceDir, "--target-project", targetDir)
	return err
}
