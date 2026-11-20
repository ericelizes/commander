package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/place"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/reviewers"
)

// DoorChecksEnv switches the write checks off in cmdrd when set to off/0/false.
const DoorChecksEnv = "COMMANDER_DOOR_CHECKS"

func doorChecksOn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DoorChecksEnv))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// roleOf is the COMMANDER_ROLE the caller sent; "" = the user (or cmdrd itself).
func roleOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "COMMANDER_ROLE="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// flagVals returns every value of a repeated flag in args ("--x v" or "--x=v").
func flagVals(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		} else if v, ok := strings.CutPrefix(args[i], flag+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

// checkWrite mirrors, in the daemon, the client-side rules (cmd/cmdr
// door_checks.go) that can be judged from the argv plus a fresh read, so an
// agent that skips the CLI is refused the same way. The user (no role) is not
// checked, as in the client. A read that fails lets the write through: the
// door must never be the reason a write cannot happen.
func (s *Server) checkWrite(ctx context.Context, dir, role string, ops [][]string) error {
	if role == "" || !doorChecksOn() {
		return nil
	}
	bc := s.opts.Beads
	if bc == nil {
		bc = beads.New()
	}
	for i, args := range ops {
		err := s.checkRunOnLabels(ctx, addedLabels(args))
		if err == nil {
			err = checkOp(ctx, bc, dir, args)
		}
		if err != nil {
			if len(ops) > 1 {
				return fmt.Errorf("op %d of %d (%s): %w", i+1, len(ops), strings.Join(args, " "), err)
			}
			return err
		}
	}
	return nil
}

func checkOp(ctx context.Context, bc beads.Client, dir string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "create":
		return checkCreate(ctx, dir, args)
	case "close":
		for _, id := range args[1:] {
			if strings.HasPrefix(id, "-") {
				break
			}
			if err := refuseWorkstreamClose(ctx, bc, dir, id); err != nil {
				return err
			}
		}
		return nil
	case "dep":
		if len(args) >= 4 && args[1] == "add" {
			return refuseAfterClosingGate(ctx, bc, dir, args[3])
		}
		return nil
	case "update":
		if len(args) < 2 {
			return nil
		}
		if slices.Contains(flagVals(args, "--status"), "closed") {
			if err := refuseWorkstreamClose(ctx, bc, dir, args[1]); err != nil {
				return err
			}
		}
		return checkUpdate(ctx, bc, dir, args[1], args[2:], flagVals(args, "--add-label"), flagVals(args, "--remove-label"))
	case "label":
		if len(args) >= 4 && args[1] == "add" {
			return checkUpdate(ctx, bc, dir, args[2], nil, args[3:], nil)
		}
	case "defer":
		for _, id := range args[1:] {
			if strings.HasPrefix(id, "-") {
				break
			}
			if err := refusePause(ctx, bc, dir, id, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// refusePause: a step is never paused. Whether an agent runs follows from the
// tree alone, so an agent may not park a step (status deferred) or put a hold
// label on any bead; what must happen first is its own bead the step is
// blocked by. Parking a gate (a withdrawn or dropped approval) is no pause.
// read, when not nil, reads the bead's labels (ok=false: unreadable).
func refusePause(ctx context.Context, bc beads.Client, dir, id string, read func() ([]string, bool)) error {
	if read == nil {
		read = func() ([]string, bool) {
			b, err := bc.Show(ctx, dir, id)
			if err != nil || b == nil {
				if err == nil {
					err = errors.New("bead not found")
				}
				logDoorFailOpen(dir, id, "read bead", err)
				return nil, false
			}
			return b.Labels, true
		}
	}
	labels, ok := read()
	if !ok || slices.Contains(labels, beads.GateLabel) || slices.Contains(labels, "kind:gate") || beads.IsClosingGate(labels) {
		return nil
	}
	return pauseRefusal(id)
}

func pauseRefusal(id string) error {
	return fmt.Errorf("refused: a step is never paused (%s). Every step is working, in a watched wait, or a card for the user. If something must happen first, add it as its own bead and make %s blocked by it: a gate or a step with the user's card (cmdr internal-create-objective --deps blocks:%s, or --gate --approver user --before %s), or a wait (cmdr internal-set-objective --waiting). If the step is not wanted, supersede it", id, id, id, id)
}

// refuseWorkstreamClose: only the user marks a workstream done. An agent that
// thinks one is finished asks them on a card instead.
func refuseWorkstreamClose(ctx context.Context, bc beads.Client, dir, id string) error {
	b, err := bc.Show(ctx, dir, id)
	if err != nil || b == nil {
		if err == nil {
			err = errors.New("bead not found")
		}
		logDoorFailOpen(dir, id, "read bead", err)
		return nil
	}
	// A workstream closes once its children are done or superseded (rule 26,
	// checked on every write): no card is needed for that.
	if beads.IsClosingGate(b.Labels) {
		if b.HumanAnswerPending {
			return nil // applying the user's own recorded answer (their click, or their words relayed with provenance)
		}
		return fmt.Errorf("%s is its workstream's Confirm done step: only the user confirms it, on its card, or by saying so in chat (record it with cmdr internal-resolve-needs --action approve --relayed '<when, and the user's exact words>')", id)
	}
	return nil
}

// refuseAfterClosingGate: a workstream's Confirm done step comes after
// everything else in it, so nothing may wait on it.
func refuseAfterClosingGate(ctx context.Context, bc beads.Client, dir, blocker string) error {
	b, err := bc.Show(ctx, dir, blocker)
	if err != nil || b == nil {
		return nil
	}
	if beads.IsClosingGate(b.Labels) {
		return fmt.Errorf("%s is its workstream's Confirm done step and comes after everything else: nothing may wait on it", blocker)
	}
	return nil
}

func checkCreate(ctx context.Context, dir string, args []string) error {
	var labels []string
	for _, l := range flagVals(args, "--labels") {
		labels = append(labels, strings.Split(l, ",")...)
	}
	if err := checkWaitLabels(ctx, dir, "", labels, func() ([]string, bool) { return nil, true }); err != nil {
		return err
	}
	if !slices.Contains(labels, beads.GateLabel) {
		return nil
	}
	var approvers []string
	for _, l := range labels {
		if a, ok := strings.CutPrefix(l, beads.ApproverLabelPrefix); ok {
			approvers = append(approvers, a)
		}
	}
	if len(approvers) == 0 {
		return fmt.Errorf("a new gate needs an approver label (approver:<who>): without one nobody can pass it. Create it with cmdr internal-create-objective --gate --approver <who> --criteria '...'")
	}
	if c := flagVals(args, "--acceptance"); len(c) == 0 || strings.TrimSpace(c[len(c)-1]) == "" {
		return fmt.Errorf("a new gate needs --criteria (what counts as passed): the diagram shows it on the gate, and an agent cannot check a gate against nothing")
	}
	if len(args) > 1 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(args[1])), "greenlight") {
		for _, a := range approvers {
			if ownercompat.Normalize(a) == "user" {
				return nil
			}
		}
		return fmt.Errorf("a gate titled Greenlight is the user's approval by name: add --approver user, or give the gate another title")
	}
	return nil
}

// reviewerGH answers the door's one cheap GitHub check of a review wait's ref (tests replace it).
var reviewerGH reviewers.Runner = reviewers.RunGH

// reviewerCheckTimeout bounds that check: past it the write goes through unchecked.
const reviewerCheckTimeout = 10 * time.Second

// checkWaitLabels refuses a waiting:* label that is not a typed wait, a slug
// time label, a timer standing in for a PR review, a review wait with no PR,
// or a review wait whose ref no review can ever match (not a GitHub login, not
// a team, not any). beadLabels reads the bead's own labels (for its pr: label)
// only when needed.
func checkWaitLabels(ctx context.Context, dir, id string, added []string, beadLabels func() ([]string, bool)) error {
	prOf := func(ls []string) string {
		for _, l := range ls {
			if strings.HasPrefix(l, "pr:") && l != "pr:" {
				return l
			}
		}
		return ""
	}
	for _, l := range added {
		if !strings.HasPrefix(l, beads.WaitLabelPrefix) && !strings.HasPrefix(l, "w:") {
			continue
		}
		w, ok := beads.ParseWaitLabel(l)
		if !ok {
			if strings.HasPrefix(l, beads.WaitLabelPrefix) {
				return fmt.Errorf("label %q is not a typed wait: write waits with cmdr internal-set-objective --waiting (ci:#N, review:<login>, operator:<role>, until:<time>:<what happens then>)", l)
			}
			continue
		}
		switch w.Kind {
		case beads.WaitUntil:
			if w.Ref != "" && !w.Cut && !beads.ReadableUntilLabel(w.Ref) {
				return fmt.Errorf("wait until:...:%q: the label is what the user reads (\"Waiting for <label>\"), so write it in plain words saying what happens then. If it really waits on something else, wait on that: review:<login>, ci:#N, main-ci:<sha>, prod-tag:<sha>, deploy:<sha>, pr:<cond>:#N, event:slack:<channel>:<regexp>, operator:<running role>, or --state needs-user with --decision", w.Ref)
			}
			if beads.ReviewTimerLabel(w.Ref) {
				return fmt.Errorf("wait until:...:%q: a timer standing in for a PR review leaves the review unseen until it fires. cmdrd watches reviews itself: --waiting review:<login> with --review <N>", w.Ref)
			}
			if beads.UserTimerLabel(w.Ref) {
				return fmt.Errorf("wait until:...:%q: a clock standing in for something the user does leaves the step waiting with no card, so the user never sees it. The user's move is a card: --state needs-user with --decision", w.Ref)
			}
			if beads.EventTimerLabel(w.Ref) {
				return fmt.Errorf("wait until:...:%q: a clock time standing in for a deploy, PR or notification leaves the step idle after it happens. cmdrd watches these itself and the step continues the moment it happens: --waiting deploy:<sha> (a successful prod deploy containing the commit), pr:<merged|green|approved>:#N, or event:slack:<channel id>:<regexp> (a matching message after now), each optionally with ,by=<RFC3339>", w.Ref)
			}
		case beads.WaitReview:
			pr := prOf(added)
			if pr == "" {
				labels, ok := beadLabels()
				if !ok {
					continue // unreadable bead: let the write through (logged)
				}
				if pr = prOf(labels); pr == "" {
					return fmt.Errorf("wait review:%s names no PR, so cmdrd can never see the review and the wait would never clear. Give the step a pr: label (--review <PR number>)", w.Ref)
				}
			}
			repo := livedata.ReviewRepo([]string{pr})
			if repo == "" {
				repo = livedata.GitHubRepo(dir)
			}
			cctx, cancel := context.WithTimeout(ctx, reviewerCheckTimeout)
			refusal, unchecked := reviewers.Check(cctx, reviewerGH, w.Ref, repo)
			cancel()
			if unchecked != nil {
				logDoorFailOpen(dir, id, "review wait reviewer "+w.Ref, unchecked)
			}
			if refusal != nil {
				return refusal
			}
		}
	}
	return nil
}

func checkUpdate(ctx context.Context, bc beads.Client, dir, id string, args, added, removed []string) error {
	var cur *beads.Bead
	// read reports ok=false when the bead cannot be read; every rule that
	// depends on it then lets the write through and logs the bypass.
	read := func() ([]string, bool) {
		if cur == nil {
			b, err := bc.Show(ctx, dir, id)
			if err != nil || b == nil {
				if err == nil {
					err = errors.New("bead not found")
				}
				logDoorFailOpen(dir, id, "read bead", err)
				return nil, false
			}
			cur = b
		}
		return cur.Labels, true
	}
	// A label this same write removes is no PR for a review wait.
	kept := func() ([]string, bool) {
		labels, ok := read()
		return slices.DeleteFunc(slices.Clone(labels), func(l string) bool { return slices.Contains(removed, l) }), ok
	}
	if err := checkWaitLabels(ctx, dir, id, added, kept); err != nil {
		return err
	}
	if slices.Contains(added, beads.HoldLabel) || slices.Contains(added, beads.ParkedLabel) {
		return pauseRefusal(id)
	}
	if st := flagVals(args, "--status"); len(st) > 0 && st[len(st)-1] == beads.StatusDeferred {
		if err := refusePause(ctx, bc, dir, id, read); err != nil {
			return err
		}
	}
	if slices.Contains(added, beads.NeedsUserLabel) {
		if labels, ok := read(); ok && beads.IsClosingGate(labels) {
			return fmt.Errorf("%s is a Confirm done step: it never gets a card; the sidebar shows Complete on it once everything else is finished", id)
		}
	}
	status := flagVals(args, "--status")
	if len(status) > 0 && status[len(status)-1] == "in_progress" {
		owner := ""
		for _, l := range added {
			if o, ok := strings.CutPrefix(l, beads.OwnerLabelPrefix); ok {
				owner = ownercompat.Normalize(o)
			}
		}
		if owner == "" {
			labels, _ := read()
			for _, l := range labels {
				if o, ok := strings.CutPrefix(l, beads.OwnerLabelPrefix); ok && !slices.Contains(removed, l) {
					owner = ownercompat.Normalize(o)
				}
			}
		}
		if owner == "user" {
			return fmt.Errorf("--state in-progress with --owner user: the board has no way to show the user working, so the row reads State missing. If the user has to act, use --state needs-user with --decision; if an agent does the work, spawn its Operator and make it the owner")
		}
	}
	if slices.Contains(added, beads.BlockedLabel) {
		for _, l := range added {
			if strings.HasPrefix(l, beads.WaitLabelPrefix) || strings.HasPrefix(l, "w:") {
				return nil
			}
		}
		labels, ok := read()
		if !ok || len(beads.WaitsOf(labels)) > 0 {
			return nil // unreadable (let the write through) or already waiting
		}
		deps, err := bc.Deps(ctx, dir, id)
		if err != nil {
			logDoorFailOpen(dir, id, "read deps", err)
			return nil
		}
		for _, d := range deps {
			if d.Type != "blocks" || d.DependsOnID == "" {
				continue
			}
			b, err := bc.Show(ctx, dir, d.DependsOnID)
			if err != nil {
				logDoorFailOpen(dir, id, "read blocker "+d.DependsOnID, err)
				return nil
			}
			if b != nil && b.Status != "closed" && b.Status != "tombstone" && b.Status != beads.StatusDeferred {
				return nil
			}
		}
		return fmt.Errorf("--state blocked on %s names nothing it waits on, so the user's board reads Stalled with nothing able to move it. Add --waiting: ci:#N, review:<login> (with --review <PR>), operator:<running role>, or until:<time>:<what happens then, in plain words>. If nothing holds it yet, use --state not-started", id)
	}
	return nil
}

var (
	failOpenMu   sync.Mutex
	failOpenSeen = map[string]time.Time{}
)

// logDoorFailOpen records, in $COMMANDER_HOME/logs/door-errors.jsonl, a check
// skipped because a read failed, so a bypass is visible. An identical line is
// written at most once a minute.
func logDoorFailOpen(dir, id, what string, err error) {
	key := dir + "|" + id + "|" + what + "|" + err.Error()
	failOpenMu.Lock()
	if t, ok := failOpenSeen[key]; ok && time.Since(t) < time.Minute {
		failOpenMu.Unlock()
		return
	}
	if len(failOpenSeen) > 1000 {
		clear(failOpenSeen)
	}
	failOpenSeen[key] = time.Now()
	failOpenMu.Unlock()
	appendDoorLog("door-errors.jsonl", map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "dir": dir, "bead": id, "skipped": what, "error": err.Error(), "result": "allowed (fail open)"})
}

// logDoorRefusal appends one line to $COMMANDER_HOME/logs/door-refusals.jsonl.
func logDoorRefusal(dir, role string, ops [][]string, why error) {
	appendDoorLog("door-refusals.jsonl", map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "dir": dir, "role": role, "ops": ops, "reason": why.Error()})
}

