package beads

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/example-user/commander/internal/store"
)

type ImportCounts struct {
	Beads        int `json:"beads"`
	Dependencies int `json:"dependencies"`
	Comments     int `json:"comments"`
}

// ImportPostgresProject preserves the complete source record as well as the
// indexed core. Caller supplies a complete `bd export --all`, not a cached UI
// export. Missing targets, duplicate IDs and conflicting parents fail the whole
// project import rather than discarding evidence or creating invented tasks.
func ImportPostgresProject(ctx context.Context, s *store.Store, pid int64, r io.Reader) (ImportCounts, error) {
	var counts ImportCounts
	if !s.IsPostgres() {
		return counts, errors.New("bead import requires Postgres")
	}
	e := s.WithTx(ctx, func(q store.Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return e
		}
		var existing int
		if e := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks WHERE project_id=?`, pid).Scan(&existing); e != nil {
			return e
		}
		if existing != 0 {
			return errors.New("bead import destination is not empty")
		}
		scan := bufio.NewScanner(r)
		scan.Buffer(make([]byte, 65536), 64<<20)
		for scan.Scan() {
			raw := append([]byte(nil), scan.Bytes()...)
			if len(raw) == 0 {
				continue
			}
			var rec struct {
				ID           string    `json:"id"`
				Title        string    `json:"title"`
				Description  string    `json:"description"`
				Acceptance   string    `json:"acceptance_criteria"`
				Notes        string    `json:"notes"`
				Status       string    `json:"status"`
				Type         string    `json:"issue_type"`
				OldType      string    `json:"type"`
				Priority     int       `json:"priority"`
				Labels       []string  `json:"labels"`
				Parent       string    `json:"parent_id"`
				Assignee     string    `json:"assignee"`
				CloseReason  string    `json:"close_reason"`
				CreatedAt    string    `json:"created_at"`
				UpdatedAt    string    `json:"updated_at"`
				Dependencies []Dep     `json:"dependencies"`
				Comments     []Comment `json:"comments"`
			}
			if e := json.Unmarshal(raw, &rec); e != nil {
				return fmt.Errorf("decode bead record %d: %w", counts.Beads+1, e)
			}
			if rec.ID == "" {
				return errors.New("bead record has no ID")
			}
			if rec.Type == "" {
				rec.Type = rec.OldType
			}
			if rec.Type == "" {
				rec.Type = "task"
			}
			if rec.Labels == nil {
				rec.Labels = []string{}
			}
			for _, d := range rec.Dependencies {
				if d.Type == "parent-child" {
					if rec.Parent != "" && rec.Parent != d.DependsOnID {
						return fmt.Errorf("conflicting parent for %s", rec.ID)
					}
					rec.Parent = d.DependsOnID
				}
			}
			labels, _ := json.Marshal(rec.Labels)
			_, e := q.ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,notes,status,issue_type,priority,labels,parent_id,assigned_agent,close_reason,created_at,updated_at,source_record) VALUES(?,?,?,?,?,?,?,?,?,?::jsonb,NULLIF(?,''),?,?,?,?,?::jsonb)`, pid, rec.ID, rec.Title, rec.Description, rec.Acceptance, rec.Notes, rec.Status, rec.Type, rec.Priority, string(labels), rec.Parent, rec.Assignee, rec.CloseReason, rec.CreatedAt, rec.UpdatedAt, string(raw))
			if e != nil {
				return fmt.Errorf("import bead %s: %w", rec.ID, e)
			}
			counts.Beads++
			for _, d := range rec.Dependencies {
				if d.IssueID != "" && d.IssueID != rec.ID {
					return fmt.Errorf("dependency source mismatch for %s", rec.ID)
				}
				if _, e = q.ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,?)`, pid, rec.ID, d.DependsOnID, d.Type); e != nil {
					return e
				}
				counts.Dependencies++
			}
			for _, c := range rec.Comments {
				if _, e = q.ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at) VALUES(?,?,?,?,?,?)`, pid, rec.ID, c.ID, c.Author, c.Text, c.CreatedAt); e != nil {
					return e
				}
				counts.Comments++
			}
		}
		if e := scan.Err(); e != nil {
			return e
		}
		return store.SeedImportedJurisdictions(ctx, q, pid)
	})
	if e != nil {
		return ImportCounts{}, e
	}
	return counts, nil
}
