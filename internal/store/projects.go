package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Project mirrors the projects table.
type Project struct {
	ID          int64
	Name        string
	Type        string // "code" | "knowledge"
	Path        string
	ActivatedAt int64 // unix seconds
	Status      string
	RepoPath    string // optional git checkout worktrees come from; "" = Path
}

// InsertProject inserts a project and returns its rowid.
func (s *Store) InsertProject(ctx context.Context, p Project) (int64, error) {
	return insertProject(ctx, s.db, p)
}

func insertProject(ctx context.Context, q Querier, p Project) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `
		INSERT INTO projects(name, type, path, activated_at, status, repo_path)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id
	`, p.Name, p.Type, p.Path, p.ActivatedAt, p.Status, p.RepoPath).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: insert project %q: %w", p.Name, err)
	}
	return id, nil
}

// GetProject returns the project with the given id, or (nil, nil) if absent.
func (s *Store) GetProject(ctx context.Context, id int64) (*Project, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, type, path, activated_at, status, repo_path
		FROM projects WHERE id = ?
	`, id)
	return scanProject(row)
}

// GetProjectByName returns (nil, nil) if not found.
func (s *Store) GetProjectByName(ctx context.Context, name string) (*Project, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, type, path, activated_at, status, repo_path
		FROM projects WHERE name = ?
	`, name)
	return scanProject(row)
}

// ListProjects returns every project in insert order.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, type, path, activated_at, status, repo_path
		FROM projects ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Path, &p.ActivatedAt, &p.Status, &p.RepoPath); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProject(row *sql.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Type, &p.Path, &p.ActivatedAt, &p.Status, &p.RepoPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan project: %w", err)
	}
	return &p, nil
}

// SetProjectRepoPath sets (or, with "", clears) the project's repo_path.
func (s *Store) SetProjectRepoPath(ctx context.Context, name, repoPath string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE projects SET repo_path = ? WHERE name = ?`, repoPath, name)
	if err != nil {
		return fmt.Errorf("store: set repo_path of %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: project %q not found", name)
	}
	return nil
}