func appendDoorLog(name string, rec map[string]any) {
	home := os.Getenv("COMMANDER_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return
		}
		home = filepath.Join(h, ".commander")
	}
	logs := filepath.Join(home, "logs")
	if os.MkdirAll(logs, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logs, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(rec)
	f.Write(append(line, '\n'))
}

// addedLabels lists the labels one op adds (update --add-label, label add,
// create --labels).
func addedLabels(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "update":
		return flagVals(args, "--add-label")
	case "label":
		if len(args) >= 4 && args[1] == "add" {
			return args[3:]
		}
	case "create":
		var out []string
		for _, l := range flagVals(args, "--labels") {
			out = append(out, strings.Split(l, ",")...)
		}
		return out
	}
	return nil
}

// checkRunOnLabels refuses a run-on: label naming a place that is not paired
// (unknown or revoked): a step must not name a Mac nobody paired.
func (s *Server) checkRunOnLabels(ctx context.Context, added []string) error {
	for _, l := range added {
		name, ok := strings.CutPrefix(strings.TrimSpace(l), place.LabelPrefix)
		if !ok {
			continue
		}
		if s.opts.Places == nil {
			return fmt.Errorf("label %s: places are not enabled on this cmdrd", l)
		}
		paired, err := s.opts.Places.Paired(ctx, name)
		if err != nil {
			return fmt.Errorf("label %s: read places: %w", l, err)
		}
		if !paired {
			return fmt.Errorf("label %s: %s is not a paired place (cmdr place list)", l, name)
		}
	}
	return nil
}
