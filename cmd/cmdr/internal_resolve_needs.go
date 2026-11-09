package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"unicode"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// runInternalResolveNeeds answers a ⚑ from the sidebar in one keystroke:
// approve marks the step done, defer parks it (not started). Either way the ask
// leaves the user's NEEDS YOU queue and the step's Manager is told what he decided,
// so it can carry on.
func runInternalResolveNeeds(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-resolve-needs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "the flagged step")
	action := fs.String("action", "", "approve, defer or decide")
	choice := fs.String("choice", "", "with --action decide: the option the user picked")
	resume := fs.Bool("resume-only", false, "only finish an already saved answer")
	text := fs.String("text", "", "with --action decide: the user's free-text answer")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *action == "decide" && strings.TrimSpace(*choice) == "" && strings.TrimSpace(*text) == "" {
		emitErr(stderr, "--action decide needs --choice and/or --text")
		return 1
	}
	if *project == "" || *bead == "" || (*action != "approve" && *action != "defer" && *action != "decide") {
		emitErr(stderr, "need --project, --bead and --action approve|defer|decide")
		return 1
	}
	return editObjective(*project, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		result := internalObjectiveEditResult{BeadID: *bead}
		release, err := lockNeedsResolution()
		if err != nil {
			return result, err
		}
		defer release()
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			return result, err
		}
		defer cleanup()
		p, err := st.GetProjectByName(ctx, *project)
		if err != nil {
			return result, err
		}
		if p == nil {
			return result, fmt.Errorf("unknown project %q", *project)
		}
		saved, err := st.GetNeedsResolution(ctx, p.ID, *bead)
		if err != nil {
			return result, err
		}
		// An answered approving gate left open (answers saved before approval
		// was explicit) is closed on resume, from the journalled choice.
		if *resume && saved != nil && saved.Action == "decide" && saved.GateChoice == "" && saved.State != store.ResolutionSuperseded && journalApproved(*saved) {
			if err := repairApprovedGate(ctx, st, newBeadsClientFn(), dir, p.ID, *saved); err != nil {
				return result, err
			}
		}
		// A snapshot can lose the card after Flush. Resume only ever finishes
		// a durable answer; it must not create an answer for an absent card.
		if *resume && (saved == nil || saved.Mailed) {
			return result, nil
		}
		finish := func(r store.NeedsResolution) error {
			if err := mailNeedsResolution(ctx, r); err != nil {
				return fmt.Errorf("answer saved; retry to tell the Manager: %w", err)
			}
			return st.AdvanceNeedsResolution(ctx, r, store.ResolutionMailed)
		}
		// Once cleanup is committed a retry only delivers the original answer.
		// It must not overwrite a Manager's subsequent status or waits.
		if *resume && saved != nil && saved.Applied && saved.State != store.ResolutionSuperseded && !saved.Mailed {
			return result, finish(*saved)
		}
		bc := newBeadsClientFn()
		b, err := bc.Show(ctx, dir, *bead)
		if err != nil {
			return result, err
		}
		answered := b != nil && slices.Contains(b.Labels, beads.DecisionAnsweredLabel)
		generation, err := st.NeedsGeneration(ctx, p.ID, *bead)
		if err != nil {
			return result, err
		}
		// A new pitch is a new question, even while an older notification is
		// pending. Never replay an old answer against its labels or status.
		if saved != nil && saved.Generation != generation {
			if saved.State < store.ResolutionMailed {
				if err := st.AdvanceNeedsResolution(ctx, *saved, store.ResolutionSuperseded); err != nil {
					return result, err
				}
			}
			saved = nil
			if *resume {
				return result, nil
			}
		}
		if saved != nil && saved.State == store.ResolutionSuperseded {
			return result, nil
		}
		if saved != nil && saved.Mailed {
			return result, nil
		}
		if saved != nil && saved.Applied {
			return result, finish(*saved)
		}
		r := saved
		if r == nil {
			n, err := st.GetNeedsYou(ctx, p.ID, *bead)
			if err != nil {
				return result, err
			}
			if n == nil && (b == nil || b.Status == "closed" || answered) {
				return result, nil
			}
			ask := *bead
			if n != nil && strings.TrimSpace(n.Ask) != "" {
				ask = n.Ask
			}
			r = &store.NeedsResolution{ProjectID: p.ID, BeadID: *bead, Generation: generation, AnswerID: rand.Text(), Action: *action, Choice: *choice, Text: *text, Ask: ask,
				Manager: wsmgr.Role(wsmgr.Owner(ctx, bc, dir, *bead))}
			if *action == "decide" && b != nil && slices.Contains(b.Labels, beads.GateLabel) {
				for _, c := range []string{beads.GateApprove, beads.GateChange, beads.GateDrop} {
					if strings.EqualFold(strings.TrimSpace(*choice), c) {
						r.GateChoice = c
					}
				}
				if r.GateChoice == "" && approvingChoice(*choice, n) {
					r.GateChoice = beads.GateApprove
				}
				r.Approved = r.GateChoice == beads.GateApprove
			}
		}
		// A crash after Close but before its receipt is recovered by reading
		// the bead, never by replaying cleanup. The generation check above has
		// already excluded a revised card.
		if saved != nil && b != nil && b.Status == "closed" && answered && !slices.Contains(b.Labels, beads.NeedsUserLabel) && !slices.ContainsFunc(b.Labels, ownercompat.IsLegacyNeeds) {
			if err := st.AdvanceNeedsResolution(ctx, *r, store.ResolutionClosed); err != nil {
				return result, err
			}
			return result, finish(*r)
		}

		closing := r.Action == "approve" || r.GateChoice == beads.GateApprove
		if r.Action == "approve" && r.State < store.ResolutionBeadWritten {
			if err := requireGatesPassed(ctx, *project, *bead); err != nil {
				return result, err
			}
		}
		if closing && r.State < store.ResolutionBeadWritten {
			if err := checkWorkflowClose(ctx, bc, dir, *project, *bead); err != nil {
				return result, err
			}
		}
		if r.State < store.ResolutionBeadWritten {
			// Collect every read and assignment before persisting or mutating beads.
			writes := beads.NewStateWrite(bc)
			state := beads.StateInProgress
			if r.Action == "defer" {
				state = beads.StateNotStarted
			}
			if closing {
				for _, label := range append([]string{beads.NeedsUserLabel, beads.BlockedLabel}, ownercompat.NeedsLabels()...) {
					if err := writes.SetLabel(ctx, dir, *bead, label, false); err != nil {
						return result, err
					}
				}
				result.Closed = true
			} else {
				if err := applyStepState(ctx, writes, dir, *bead, state); err != nil {
					return result, err
				}
				result.State = state
				if r.GateChoice == beads.GateDrop {
					if err := writes.SetStatus(ctx, dir, *bead, beads.StatusDeferred); err != nil {
						return result, err
					}
					result.State = beads.StateNotStarted
				} else if r.Action == "decide" {
					if err := applyStepOwner(ctx, writes, dir, *bead, r.Manager); err != nil {
						return result, err
					}
					result.Owner = r.Manager
				}
			}
			if b != nil {
				// Preserve other waits and their original timestamps exactly.
				for _, label := range b.Labels {
					if w, ok := beads.ParseWaitLabel(label); ok && w.Kind == beads.WaitUser {
						if err := writes.SetLabel(ctx, dir, *bead, label, false); err != nil {
							return result, err
						}
					}
				}
			}
			if err := writes.SetLabel(ctx, dir, *bead, beads.DecisionAnsweredLabel, true); err != nil {
				return result, err
			}
			if saved == nil {
				answer := map[string]string{"approve": "Approved", "defer": "Not yet"}[r.Action]
				if r.Action == "decide" {
					answer = strings.TrimSpace(strings.TrimSpace(r.Choice) + "\n" + strings.TrimSpace(r.Text))
				}
				if err := st.SaveNeedsResolution(ctx, *r, answer); err != nil {
					return result, err
				}
			}
			if err := writes.Flush(ctx); err != nil {
				return result, err
			}
			if err := st.DeleteNeedsYou(ctx, p.ID, *bead); err != nil {
				return result, err
			}
			if err := st.AdvanceNeedsResolution(ctx, *r, store.ResolutionBeadWritten); err != nil {
				return result, err
			}
		}
		// Cleanup has a durable receipt before Close releases dependants.
		// A retry may close only the matching generation, still open for this
		// answer. It never repeats cleanup after the bead-written receipt.
		if closing {
			current, err := st.NeedsGeneration(ctx, p.ID, *bead)
			if err != nil {
				return result, err
			}
			if current != r.Generation || (r.State >= store.ResolutionBeadWritten && (b == nil || (b.Status != "open" && b.Status != "in_progress") || !answered || slices.Contains(b.Labels, beads.NeedsUserLabel) || slices.ContainsFunc(b.Labels, ownercompat.IsLegacyNeeds))) {
				return result, st.AdvanceNeedsResolution(ctx, *r, store.ResolutionSuperseded)
			}
		}
		if closing && (b == nil || b.Status != "closed") {
			if err := holdApprovedWork(ctx, bc, dir, *bead); err != nil {
				return result, fmt.Errorf("answer saved; dispatch custody could not be recorded, so gate remains open: %w", err)
			}
			if err := bc.Close(ctx, dir, *bead); err != nil {
				return result, err
			}
		}
		if err := st.AdvanceNeedsResolution(ctx, *r, store.ResolutionClosed); err != nil {
			return result, err
		}

		if err := finish(*r); err != nil {
			return result, err
		}
		return result, nil
	})
}

