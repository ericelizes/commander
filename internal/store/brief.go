package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// BriefBead is a bead that changed in the Brief's window, with the top-level
// workstream it belongs to ("" when it is under none).
type BriefBead struct {
	Project         string
	ID              string
	Title           string
	Status          string
	Labels          []string
	CloseReason     string
	UpdatedAt       time.Time
	Parent          string // its parent's title
	Workstream      string // the top-level kind:workstream bead over it (itself included)
	WorkstreamTitle string
}

// BriefReport is an agent's "Final report:" comment on a bead.
type BriefReport struct {
	Project   string
	BeadID    string
	Author    string
	Text      string
	CreatedAt time.Time
}

// briefTime is how bead times are stored (RFC 3339, UTC): text order is time order.
const briefTime = "2006-01-02T15:04:05Z"

// BriefBeads reads the beads that changed (or got a final report) since since,
// the final reports written since then, and each bead's top-level workstream.
// Postgres only.
func (s *Store) BriefBeads(ctx context.Context, since time.Time) ([]BriefBead, []BriefReport, error) {
	if !s.postgres {
		return nil, nil, errors.New("the Brief reads beads from Postgres only")
	}
	at := since.UTC().Format(briefTime)
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE changed AS (
  SELECT project_id, id FROM bead_tasks WHERE deleted_at IS NULL AND updated_at >= ?
  UNION
  SELECT project_id, bead_id FROM bead_comments WHERE created_at >= ? AND text LIKE 'Final report:%'
), up(project_id, bead, cur, depth) AS (
  SELECT project_id, id, id, 0 FROM changed
  UNION ALL
  SELECT u.project_id, u.bead, t.parent_id, u.depth+1 FROM up u JOIN bead_tasks t ON t.project_id=u.project_id AND t.id=u.cur
  WHERE COALESCE(t.parent_id,'')<>'' AND u.depth<30
), ws AS (
  SELECT DISTINCT ON (u.project_id, u.bead) u.project_id, u.bead, w.id AS wid, w.title AS wtitle
  FROM up u JOIN bead_tasks w ON w.project_id=u.project_id AND w.id=u.cur
  WHERE jsonb_exists(w.labels,'kind:workstream')
  ORDER BY u.project_id, u.bead, u.depth DESC
)
SELECT p.name, b.id, b.title, b.status, b.labels::text, b.close_reason, b.updated_at,
  COALESCE(par.title,''), COALESCE(ws.wid,''), COALESCE(ws.wtitle,'')
FROM changed c
JOIN bead_tasks b ON b.project_id=c.project_id AND b.id=c.id
JOIN projects p ON p.id=b.project_id
LEFT JOIN bead_tasks par ON par.project_id=b.project_id AND par.id=b.parent_id
LEFT JOIN ws ON ws.project_id=b.project_id AND ws.bead=b.id
ORDER BY b.updated_at`, at, at)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var beads []BriefBead
	for rows.Next() {
		var b BriefBead
		var labels, updated string
		if err := rows.Scan(&b.Project, &b.ID, &b.Title, &b.Status, &labels, &b.CloseReason, &updated, &b.Parent, &b.Workstream, &b.WorkstreamTitle); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(labels), &b.Labels)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		beads = append(beads, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT p.name, c.bead_id, c.author, c.text, c.created_at
FROM bead_comments c JOIN projects p ON p.id=c.project_id
WHERE c.created_at >= ? AND c.text LIKE 'Final report:%'
ORDER BY c.created_at`, at)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var reports []BriefReport
	for rows.Next() {
		var r BriefReport
		var created string
		if err := rows.Scan(&r.Project, &r.BeadID, &r.Author, &r.Text, &created); err != nil {
			return nil, nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		reports = append(reports, r)
	}
	return beads, reports, rows.Err()
}
