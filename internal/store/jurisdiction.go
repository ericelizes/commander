package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var commanderIdentity = regexp.MustCompile(`^commander(-[a-z0-9][a-z0-9-]{0,88})?$`)

var ErrJurisdictionConflict = errors.New("planning ownership changed; reread the current jurisdiction")

type Jurisdiction struct {
	ProjectID   int64  `json:"project_id"`
	BeadID      string `json:"bead_id"`
	CommanderID string `json:"commander_id"`
	Explicit    bool   `json:"explicit"`
	Revision    int64  `json:"revision"`
}

type CommanderSession struct {
	AgentID   string `json:"agent_id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) GetCommander(ctx context.Context, id string) (*CommanderSession, error) {
	if !s.postgres {
		return nil, errors.New("Commander jurisdictions require PostgreSQL")
	}
	var c CommanderSession
	e := s.db.QueryRowContext(ctx, `SELECT agent_id,title,created_at FROM commander_sessions WHERE agent_id=?`, id).Scan(&c.AgentID, &c.Title, &c.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, e
}

func (s *Store) ListCommanders(ctx context.Context) ([]CommanderSession, error) {
	if !s.postgres {
		return nil, errors.New("Commander jurisdictions require PostgreSQL")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT agent_id,title,created_at FROM commander_sessions ORDER BY created_at,agent_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []CommanderSession{}
	for rows.Next() {
		var c CommanderSession
		if e = rows.Scan(&c.AgentID, &c.Title, &c.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RegisterCommander records a logical conversation identity, not a process.
// Runtime creation and capability validation happen at the application door.
func (s *Store) RegisterCommander(ctx context.Context, id, title string) error {
	if !s.postgres {
		return errors.New("Commander jurisdictions require PostgreSQL")
	}
	if !commanderIdentity.MatchString(id) || strings.TrimSpace(title) == "" {
		return errors.New("Commander requires a valid id and title")
	}
	_, e := s.db.ExecContext(ctx, `INSERT INTO commander_sessions(agent_id,title) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET title=excluded.title`, id, title)
	return e
}

func (s *Store) Jurisdiction(ctx context.Context, pid int64, bead string) (Jurisdiction, error) {
	j := Jurisdiction{ProjectID: pid, BeadID: bead}
	if !s.postgres {
		return j, errors.New("Commander jurisdictions require PostgreSQL")
	}
	e := s.db.QueryRowContext(ctx, `SELECT commander_planning_owner(b.project_id,b.id),EXISTS(SELECT 1 FROM commander_jurisdictions x WHERE x.project_id=b.project_id AND x.bead_id=b.id),COALESCE(p.revision,1) FROM bead_tasks b LEFT JOIN jurisdiction_projects p ON p.project_id=b.project_id WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL`, pid, bead).Scan(&j.CommanderID, &j.Explicit, &j.Revision)
	return j, e
}

type JurisdictionTransfer struct {
	ProjectID                                           int64
	BeadID, Actor, ExpectedOwner, NextCommander, Reason string
	ExpectedRevision                                    int64
	Inherit                                             bool
	// CreateTitle atomically creates a new conversation with this handoff.
	// Only the user-facing door may create planning identities.
	CreateTitle string
}

// TransferJurisdiction changes one planning boundary, preserving all nested
// overrides, live execution claims, holds and recorded decisions. Empty Actor
// means an interactive user at the trusted application door, as other cmdr doors.
func (s *Store) TransferJurisdiction(ctx context.Context, t JurisdictionTransfer) (Jurisdiction, error) {
	var result Jurisdiction
	if !s.postgres {
		return result, errors.New("Commander jurisdictions require PostgreSQL")
	}
	if t.ProjectID <= 0 || t.BeadID == "" || t.ExpectedRevision < 1 || t.ExpectedOwner == "" || strings.TrimSpace(t.Reason) == "" {
		return result, errors.New("transfer requires project, bead, expected owner/revision and reason")
	}
	e := s.WithTx(ctx, func(q Querier) error {
		// Same lock as guarded graph edits: parentage cannot change underneath
		// ownership validation or subtree generation invalidation.
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, t.ProjectID); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, `INSERT INTO jurisdiction_projects(project_id) VALUES(?) ON CONFLICT DO NOTHING`, t.ProjectID); e != nil {
			return e
		}
		var revision int64
		if e := q.QueryRowContext(ctx, `SELECT revision FROM jurisdiction_projects WHERE project_id=? FOR UPDATE`, t.ProjectID).Scan(&revision); e != nil {
			return e
		}
		if revision != t.ExpectedRevision {
			return ErrJurisdictionConflict
		}
		var owner string
		if e := q.QueryRowContext(ctx, `SELECT commander_planning_owner(project_id,id) FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, t.ProjectID, t.BeadID).Scan(&owner); e != nil {
			return e
		}
		if owner != t.ExpectedOwner {
			return ErrJurisdictionConflict
		}
		if t.Actor != "" && t.Actor != owner {
			return fmt.Errorf("%s does not own planning jurisdiction for %s", t.Actor, t.BeadID)
		}
		next := t.NextCommander
		if t.CreateTitle != "" {
			if t.Actor != "" || t.Inherit {
				return errors.New("only the user may create a Commander conversation with a scope")
			}
			if next == "commander" || !commanderIdentity.MatchString(next) {
				return errors.New("invalid new Commander id")
			}
			if strings.TrimSpace(t.CreateTitle) == "" {
				return errors.New("conversation title cannot be empty")
			}
			if _, e := q.ExecContext(ctx, `INSERT INTO commander_sessions(agent_id,title) VALUES(?,?)`, next, t.CreateTitle); e != nil {
				return e
			}
		}
		if t.Inherit {
			if t.NextCommander != "" {
				return errors.New("inherit cannot also specify a Commander")
			}
			var parent sql.NullString
			if e := q.QueryRowContext(ctx, `SELECT parent_id FROM bead_tasks WHERE project_id=? AND id=?`, t.ProjectID, t.BeadID).Scan(&parent); e != nil {
				return e
			}
			if e := q.QueryRowContext(ctx, `SELECT commander_planning_owner(?,?)`, t.ProjectID, parent).Scan(&next); e != nil {
				return e
			}
			if _, e := q.ExecContext(ctx, `DELETE FROM commander_jurisdictions WHERE project_id=? AND bead_id=?`, t.ProjectID, t.BeadID); e != nil {
				return e
			}
		} else {
			if next == "" {
				return errors.New("transfer requires a next Commander")
			}
			// The FK refuses unknown conversation identities.
			if _, e := q.ExecContext(ctx, `INSERT INTO commander_jurisdictions(project_id,bead_id,commander_id) VALUES(?,?,?) ON CONFLICT(project_id,bead_id) DO UPDATE SET commander_id=excluded.commander_id,origin='explicit'`, t.ProjectID, t.BeadID, next); e != nil {
				return e
			}
		}
		revision++
		if _, e := q.ExecContext(ctx, `UPDATE jurisdiction_projects SET revision=? WHERE project_id=?`, revision, t.ProjectID); e != nil {
			return e
		}
		// Rebind planning responsibility only; a working executor retains its claim.
		// Bump the generation for every affected bead to fence old recovery requests.
		if _, e := q.ExecContext(ctx, `WITH RECURSIVE scope(id,path) AS (
   SELECT id,ARRAY[id] FROM bead_tasks WHERE project_id=? AND id=?
   UNION ALL SELECT b.id,s.path||b.id FROM bead_tasks b JOIN scope s ON b.parent_id=s.id WHERE b.project_id=? AND NOT b.id=ANY(s.path)
   AND NOT EXISTS(SELECT 1 FROM commander_jurisdictions j WHERE j.project_id=b.project_id AND j.bead_id=b.id)
  ) UPDATE bead_tasks b SET responsible_agent_id=CASE WHEN b.responsible_agent_id=? THEN ? ELSE b.responsible_agent_id END,
   responsibility_epoch=b.responsibility_epoch+1 WHERE b.project_id=? AND b.id IN (SELECT id FROM scope)`, t.ProjectID, t.BeadID, t.ProjectID, owner, next, t.ProjectID); e != nil {
			return e
		}
		actor := t.Actor
		if actor == "" {
			actor = "user"
		}
		if _, e := q.ExecContext(ctx, `INSERT INTO jurisdiction_history(project_id,bead_id,revision,actor,previous_commander,next_commander,inherit,reason) VALUES(?,?,?,?,?,?,?,?)`, t.ProjectID, t.BeadID, revision, actor, owner, next, t.Inherit, t.Reason); e != nil {
			return e
		}
		// Invalidate graph caches even for a scope with only executor-owned beads.
		if _, e := q.ExecContext(ctx, `UPDATE projects SET state_revision=state_revision+1 WHERE id=?`, t.ProjectID); e != nil {
			return e
		}
		result = Jurisdiction{ProjectID: t.ProjectID, BeadID: t.BeadID, CommanderID: next, Revision: revision, Explicit: !t.Inherit}
		return nil
	})
	return result, e
}

