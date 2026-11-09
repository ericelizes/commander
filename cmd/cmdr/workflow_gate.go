package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
)

var greenlightGateRE = regexp.MustCompile(`(?i)^greenlight\b`)

// firstGateRE matches the titles of a scope's first approval gate: a "workflow
// approved" gate (any prefix, e.g. "Dev flow: workflow approved"), or the
// workflow / spec / approach / route gate that plays it.
var firstGateRE = regexp.MustCompile(`(?i)workflow approved|^(workflow|spec|approach|route)\b`)

func isClosedBead(b beads.Bead) bool {
	return strings.Contains(strings.ToLower(b.Status), "closed")
}

func isDeferredBead(b beads.Bead) bool {
	return strings.Contains(strings.ToLower(b.Status), "deferred")
}

func isGateBead(b beads.Bead) bool {
	return slices.Contains(b.Labels, beads.GateLabel) || slices.Contains(b.Labels, "kind:gate")
}

func isRetiredBead(b beads.Bead) bool {
	return slices.Contains(b.Labels, "retired") || slices.Contains(b.Labels, "superseded")
}

// workflowGateScope is the bead whose first gate covers id: the nearest track
// above it, else the workstream (chain = Lineage, id first). "" when id is
// itself the workstream: nothing is above it.
func workflowGateScope(chain []string, all map[string]beads.Bead) string {
	if len(chain) < 2 {
		return ""
	}
	for _, id := range chain[1 : len(chain)-1] {
		if beads.IsWorkstream(all[id].Labels) {
			return id
		}
	}
	return chain[len(chain)-1]
}

// isFirstGateCandidate: the title (or a user-approved Greenlight) of a scope's
// first approval gate.
func isFirstGateCandidate(b beads.Bead) bool {
	title := strings.TrimSpace(b.Title)
	userGreenlight := greenlightGateRE.MatchString(title) && slices.ContainsFunc(b.Labels, func(label string) bool {
		who, ok := strings.CutPrefix(label, beads.ApproverLabelPrefix)
		return ok && ownercompat.Normalize(who) == "user"
	})
	return firstGateRE.MatchString(title) || userGreenlight
}

