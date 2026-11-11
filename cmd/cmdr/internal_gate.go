package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/store"
)

// gateRunFn runs an auto gate's command in dir and returns its combined output.
// Tests replace it.
var gateRunFn gates.RunFn = gates.RunShell

// runInternalGate is `cmdr internal-gate add|apply|check|pass|list|rm|link|unlink|convert|publish|retire-legacy|migrate`:
// first-class gates on a stage bead, each stored as a kind:gate bead
// (internal/gates). internal-close-objective refuses until every one passed.
func runInternalGate(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		emitErr(stderr, "internal-gate: want add, apply, check, pass, list, rm, link, unlink, convert, publish, retire-legacy or migrate")
		return 1
	}
	verb, args := args[0], args[1:]
	fs := flag.NewFlagSet("internal-gate "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug (optional for pass, list and rm: the bead id names it)")
	beadID := fs.String("bead", "", "stage (step) bead id")
	name := fs.String("name", "", "gate name")
	check := fs.String("check", "", "add: a shell command (auto) or the rule to verify (reviewer:/user)")
	verifier := fs.String("verifier", "", "add: auto, operator, reviewer:<login> or user")
	evidence := fs.String("evidence", "", "pass: what shows the gate holds (text or url); with --no-pr (pass, rm, apply): the user's approval of the ship (answer id or decision)")
	noPR := fs.String("no-pr", "", "pass, rm, apply: why this ship has no PR (15+ chars); lets a merge gate with no pr: label and no review block pass or go, recorded on the gate. Owning Manager, Commander or the user")
	reason := fs.String("reason", "", "unlink: why the edge is obsolete (recorded on the gate); add: why the user must be the verifier (a genuine decision: prod merge, outward message, spend, personal or regulated data egress, pilot go/no-go)")
	template := fs.String("template", "", "apply: "+strings.Join(gateTemplates, "|")+" (the stage single standard gate)")
	repo := fs.String("repo", "", "apply: owner/repo of the PRs")
	prs := fs.String("pr", "", "apply: PR numbers, comma separated (stacks in merge order)")
	reviewers := fs.String("reviewers", "", "apply pr-approved: reviewer logins, comma separated")
	liveCheck := fs.String("live-check", "", "apply live: a runnable check (else an Operator wires one)")
	by := fs.String("by", "", "pass: who passed it (default $USER)")
	asJSON := fs.Bool("json", false, "list: JSON output")
	after := fs.String("after", "", "link: comma-separated bead ids --bead waits on")
	before := fs.String("before", "", "link: comma-separated bead ids that wait on --bead")
	parent := fs.String("parent", "", "publish: existing parent of the guarded stage")
	repairWaiters := fs.String("repair-ancestor-waits", "", "publish: explicit comma-separated redundant strict ancestors; requires --reason, preserves guarded leaf wait")
	replacement := fs.String("replacement", "", "retire-legacy: exact genuine open user gate")
	dryRun := fs.Bool("dry-run", false, "publish/retire-legacy: validate fresh evidence without writing")
	approver := fs.String("approver", "", "convert: who approves the gate, comma-separated (user, a GitHub login, a name)")
	criteria := fs.String("criteria", "", "convert: what counts as passed, in one sentence")
	waiter := fs.String("waiter", "", "unlink: comma-separated superseded steps whose wait on the gate --bead is removed (reopening an unlinked step does not restore its wait; link it again with cmdr internal-gate link)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *replacement != "" && verb != "retire-legacy" {
		emitErr(stderr, "--replacement is only supported for retire-legacy")
		return 1
	}
	if *noPR != "" && verb != "pass" && verb != "rm" && verb != "apply" {
		emitErr(stderr, "--no-pr is only supported for pass, rm and apply")
		return 1
	}
	if *repairWaiters != "" && verb != "publish" {
		emitErr(stderr, "--repair-ancestor-waits is only supported for publish")
		return 1
	}
	if verb == "migrate" {
		return runGateMigrate(stdout, stderr)
	}
	if *beadID == "" || (verb != "retire-legacy" && verb != "publish" && verb != "list" && verb != "check" && verb != "apply" && verb != "link" && verb != "unlink" && verb != "convert" && *name == "") {
		emitErr(stderr, "missing required flag(s): --bead, and --name (except for publish, retire-legacy, list, check, apply, link and unlink)")
		return 1
	}
	if (verb == "retire-legacy" || verb == "publish" || verb == "add" || verb == "check" || verb == "apply" || verb == "link" || verb == "unlink" || verb == "convert") && *projectName == "" {
		emitErr(stderr, "missing required flag: --project")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()

	var out any
	switch verb {
	case "retire-legacy":
		err = gateRetireLegacy(ctx, st, *projectName, *beadID, *replacement, *reason, *dryRun)
		out = map[string]any{"bead_id": *beadID, "replacement": *replacement, "dry_run": *dryRun}
	case "publish":
		p, e := st.GetProjectByName(ctx, *projectName)
		if e != nil || p == nil {
			err = fmt.Errorf("project %q not added", *projectName)
		} else {
			if *repairWaiters != "" {
				err = gatePublishRepair(ctx, p.Path, *beadID, *parent, splitIDs(*repairWaiters), *reason, *dryRun)
			} else {
				err = gatePublish(ctx, p.Path, *beadID, *parent, *dryRun)
			}
			out = map[string]any{"gate_bead": *beadID, "parent": *parent, "dry_run": *dryRun}
			if *repairWaiters != "" {
				out.(map[string]any)["repair_ancestor_waits"] = splitIDs(*repairWaiters)
			}
		}
	case "add":
		*verifier = ownercompat.Normalize(*verifier)
		out, err = gateAdd(ctx, st, *projectName, *beadID, store.Gate{Name: *name, Check: *check, Verifier: *verifier}, *reason)
	case "apply":
		out, err = gateApply(ctx, st, *projectName, *beadID, *template, templateArgs{Repo: *repo, PRs: splitList(*prs), Reviewers: splitList(*reviewers), LiveCheck: *liveCheck}, *noPR, *evidence)
	case "check":
		out, err = gateCheck(ctx, st, *projectName, *beadID, *name)
	case "pass":
		out, err = gatePass(ctx, st, *projectName, *beadID, *name, *evidence, *by, *noPR)
	case "rm":
		out, err = gateRemove(ctx, st, *projectName, *beadID, *name, *noPR, *evidence)
	case "link":
		out, err = gateLink(ctx, st, *projectName, *beadID, splitIDs(*after), splitIDs(*before))
	case "convert":
		out, err = gateConvert(ctx, st, *projectName, *beadID, *approver, *criteria)
	case "unlink":
		out, err = gateUnlink(ctx, st, *projectName, *beadID, splitIDs(*waiter), *reason)
	case "list":
		var gs []store.Gate
		gs, err = gatesOf(ctx, st, *projectName, *beadID)
		if err == nil && !*asJSON {
			for _, g := range gs {
				fmt.Fprintf(stdout, "%-8s %-20s %s\n", g.Status, g.Name, g.Verifier)
			}
			return 0
		}
		out = map[string]any{"bead_id": *beadID, "gates": nonNilGates(gs)}
	default:
		err = fmt.Errorf("internal-gate: unknown verb %q (want add, apply, check, pass, list, rm, link, unlink, convert, publish, retire-legacy or migrate)", verb)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// gateLink writes the waits of an existing gate or track (or plain) bead, the
// edges internal-dep refuses: it waits on after, and before waits on it. Same
// cycle checks as placing a new gate with internal-create-objective.
func gateLink(ctx context.Context, st *store.Store, projectName, id string, after, before []string) (any, error) {
	if len(after)+len(before) == 0 {
		return nil, errors.New("link: name the waits with --after and/or --before")
	}
	p, err := st.GetProjectByName(ctx, projectName)
	if err != nil || p == nil {
		return nil, fmt.Errorf("project %q not added", projectName)
	}
	bc := newBeadsClientFn()
	if b, err := bc.Show(ctx, p.Path, id); err != nil {
		return nil, fmt.Errorf("could not read %s: %w", id, err)
	} else if b == nil {
		return nil, fmt.Errorf("no bead %s", id)
	}
	if err := checkGateLinksAcyclic(ctx, bc, p.Path, after, before); err != nil {
		return nil, err
	}
	refuseCycle := func(waiter, on string) error {
		if waiter == on || dependsOn(ctx, bc, p.Path, on, waiter) {
			return fmt.Errorf("%s cannot wait on %s: %s already waits on %s (or they are the same bead), so the edge would be a cycle and neither could ever start", waiter, on, on, waiter)
		}
		return nil
	}
	for _, a := range after {
		if err := refuseCycle(id, a); err != nil {
			return nil, err
		}
	}
	for _, b := range before {
		if err := refuseCycle(b, id); err != nil {
			return nil, err
		}
	}
	for _, a := range after {
		if err := writeLink(ctx, bc, p.Path, id, a); err != nil {
			return nil, fmt.Errorf("making %s wait for %s failed: %w", id, a, err)
		}
	}
	for _, b := range before {
		if err := writeLink(ctx, bc, p.Path, b, id); err != nil {
			return nil, fmt.Errorf("making %s wait for %s failed: %w", b, id, err)
		}
	}
	return map[string]any{"bead_id": id, "after": after, "before": before}, nil
}

func nonNilGates(gs []store.Gate) []store.Gate {
	if gs == nil {
		return []store.Gate{}
	}
	return gs
}

// gateProject resolves --project, or finds the one project whose gate beads
// name beadID as their stage. Any gates-table rows left in the project move
// into beads first, so every verb sees the same gates.
func gateProject(ctx context.Context, st *store.Store, projectName, beadID string) (store.Project, error) {
	bc := newBeadsClientFn()
	if projectName != "" {
		p, err := st.GetProjectByName(ctx, projectName)
		if err != nil || p == nil {
			return store.Project{}, fmt.Errorf("project %q not added", projectName)
		}
		if _, err := gates.Migrate(ctx, st, bc, *p); err != nil {
			return store.Project{}, err
		}
		return *p, nil
	}
	ps, err := st.ListProjects(ctx)
	if err != nil {
		return store.Project{}, err
	}
	for _, p := range ps {
		if _, err := gates.Migrate(ctx, st, bc, p); err != nil {
			return store.Project{}, err
		}
		ns, err := gates.ForStageFresh(ctx, bc, p.Path, p.ID, beadID)
		if err != nil {
			return store.Project{}, err
		}
		if len(ns) > 0 {
			return p, nil
		}
	}
	return store.Project{}, fmt.Errorf("%s: %w", beadID, errNoGates)
}

// errNoGates: no project has gates on the stage (list reads it as an empty list).
var errNoGates = errors.New("has no gates (pass --project to be explicit)")

// stageGates reads fresh evidence for check, pass, close and mutation decisions.
// The CLI list shares this action path; snapshot display uses gates.All instead.
func stageGates(ctx context.Context, bc beads.Client, p store.Project, beadID string) ([]gates.Node, error) {
	return gates.ForStageFresh(ctx, bc, p.Path, p.ID, beadID)
}

func gatesOf(ctx context.Context, st *store.Store, projectName, beadID string) ([]store.Gate, error) {
	p, err := gateProject(ctx, st, projectName, beadID)
	if errors.Is(err, errNoGates) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ns, err := stageGates(ctx, newBeadsClientFn(), p, beadID)
	if err != nil {
		return nil, err
	}
	return gateValues(ns), nil
}

func gateValues(ns []gates.Node) []store.Gate {
	var out []store.Gate
	for _, n := range ns {
		out = append(out, n.Gate)
	}
	return out
}

// runGateMigrate is `internal-gate migrate`: every gates-table row of every
// project becomes a gate bead. Safe to re-run; failed rows stay and are listed.
func runGateMigrate(stdout, stderr *os.File) int {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()
	n, migrateErr := gates.MigrateAll(context.Background(), st, newBeadsClientFn())
	result := map[string]any{"migrated": n}
	if migrateErr != nil {
		result["failures"] = strings.Split(migrateErr.Error(), "\n")
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	if migrateErr != nil {
		emitErr(stderr, migrateErr.Error())
		return 1
	}
	return 0
}

func gateAdd(ctx context.Context, st *store.Store, projectName, beadID string, g store.Gate, reason string) (any, error) {
	p, err := gateProject(ctx, st, projectName, beadID)
	if err != nil {
		return nil, err
	}
	bc := newBeadsClientFn()
	g.ProjectID, g.BeadID = p.ID, beadID
	var warnings []string
	// A gate must catch something real that finishing the work would not show:
	// at most 2 per stage (replacing a gate of the same name does not count).
	existing, err := stageGates(ctx, bc, p, beadID)
	if err != nil {
		return nil, err
	}
	others, replacing := 0, false
	for _, e := range existing {
		if e.Gate.Name != g.Name {
			others++
		} else {
			replacing = true
		}
	}
	if others >= maxGatesPerStage {
		return nil, fmt.Errorf("%s already has %d gates (max %d per stage): the stage is the gate. Use internal-gate apply --template <spec|pr-ready|reviewed|pr-approved|merged|live|pilot|investigation|outward|spend> (it replaces the stage gate), or remove the gate first (internal-gate rm)", beadID, others, maxGatesPerStage)
	}
	if g.Verifier == store.VerifierUser && strings.TrimSpace(reason) == "" {
		warnings = append(warnings, "verifier user is only for genuine decisions (merging to prod, outward messages, spend, personal or regulated data egress, pilot go/no-go); never for confirm scope, approve deploy, accept writeup or confirm smoke test. Add --reason \"<why only the user can decide>\" or use reviewer:<login>/operator")
	}
	if g.Verifier == store.VerifierAuto {
		if warnings, err = validateAutoCheck(g.Check); err != nil {
			return nil, err
		}
	}
	if err := checkStandingDecisions(ctx, bc, p.Path, beadID, g.Name, g.Check); err != nil {
		return nil, err
	}
	var proposed *proposal
	if manager := proposingManager(); manager != "" && !replacing && workflowApproved(ctx, bc, p.Path, beadID) {
		kind := beads.ProposedGate
		if reviewTitle.MatchString(g.Name) {
			kind = beads.ProposedReview
		}
		proposed = &proposal{kind: kind, detail: fmt.Sprintf("gate %q (verifier %s) on stage %s, after the workflow was approved: %s", g.Name, g.Verifier, beadID, g.Check)}
	}
	gateID, err := gates.Upsert(ctx, bc, p.Path, g)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"project": p.Name, "bead_id": beadID, "gate": g.Name, "gate_bead": gateID, "verifier": g.Verifier, "status": store.GateOpen}
	if proposed != nil {
		if err := bc.SetLabel(ctx, p.Path, gateID, proposed.label(time.Now()), true); err != nil {
			return nil, fmt.Errorf("gate %s added, but marking it proposed failed: %w", gateID, err)
		}
		announceProposal(ctx, bc, p.Path, p.Name, gateID, proposingManager(), *proposed, os.Stderr)
		out["proposed"] = proposed.kind
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out, nil
}

// maxGatesPerStage caps gates on one stage: the stage is the gate (standard
// lifecycle, see internal_gate_templates.go).
var maxGatesPerStage = 1

// shellBuiltins start a check without being on PATH.
var shellBuiltins = map[string]bool{"[": true, "[[": true, "test": true, "cd": true, "true": true, "false": true, "!": true, "(": true, "{": true,
	"if": true, "for": true, "while": true, "case": true, "exit": true, "export": true, "set": true, "echo": true, "eval": true, ":": true, "command": true, "exec": true}

// validateAutoCheck rejects a check that cannot run (not valid shell) or points
// at an Operator worktree, and warns when it reads as prose rather than a command.
func validateAutoCheck(check string) (warnings []string, err error) {
	check = strings.TrimSpace(check)
	if out, shErr := exec.Command("sh", "-n", "-c", check).CombinedOutput(); shErr != nil {
		return nil, fmt.Errorf("--check is not valid shell (sh -n): %s", strings.TrimSpace(string(out)))
	}
	if strings.Contains(check, ".worktrees/") || strings.Contains(check, ".worktrees ") || strings.HasSuffix(check, ".worktrees") || strings.Contains(check, ".worktrees.noindex") {
		return nil, fmt.Errorf("--check references an Operator worktree (.worktrees/...), which is removed once the Operator finishes: check the PR or branch with gh instead (gh pr checks <n>, gh api repos/<o>/<r>/...), or use --verifier operator")
	}
	words := strings.Fields(check)
	if len(words) >= 4 {
		first := words[0]
		if _, lookErr := exec.LookPath(first); lookErr != nil && !shellBuiltins[first] && !strings.ContainsAny(first, "/=$") {
			warnings = append(warnings, fmt.Sprintf("--check reads as prose, not a command (%q is not on PATH): auto gates run it with sh -c; use --verifier operator|user|reviewer:<login> for a rule a person verifies", first))
		}
	}
	return warnings, nil
}

// gateCheckResult is one gate's outcome from `internal-gate check`.
type gateCheckResult struct {
	Name     string `json:"name"`
	Verifier string `json:"verifier"`
	Status   string `json:"status"`
	Evidence string `json:"evidence,omitempty"`
}

// gateCheck runs the auto gates (all, or the one named). Gates a person verifies
// stay as they are and get a matching waiting record, so cmdrd wakes the Manager.
func gateCheck(ctx context.Context, st *store.Store, projectName, beadID, only string) (any, error) {
	project, err := gateProject(ctx, st, projectName, beadID)
	if err != nil {
		return nil, err
	}
	bc := newBeadsClientFn()
	ns, err := stageGates(ctx, bc, project, beadID)
	if err != nil {
		return nil, err
	}
	results := []gateCheckResult{}
	for _, n := range ns {
		g := n.Gate
		if only != "" && g.Name != only {
			continue
		}
		if g.Verifier != store.VerifierAuto {
			if g.Status != store.GatePassed && g.Verifier != store.VerifierOperator {
				if err := ensureGateWait(ctx, bc, project.Path, beadID, g.Verifier, time.Now()); err != nil {
					return nil, err
				}
			}
			results = append(results, gateCheckResult{Name: g.Name, Verifier: g.Verifier, Status: g.Status, Evidence: g.Evidence})
			continue
		}
		if beads.PostgresConfigured() {
			result, err := beads.CheckAutomaticGate(ctx, project.Path, n.ID)
			if err != nil {
				return nil, err
			}
			results = append(results, gateCheckResult{Name: g.Name, Verifier: g.Verifier, Status: result.Status, Evidence: result.Evidence})
			continue
		}
		now := time.Now()
		status, evidence := gates.EvalAuto(ctx, gateRunFn, project.Path, g.Check)
		if status == store.GatePassed {
			if err := checkWorkflowClose(ctx, bc, project.Path, project.Name, n.ID); err != nil {
				return nil, err
			}
			if err := requireGateClosesHeld(ctx, bc, project.Path, n.ID, "internal-gate check"); err != nil {
				return nil, err
			}
		}
		if err := gates.SetResult(ctx, bc, project.Path, n, status, evidence, "auto", now); err != nil {
			return nil, err
		}
		results = append(results, gateCheckResult{Name: g.Name, Verifier: g.Verifier, Status: status, Evidence: evidence})
	}
	if only != "" && len(results) == 0 {
		return nil, fmt.Errorf("no gate %q on %s", only, beadID)
	}
	return map[string]any{"project": project.Name, "bead_id": beadID, "gates": results}, nil
}

// ensureGateWait makes sure the bead records a wait on the gate's verifier,
// keeping the time of one already there.
func ensureGateWait(ctx context.Context, bc beads.Client, dir, beadID, verifier string, now time.Time) error {
	arg := verifier
	if login, ok := strings.CutPrefix(verifier, store.VerifierReviewerPrefix); ok {
		arg = beads.WaitReview + ":" + login
	}
	w, err := beads.ParseWait(arg)
	if err != nil {
		return err
	}
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return err
	}
	if b != nil {
		for _, existing := range beads.WaitsOf(b.Labels) {
			if existing.Kind == w.Kind && existing.Ref == w.Ref {
				return nil
			}
		}
	}
	w.Since = now
	return bc.SetLabel(ctx, dir, beadID, w.Label(), true)
}

func gatePass(ctx context.Context, st *store.Store, projectName, beadID, name, evidence, by, noPR string) (any, error) {
	if strings.TrimSpace(evidence) == "" {
		return nil, fmt.Errorf("passing a gate needs --evidence: what shows it holds (text or url)")
	}
	p, err := gateProject(ctx, st, projectName, beadID)
	if err != nil {
		return nil, err
	}
	bc := newBeadsClientFn()
	waiver, err := newNoPRWaiver(ctx, bc, p.Path, beadID, noPR, evidence)
	if err != nil {
		return nil, err
	}
	ns, err := stageGates(ctx, bc, p, beadID)
	if err != nil {
		return nil, err
	}
	for _, n := range ns {
		g := n.Gate
		if g.Name != name {
			continue
		}
		if g.Verifier == store.VerifierAuto {
			return nil, fmt.Errorf("gate %q is auto: run `cmdr internal-gate check` instead of passing it by hand", name)
		}
		if err := requireGateClosesHeld(ctx, bc, p.Path, n.ID, "internal-gate pass "+name); err != nil {
			return nil, err
		}
		if err := checkWorkflowCloseWaiving(ctx, bc, p.Path, p.Name, n.ID, waiver); err != nil {
			return nil, err
		}
		ctx, err = noPRCompletionContext(ctx, bc, p.Path, n.ID, waiver)
		if err != nil {
			return nil, err
		}
		if by == "" {
			by = os.Getenv("USER")
		}
		if by == "" {
			by = g.Verifier
		}
		if err := gates.SetResult(ctx, bc, p.Path, n, store.GatePassed, strings.TrimSpace(evidence), by, time.Now()); err != nil {
			return nil, err
		}
		return map[string]any{"bead_id": beadID, "gate": name, "gate_bead": n.ID, "status": store.GatePassed, "by": by}, nil
	}
	return nil, fmt.Errorf("no gate %q on %s", name, beadID)
}

func gateRemove(ctx context.Context, st *store.Store, projectName, beadID, name, noPR, evidence string) (any, error) {
	p, err := gateProject(ctx, st, projectName, beadID)
	if err != nil {
		return nil, err
	}
	bc := newBeadsClientFn()
	waiver, err := newNoPRWaiver(ctx, bc, p.Path, beadID, noPR, evidence)
	if err != nil {
		return nil, err
	}
	ns, err := stageGates(ctx, bc, p, beadID)
	if err != nil {
		return nil, err
	}
	// Retiring a gate ends it as surely as passing it: a merge gate goes through the same check.
	for _, n := range ns {
		if n.Gate.Name == name {
			if err := checkWorkflowCloseWaiving(ctx, bc, p.Path, p.Name, n.ID, waiver); err != nil {
				return nil, err
			}
			if err := requireGateClosesHeld(ctx, bc, p.Path, n.ID, "internal-gate rm "+name); err != nil {
				return nil, err
			}
			ctx, err = noPRCompletionContext(ctx, bc, p.Path, n.ID, waiver)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := gates.Remove(ctx, bc, p.Path, p.ID, beadID, name); err != nil {
		return nil, err
	}
	return map[string]any{"bead_id": beadID, "gate": name, "removed": true}, nil
}

// requireGatesPassed refuses to close a stage while any of its gates is not
// passed with evidence, naming each one.
func requireGatesPassed(ctx context.Context, projectName, beadID string) error {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return err
	}
	defer cleanup()
	project, err := st.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		return nil // no state for the project: nothing to enforce
	}
	if _, err := gates.Migrate(ctx, st, newBeadsClientFn(), *project); err != nil {
		return err
	}
	ns, err := stageGates(ctx, newBeadsClientFn(), *project, beadID)
	if err != nil {
		return err
	}
	un := store.UnpassedGates(gateValues(ns))
	if len(un) == 0 {
		return nil
	}
	var parts []string
	for _, g := range un {
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", g.Name, g.Verifier, g.Status))
	}
	return fmt.Errorf("%s has gates that have not passed: %s. Run `cmdr internal-gate check --project %s --bead %s` for auto gates; others are passed with `internal-gate pass --bead %s --name <gate> --evidence <text>`",
		beadID, strings.Join(parts, ", "), projectName, beadID, beadID)
}

// gatePublish applies the same ownership boundary as unlink before any write.
func gatePublish(ctx context.Context, dir, id, parent string, dryRun bool) error {
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator(role) {
		return fmt.Errorf("refused: Operators do not publish approval cards; ask the owning Manager")
	}
	if beads.PostgresConfigured() {
		return beads.PublishViaService(ctx, dir, id, parent, nil, "", dryRun)
	}
	bc := freshReader(newBeadsClientFn())
	owners, err := ownerRoles(ctx, bc, dir, parent)
	if err != nil {
		return err
	}
	if role != "" && role != "commander" && !roleAmong(role, owners) {
		return fmt.Errorf("refused: only %s, Commander or the user may publish this card", owners[0])
	}
	return gates.PublishPlan(ctx, bc, dir, id, parent, dryRun)
}

func gatePublishRepair(ctx context.Context, dir, id, parent string, waiters []string, reason string, dry bool) error {
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator(role) {
		return fmt.Errorf("refused: Operators do not publish approval cards; ask the owning Manager")
	}
	if beads.PostgresConfigured() {
		return beads.PublishViaService(ctx, dir, id, parent, waiters, reason, dry)
	}
	return gates.RepairPublication(ctx, freshReader(newBeadsClientFn()), dir, id, parent, waiters, reason, role, dry)
}

// requireGateClosesHeld refuses an agent's ending of a gate (pass, retire, auto check) that would leave the work it
// releases with no holder, judged on fresh reads of the gate's closure.
// The user's (no role) is never refused: the released work gets the Manager's receipt before
// the gate ends (custody, make before break).
func requireGateClosesHeld(ctx context.Context, bc beads.Client, dir, gateID, verb string) error {
	owed, err := requireHeldAfter(ctx, bc, dir, gateID, verb+" (it closes "+gateID+")", holdFlags+"; write it first with cmdr internal-set-objective on the released step",
		map[string]holding.Edit{gateID: func(b *holding.Bead) { b.Status = holding.StatusClosed }})
	if err != nil {
		return err
	}
	_, err = owed.settle(ctx, gateID)
	return err
}
