package store

import "context"

// GetProjectByID is an alias for GetProject, retained for naming-consistency
// with the rest of the lead-data layer. Returns (nil, nil) on not-found.
func (s *Store) GetProjectByID(ctx context.Context, id int64) (*Project, error) {
	return s.GetProject(ctx, id)
}
