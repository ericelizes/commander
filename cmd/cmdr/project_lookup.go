package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

func resolveProjectPath(name string) (string, error) {
	project, err := resolveProject(name)
	if err != nil {
		return "", err
	}
	return project.Path, nil
}

func resolveProject(name string) (*store.Project, error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, fmt.Errorf("resolve state dir: %w", err)
	}
	if err := checkStateStorePresent(statePath); err != nil {
		return nil, err
	}

	st, err := store.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	project, err := st.GetProjectByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get project %q: %w", name, err)
	}
	if project == nil {
		return nil, fmt.Errorf("project %q not added — run `cmdr internal-add-project` from chat first", name)
	}
	if project.Path == "" {
		return nil, fmt.Errorf("project %q has empty path in state.db", name)
	}
	if _, err := os.Stat(project.Path); err != nil {
		return nil, fmt.Errorf("project path %q does not exist: %w", project.Path, err)
	}
	return project, nil
}

func resolveTaskOpenTarget(taskBeadID string) (resolvedOpenTarget, error) {
	statePath, err := stateDBPath()
	if err != nil {
		return resolvedOpenTarget{}, fmt.Errorf("resolve state dir: %w", err)
	}
	if err := checkStateStorePresent(statePath); err != nil {
		return resolvedOpenTarget{}, err
	}

	st, err := store.Open(statePath)
	if err != nil {
		return resolvedOpenTarget{}, fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	projectIDs := make(map[int64]struct{})

	runs, err := st.ListAgentRunsByTaskBeadID(ctx, taskBeadID, "running")
	if err != nil {
		return resolvedOpenTarget{}, fmt.Errorf("resolve task %q from running operators: %w", taskBeadID, err)
	}
	for _, run := range runs {
		projectIDs[run.ProjectID] = struct{}{}
	}
	if len(projectIDs) == 0 {
		links, err := st.ListObjectiveTasksByTaskBeadID(ctx, taskBeadID)
		if err != nil {
			return resolvedOpenTarget{}, fmt.Errorf("resolve task %q from objective links: %w", taskBeadID, err)
		}
		for _, link := range links {
			projectIDs[link.ProjectID] = struct{}{}
		}
	}
	if len(projectIDs) == 0 {
		return resolvedOpenTarget{}, fmt.Errorf("task %q was not found in running operators or objective links", taskBeadID)
	}

	projects := make([]store.Project, 0, len(projectIDs))
	for projectID := range projectIDs {
		project, err := st.GetProject(ctx, projectID)
		if err != nil {
			return resolvedOpenTarget{}, fmt.Errorf("lookup project %d for task %q: %w", projectID, taskBeadID, err)
		}
		if project == nil {
			return resolvedOpenTarget{}, fmt.Errorf("task %q points at missing project id %d", taskBeadID, projectID)
		}
		if project.Path == "" {
			return resolvedOpenTarget{}, fmt.Errorf("project %q has empty path in state.db", project.Name)
		}
		if _, err := os.Stat(project.Path); err != nil {
			return resolvedOpenTarget{}, fmt.Errorf("project path %q does not exist: %w", project.Path, err)
		}
		projects = append(projects, *project)
	}

	if len(projects) > 1 {
		names := make([]string, 0, len(projects))
		for _, project := range projects {
			names = append(names, project.Name)
		}
		sort.Strings(names)
		return resolvedOpenTarget{}, fmt.Errorf(
			"task %q is ambiguous across projects: %s — activate or open it via Commander chat first",
			taskBeadID,
			strings.Join(names, ", "),
		)
	}

	project := projects[0]
	return resolvedOpenTarget{
		kind:        targetTask,
		projectName: project.Name,
		projectPath: project.Path,
		taskBeadID:  taskBeadID,
	}, nil
}

type workspaceActivationOutcome string

const (
	workspaceActivationNone      workspaceActivationOutcome = "none"
	workspaceActivationSingle    workspaceActivationOutcome = "single"
	workspaceActivationAmbiguous workspaceActivationOutcome = "ambiguous"
)

type workspaceActivationMatch struct {
	Name string
	Path string
	Type string
}

type workspaceActivationResolution struct {
	Query   string
	Root    string
	Outcome workspaceActivationOutcome
	Matches []workspaceActivationMatch
}

func resolveWorkspaceActivation(name string) (workspaceActivationResolution, error) {
	root, err := promptassets.CommanderWorkspaceRoot()
	if err != nil {
		return workspaceActivationResolution{}, err
	}
	return resolveWorkspaceActivationAtRoot(root, name)
}

func resolveWorkspaceActivationAtRoot(root, name string) (workspaceActivationResolution, error) {
	query := normalizeWorkspaceProjectName(name)
	if query == "" {
		return workspaceActivationResolution{
			Query:   name,
			Root:    root,
			Outcome: workspaceActivationNone,
		}, nil
	}

	var matches []workspaceActivationMatch
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		base := filepath.Base(path)
		if shouldSkipWorkspaceDir(base) {
			return filepath.SkipDir
		}
		if normalizeWorkspaceProjectName(base) != query {
			return nil
		}

		projectType := "knowledge"
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			projectType = "code"
		}
		matches = append(matches, workspaceActivationMatch{
			Name: base,
			Path: path,
			Type: projectType,
		})
		return filepath.SkipDir
	})
	if err != nil {
		return workspaceActivationResolution{}, fmt.Errorf("search workspace root %s: %w", root, err)
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Path < matches[j].Path
	})

	resolution := workspaceActivationResolution{
		Query:   name,
		Root:    root,
		Matches: matches,
	}
	switch len(matches) {
	case 0:
		resolution.Outcome = workspaceActivationNone
	case 1:
		resolution.Outcome = workspaceActivationSingle
	default:
		resolution.Outcome = workspaceActivationAmbiguous
	}
	return resolution, nil
}

func normalizeWorkspaceProjectName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "the ")
	name = strings.TrimSuffix(name, " project")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, " ", "-")
	return strings.Trim(name, "-")
}

func shouldSkipWorkspaceDir(name string) bool {
	switch name {
	case ".git", ".commander", "node_modules", "vendor":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// PostgreSQL installations deliberately have no SQLite state file. Validate
// the selected configuration without creating a retired backend as a sentinel.
func checkStateStorePresent(path string) error {
	dsn, e := store.PostgresDSN(path)
	if e != nil {
		return e
	}
	if dsn != "" {
		return nil
	}
	if _, e = os.Stat(path); e != nil {
		return fmt.Errorf("no state db at %s: %w", path, e)
	}
	return nil
}