// legacyApprovals are the exact labels (normalised) that approve on a card
// written before options carried "approves".
var legacyApprovals = []string{"approve", "approved", "approve it", "go ahead", "go", "build it", "build", "yes", "merge", "merge it", "ship it", "install", "approve install", "approve rebuild", "approve the install"}

// deferringWords make a label never approve, whatever else it says.
var deferringWords = []string{"later", "back", "but", "not", "don't", "dont", "never", "wait", "hold", "no", "cancel", "change", "drop", "instead"}

// normalizeLabel lowercases, trims and strips punctuation (apostrophes stay).
func normalizeLabel(label string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '\u2019' {
			return r
		}
		return ' '
	}, strings.ToLower(label))
	return strings.Join(strings.Fields(strings.ReplaceAll(clean, "\u2019", "'")), " ")
}

// legacyApprovingLabel is the label guess for cards without any approves flag.
func legacyApprovingLabel(choice string) bool {
	label := normalizeLabel(choice)
	if label == "" {
		return false
	}
	for _, w := range strings.Fields(label) {
		if slices.Contains(deferringWords, w) {
			return false
		}
	}
	return slices.Contains(legacyApprovals, label) || strings.HasPrefix(label, "approve ")
}

// approvingChoice reports whether the picked option approves. A card with any
// approves:true option decides by those options alone; only a card without the
// flag falls back to the label guess.
func approvingChoice(choice string, n *store.NeedsYou) bool {
	choice = strings.TrimSpace(choice)
	if n != nil && n.Decision != "" {
		if d, err := store.ParseDecision(n.Decision); err == nil {
			flagged := false
			for _, o := range d.Options {
				if o.Approves {
					flagged = true
					if strings.EqualFold(strings.TrimSpace(o.Label), choice) {
						return true
					}
				}
			}
			if flagged {
				return false
			}
		}
	}
	return legacyApprovingLabel(choice)
}

