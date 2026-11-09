package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/example-user/commander/internal/store"
)

type internalResolveActivationResult struct {
	Name          string                     `json:"name"`
	AlreadyActive bool                       `json:"already_active"`
	ActivePath    string                     `json:"active_path,omitempty"`
	Outcome       workspaceActivationOutcome `json:"outcome"`
	Root          string                     `json:"root"`
	Matches       []workspaceActivationMatch `json:"matches"`
}

func runInternalResolveOpenProject(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-resolve-add-project", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	name := fs.String("name", "", "project name to resolve")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *name == "" {
		emitErr(stderr, "missing required flag: --name")
		return 1
	}

	result := internalResolveActivationResult{Name: normalizeWorkspaceProjectName(*name)}

	statePath, err := stateDBPath()
	if err == nil {
		if st, openErr := store.Open(statePath); openErr == nil {
			defer st.Close()
			ctx := context.Background()
			if project, getErr := st.GetProjectByName(ctx, result.Name); getErr == nil && project != nil && project.Path != "" {
				result.AlreadyActive = true
				result.ActivePath = project.Path
				result.Matches = []workspaceActivationMatch{{
					Name: result.Name,
					Path: result.ActivePath,
					Type: project.Type,
				}}
			}
		}
	}
	if result.AlreadyActive {
		result.Outcome = workspaceActivationSingle
		result.Root = ""
	} else {
		resolution, err := resolveWorkspaceActivation(*name)
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		result.Outcome = resolution.Outcome
		result.Root = resolution.Root
		result.Matches = resolution.Matches
	}

	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}
