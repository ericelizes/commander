package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowclose"
)

// workflowOperatorCommits says which of shas an Operator run of project wrote
// (sha -> why), task "" meaning every run of the project. An external writer is
// the user's own sessions, so a commit an Operator wrote is never bound as one,
// and a reviewer task that wrote a bound external commit is not independent.
var workflowOperatorCommits = func(ctx context.Context, project, task string, shas []string) (map[string]string, error) {
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
	var runs []store.AgentRun
	if task == "" {
		runs, err = st.ListAgentRunsByProject(ctx, p.ID, "")
	} else {
		runs, err = st.ListAgentRunsByTaskBeadID(ctx, task, "")
	}
	if err != nil {
		return nil, err
	}
	runs = slices.DeleteFunc(runs, func(r store.AgentRun) bool { return r.ProjectID != p.ID })
	repo := p.RepoPath
	if repo == "" {
		repo = p.Path
	}
	return operatorAuthorship(ctx, repo, runs, task == "", shas)
}

func operatorAuthorship(ctx context.Context, repo string, runs []store.AgentRun, anyOperator bool, shas []string) (map[string]string, error) {
	return workflowclose.OperatorAuthorship(ctx, repo, runs, anyOperator, shas)
}

const maxCopyPaths = workflowclose.MaxCopyPaths
