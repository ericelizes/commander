package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/spawn"
)

// workflowResultRuns reads exact task/run attribution, never mutable bead owners.
var workflowResultRuns = func(ctx context.Context, project, task string) (map[int64]string, error) {
	st, done, err := needsYouStoreFn()
	if err != nil {
		return nil, err
	}
	defer done()
	p, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("unknown project %s", project)
	}
	runs, err := st.ListAgentRunsByTaskBeadID(ctx, task, "")
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, r := range runs {
		if r.ProjectID != p.ID || r.TaskBeadID != task || r.ID <= 0 {
			return nil, fmt.Errorf("ambiguous task run %s", task)
		}
		role := strings.ToLower(r.AgentName)
		if strings.HasPrefix(role, "op-") {
			role = "operator-" + role
		}
		if !strings.HasPrefix(role, "operator-") || role == "operator-" {
			return nil, fmt.Errorf("invalid review run actor")
		}
		out[r.ID] = spawn.CanonicalRole(role)
	}
	return out, nil
}