// isBusyRead: the read was killed, timed out or hit the db lock (not a real
// answer from br).
func isBusyRead(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, w := range []string{"signal: killed", "deadline exceeded", "busy", "locked"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// readOnceMore runs read, and once more when it was killed or timed out while
// the caller's context is still alive (the br queue spaces the second try).
func readOnceMore[T any](ctx context.Context, read func() (T, error)) (T, error) {
	v, err := read()
	if err != nil && ctx.Err() == nil && isBusyRead(err) {
		return read()
	}
	return v, err
}

// gateReadError is the error for a gate read that failed: a busy db reads as
// an instruction to retry, never as raw timeout text.
func gateReadError(id string, err error) error {
	if isBusyRead(err) {
		log.Printf("workflow gate of %s: %v", id, err)
		return fmt.Errorf("could not read the workflow gate of %s (busy), retry in a minute", id)
	}
	return fmt.Errorf("check the workflow gate of %s: %w", id, err)
}

// firstGateOf finds scope's first approval gate among its child gate beads
// (parents maps a gate id to its parent). closed = an approved one exists;
// else open is the id of one still open ("" = none exists).
func firstGateOf(scope string, all []beads.Bead, parents map[string]string) (closed bool, open string) {
	for _, b := range all {
		if !isGateBead(b) || isRetiredBead(b) || parents[b.ID] != scope || !isFirstGateCandidate(b) {
			continue
		}
		if isClosedBead(b) || b.ID == closingBead {
			return true, ""
		}
		if open == "" {
			open = b.ID
		}
	}
	return false, open
}

// workflowGateMessage is the refusal text for a scope without an approved gate.
func workflowGateMessage(scope, open string) string {
	if open != "" {
		return fmt.Sprintf("workflow not approved: gate %s of %s is still open: the user approves it (internal-resolve-needs --action decide --choice Approve), or spawn the Operator that writes the workflow with --writes-workflow", open, scope)
	}
	return fmt.Sprintf("workflow not approved: %s has no Workflow approved gate: create it with: cmdr internal-create-objective --parent %s --title 'Workflow approved' --gate --approver user --before <stages> --criteria '<what counts as approved, one sentence>'", scope, scope)
}

// workflowGateEnforced: COMMANDER_WORKFLOW_GATE=enforce makes a missing or open
// first gate a refusal; the default only warns (until an install flips it).
func workflowGateEnforced() bool {
	return os.Getenv("COMMANDER_WORKFLOW_GATE") == "enforce"
}

// requireWorkflowGate checks work on id, a bead under a workstream or track,
// against that scope's first approval gate. A missing or open gate is an error
// when enforced, else a warning on stderr plus a line in
// $COMMANDER_HOME/logs/workflow-gate-warnings.jsonl and a nil result. A step
// an approved user gate covers on the board (coveringApproval) needs no first
// gate of its own: the approval carries over. Exempt:
// gate beads, operational beads, closed/deferred beads, the workstream itself,
// and a scope that is itself closed/deferred (finished history).
func requireWorkflowGate(ctx context.Context, bc beads.Client, dir, id string, stderr io.Writer) error {
	scope, open, err := workflowGateProblem(ctx, bc, dir, id)
	if err != nil {
		if workflowGateEnforced() {
			return err
		}
		fmt.Fprintf(stderr, "warning: %v; continuing (the workflow gate is warn-only)\n", err)
		logWorkflowGateWarning(id, "", "", err.Error())
		return nil
	}
	if scope == "" || coveringApproval(id) != "" {
		return nil
	}
	msg := workflowGateMessage(scope, open)
	if workflowGateEnforced() {
		return fmt.Errorf("%s", msg)
	}
	fmt.Fprintf(stderr, "warning (will refuse after the install): %s\n", msg)
	logWorkflowGateWarning(id, scope, open, msg)
	return nil
}

// logWorkflowGateWarning appends one JSON line; best effort.
func logWorkflowGateWarning(bead, scope, gate, reason string) {
	dir := filepath.Join(commanderHomeDir(), "logs")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "workflow-gate-warnings.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339), "bead": bead, "scope": scope, "gate": gate, "reason": reason,
	})
}

// workflowGateProblem returns the scope whose first gate is missing (open "")
// or still open (open = its id); scope "" = nothing to report.
func workflowGateProblem(ctx context.Context, bc beads.Client, dir, id string) (scope, open string, err error) {
	all, err := readOnceMore(ctx, func() ([]beads.Bead, error) {
		return bc.List(ctx, dir, beads.ListOptions{Status: "all"})
	})
	if err != nil {
		return "", "", gateReadError(id, err)
	}
	byID := make(map[string]beads.Bead, len(all))
	for _, b := range all {
		byID[b.ID] = b
	}
	if self, ok := byID[id]; ok && (isGateBead(self) || isClosedBead(self) || isDeferredBead(self) ||
		slices.Contains(self.Labels, "kind:operational")) {
		return "", "", nil
	}
	chain := beads.Lineage(ctx, bc, dir, nil, id)
	scope = workflowGateScope(chain, byID)
	if scope == "" {
		return "", "", nil
	}
	if s, ok := byID[scope]; ok && (isClosedBead(s) || isDeferredBead(s)) {
		return "", "", nil
	}
	// Only a gate that can be the scope's first gate needs its parent read.
	var gateIDs []string
	for _, b := range all {
		if isGateBead(b) && !isRetiredBead(b) && isFirstGateCandidate(b) {
			gateIDs = append(gateIDs, b.ID)
		}
	}
	parents, err := readOnceMore(ctx, func() (map[string]string, error) {
		return parentsOf(ctx, bc, dir, gateIDs)
	})
	if err != nil {
		return "", "", gateReadError(id, err)
	}
	closed, open := firstGateOf(scope, all, parents)
	if closed {
		return "", "", nil
	}
	return scope, open, nil
}

