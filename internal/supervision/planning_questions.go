package supervision

import (
	"context"
	"fmt"
	"github.com/example-user/commander/internal/store"
	"strings"
	"time"
)

func (w *Worker) planningQuestionGroups(ctx context.Context, mode string, now time.Time) (map[string][]store.PlanningQuestionDelivery, error) {
	jobs, e := w.Store.LeasePlanningQuestions(ctx, now, mode == "active", 128)
	if e != nil {
		return nil, e
	}
	groups := map[string][]store.PlanningQuestionDelivery{}
	seen := map[string]bool{}
	for _, job := range jobs {
		if mode != "active" {
			if e = w.Store.FinishPlanningQuestionDelivery(ctx, job, now.Add(time.Minute), false, "Observation only"); e != nil {
				return nil, e
			}
			continue
		}
		target := job.Owner
		if job.Attempts >= 3 {
			target = "commander"
		}
		key := target + "\x00" + job.Owner + "\x00" + job.QuestionID
		if seen[key] {
			continue
		}
		seen[key] = true
		groups[target] = append(groups[target], job)
	}
	return groups, nil
}
func (w *Worker) finishPlanningQuestions(ctx context.Context, jobs []store.PlanningQuestionDelivery, due time.Time, attempted bool, detail string) error {
	for _, job := range jobs {
		if e := w.Store.FinishPlanningQuestionDelivery(ctx, job, due, attempted, detail); e != nil {
			return e
		}
	}
	return nil
}
func (w *Worker) preparePlanningQuestions(ctx context.Context, jobs []store.PlanningQuestionDelivery) ([]store.PlanningQuestionDelivery, error) {
	out := []store.PlanningQuestionDelivery{}
	for _, job := range jobs {
		ok, e := w.Store.PlanningQuestionDeliveryCurrent(ctx, job)
		if e != nil {
			return nil, e
		}
		if ok {
			out = append(out, job)
		}
	}
	return out, nil
}
func planningQuestionPrompt(target string, jobs []store.PlanningQuestionDelivery) string {
	if len(jobs) == 0 {
		return ""
	}
	var text strings.Builder
	text.WriteString("\nDurable planning decisions need attention. These are recorded coordination questions, not execution claims, human approvals or new permission. Delivery alone does not answer them. Read current scopes and the exact saved proposal before acting.\n")
	for _, job := range jobs {
		if target != job.Owner {
			fmt.Fprintf(&text, "- Decision %s belongs to %s. Repair its conversation/delivery; do not answer outside your jurisdiction or take ownership implicitly. Inspect: cmdr question get --id %s\n", job.QuestionID, job.Owner, job.QuestionID)
		} else {
			fmt.Fprintf(&text, "- Read and resolve planning question: cmdr question get --id %s . Use cmdr plan get --id %s for its saved plan, if present. Record the exact answer with cmdr question answer; preserve holds and human approval boundaries.\n", job.QuestionID, job.QuestionID)
		}
	}
	return text.String()
}
