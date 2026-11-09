package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/config"
)

// pushLimits reads [needs] push_count / push_days (defaults 3 and 7; 0 = off).
func pushLimits() beads.PushLimits {
	lim := beads.PushLimits{Count: config.DefaultPushCount, Days: config.DefaultPushDays}
	if cfg, err := config.Load(filepath.Join(commanderHomeDir(), "config.toml")); err == nil {
		lim = beads.PushLimits{Count: cfg.Needs.PushCountOr(), Days: cfg.Needs.PushDaysOr()}
	}
	return lim
}

// planBeadPush judges moving the bead's until:/parked-until target to target.
// An unreadable bead fails open: no plan, no refusal.
func planBeadPush(ctx context.Context, bc beads.Client, dir, beadID string, target time.Time, reason string) (beads.PushPlan, error) {
	if target.IsZero() {
		return beads.PushPlan{}, nil
	}
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil || b == nil {
		return beads.PushPlan{}, nil
	}
	comments, _ := bc.Comments(ctx, dir, beadID)
	return beads.PlanPush(b.Labels, comments, target, reason, pushLimits(), time.Now())
}

// finishPush writes a plan after the move it judged happened, and mails
// Commander once when it flagged the bead.
func finishPush(ctx context.Context, bc beads.Client, dir, project, beadID string, plan beads.PushPlan, stderr *os.File) error {
	if plan.Comment == "" && !plan.Counted {
		return nil
	}
	if err := beads.ApplyPush(ctx, bc, dir, beadID, plan); err != nil {
		return fmt.Errorf("the date moved but recording the push failed: %w", err)
	}
	if plan.NewlyFlag {
		mailPushFlag(ctx, bc, dir, project, beadID, plan.N, stderr)
	}
	return nil
}

func mailPushFlag(ctx context.Context, bc beads.Client, dir, project, beadID string, n int, stderr *os.File) {
	title := beadID
	if b, err := bc.Show(ctx, dir, beadID); err == nil && b != nil && b.Title != "" {
		title = b.Title
	}
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "commander"
	}
	subject := fmt.Sprintf("[pushed] %s keeps moving (%s)", title, project)
	body := fmt.Sprintf("%s moved the date of %s %q (project %s) later again, push %d, past the limit, with a reason (see the comment on the bead). The step is labelled pushed-flagged.\n\nIf this work is not coming back, tell the Manager to finish, withdraw or park it. To give it a fresh count: cmdr internal-set-objective --project %s --bead %s --push-reset", who, beadID, title, project, n, project, beadID)
	st, client, key, cleanup, err := mailDeps()
	if err == nil {
		defer cleanup()
		err = sendRoleMail(ctx, st, client, key, who, []string{"commander"}, subject, body, agentmail.SendOptions{Importance: "high"})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: %s was flagged as pushed, but mailing Commander failed: %v\n", beadID, err)
	}
}

// runPushReset is `internal-set-objective --push-reset`: Commander and the user
// clear a bead's push counter.
func runPushReset(project, beadID string, stdout, stderr *os.File) int {
	if role := os.Getenv("COMMANDER_ROLE"); role != "" && role != "commander" {
		emitErr(stderr, "--push-reset is for Commander and the user: ask Commander")
		return 1
	}
	return editObjective(project, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		bc := newBeadsClientFn()
		result := internalObjectiveEditResult{BeadID: beadID}
		cleared, err := beads.ClearPush(ctx, bc, dir, beadID)
		if err != nil {
			return result, err
		}
		if cleared {
			_ = bc.AddComment(ctx, dir, beadID, "push counter reset by "+firstNonEmpty(os.Getenv("COMMANDER_ROLE"), "the user"))
		}
		return result, nil
	})
}