// journalApproved is the verdict stored when the answer was saved. Journals
// from before the field only count for the exact label Approve or the exact
// allow-list, never the loose first-word guess.
func journalApproved(r store.NeedsResolution) bool {
	return r.Approved || legacyApprovingLabel(r.Choice)
}

// repairApprovedGate closes a gate whose approving answer is journalled for the
// current pitch but whose bead is still open.
func repairApprovedGate(ctx context.Context, st *store.Store, bc beads.Client, dir string, projectID int64, r store.NeedsResolution) error {
	current, err := st.NeedsGeneration(ctx, projectID, r.BeadID)
	if err != nil || current != r.Generation {
		return err
	}
	b, err := bc.Show(ctx, dir, r.BeadID)
	if err != nil || b == nil || b.Status == "closed" || !slices.Contains(b.Labels, beads.GateLabel) || !slices.Contains(b.Labels, beads.DecisionAnsweredLabel) {
		return err
	}
	p, err := st.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("missing project")
	}
	if err := checkWorkflowClose(ctx, bc, dir, p.Name, r.BeadID); err != nil {
		return err
	}
	if err := holdApprovedWork(ctx, bc, dir, r.BeadID); err != nil {
		return err
	}
	return bc.Close(ctx, dir, r.BeadID)
}

// A process lock serializes CLI and app retries without holding a SQLite write
// transaction over br/mail IO. Contention returns a retryable error.
func lockNeedsResolution() (func(), error) {
	path, err := stateDBPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".resolve.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another answer is being saved; retry: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func mailNeedsResolution(ctx context.Context, r store.NeedsResolution) error {
	subject, body := needsResolutionMail(r)
	st, client, key, cleanup, err := mailDeps()
	if err != nil {
		return err
	}
	defer cleanup()
	return sendRoleMail(ctx, st, client, key, "commander", []string{r.Manager}, subject, body, agentmail.SendOptions{Importance: "normal"})
}

