package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
)

// questionCard is the card for an agent's question to the user: the question
// in its own words, the agent's recommendation as context, answered in free text.
func questionCard(q store.OpenQuestion) (ask, decision string) {
	question := strings.TrimSpace(strings.SplitN(q.Question, "\n", 2)[0])
	if len(question) > 240 {
		question = question[:240] + "…"
	}
	context := "The agent on “" + q.Title + "” asks: " + strings.TrimSpace(q.Question)
	if r := strings.TrimSpace(q.Recommendation); r != "" {
		context += "\n\nIts recommendation: " + r
	}
	if len(context) > 3500 {
		context = context[:3500] + "…"
	}
	b, _ := json.Marshal(map[string]any{"question": question, "context": context, "input": true})
	return store.QuestionCardAsk, string(b)
}

// raiseQuestionCard puts a question asked --to user on the user's list as a
// card; the user's answer goes back to the agent and resumes it.
var raiseQuestionCard = func(q store.OpenQuestion) error {
	ask, decision := questionCard(q)
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	if runInternalSetObjective([]string{"--project", q.Project, "--bead", q.Bead, "--state", beads.StateNeedsUser, "--ask", ask, "--decision", decision, "--no-notify"}, null, os.Stderr) != 0 {
		return errors.New("the question is recorded but its card was not raised: ask again with the same --request")
	}
	return nil
}

// runInternalWithdrawCard takes an agent question's card off the user's list
// once the question behind it was answered or superseded (cmdrd runs it).
func runInternalWithdrawCard(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-withdraw-card", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "the step whose question card goes")
	if err := fs.Parse(args); err != nil || *project == "" || *bead == "" {
		emitErr(stderr, "usage: internal-withdraw-card --project P --bead B")
		return 1
	}
	if beads.HasExecutionAttribution(os.Environ()) {
		emitErr(stderr, "execution sessions cannot withdraw the user's cards")
		return 1
	}
	return editObjective(*project, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		result := internalObjectiveEditResult{BeadID: *bead}
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			return result, err
		}
		defer cleanup()
		p, err := st.GetProjectByName(ctx, *project)
		if err != nil || p == nil {
			return result, fmt.Errorf("project %q: %v", *project, err)
		}
		card, err := st.GetNeedsYou(ctx, p.ID, *bead)
		if err != nil {
			return result, err
		}
		if card == nil || card.Ask != store.QuestionCardAsk {
			return result, fmt.Errorf("%s has no agent-question card", *bead)
		}
		if id, _, err := st.PendingQuestionOn(ctx, *project, *bead); err != nil {
			return result, err
		} else if id != "" {
			return result, fmt.Errorf("%s: question %s is still waiting, so its card stays", *bead, id)
		}
		bc := newBeadsClientFn()
		b, err := bc.Show(ctx, dir, *bead)
		if err != nil {
			return result, err
		}
		if b != nil && b.Status != "closed" && (slices.Contains(b.Labels, beads.NeedsUserLabel) || slices.ContainsFunc(b.Labels, ownercompat.IsLegacyNeeds)) {
			writes := beads.NewStateWrite(bc)
			for _, l := range append([]string{beads.NeedsUserLabel}, ownercompat.NeedsLabels()...) {
				if err := writes.SetLabel(ctx, dir, *bead, l, false); err != nil {
					return result, err
				}
			}
			if err := writes.Flush(ctx); err != nil {
				return result, err
			}
		}
		return result, st.DeleteNeedsYou(ctx, p.ID, *bead)
	})
}
