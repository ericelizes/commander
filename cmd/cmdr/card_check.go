package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// runCardCheck is `internal-set-objective --card-check still-right --reason T`:
// the Manager confirms the open card on a bead is still right, so its basis is
// re-baselined (the card keeps its text and its version) and the board stops
// reading it out of date. Nothing is re-checked for the Manager: it must have
// looked. Refused when the bead's spec did not change and only the age rule
// fired and a previous check already answered the age rule once: then either
// the card is re-raised (--state needs-user --decision) or withdrawn.
func runCardCheck(project, beadID, mode, reason string, stdout, stderr *os.File) int {
	if mode != "still-right" {
		emitErr(stderr, `--card-check takes "still-right" (the card is still right as written); to change the card re-raise it with --state needs-user --decision`)
		return 1
	}
	if strings.HasPrefix(os.Getenv("COMMANDER_ROLE"), "operator-") {
		emitErr(stderr, "an Operator cannot confirm a card: ask your Manager")
		return 1
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < 15 {
		emitErr(stderr, "--card-check needs --reason (15+ characters): what you checked that shows the card is still right")
		return 1
	}
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "commander"
	}
	return editObjective(project, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		result := internalObjectiveEditResult{BeadID: beadID}
		days := config.DefaultStaleDays
		if cfg, err := config.Load(filepath.Join(commanderHomeDir(), "config.toml")); err == nil {
			days = cfg.Needs.StaleDaysOr()
		}
		msg, err := cardCheck(ctx, newBeadsClientFn(), dir, project, beadID, who, reason, time.Now(), days)
		if err != nil {
			return result, err
		}
		result.Card = msg
		return result, nil
	})
}

// cardCheck is the verb's logic, apart from flags and config.
func cardCheck(ctx context.Context, bc beads.Client, dir, projectName, beadID, who, reason string, now time.Time, staleDays int) (string, error) {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return "", err
	}
	defer cleanup()
	project, err := st.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		return "", fmt.Errorf("project %q: %v", projectName, err)
	}
	card, err := st.GetNeedsYou(ctx, project.ID, beadID)
	if err != nil {
		return "", err
	}
	if card == nil || card.Decision == "" {
		return "", fmt.Errorf("%s has no open card to confirm. Raise one with --state needs-user --decision", beadID)
	}
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil || b == nil {
		return "", fmt.Errorf("could not read %s to check its spec: %v", beadID, err)
	}
	spec := beads.SpecHash(*b)
	old := card.Basis
	if old == nil {
		old = &store.Basis{}
	}
	raised := old.Raised
	if raised == 0 {
		raised = card.Since
	}
	specChanged := old.Spec != "" && old.Spec != spec
	ageFired := staleDays > 0 && raised > 0 && now.Unix()-raised > int64(staleDays)*86400
	next := store.Basis{Spec: spec, Named: old.Named, PRs: old.PRs, Answers: old.Answers, Raised: now.Unix()}
	dependencyChanged := false
	if snap := boardSnapshot(); snap != nil {
		d, _ := store.ParseDecision(card.Decision)
		r, _ := livedata.CardStaleIn(livedata.NeedsYou{Project: projectName, BeadID: beadID, Decision: d, Ask: card.Ask, Note: card.Note, Basis: old}, *snap, now.Unix())
		dependencyChanged = r == "named" || r == "pr" || r == "decision"
		text := card.Ask + "\n" + card.Note
		for _, n := range livedata.PRLabels(b.Labels) {
			text += "\nPR #" + n
		}
		fresh := livedata.CardDependencies(*snap, projectName, beadID, d, text)
		next.Named, next.PRs, next.Answers = fresh.Named, fresh.PRs, fresh.Answers
	}
	switch {
	case specChanged || dependencyChanged:
		// The step was edited and the Manager says the card still holds.
	case ageFired:
		if old.AgeChecks >= 1 {
			return "", fmt.Errorf("%s: the step did not change and you already confirmed this card on age alone once. Re-raise it with what is true now (--state needs-user --decision) or withdraw it (--status in-progress)", beadID)
		}
		next.AgeChecks = old.AgeChecks + 1
	default:
		return "", fmt.Errorf("%s: the card is not out of date (the step is unchanged and it is under %d days old): nothing to confirm", beadID, staleDays)
	}
	if err := st.RebaselineCard(ctx, project.ID, beadID, next); err != nil {
		return "", err
	}
	why := "the step's spec changed"
	if dependencyChanged {
		why = "a named step, PR or newer decision changed"
	} else if !specChanged {
		why = fmt.Sprintf("age (raised %d days ago)", (now.Unix()-raised)/86400)
	}
	if err := bc.AddComment(ctx, dir, beadID, fmt.Sprintf("card checked by %s: still right (%s): %s", who, why, reason)); err != nil {
		return "", fmt.Errorf("the card is re-baselined but the comment was not written: %w", err)
	}
	return "re-baselined", nil
}
