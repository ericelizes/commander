package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ownercompat"
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
		if err := checkOp(ctx, bc, dir, args); err != nil {
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
		return checkCreate(args)
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
	}
	return nil
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
	if beads.IsWorkstream(b.Labels) {
		return fmt.Errorf("%s is a workstream and only the user marks a workstream done: put a card on it asking them to confirm it is done, and leave it open", id)
	}
	if beads.IsClosingGate(b.Labels) {
		return fmt.Errorf("%s is its workstream's Confirm done step: only the user confirms it, on its card", id)
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

func checkCreate(args []string) error {
	var labels []string
	for _, l := range flagVals(args, "--labels") {
		labels = append(labels, strings.Split(l, ",")...)
	}
	if err := checkWaitLabels(labels, func() ([]string, bool) { return nil, true }); err != nil {
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

// checkWaitLabels refuses a waiting:* label that is not a typed wait, a slug
// time label, a timer standing in for a PR review, or a review wait with no PR.
// beadLabels reads the bead's own labels (for its pr: label) only when needed.
func checkWaitLabels(added []string, beadLabels func() ([]string, bool)) error {
	hasPR := func(ls []string) bool {
		for _, l := range ls {
			if strings.HasPrefix(l, "pr:") && l != "pr:" {
				return true
			}
		}
		return false
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
				return fmt.Errorf("wait until:...:%q: the label is what the user reads (\"Waiting for <label>\"), so write it in plain words saying what happens then. If it really waits on something else, wait on that: review:<login>, ci:#N, main-ci:<sha>, prod-tag:<sha>, operator:<running role>, or --state needs-user with --decision", w.Ref)
			}
			if beads.ReviewTimerLabel(w.Ref) {
				return fmt.Errorf("wait until:...:%q: a timer standing in for a PR review leaves the review unseen until it fires. cmdrd watches reviews itself: --waiting review:<login> with --review <N>", w.Ref)
			}
		case beads.WaitReview:
			if hasPR(added) {
				continue
			}
			labels, ok := beadLabels()
			if !ok {
				continue // unreadable bead: let the write through (logged)
			}
			if !hasPR(labels) {
				return fmt.Errorf("wait review:%s names no PR, so cmdrd can never see the review and the wait would never clear. Give the step a pr: label (--review <PR number>)", w.Ref)
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
	if err := checkWaitLabels(added, kept); err != nil {
		return err
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
