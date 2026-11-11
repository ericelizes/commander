package api

import (
	"context"
	"fmt"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowclose"
	"strings"
)

func (s *Server) completionValidator(dir, actor string, waivers []beads.CompletionWaiver) beads.CompletionValidator {
	return func(ctx context.Context, q store.Querier, pid int64, id string) error {
		var project string
		if e := q.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=?`, pid).Scan(&project); e != nil {
			return e
		}
		v := workflowclose.New(q, agentrt.CommanderHome())
		v.Repo = livedata.GitHubRepo
		v.GH = s.opts.WorkflowGH
		bc := beads.CompletionClient(q, pid)
		if beads.CompletionPurpose(ctx) == "pass" {
			b, err := bc.Show(ctx, dir, id)
			if err != nil {
				return err
			}
			if b == nil {
				return beads.ErrNotFound
			}
			automatic := false
			for _, l := range b.Labels {
				if l == "verifier:auto" {
					automatic = true
				}
			}
			if automatic {
				result, ok := beads.AutomaticGateResultOf(ctx)
				if !ok || result.ProjectID != pid || result.Bead != id || result.Status != "passed" {
					return fmt.Errorf("automatic gates require a canonical successful check; use cmdr internal-gate check")
				}
			}
		}
		var waiver *workflowclose.NoPRWaiver
		for _, request := range waivers {
			if request.Bead != id {
				continue
			}
			if waiver != nil {
				return fmt.Errorf("duplicate no-PR waiver for %s", id)
			}
			reason, evidence := strings.Join(strings.Fields(request.Reason), " "), strings.Join(strings.Fields(request.Evidence), " ")
			if len([]rune(reason)) < 15 || evidence == "" || len(reason) > 4000 || len(evidence) > 16000 {
				return fmt.Errorf("no-PR waiver requires a bounded reason (15+ characters) and the user's approval evidence")
			}
			b, err := bc.Show(ctx, dir, id)
			if err != nil {
				return err
			}
			if b == nil {
				return beads.ErrNotFound
			}
			details, err := beads.FreshDetails(ctx, bc, dir, []string{id})
			if err != nil {
				return err
			}
			d, ok := details[id]
			if !ok {
				return fmt.Errorf("no fresh definition for %s", id)
			}
			if request.Definition == "" || request.Definition != beads.CompletionDefinition(*b, d) {
				return fmt.Errorf("no-PR waiver definition changed; reread the gate and its recorded approval")
			}
			if actor != "" && b.PlanningCommander != actor {
				return fmt.Errorf("no-PR waiver belongs to planning Commander %s", b.PlanningCommander)
			}
			by := actor
			if by == "" {
				by = "the user"
			}
			waiver = &workflowclose.NoPRWaiver{By: by, Reason: reason, Evidence: evidence}
		}
		return v.CheckWaiving(ctx, bc, dir, project, id, waiver)
	}
}
