package store

import (
	"context"
	"fmt"
)

// InspectorWindow resolves the (project id, workstream bead id) key of a
// window's inspector. An empty wsBeadID is the Commander window (0, ""). With a
// workstream, projectName may be empty: the project is then found through the
// workstream's Manager row.
func (s *Store) InspectorWindow(ctx context.Context, projectName, wsBeadID string) (int64, string, error) {
	if wsBeadID == "" {
		if projectName != "" {
			return 0, "", fmt.Errorf("--project needs --ws (the Commander window has no project)")
		}
		return 0, "", nil
	}
	if projectName != "" {
		p, err := s.GetProjectByName(ctx, projectName)
		if err != nil {
			return 0, "", err
		}
		if p == nil {
			return 0, "", fmt.Errorf("project %q not found", projectName)
		}
		return p.ID, wsBeadID, nil
	}
	managers, err := s.ListWorkstreamManagers(ctx)
	if err != nil {
		return 0, "", err
	}
	for _, m := range managers {
		if m.WorkstreamBeadID == wsBeadID {
			return m.ProjectID, wsBeadID, nil
		}
	}
	return 0, "", fmt.Errorf("workstream %q has no Manager yet; pass --project", wsBeadID)
}