// SeedImportedJurisdictions is used only inside a new project's empty-destination
// import transaction, after its complete tree is present. Registration rows are
// imported before beads during SQLite migration, so schema-time seeding alone
// cannot recover their scope. Existing jurisdiction is never overwritten.
func SeedImportedJurisdictions(ctx context.Context, q Querier, pid int64) error {
	var count int
	if e := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM commander_jurisdictions WHERE project_id=?`, pid).Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return errors.New("import cannot overwrite planning jurisdiction")
	}
	for _, stmt := range []string{
		`INSERT INTO jurisdiction_projects(project_id) VALUES(?) ON CONFLICT DO NOTHING`,
		`INSERT INTO commander_sessions(agent_id,title) SELECT DISTINCT m.am_role,m.am_role FROM workstream_managers m JOIN bead_tasks b ON b.project_id=m.project_id AND b.id=m.workstream_bead_id WHERE m.project_id=? AND m.am_role<>'' ON CONFLICT DO NOTHING`,
		`INSERT INTO commander_jurisdictions(project_id,bead_id,commander_id,origin) SELECT m.project_id,m.workstream_bead_id,m.am_role,'legacy' FROM workstream_managers m JOIN bead_tasks b ON b.project_id=m.project_id AND b.id=m.workstream_bead_id WHERE m.project_id=? AND m.am_role<>''`,
		`UPDATE bead_tasks SET responsible_agent_id=commander_planning_owner(project_id,id) WHERE project_id=? AND active_run_id IS NULL`,
	} {
		if _, e := q.ExecContext(ctx, stmt, pid); e != nil {
			return e
		}
	}
	return nil
}