// parentsOf maps each id to its parent by parent-child edge, in one batched read.
func parentsOf(ctx context.Context, bc beads.Client, dir string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	details, err := beads.FreshDetails(ctx, bc, dir, ids)
	if err != nil {
		return nil, err
	}
	for id, d := range details {
		out[id] = d.Parent
	}
	return out, nil
}

// workflowGap is one open workstream or track the gate rule now blocks.
type workflowGap struct {
	Project string `json:"project"`
	Bead    string `json:"bead"`
	Title   string `json:"title"`
	Kind    string `json:"kind"` // workstream or track
	Gate    string `json:"gate,omitempty"`
	Problem string `json:"problem"` // missing or open
}

// workflowGaps lists every open workstream and track of a project whose first
// approval gate is missing or still open. Read-only.
func workflowGaps(ctx context.Context, bc beads.Client, project, dir string) ([]workflowGap, error) {
	all, err := bc.List(ctx, dir, beads.ListOptions{Status: "all"})
	if err != nil {
		return nil, err
	}
	var scopes, ids []string
	for _, b := range all {
		ids = append(ids, b.ID)
		if isGateBead(b) {
			continue
		}
		if slices.Contains(b.Labels, "kind:objective") && !isClosedBead(b) && !isDeferredBead(b) {
			scopes = append(scopes, b.ID)
		}
	}
	parents, err := parentsOf(ctx, bc, dir, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]beads.Bead{}
	for _, b := range all {
		byID[b.ID] = b
	}
	var gaps []workflowGap
	for _, id := range scopes {
		b := byID[id]
		kind := "workstream"
		switch {
		case beads.IsWorkstream(b.Labels) && parents[id] != "":
			kind = "track"
		case parents[id] != "":
			continue // a step, covered by its scope
		}
		closed, open := firstGateOf(id, all, parents)
		if closed {
			continue
		}
		g := workflowGap{Project: project, Bead: id, Title: b.Title, Kind: kind, Gate: open, Problem: "missing"}
		if open != "" {
			g.Problem = "open"
		}
		gaps = append(gaps, g)
	}
	return gaps, nil
}

// runInternalWorkflowGaps: `internal-workflow-gaps [--project p] [--json]`
// lists every open workstream/track (all registered projects without
// --project) that internal-spawn-operator and internal-set-objective --state
// in-progress now refuse, because its Workflow approved gate is missing or open.
// Read-only; there is no backfill.
func runInternalWorkflowGaps(args []string, stdout, stderr *os.File) int {
	var projectName string
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			if i+1 >= len(args) {
				emitErr(stderr, "--project needs a value")
				return 1
			}
			i++
			projectName = args[i]
		case "--json":
			asJSON = true
		default:
			emitErr(stderr, "usage: internal-workflow-gaps [--project <slug>] [--json]")
			return 1
		}
	}
	statePath, err := stateDBPath()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	st, err := store.Open(statePath)
	if err != nil {
		emitErr(stderr, fmt.Sprintf("open state db: %v", err))
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	projects, err := st.ListProjects(ctx)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	bc := newBeadsClientFn()
	gaps := []workflowGap{}
	for _, p := range projects {
		if projectName != "" && p.Name != projectName {
			continue
		}
		g, err := workflowGaps(ctx, bc, p.Name, p.Path)
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s: %v\n", p.Name, err)
			continue
		}
		gaps = append(gaps, g...)
	}
	if asJSON {
		if err := json.NewEncoder(stdout).Encode(gaps); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		return 0
	}
	for _, g := range gaps {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", g.Project, g.Bead, g.Kind, g.Problem, g.Title)
		if g.Gate != "" {
			line += " (gate " + g.Gate + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "%d open workstream/track without an approved Workflow gate\n", len(gaps))
	return 0
}