// needsResolutionMail is the answer's mail. Every subject here must pass
// mailstate.IsAnswer, which keeps it from being held as old history.
func needsResolutionMail(r store.NeedsResolution) (subject, body string) {
	subject, body = "the user approved: "+r.Ask, fmt.Sprintf("the user approved %q (step %s) from his sidebar. It is marked done; carry on.", r.Ask, r.BeadID)
	if r.Action == "decide" {
		subject = "[decision] " + r.BeadID + ": " + strings.TrimSpace(r.Choice)
		body = decisionMailBody(r.Ask, r.BeadID, r.Choice, r.Text)
	}
	if r.Action == "defer" {
		subject = "the user deferred: " + r.Ask
		body = fmt.Sprintf("the user deferred %q (step %s) from his sidebar. It is back to not-started: park it, and do not re-flag it unless something changes.", r.Ask, r.BeadID)
	}
	if r.GateChoice != "" {
		subject, body = workflowGateMail(r.BeadID, r.Ask, r.GateChoice, r.Text)
	}
	body += "\n\n[decision] " + r.BeadID + " answer " + r.AnswerID + " (generation " + r.Generation + ")"
	return subject, body
}

// workflowGateMail is what the Manager reads when the user answers a workflow gate.
func workflowGateMail(bead, ask, choice, text string) (subject, body string) {
	subject = fmt.Sprintf("[workflow %s] %s", strings.ToLower(choice), bead)
	switch choice {
	case beads.GateApprove:
		body = fmt.Sprintf("the user approved the workflow %q (gate %s). The gate is closed: the stages behind it may start.", ask, bead)
	case beads.GateChange:
		body = fmt.Sprintf("the user wants the workflow %q (gate %s) changed. It stays unapproved and is now owned by the Manager. Rewrite it on the gate bead, then explicitly re-flag with --state needs-user --decision to ask again.", ask, bead)
	default:
		body = fmt.Sprintf("the user dropped the workflow %q (gate %s). The gate is parked. Do not start it: stop and close the track.", ask, bead)
	}
	if c := strings.TrimSpace(choice); c != "" && c != beads.GateApprove {
		body += "\n\nHe picked: " + c
	}
	if t := strings.TrimSpace(text); t != "" {
		body += "\n\nUser's note: " + t
	}
	return subject, body
}

// decisionMailBody is what the Manager reads when the user submits a decision card.
func decisionMailBody(question, bead, choice, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[decision] %s: %s", bead, strings.TrimSpace(choice))
	if t := strings.TrimSpace(text); t != "" {
		fmt.Fprintf(&b, " + %s", t)
	}
	fmt.Fprintf(&b, "\n\nUser answered your decision card %q on step %s. The flag is cleared and the step is in-progress again: act on his answer, then update or close the step.", question, bead)
	return b.String()
}
