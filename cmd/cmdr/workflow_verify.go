package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/reviewrun"
	"github.com/example-user/commander/internal/workflowclose"
	"github.com/example-user/commander/internal/workflowtemplate"
)

var workflowGH workflowtemplate.GHRunner = workflowtemplate.RunGH

// workflowRepo is the project's GitHub repo, the one the display resolves bare pr:N labels through.
var workflowRepo = livedata.GitHubRepo

// workflowFreshDetails bypasses display caches for completion checks.
var workflowFreshDetails = beads.FreshDetails

func workflowVerifier() workflowclose.Verifier {
	return workflowclose.Verifier{RecheckRetired: beads.PostgresConfigured(),
		GH: workflowGH, Repo: workflowRepo, FreshDetails: workflowFreshDetails,
		TaskRoles: workflowTaskRoles, OperatorCommits: workflowOperatorCommits,
		LoadResults: loadReviewResults, FileFollowUps: fileReviewFollowUps,
		ResolveAddendum: resolveWorkflowAddendum,
		Warn:            func(message string) { fmt.Fprintf(os.Stderr, "warning: %s\n", message) },
	}
}
func workflowNodes(ctx context.Context, bc beads.Client, dir, id string) ([]workflowtemplate.Node, error) {
	return workflowVerifier().Nodes(ctx, bc, dir, id)
}
func checkWorkflowClose(ctx context.Context, bc beads.Client, dir, project, id string) error {
	return workflowVerifier().Check(ctx, freshReader(bc), dir, project, id)
}
func checkWorkflowCloseWaiving(ctx context.Context, bc beads.Client, dir, project, id string, w *noPRWaiver) error {
	var shared *workflowclose.NoPRWaiver
	if w != nil {
		shared = &workflowclose.NoPRWaiver{By: w.By, Reason: w.Reason, Evidence: w.Evidence}
	}
	return workflowVerifier().CheckWaiving(ctx, freshReader(bc), dir, project, id, shared)
}
func workflowNode(b beads.Bead, d beads.Detail) workflowtemplate.Node {
	return workflowclose.Node(b, d)
}
func sortedWorkflowNodes(nodes map[string]workflowtemplate.Node) []workflowtemplate.Node {
	return workflowclose.SortedNodes(nodes)
}

// loadReviewResults reads what judging a review's typed results needs: the task's
// actual runs and the original launch record each result names.
func loadReviewResults(ctx context.Context, project, task string, review *workflowtemplate.Review) error {
	var err error
	for _, c := range review.Comments {
		if _, present, _ := workflowtemplate.ParseReviewResult(c); present {
			review.Runs, err = workflowResultRuns(ctx, project, task)
			if err != nil {
				return err
			}
			break
		}
	}
	review.Recorded = make(map[string]reviewrun.Recorded)
	for _, c := range review.Comments {
		if result, present, parseErr := workflowtemplate.ParseReviewResult(c); present {
			if parseErr == nil {
				if original, loadErr := reviewrun.ReadRecorded(filepath.Join(agentrt.CommanderHome(), "review-executions"), result.RunID, result.ManifestHash); loadErr == nil {
					review.Recorded[result.ManifestHash] = original
				}
			}
		}
	}
	return nil
}
