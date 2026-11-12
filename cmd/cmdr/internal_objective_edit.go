package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/place"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
	"github.com/example-user/commander/internal/wsmgr"
)

type internalObjectiveEditResult struct {
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	Title   string `json:"title,omitempty"`
	Closed  bool   `json:"closed,omitempty"`
	Status  string `json:"status,omitempty"`
	Starred *bool  `json:"starred,omitempty"`
	State   string `json:"state,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Viz     string `json:"viz,omitempty"`
	Lane    string `json:"lane,omitempty"`
	Track   *bool  `json:"track,omitempty"`
	// MaxOperators is the workstream's Operator cap override ("none" = default).
	MaxOperators string `json:"max_operators,omitempty"`
	RunOn        string `json:"run_on,omitempty"`
	// Then lists the steps a close's --then-* hold was written on.
	Then []string `json:"then,omitempty"`
	// Custody lists the steps a close or create left unheld that now carry the
	// Manager's "Manager to start" receipt.
	Custody []string `json:"custody,omitempty"`
	// Card is "stored" (new card) or "replaced" (an open card was swapped) when a --decision was written.
	Card string `json:"card,omitempty"`
	// Proposed is the kind of proposal the write became (a Manager's ask or long
	// wait): Commander accepts or overrules it before it reaches the user.
	Proposed string `json:"proposed,omitempty"`
}

// runInternalSetObjective sets an objective's star and/or started state:
// --star true|false, --status open|in_progress,
// --state not-started|in-progress|blocked|needs-user, --owner <name>|none.
func runInternalSetObjective(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-set-objective", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	beadID := fs.String("bead", "", "objective bead id")
	star := fs.String("star", "", "true or false")
	status := fs.String("status", "", "open or in_progress")
	state := fs.String("state", "", "not-started, in-progress, blocked or needs-user")
	note := fs.String("note", "", "with --state needs-user: what the user must verify or decide (goes in the mail to Commander)")
	ask := fs.String("ask", "", "with --state needs-user: what the user must DO, verb first, <= 30 chars (shown in his sidebar), e.g. \"Run 4 staging cmds\"")
	decisionArg := fs.String("decision", "", "with --state needs-user (REQUIRED): the decision card the user sees, as JSON or @file: {question, context, options[{label, implications, recommended, approves}], input, steps[]}")
	review := fs.String("review", "", "PR this step is in review as: N, or <owner>/<name>#N for a PR in another repo (cmdrd watches its reviews there); shown as ⎇ #N in the sidebar; or \"none\" (removes every pr: label, recorded in a comment; not for Operators)")
	from := fs.String("from", "", "sender role for the needs-user mail (default: the Manager of the step's workstream)")
	writesWorkflow := fs.Bool("writes-workflow", false, "with --state in-progress: the work is writing the workflow itself: skip the workflow-approved check")
	noNotify := fs.Bool("no-notify", false, "don't mail Commander when moving a step to needs-user")
	owner := fs.String("owner", "", "who owns the step (e.g. operator-x, user); \"none\" clears")
	until := fs.String("until", "", "make the step an owned job its owner keeps at until this finish line (merged, approved, live, adopted); \"none\" clears")
	runtimeArg := fs.String("runtime", "", "the runtime this step's agents launch on first: claude or codex (the other is the fallback); on a nested workstream, all its steps; \"none\" clears")
	viz := fs.String("viz", "", "the bead's level in the app's Graph: 1 (gates), 2 (stages) or 3 (everything); \"none\" restores the default (gate 1, stage 2, task 3)")
	lane := fs.String("lane", "", "whose move the step is in the diagram, only where owner, waits, gates and needs-user cannot say it: you, agents, elsewhere or elsewhere:<who in plain words>; \"none\" clears")
	track := fs.Bool("workstream", false, "make the bead a nested workstream of its parent workstream: a separate outcome with its own gate chain")
	fs.BoolVar(track, "track", false, "old name of --workstream")
	noTrack := fs.Bool("no-workstream", false, "turn a nested workstream back into a plain step (it stays under its parent)")
	fs.BoolVar(noTrack, "no-track", false, "old name of --no-workstream")
	runOn := fs.String("run-on", "", "the paired place this step's Operator runs on (mac:<name>, see cmdr place list); \"none\" runs it on cmdrd's machine again. Spawns there need a closed user gate over the step")
	maxOps := fs.String("max-operators", "", "on a workstream bead: how many Operators may run under it at once, overriding [concurrency] max_operators_per_workstream; \"none\" restores the default")
	holdArg := fs.String("hold", "", "hold the bead (and everything under it): internal-ship refuses and Operators stop shipping; the value is the reason (use \"-\" for none)")
	noHold := fs.Bool("no-hold", false, "clear a hold")
	park := fs.Bool("park", false, "park the ask the user put on hold (status deferred): out of NEEDS YOU, rows and checkpoints; its card is kept. With --until (RFC3339 or 2h, 3d) cmdrd un-parks it then and mails the Manager")
	unpark := fs.Bool("unpark", false, "bring a parked bead back to the status it had, with its card")
	cardCheckArg := fs.String("card-check", "", `"still-right": confirm the open card on the bead is still right as written (needs --reason); it stops reading out of date. Refused when nothing changed and only the age rule fired twice in a row`)
	pushReason := fs.String("push-reason", "", "with --waiting until:... or --park --until: why the date moves LATER again (15+ chars, not a repeat). Without it the push past [needs] push_count / push_days is refused; with it below them it is a comment only")
	pushReset := fs.Bool("push-reset", false, "clear the bead's push counter (Commander and the user only)")
	notSameAsk := fs.String("not-the-same-ask", "", "with --state needs-user on a gate: an ancestor already holds an open card about the same PR, but this card asks a different question; the value says what differs")
	reason := fs.String("reason", "", "with --park: why it is on hold; with --card-check: what you checked; with --review none: why the label goes (15+ chars, required on a merge target or a merge gate's stage), recorded on the bead")
	var waiting stringList
	fs.Var(&waiting, "waiting", "what the owner waits on, repeatable: ci:#N, ci:<owner/repo>#N, review:<login>, user, operator:<role>, until:<RFC3339>:<plain words>; \"none\" clears. cmdrd wakes the Manager when CI finishes, a reviewer answers or the time is reached")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *beadID == "" || (*star == "" && *status == "" && *state == "" && *owner == "" && *review == "" && *until == "" && *viz == "" && *lane == "" && *maxOps == "" && *runOn == "" && *runtimeArg == "" && *decisionArg == "" && len(waiting) == 0 && !*track && !*noTrack && *holdArg == "" && !*noHold && !*park && !*unpark && *cardCheckArg == "" && !*pushReset) {
		emitErr(stderr, "missing required flag(s): --project, --bead, and one of --star, --status, --state, --owner, --review, --until, --viz, --lane, --max-operators, --run-on, --runtime, --decision, --waiting, --workstream, --no-workstream, --hold, --no-hold, --park, --unpark, --card-check, --push-reset")

		return 1
	}
	if *pushReset {
		return runPushReset(*projectName, *beadID, stdout, stderr)
	}
	if *cardCheckArg != "" {
		return runCardCheck(*projectName, *beadID, *cardCheckArg, *reason, stdout, stderr)
	}
	if *park || *unpark {
		if *park && *unpark {
			emitErr(stderr, "--park and --unpark exclude each other")
			return 1
		}
		if strings.HasPrefix(os.Getenv("COMMANDER_ROLE"), "operator-") {
			emitErr(stderr, "an Operator cannot park or unpark a bead: ask your Manager")
			return 1
		}
		return runParkEdit(*projectName, *beadID, *park, *until, *reason, *pushReason, stdout, stderr)
	}
	if *holdArg != "" && *noHold {
		emitErr(stderr, "--hold and --no-hold exclude each other")
		return 1
	}
	if err := refuseOperatorHold(*holdArg != "" || *noHold); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := refuseOperatorReviewClear(*review); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if *track && *noTrack {
		emitErr(stderr, "--workstream and --no-workstream exclude each other")
		return 1
	}
	waits, err := parseWaitingFlags(waiting)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	laneValue, err := laneFlagValue(*lane)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if *runtimeArg != "" && *runtimeArg != "claude" && *runtimeArg != "codex" && *runtimeArg != "none" {
		emitErr(stderr, "--runtime must be claude, codex or none")
		return 1
	}
	if *viz != "" && *viz != "none" && *viz != "1" && *viz != "2" && *viz != "3" {
		emitErr(stderr, "--viz must be 1, 2, 3 or none")
		return 1
	}
	if err := checkRunOn(*runOn); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if n, err := strconv.Atoi(*maxOps); *maxOps != "" && *maxOps != "none" && (err != nil || n <= 0) {
		emitErr(stderr, "--max-operators must be a number > 0 or none")
		return 1
	}
	*state = ownercompat.Normalize(*state)
	*owner = ownercompat.Normalize(*owner)
	switch *state {
	case "", beads.StateNotStarted, beads.StateInProgress, beads.StateBlocked, beads.StateNeedsUser:
	default:
		emitErr(stderr, "--state must be not-started, in-progress, blocked or needs-user (use internal-close-objective for done)")
		return 1
	}
	if *star != "" && *star != "true" && *star != "false" {
		emitErr(stderr, "--star must be true or false")
		return 1
	}
	if *status != "" && *status != "open" && *status != "in_progress" {
		emitErr(stderr, "--status must be open or in_progress (use internal-close-objective for done)")
		return 1
	}
	return editObjective(*projectName, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		writes := beads.NewStateWrite(newBeadsClientFn())
		var bc beads.Client = writes
		result := internalObjectiveEditResult{BeadID: *beadID, Status: *status}
		if *state == beads.StateNeedsUser || *decisionArg != "" {
			if b, err := bc.Show(ctx, dir, *beadID); err == nil && b != nil && beads.IsClosingGate(b.Labels) {
				return result, fmt.Errorf("%s is a Confirm done step: it never gets a card; the sidebar shows Complete on it once everything else is finished", *beadID)
			}
		}
		if (*state == beads.StateInProgress || *status == "in_progress") && !*writesWorkflow {
			if err := requireWorkflowGate(ctx, bc, dir, *beadID, stderr); err != nil {
				return result, err
			}
		}
		// Entering needs-user from any other state tells Commander right away,
		// so a pending sign-off is never silent.
		var notify *needsUserNotice
		decisionJSON, question := "", ""
		var decision *store.Decision
		if laneValue != "" {
			if err := checkLaneNotOnGate(ctx, bc, dir, *beadID); err != nil {
				return result, err
			}
		}
		if *decisionArg != "" {
			d, err := loadDecision(*decisionArg)
			if err != nil {
				return result, err
			}
			decision = d
			decisionJSON, question = d.JSON(), d.Question
			// The card carries the question and context; derive what the flag
			// otherwise needs from it.
			if strings.TrimSpace(*ask) == "" {
				*ask = truncateRunes(d.Question, store.MaxAskLen)
			}
			if strings.TrimSpace(*note) == "" {
				*note = strings.TrimSpace(d.Question + "\n" + d.Context)
			}
		}
		// --no-notify is the user's own mark key; from an agent it is no way around the card.
		agent := os.Getenv("COMMANDER_ROLE") != ""
		entering := false // newly flagged needs-user, mailed or not
		if *state == beads.StateNeedsUser && (!*noNotify || agent) {
			notify = newNeedsUserNotice(ctx, bc, dir, *projectName, *beadID, *note, *from)
			// A flag with no card tells the user nothing he can answer: refuse before
			// changing anything. (Already-flagged steps send no mail, so no card.)
			if notify != nil && decisionJSON == "" {
				return result, fmt.Errorf("marking a step needs-user requires --decision '<json>' (or @file): {\"question\": \"one line: what the user decides\", \"context\": \"2-4 lines\", \"options\": [{\"label\": \"...\", \"implications\": \"...\", \"recommended\": true, \"approves\": true}], \"input\": true, \"steps\": [\"exact command\"]}. the user answers it as a form in his inspector")
			}
			if notify != nil {
				if err := store.ValidateAsk(*ask); err != nil {
					return result, fmt.Errorf("marking a step needs-user requires --ask: %w", err)
				}
				notify.ask = strings.TrimSpace(*ask)
			}
			entering = notify != nil
			if *noNotify {
				notify = nil
			}
		}
		if *state == "" && decisionJSON != "" {
			if err := preflightReplaceCard(ctx, *projectName, *beadID, *ask, *note, decisionJSON); err != nil {
				return result, err
			}
		}
		if *star != "" {
			on := *star == "true"
			if err := bc.SetLabel(ctx, dir, *beadID, "star", on); err != nil {
				return result, err
			}
			result.Starred = &on
		}
		reviewRecord := ""
		if *review != "" {
			if reviewRecord, err = applyReview(ctx, bc, dir, *beadID, *review, *reason, len(waiting) > 0); err != nil {
				return result, err
			}
		}
		var owed custodyOwed
		if *state == beads.StateNotStarted || *owner == "none" || (len(waiting) > 0 && len(waits) == 0) {
			dropped := func(b *holding.Bead) {
				if *state == beads.StateNotStarted {
					// applyStepState opens the bead and takes needs-user off it (the card goes with
					// it, syncNeedsYou): simulate all of it, not just the owner (M5).
					b.Owner, b.Status, b.NeedsUser = "", "open", false
				}
				if *owner == "none" {
					b.Owner = ""
				} else if *owner != "" {
					b.Owner = *owner
				}
				if len(waiting) > 0 {
					b.Waits, b.Untyped, b.Until = len(waits), "", false
				}
			}
			if owed, err = requireHeldAfter(ctx, bc, dir, *beadID, "internal-set-objective on "+*beadID, holdFlags, map[string]holding.Edit{*beadID: dropped}); err != nil {
				return result, err
			}
		}
		if *status != "" {
			if err := checkReopenPlacement(ctx, bc, dir, *beadID); err != nil {
				return result, err
			}
			if err := bc.SetStatus(ctx, dir, *beadID, *status); err != nil {
				return result, err
			}
		}
		if *state == beads.StateInProgress && !*writesWorkflow {
			if err := checkReached(ctx, bc, dir, *projectName, *beadID, "--state in-progress"); err != nil {
				return result, err
			}
		}
		if *state == beads.StateInProgress {
			if err := requireOwner(ctx, bc, dir, *beadID, *owner); err != nil {
				return result, err
			}
			if err := checkOwnerLive(ctx, *owner); err != nil {
				return result, err
			}
		}
		// The checks at the door (door_checks.go): refuse a state that would read
		// wrong on the user's board, before anything is written.
		if *state == beads.StateBlocked {
			if err := checkBlockedNamesWait(ctx, bc, dir, *beadID, waits); err != nil {
				return result, err
			}
		}
		if *state == beads.StateNeedsUser && entering {
			if err := checkAskReady(ctx, bc, dir, *beadID); err != nil {
				return result, err
			}
			sameAskGiven := false
			fs.Visit(func(f *flag.Flag) { sameAskGiven = sameAskGiven || f.Name == "not-the-same-ask" })
			if err := checkNotSameAskReason(sameAskGiven, *notSameAsk); err != nil {
				return result, err
			}
			var escapedAncestor string
			if err := checkAskNotSettled(ctx, bc, dir, *projectName, *beadID, decision, *status == "open", *notSameAsk, &escapedAncestor); err != nil {
				return result, err
			}
			if escapedAncestor != "" {
				if err := logSecondCard(ctx, bc, dir, *beadID, escapedAncestor, *notSameAsk); err != nil {
					return result, err
				}
			}
			// A gate's own card may be written ahead: the board keeps the gate
			// Not reached, its card out of Needs you, until its blockers are done.
			if !gateByID(ctx, bc, dir, *beadID) {
				if err := checkReached(ctx, bc, dir, *projectName, *beadID, "--state needs-user"); err != nil {
					return result, err
				}
			}
		}
		if *state != "" || *owner != "" || len(waits) > 0 {
			if b, err := bc.Show(ctx, dir, *beadID); err == nil && b != nil {
				finalState, finalOwner := beads.StepState(b.Status, b.Labels)
				if *state != "" {
					finalState = *state
				}
				if *owner == "none" {
					finalOwner = ""
				} else if *owner != "" {
					finalOwner = *owner
				}
				if err := checkUntilNotOnUserDecision(b, finalOwner, waits); err != nil {
					return result, err
				}
				if *state != "" || *owner != "" {
					if err := checkUserOwnsOnlyAsks(b, finalOwner, finalState); err != nil {
						return result, err
					}
				}
			}
		}
		if len(waits) > 0 {
			if err := checkReviewWaitsHavePR(ctx, bc, dir, *beadID, *review, waits); err != nil {
				return result, err
			}
			if slices.ContainsFunc(waits, func(w beads.Wait) bool { return w.Kind == beads.WaitUser }) {
				if err := checkReached(ctx, bc, dir, *projectName, *beadID, "--waiting user"); err != nil {
					return result, err
				}
			}
			if err := checkOneWaitPerReview(*projectName, *beadID, *review, waits); err != nil {
				return result, err
			}
			if err := checkUntilLabelsReadable(waits); err != nil {
				return result, err
			}
		}
		// A later until: date is a push; judge it before anything is written.
		var push beads.PushPlan
		if len(waits) > 0 {
			if push, err = planBeadPush(ctx, bc, dir, *beadID, beads.UntilTarget(waits), *pushReason); err != nil {
				return result, err
			}
		}
		proposed, err := proposeStateWrite(ctx, bc, dir, *beadID, entering, *ask, question, waits)
		if err != nil {
			return result, err
		}
		if *state != "" {
			if err := applyStepState(ctx, bc, dir, *beadID, *state); err != nil {
				return result, err
			}
			result.State = *state

		}
		if len(waiting) > 0 {
			checkedWaits := waits
			if *state == beads.StateNeedsUser && decisionJSON != "" {
				// The decision will be persisted after the batched bead write.
				checkedWaits = nil
				for _, wait := range waits {
					if wait.Kind != beads.WaitUser {
						checkedWaits = append(checkedWaits, wait)
					}
				}
			}
			if err := checkWaitsAreReal(ctx, *projectName, *beadID, checkedWaits); err != nil {
				return result, err
			}
			if err := applyWaiting(ctx, bc, dir, *beadID, waits, time.Now()); err != nil {
				return result, err
			}
			if err := finishPush(ctx, bc, dir, *projectName, *beadID, push, stderr); err != nil {
				return result, err
			}
		}
		if *track || *noTrack {
			if err := applyTrack(ctx, bc, dir, *beadID, *track); err != nil {
				return result, err
			}
			result.Track = track
			if *track {
				startNestedManager(ctx, bc, *projectName, dir, *beadID, func(w string) { fmt.Fprintln(stderr, "warning: "+w) })
			}
		}
		if *holdArg != "" || *noHold {
			if err := applyHold(ctx, bc, dir, *beadID, *holdArg, *noHold); err != nil {
				return result, err
			}
		}
		if *runtimeArg != "" {
			if err := applySoleLabel(ctx, bc, dir, *beadID, "runtime:", *runtimeArg); err != nil {
				return result, err
			}
		}
		if *viz != "" {
			if err := applySoleLabel(ctx, bc, dir, *beadID, beads.VizLabelPrefix, *viz); err != nil {
				return result, err
			}
			result.Viz = *viz
		}
		if laneValue != "" {
			if err := applySoleLabel(ctx, bc, dir, *beadID, beads.LaneLabelPrefix, laneValue); err != nil {
				return result, err
			}
			result.Lane = laneValue
		}
		if *runOn != "" {
			if err := applySoleLabel(ctx, bc, dir, *beadID, place.LabelPrefix, *runOn); err != nil {
				return result, err
			}
			result.RunOn = *runOn
		}
		if *maxOps != "" {
			// The cap is read from the owning workstream bead only (spawn.CheckWorkstreamCap):
			// the root, or a nested workstream with its own Manager.
			if root := beads.Owner(ctx, bc, dir, nil, nil, *beadID); root != *beadID {
				return result, fmt.Errorf("--max-operators goes on the workstream bead %s, not %s", root, *beadID)
			}
			if err := applySoleLabel(ctx, bc, dir, *beadID, beads.MaxOperatorsLabelPrefix, *maxOps); err != nil {
				return result, err
			}
			result.MaxOperators = *maxOps
		}
		if *until != "" {
			if err := applyUntil(ctx, bc, dir, *beadID, *until); err != nil {
				return result, err
			}
		}
		if *owner != "" {
			if err := applyStepOwner(ctx, bc, dir, *beadID, *owner); err != nil {
				return result, err
			}
			result.Owner = *owner
		}
		if *state != "" || *owner != "" || len(waiting) > 0 {
			if err := clearSettleMarkers(ctx, bc, dir, *beadID, len(waits) > 0 || *state == beads.StateNeedsUser || (strings.HasPrefix(*owner, "operator-") && *state == beads.StateInProgress)); err != nil {
				return result, err
			}
		}
		if (*state == beads.StateInProgress || strings.HasPrefix(*owner, "manager-")) && len(waits) == 0 && *state != beads.StateNeedsUser {
			if err := holdManagerOwner(ctx, bc, dir, *beadID, *owner, time.Now()); err != nil {
				return result, err
			}
		}
		if reviewRecord != "" {
			if err := bc.AddComment(ctx, dir, *beadID, reviewRecord); err != nil {
				return result, err
			}
		}
		// The state flush is the verb's decisive write (door intent, M3; see verbIntent).
		if err := writes.Flush(beads.WithIntent(ctx, verbIntent(*beadID, "internal-set-objective on "+*beadID))); err != nil {
			return result, err
		}
		if proposed != nil {
			announceProposal(ctx, bc, dir, *projectName, *beadID, proposingManager(), *proposed, stderr)
			result.Proposed = proposed.kind
		}
		if *state != "" {
			if notify != nil && (proposed == nil || proposed.kind != beads.ProposedAsk) {
				notify.send(ctx, stderr)
			}
			// Keep the sidebar's NEEDS YOU queue in step with the bead.
			if decisionJSON != "" {
				result.Card = "stored"
				if hasCard(ctx, *projectName, *beadID) {
					result.Card = "replaced"
				}
			}
			if err := syncNeedsYou(ctx, *projectName, *beadID, *state, *ask, *note, decisionJSON, cardBasis(ctx, bc, dir, *beadID, decisionJSON, *projectName, *ask+"\n"+*note)); err != nil {
				return result, fmt.Errorf("the step's state changed but its NEEDS YOU card was not updated, so the user sees the old one: %w", err)
			}
		} else if decisionJSON != "" {
			// A replacement card follows the successful bead write.
			if err := replaceCard(ctx, *projectName, *beadID, *ask, *note, decisionJSON, cardBasis(ctx, bc, dir, *beadID, decisionJSON, *projectName, *ask+"\n"+*note)); err != nil {
				return result, err
			}
			result.Card = "replaced"
		}
		// The user's write that left the step with no holder hands it to its Manager now that it landed.
		held, err := owed.settle(ctx, "")
		if err == nil {
			err = writes.Flush(ctx) // owed.bc is this command's batched writer
		}
		if err != nil {
			return result, err
		}
		result.Custody = held
		return result, nil
	})
}

// cardBasis is what a card raised now is raised against: the fingerprint of its
// bead's spec and dependencies already on the board. nil when there is no card or the bead cannot be read (a card
// without a basis only ages).
func cardBasis(ctx context.Context, bc beads.Client, dir, beadID, decision, project, text string) *store.Basis {
	if decision == "" || bc == nil {
		return nil
	}
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil || b == nil {
		return nil
	}
	for _, n := range livedata.PRLabels(b.Labels) {
		text += "\nPR #" + n
	}
	basis := &store.Basis{}
	if snap := boardSnapshot(); snap != nil {
		d, _ := store.ParseDecision(decision)
		basis = livedata.CardDependencies(*snap, project, beadID, d, text)
	}
	basis.Spec, basis.Raised = beads.SpecHash(*b), time.Now().Unix()
	return basis
}

// hasCard reports whether the step already has a NEEDS YOU row (best effort).
func hasCard(ctx context.Context, projectName, beadID string) bool {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return false
	}
	defer cleanup()
	project, err := st.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		return false
	}
	existing, _ := st.GetNeedsYou(ctx, project.ID, beadID)
	return existing != nil
}

// needsUserNotice is the high-importance mail to Commander for a step that
// just started waiting on the user. nil means "already needed the user; stay quiet".
type needsUserNotice struct {
	from, subject, body string
	ask                 string
}

func newNeedsUserNotice(ctx context.Context, bc beads.Client, dir, project, beadID, note, from string) *needsUserNotice {
	root := wsmgr.Owner(ctx, bc, dir, beadID)
	var step, workstream *beads.Bead
	if all, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"}); err == nil {
		for i := range all {
			switch all[i].ID {
			case beadID:
				step = &all[i]
			case root:
				workstream = &all[i]
			}
		}
	}
	title := beadID
	if step != nil {
		if state, _ := beads.StepState(step.Status, step.Labels); state == beads.StateNeedsUser {
			return nil
		}
		title = step.Title
	}
	if from == "" {
		from = wsmgr.Role(root) // the Manager of this step's workstream
	}
	body := fmt.Sprintf("Step %s %q (project %s", beadID, title, project)
	if workstream != nil {
		body += fmt.Sprintf(", workstream %q", workstream.Title)
	}
	body += ") now needs the user to verify or decide."
	if note != "" {
		body += "\n\n" + note
	}
	body += "\n\nAsk (what the user sees in his sidebar): {ASK}"
	body += "\n\nRaise it with the user in chat now and keep it in front of him until he signs off. Do not close the step until he confirms."
	return &needsUserNotice{from: from, subject: fmt.Sprintf("⚑ needs you: %s (%s)", title, project), body: body}
}

// send mails Commander. A mail failure is reported but never undoes the state change.
func (n *needsUserNotice) send(ctx context.Context, stderr *os.File) {
	st, client, key, cleanup, err := mailDeps()
	if err == nil {
		defer cleanup()
		body := strings.Replace(n.body, "{ASK}", n.ask, 1)
		subject := n.subject
		if n.ask != "" {
			subject = fmt.Sprintf("⚑ needs you: %s — %s", n.ask, strings.TrimPrefix(n.subject, "⚑ needs you: "))
		}
		err = sendRoleMail(ctx, st, client, key, n.from, []string{"commander"}, subject, body, agentmail.SendOptions{Importance: "high"})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: step is marked needs-user but mailing Commander failed: %v\n", err)
	}
}

// applyStepState moves a step to state. Not-started / in-progress set the bead
// status and clear both markers; blocked / needs-user set their marker (and
// clear the other) without touching status. Removing an absent label is a no-op.
func applyStepState(ctx context.Context, bc beads.Client, dir, beadID, state string) (err error) {
	writes := beads.NewStateWrite(bc)
	bc = writes
	defer func() {
		if err == nil {
			err = writes.Flush(ctx)
		}
	}()
	set := func(label string, on bool) error { return bc.SetLabel(ctx, dir, beadID, label, on) }
	for _, l := range ownercompat.NeedsLabels() {
		if err := set(l, false); err != nil {
			return err
		}
	}
	switch state {
	case beads.StateNotStarted, beads.StateInProgress:
		status := "open"
		if state == beads.StateInProgress {
			status = "in_progress"
		}
		if err := bc.SetStatus(ctx, dir, beadID, status); err != nil {
			return err
		}
		if err := set(beads.BlockedLabel, false); err != nil {
			return err
		}
		return set(beads.NeedsUserLabel, false)
	case beads.StateBlocked:
		if err := set(beads.NeedsUserLabel, false); err != nil {
			return err
		}
		return set(beads.BlockedLabel, true)
	default: // needs-user
		if err := set(beads.DecisionAnsweredLabel, false); err != nil {
			return err
		}
		if err := set(beads.BlockedLabel, false); err != nil {
			return err
		}
		return set(beads.NeedsUserLabel, true)
	}
}

// requireOwner refuses to put a step in progress with nobody on it: work that
// no bead names an owner for is work the user cannot see. newOwner is the --owner
// given in the same call, if any.
func requireOwner(ctx context.Context, bc beads.Client, dir, beadID, newOwner string) error {
	if newOwner != "" {
		if newOwner == "none" {
			return fmt.Errorf("a step in progress needs an owner: drop --owner none, or use --state not-started")
		}
		return nil
	}
	existing, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		return err
	}
	for _, b := range existing {
		if b.ID != beadID {
			continue
		}
		for _, l := range b.Labels {
			if strings.HasPrefix(l, beads.OwnerLabelPrefix) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s has no owner: pass --owner operator-<x> (spawn one with internal-spawn-operator --step %s), or --owner user for work in his sessions", beadID, beadID)
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// parseWaitingFlags parses every --waiting; a nil result means "none" (clear).
func parseWaitingFlags(args []string) ([]beads.Wait, error) {
	var waits []beads.Wait
	for _, a := range args {
		if strings.TrimSpace(a) == "none" {
			if len(args) > 1 {
				return nil, fmt.Errorf("--waiting none cannot be combined with other waits")
			}
			return nil, nil
		}
		w, err := beads.ParseWait(a)
		if err != nil {
			return nil, err
		}
		waits = append(waits, w)
	}
	return waits, nil
}

// applyWaiting makes waits the bead's whole set of waiting-on records (none
// clears), each stamped with now. Setting resets the last-checked stamp.
func applyWaiting(ctx context.Context, bc beads.Client, dir, beadID string, waits []beads.Wait, now time.Time) error {
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return err
	}
	if b != nil {
		for _, l := range b.Labels {
			_, isWait := beads.ParseWaitLabel(l)
			_, isChecked := beads.ParseCheckedLabel(l)
			if isWait || isChecked || l == beads.WaitNudgedLabel {
				if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
					return err
				}
			}
		}
	}
	for _, w := range waits {
		w.Since = now
		if l := w.Label(); len(l) > beads.MaxLabelLen {
			hint := "for a PR in this project's own repo use ci:#N"
			if w.Sha != "" {
				hint = "use a short sha (7-12 hex), which cmdrd resolves"
			}
			return fmt.Errorf("--waiting %s: too long to record (%d > %d characters); %s", w.Kind, len(l), beads.MaxLabelLen, hint)
		}
		if err := bc.SetLabel(ctx, dir, beadID, w.Label(), true); err != nil {
			return err
		}
	}
	return nil
}

// applyUntil sets the step's one until:<finish line> label (or clears it).
func applyUntil(ctx context.Context, bc beads.Client, dir, beadID, until string) error {
	until = strings.TrimSpace(until)
	if until != "none" && (until == "" || strings.ContainsAny(until, " ,")) {
		return fmt.Errorf("--until must be one word (merged, approved, live, adopted) or \"none\", got %q", until)
	}
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return err
	}
	if b != nil {
		for _, l := range b.Labels {
			if strings.HasPrefix(l, "until:") && l != "until:"+until {
				if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
					return err
				}
			}
		}
	}
	if until == "none" {
		return nil
	}
	return bc.SetLabel(ctx, dir, beadID, "until:"+until, true)
}

// applySoleLabel makes prefix+value the bead's only label with that prefix
// (viz:<n>, max-operators:<n>); "none" clears it.
func applySoleLabel(ctx context.Context, bc beads.Client, dir, beadID, prefix, value string) error {
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return err
	}
	if b != nil {
		for _, l := range b.Labels {
			if strings.HasPrefix(l, prefix) && l != prefix+value {
				if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
					return err
				}
			}
		}
	}
	if value == "none" {
		return nil
	}
	return bc.SetLabel(ctx, dir, beadID, prefix+value, true)
}

// clearSettleMarkers drops settled-run receipts. Approval dispatch custody
// survives Manager-only edits until a real successor is recorded.
func clearSettleMarkers(ctx context.Context, bc beads.Client, dir, beadID string, dispatchAccepted bool) error {
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil || b == nil {
		return err
	}
	for _, l := range b.Labels {
		if !dispatchAccepted {
			if w, ok := beads.ParseWaitLabel(l); ok && beads.IsManagerDispatch(w) {
				continue
			}
			if strings.HasPrefix(l, "handoff-notified:") {
				continue
			}
		}
		if beads.IsSettleMarker(l) {
			if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyStepOwner makes owner the step's only owner:<name> label; "none" clears.
func applyStepOwner(ctx context.Context, bc beads.Client, dir, beadID, owner string) error {
	owner = beads.OwnerLabelName(owner)
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return err
	}
	if b != nil {
		for _, l := range b.Labels {
			if strings.HasPrefix(l, beads.OwnerLabelPrefix) && l != beads.OwnerLabelPrefix+owner {
				if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
					return err
				}
			}
		}
	}
	if owner == "none" {
		return nil
	}
	return bc.SetLabel(ctx, dir, beadID, beads.OwnerLabelPrefix+owner, true)
}

func runInternalRenameObjective(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-rename-objective", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	beadID := fs.String("bead", "", "objective bead id")
	title := fs.String("title", "", "new objective title")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *beadID == "" || *title == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead, --title")
		return 1
	}
	return editObjective(*projectName, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		err := newBeadsClientFn().Rename(ctx, dir, *beadID, *title)
		return internalObjectiveEditResult{BeadID: *beadID, Title: *title}, err
	})
}

func runInternalCloseObjective(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-close-objective", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	beadID := fs.String("bead", "", "objective bead id")
	reason := fs.String("reason", "", "why it is closed (recorded on the bead; open children and unpassed gates still refuse the close)")
	var hold thenHold
	var thenWaiting stringList
	fs.StringVar(&hold.step, "then-step", "", "the step the --then-* hold goes on (default: every step the close leaves unheld)")
	fs.Var(&thenWaiting, "then-waiting", "what the released work waits on next, repeatable, in the --waiting grammar (until:<RFC3339>:<plain words>, ci:#N, review:<login>, operator:<role>); written before the close")
	fs.StringVar(&hold.owner, "then-owner", "", "the running agent that takes the released work (set in progress); written before the close")
	fs.StringVar(&hold.decision, "then-decision", "", "the user's card for the released work (set needs-user), as JSON or @file; written before the close")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *beadID == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	if waits, err := parseWaitingFlags(thenWaiting); err != nil || (len(thenWaiting) > 0 && len(waits) == 0) {
		emitErr(stderr, fmt.Sprintf("--then-waiting must name what the work waits on: %v", err))
		return 1
	}
	hold.waiting = thenWaiting
	hold.owner = ownercompat.Normalize(hold.owner)
	if hold.step != "" && !hold.set() {
		emitErr(stderr, "--then-step needs --then-waiting, --then-owner or --then-decision")
		return 1
	}
	return editObjective(*projectName, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		result := internalObjectiveEditResult{BeadID: *beadID}
		if os.Getenv("COMMANDER_ROLE") == "" {
			if err := closeConfirmSteps(ctx, newBeadsClientFn(), dir, *beadID); err != nil {
				return result, err
			}
		}
		if err := requireParentFinished(ctx, newBeadsClientFn(), dir, *beadID); err != nil {
			return result, err
		}
		if err := checkWorkflowClose(ctx, newBeadsClientFn(), dir, *projectName, *beadID); err != nil {
			return result, err
		}
		if err := requireGatesPassed(ctx, *projectName, *beadID); err != nil {
			return result, err
		}
		targets, custody, err := checkCloseLeavesWorkHeld(ctx, newBeadsClientFn(), dir, *beadID, closeDoorRole(ctx, newBeadsClientFn(), dir, *beadID), hold)
		if err != nil {
			return result, err
		}
		// The hold lands before the close, so no read of the board falls between them.
		closingBead = *beadID
		defer func() { closingBead = "" }()
		for _, t := range targets {
			if err := runQuiet(runInternalSetObjective, hold.args(*projectName, t)); err != nil {
				return result, fmt.Errorf("the hold on %s was refused, so %s was not closed: %w", t, *beadID, err)
			}
		}
		result.Then = targets
		custodyAt := time.Now()
		if ids := custody; len(ids) > 0 {
			held, err := holdForManager(ctx, newBeadsClientFn(), dir, ids, *beadID, custodyAt)
			if err != nil {
				return result, custodyErr(ids, *beadID, err)
			}
			result.Custody = held
		}
		// The close is the verb's decisive write: cmdrd judges it again on fresh reads and lays it
		// on the board at once (door intent, M3).
		closeCtx := beads.WithIntent(ctx, verbIntent(*beadID, "closing "+*beadID))
		var closeErr error
		if strings.TrimSpace(*reason) != "" {
			closeErr = newBeadsClientFn().CloseWithReason(closeCtx, dir, *beadID, strings.TrimSpace(*reason))
		} else {
			closeErr = newBeadsClientFn().Close(closeCtx, dir, *beadID)
		}
		if err := closeErr; err != nil {
			// The close did not happen: the steps it would have released stay behind it.
			if len(result.Custody) > 0 {
				if left := releaseCustody(ctx, newBeadsClientFn(), dir, result.Custody, custodyAt); len(left) > 0 {
					err = fmt.Errorf("%w; the Manager's %q hold written on %s could not be taken back: clear it with internal-set-objective --bead <id> --waiting none", err, beads.ManagerStartWait, strings.Join(left, ", "))
				}
				result.Custody = nil
			}
			if len(targets) > 0 {
				return result, fmt.Errorf("the hold on %s was written but closing %s failed: %w", strings.Join(targets, ", "), *beadID, err)
			}
			return result, err
		}
		_ = syncNeedsYou(ctx, *projectName, *beadID, "closed", "", "", "", nil)
		_, _ = beads.ClearPush(ctx, newBeadsClientFn(), dir, *beadID)
		result.Closed = true
		return result, nil
	})
}

// runQuiet runs a cmdr command in-process and returns its error message, if any.
func runQuiet(run func([]string, *os.File, *os.File) int, args []string) error {
	out, err := os.CreateTemp("", "cmdr-out-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	errOut, err := os.CreateTemp("", "cmdr-err-*")
	if err != nil {
		return err
	}
	defer os.Remove(errOut.Name())
	defer errOut.Close()
	if run(args, out, errOut) == 0 {
		return nil
	}
	raw, _ := os.ReadFile(errOut.Name())
	text := strings.TrimSpace(string(raw))
	lines := strings.Split(text, "\n")
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1]), &e) == nil && e.Error != "" {
		return fmt.Errorf("%s", e.Error)
	}
	return fmt.Errorf("%s", text)
}

func editObjective(projectName string, stdout, stderr *os.File, apply func(ctx context.Context, dir string) (internalObjectiveEditResult, error)) int {
	project, err := resolveProject(projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()

	result, err := apply(ctx, project.Path)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	result.Project = project.Name
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// needsYouStoreFn opens the state db for the NEEDS YOU queue; tests replace it.
var needsYouStoreFn = func() (*store.Store, func(), error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(statePath)
	if err != nil {
		return nil, nil, err
	}
	return st, func() { _ = st.Close() }, nil
}

// syncNeedsYou records the ask for a step that is now needs-user, and clears it
// for any other state (or when the step is closed). With no ask given for a
// needs-user step (the user's own mark key) the ask is derived from nothing: an
// existing row is kept, otherwise a generic "Look at this" ask is stored.
func syncNeedsYou(ctx context.Context, projectName, beadID, state, ask, note, decision string, basis *store.Basis) error {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return err
	}
	defer cleanup()
	project, err := st.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		return fmt.Errorf("project %q: %v", projectName, err)
	}
	if state != beads.StateNeedsUser {
		return st.DeleteNeedsYou(ctx, project.ID, beadID)
	}
	ask = strings.TrimSpace(ask)
	if ask == "" && decision != "" {
		// A card always has its question; it is the one-line ask.
		if d, err := store.ParseDecision(decision); err == nil {
			ask = truncateRunes(d.Question, store.MaxAskLen)
		}
	}
	if ask == "" {
		if existing, _ := st.GetNeedsYou(ctx, project.ID, beadID); existing != nil && strings.TrimSpace(note) == "" && decision == "" {
			return nil // already flagged; keep what it says
		}
		ask = "Look at this"
	}
	return st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: project.ID, BeadID: beadID, Ask: ask, Note: note, Decision: decision, Basis: basis})
}

// replaceCard swaps the card on a step that already waits on the user. Without
// one there is nothing to replace: raising a card is --state needs-user.
func preflightReplaceCard(ctx context.Context, projectName, beadID, ask, note, decision string) error {
	return updateCard(ctx, projectName, beadID, ask, note, decision, false, nil)
}
func replaceCard(ctx context.Context, projectName, beadID, ask, note, decision string, basis *store.Basis) error {
	return updateCard(ctx, projectName, beadID, ask, note, decision, true, basis)
}
func updateCard(ctx context.Context, projectName, beadID, ask, note, decision string, persist bool, basis *store.Basis) error {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return fmt.Errorf("could not open the NEEDS YOU queue to replace the card on %s: %w", beadID, err)
	}
	defer cleanup()
	project, err := st.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		return fmt.Errorf("project %q: %v", projectName, err)
	}
	existing, err := st.GetNeedsYou(ctx, project.ID, beadID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("--decision on %s: it has no open card to replace. Raise one with --state needs-user --decision '<json>'", beadID)
	}
	if strings.TrimSpace(ask) == "" {
		ask = existing.Ask
	}
	card := store.NeedsYou{ProjectID: project.ID, BeadID: beadID, Ask: ask, Note: note, Decision: decision, Basis: basis}
	if err := store.ValidateAsk(card.Ask); err != nil {
		return err
	}
	if !persist {
		return nil
	}
	return st.UpsertNeedsYou(ctx, card)
}

// checkWaitsAreReal refuses a wait nothing will ever end: a wait on the user
// with no question in their NEEDS YOU queue (on this step or one under it), and
// a wait on an Operator that is not running. Either leaves the row reading
// Stalled with nobody able to move it.
func checkWaitsAreReal(ctx context.Context, projectName, beadID string, waits []beads.Wait) error {
	var user, ops []string
	for _, w := range waits {
		switch w.Kind {
		case beads.WaitUser:
			user = append(user, "user")
		case beads.WaitOperator:
			ops = append(ops, w.Ref)
		}
	}
	if len(user) == 0 && len(ops) == 0 {
		return nil
	}
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return nil // no store to check against: do not block the write
	}
	defer cleanup()
	if len(user) > 0 {
		asks, err := st.ListNeedsYou(ctx)
		if err == nil && !hasAskAtOrUnder(asks, beadID) {
			return fmt.Errorf("--waiting user on %s: the user has no question for it, so nothing would ever end this wait. Set the step that needs them --state needs-user with --decision (that is the wait), or wait on what is really holding it (ci:, review:, operator:<running role>, until:)", beadID)
		}
	}
	if len(ops) > 0 {
		running := map[string]bool{}
		projects, _ := st.ListProjects(ctx)
		for _, p := range projects {
			runs, err := st.ListAgentRunsByProject(ctx, p.ID, "running")
			if err != nil {
				continue
			}
			for _, r := range runs {
				running[spawn.CanonicalRole(r.AgentName)] = true
			}
		}
		for _, op := range ops {
			if !running[spawn.CanonicalRole("operator-"+strings.TrimPrefix(op, "operator-"))] {
				return fmt.Errorf("--waiting operator:%s on %s: no Operator by that name is running, so nothing would ever end this wait. Spawn it first (cmdr internal-spawn-operator), or wait on what is really holding the step", op, beadID)
			}
		}
	}
	return nil
}

func hasAskAtOrUnder(asks []store.NeedsYou, beadID string) bool {
	for _, a := range asks {
		if a.BeadID == beadID || strings.HasPrefix(a.BeadID, beadID+".") {
			return true
		}
	}
	return false
}

// loadDecision reads --decision: inline JSON, "@path", or a path to a JSON file.
func loadDecision(arg string) (*store.Decision, error) {
	raw := strings.TrimSpace(arg)
	path := strings.TrimPrefix(raw, "@")
	if strings.HasPrefix(raw, "@") || !strings.HasPrefix(raw, "{") {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("--decision: %w", err)
		}
		raw = string(b)
	}
	d, err := store.ParseDecision(raw)
	if err != nil {
		return nil, err
	}
	if err := checkCardDrawsNoWorkflow(d); err != nil {
		return nil, err
	}
	return d, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// applyReview marks a step in review as PR n (labels in-review + pr:n), or
// clears it. A PR in another repo is <owner/name>#N (pr:<owner/name>#N), and
// cmdrd watches its reviews there. A clear returns the comment that records it.
func applyReview(ctx context.Context, bc beads.Client, dir, beadID, review, reason string, waitsGiven bool) (string, error) {
	if review == "none" {
		return clearReview(ctx, bc, dir, beadID, reason, waitsGiven)
	}
	label, err := reviewLabel(review)
	if err != nil {
		return "", err
	}
	if err := bc.SetLabel(ctx, dir, beadID, "in-review", true); err != nil {
		return "", err
	}
	return "", bc.SetLabel(ctx, dir, beadID, label, true)
}

// refuseOperatorReviewClear keeps an Operator from clearing a PR label: as a hold,
// that is its Manager's call. Setting one stays the Operator's.
func refuseOperatorReviewClear(review string) error {
	if review == "none" && strings.HasPrefix(os.Getenv("COMMANDER_ROLE"), "operator-") {
		return fmt.Errorf("an Operator cannot clear a PR label: ask your Manager")
	}
	return nil
}

// clearReview removes in-review and every pr: label, in whichever spelling, and
// returns the comment recording who, what and why ("" when nothing was there). The
// caller writes it after the command's other checks and before the labels go, so a
// refused command records nothing and a clear never goes unrecorded. A label the
// merge rule judges needs a reason. A plugged block keeps its own PR reading
// (workflowtemplate.TargetPRs), so a target stays judged at the doors. A review wait
// left with no PR could never clear, so it is refused unless this command also sets
// the waits (--waiting none or others).
func clearReview(ctx context.Context, bc beads.Client, dir, beadID, reason string, waitsGiven bool) (string, error) {
	b, err := beads.FreshShow(ctx, bc, dir, beadID)
	if err != nil {
		return "", fmt.Errorf("--review none: could not read %s, nothing cleared: %w", beadID, err)
	}
	if b == nil {
		return "", fmt.Errorf("--review none: no bead %s", beadID)
	}
	if !waitsGiven && slices.ContainsFunc(beads.WaitsOf(b.Labels), func(w beads.Wait) bool { return w.Kind == beads.WaitReview }) {
		return "", fmt.Errorf("--review none on %s: it still waits on a review, which could never clear with no PR. Pass --waiting too: none, or what it waits on now if that is not a review", beadID)
	}
	var removed []string
	for _, l := range b.Labels {
		if strings.HasPrefix(strings.ToLower(l), "pr:") {
			removed = append(removed, l)
		}
	}
	reason = strings.TrimSpace(reason)
	if len(removed) > 0 && len([]rune(reason)) < minReviewClearReason {
		judged, err := prLabelsJudged(ctx, bc, dir, *b)
		if err != nil {
			return "", fmt.Errorf("--review none: could not read the gates of %s, nothing cleared: %w", beadID, err)
		}
		if judged {
			return "", fmt.Errorf("--review none on %s would remove %s, which the merge rule judges it on: with the label gone, closing it no longer answers for that PR. Nothing cleared. If the label is a mistake, repeat with --reason '<why, %d+ characters>', which goes in a comment on the bead", beadID, strings.Join(removed, ", "), minReviewClearReason)
		}
	}
	if slices.Contains(b.Labels, "in-review") {
		removed = append(removed, "in-review")
	}
	if len(removed) == 0 {
		return "", nil
	}
	for _, l := range removed {
		if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
			return "", err
		}
	}
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "user"
	}
	if reason == "" {
		reason = "no reason given"
	}
	return fmt.Sprintf("review labels cleared by %s: %s. Reason: %s", who, strings.Join(removed, ", "), reason), nil
}

// minReviewClearReason is the shortest --reason a judged label can go on.
const minReviewClearReason = 15

// prLabelsJudged is whether the merge rule reads b's pr: labels: b is a merge target,
// or the stage of a merge gate. No GitHub read, so a failed one cannot open this; a
// failed read of the gates is an error, never "not judged".
func prLabelsJudged(ctx context.Context, bc beads.Client, dir string, b beads.Bead) (bool, error) {
	// The rule reads pr: in lower case; a label in another spelling still fails closed here.
	lowered := b
	lowered.Labels = make([]string, len(b.Labels))
	for i, l := range b.Labels {
		lowered.Labels[i] = strings.ToLower(l)
	}
	if workflowtemplate.MergeTarget(workflowtemplate.Node{Bead: lowered}) {
		return true, nil
	}
	guards, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{"gate-of:" + beads.EscapeLabel(b.ID)}, Status: "all"})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(guards, func(g beads.Bead) bool { return workflowtemplate.MergeGate(workflowtemplate.Node{Bead: g}) }), nil
}

var repoRef = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// reviewLabel is the pr: label for --review: N, #N, or <owner/name>#N.
func reviewLabel(review string) (string, error) {
	v := strings.TrimSpace(review)
	repo, n, cross := strings.Cut(v, "#")
	if !cross || repo == "" {
		repo, n = "", strings.TrimPrefix(v, "#")
	}
	if _, err := strconv.Atoi(n); err != nil || (repo != "" && !repoRef.MatchString(repo)) {
		return "", fmt.Errorf("--review must be a PR number (N or #N), a PR in another repo (<owner>/<name>#N), or \"none\", got %q", review)
	}
	if repo != "" {
		return "pr:" + repo + "#" + n, nil
	}
	return "pr:" + n, nil
}

// checkReopenPlacement keeps a closed or parked bead from coming back into a level
// whose open children are the other kind.
func checkReopenPlacement(ctx context.Context, bc beads.Client, dir, beadID string) error {
	b, err := beads.FreshShow(ctx, bc, dir, beadID)
	if err != nil || b == nil || (b.Status != "closed" && b.Status != beads.StatusDeferred) {
		return nil // the write that follows reports a bead it cannot reach
	}
	d, err := beads.FreshDetails(ctx, bc, dir, []string{beadID})
	if err != nil {
		return nil
	}
	return beads.CheckPlacement(ctx, bc, dir, d[beadID].Parent, beads.Placement{Workstream: beads.IsWorkstream(b.Labels), Self: beadID})
}

// applyTrack makes a bead a nested workstream of its parent, or a plain step. A
// bead with no parent edge cannot be nested (internal-track convert moves one
// under a workstream first).
func applyTrack(ctx context.Context, bc beads.Client, dir, beadID string, on bool) error {
	d, err := beads.FreshDetails(ctx, bc, dir, []string{beadID})
	if err != nil {
		return err
	}
	parent := d[beadID].Parent
	if on && parent == "" {
		return fmt.Errorf("%s has no parent: a nested workstream sits under a workstream (internal-track convert --bead %s --into <workstream>)", beadID, beadID)
	}
	if err := beads.CheckPlacement(ctx, bc, dir, parent, beads.Placement{Workstream: on, Self: beadID}); err != nil {
		return err
	}
	if !on {
		children, err := beads.OpenChildren(ctx, bc, dir, beadID)
		if err != nil {
			return err
		}
		for _, k := range children {
			if beads.IsWorkstream(k.Labels) {
				return fmt.Errorf("%s has an open workstream under it (%s): a step cannot hold one", beadID, k.ID)
			}
		}
	}
	if err := bc.SetLabel(ctx, dir, beadID, beads.TrackLabel, false); err != nil {
		return err
	}
	return bc.SetLabel(ctx, dir, beadID, beads.WorkstreamLabel, on)
}

// refuseOperatorHold keeps an Operator from holding or releasing any bead: a
// hold is the Manager's (or Commander's) order to stop.
func refuseOperatorHold(touchesHold bool) error {
	if touchesHold && strings.HasPrefix(os.Getenv("COMMANDER_ROLE"), "operator-") {
		return fmt.Errorf("an Operator cannot set or clear a hold: ask your Manager")
	}
	return nil
}

// applyHold sets (reason, who) or clears the hold label and records who and why
// in a comment.
func applyHold(ctx context.Context, bc beads.Client, dir, beadID, reason string, clear bool) error {
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "commander"
	}
	if clear {
		if err := bc.SetLabel(ctx, dir, beadID, beads.HoldLabel, false); err != nil {
			return err
		}
		return bc.AddComment(ctx, dir, beadID, "hold cleared by "+who)
	}
	if reason == "-" {
		reason = "no reason given"
	}
	if err := bc.SetLabel(ctx, dir, beadID, beads.HoldLabel, true); err != nil {
		return err
	}
	return bc.AddComment(ctx, dir, beadID, beads.HoldComment(who, reason))
}

// runParkEdit is --park / --unpark: Park and Unpark touch only the status and
// the parking labels, so a needs-user marker and the stored card stay.
func runParkEdit(project, beadID string, park bool, until, reason, pushReason string, stdout, stderr *os.File) int {
	var untilAt time.Time
	if park && strings.TrimSpace(until) != "" {
		t, err := beads.ParseParkUntil(until, time.Now())
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		untilAt = t
	}
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "commander"
	}
	return editObjective(project, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		bc := newBeadsClientFn()
		result := internalObjectiveEditResult{BeadID: beadID}
		if !park {
			if err := checkReopenPlacement(ctx, bc, dir, beadID); err != nil {
				return result, err
			}
		}
		if park {
			if err := checkStandingDecisions(ctx, bc, dir, beadID, reason); err != nil {
				return result, err
			}
			push, err := planBeadPush(ctx, bc, dir, beadID, untilAt, pushReason)
			if err != nil {
				return result, err
			}
			if err := beads.Park(ctx, bc, dir, beadID, who, strings.TrimSpace(reason), untilAt); err != nil {
				return result, err
			}
			if err := finishPush(ctx, bc, dir, project, beadID, push, stderr); err != nil {
				return result, err
			}
			result.Status = beads.StatusDeferred
			if p := proposedPark(untilAt, reason, time.Now()); p != nil && proposingManager() != "" {
				if err := bc.SetLabel(ctx, dir, beadID, p.label(time.Now()), true); err != nil {
					return result, fmt.Errorf("%s is parked, but marking the park proposed failed: %w", beadID, err)
				}
				announceProposal(ctx, bc, dir, project, beadID, proposingManager(), *p, stderr)
				result.Proposed = p.kind
			}
			return result, nil
		}
		owed, err := requireHeldAfter(ctx, bc, dir, beadID, "internal-set-objective --unpark "+beadID, holdFlags+"; put it on the bead first, then unpark",
			map[string]holding.Edit{beadID: func(b *holding.Bead) { b.Status = "open" }})
		if err != nil {
			return result, err
		}
		// The user's unpark of a step nothing else holds: it comes out of the park carrying the
		// Manager's receipt (Unpark's hold labels); anything else it leaves unheld is settled after.
		var hold []string
		if i := slices.Index(owed.ids, beadID); i >= 0 {
			hold = []string{custodyLabel(time.Now())}
			owed.ids = slices.Delete(slices.Clone(owed.ids), i, i+1)
			result.Custody = []string{beadID}
		}
		if _, err := beads.Unpark(ctx, bc, dir, beadID, who, hold...); err != nil {
			return result, err
		}
		held, err := owed.settle(ctx, "")
		result.Custody = append(result.Custody, held...)
		if err != nil {
			return result, err
		}
		return result, nil
	})
}

// holdManagerOwner makes a Manager's name a real holder. A Manager owner is only accountability,
// so a step it owns with no other holder gets the Manager dispatch wait, which cmdrd wakes and
// escalates (the same custody holdApprovedWork records). Any wait, user card or Operator already on
// the step is the holder and nothing is added.
func holdManagerOwner(ctx context.Context, bc beads.Client, dir, beadID, newOwner string, now time.Time) error {
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil || b == nil {
		return err
	}
	_, owner := beads.StepState(b.Status, b.Labels)
	if newOwner != "" && newOwner != "none" {
		owner = newOwner
	}
	if !strings.HasPrefix(owner, "manager-") {
		return nil
	}
	for _, l := range b.Labels {
		if _, ok := beads.ParseWaitLabel(l); ok {
			return nil
		}
	}
	if st, _ := beads.StepState(b.Status, b.Labels); st == beads.StateNeedsUser {
		return nil
	}
	w := beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(2 * time.Minute), Ref: beads.ManagerDispatchWait}
	return bc.SetLabel(ctx, dir, beadID, w.Label(), true)
}
