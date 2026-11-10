package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// internal-comment and internal-dep are the cmdr verbs for the two bead writes
// agents used to do with bd/br directly (comments add, dep add): the agent
// write guard refuses those, and these go through the beads client and so
// cmdrd's write door.

// beadWriteTimeout bounds a bead-write verb (its reads plus the door call).
// The door's worst case is br's 40s plus settleWait 20s, so 15s/30s budgets
// gave up on writes cmdrd then applied (il9.153.2). A var for tests.
var beadWriteTimeout = 3 * time.Minute

func runInternalComment(args []string, in io.Reader, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-comment", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "bead id to comment on")
	text := fs.String("text", "", "the comment")
	file := fs.String("file", "", "read the comment from this file (- = stdin)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *bead == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	body := *text
	if *file != "" {
		var raw []byte
		var err error
		if *file == "-" {
			raw, err = io.ReadAll(in)
		} else {
			if sharedReportPath(*file) {
				emitErr(stderr, fmt.Sprintf("refused: %s is a shared fixed path, so another Operator's report can land on your bead. Use --text, --file - (stdin), or a private file in your worktree (mktemp -p <worktree>)", *file))
				return 1
			}
			raw, err = os.ReadFile(*file)
		}
		if err != nil {
			emitErr(stderr, fmt.Sprintf("read %s: %v", *file, err))
			return 1
		}
		body = string(raw)
	}
	if strings.TrimSpace(body) == "" {
		emitErr(stderr, "the comment is empty: pass --text or --file")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if strings.HasPrefix(strings.TrimSpace(body), workflowtemplate.AddendumPrefix) {
		emitErr(stderr, "refused: use cmdr workflow add-review-addendum")
		return 1
	}
	// Evidence is written only by a door, after its check ran.
	if strings.HasPrefix(strings.TrimSpace(body), withdrawalPrefix) {
		emitErr(stderr, "refused: withdrawal audit is written only by internal-gate withdraw")
		return 1
	}
	if strings.HasPrefix(strings.TrimSpace(body), legacyRetirementPrefix) {
		emitErr(stderr, "refused: legacy retirement audit is written only by internal-gate retire-legacy")
		return 1
	}
	if strings.HasPrefix(strings.TrimSpace(body), gates.PublicationRepairPrefix) {
		emitErr(stderr, "refused: publication repair audit is written only by internal-gate publish --repair-ancestor-waits")
		return 1
	}
	if strings.HasPrefix(strings.TrimSpace(body), workflowtemplate.BindingPrefix) {
		emitErr(stderr, "refused: use cmdr workflow bind-writers to select writer tasks with validated provenance")
		return 1
	}
	if strings.HasPrefix(strings.TrimSpace(body), workflowtemplate.EvidencePrefix) {
		emitErr(stderr, fmt.Sprintf("refused: a %scomment is evidence that a check ran, and only the completion door writes it. Run cmdr internal-close-objective --project %s --bead %s", workflowtemplate.EvidencePrefix, *projectName, *bead))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	if err := newBeadsClientFn().AddComment(ctx, project.Path, *bead, body); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "commented on %s\n", *bead)
	return 0
}

// sharedReportPath reports whether p (relative paths resolved from the working
// directory) is a bare file directly in a shared temp dir (/tmp, /var/tmp,
// /dev/shm, $TMPDIR, and their real paths, e.g. /private/tmp on macOS): a name
// every Operator may pick, so two runs collide. A private directory made by
// mktemp -d under one of them passes.
func sharedReportPath(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	d := realDir(filepath.Dir(abs))
	for _, shared := range []string{"/tmp", "/var/tmp", "/dev/shm", os.TempDir()} {
		if d == filepath.Clean(shared) || d == realDir(shared) {
			return true
		}
	}
	return false
}

func realDir(d string) string {
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return filepath.Clean(d)
}

func runInternalDep(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-dep", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "expected source project slug")
	targetName := fs.String("target-project", "", "expected registered target project slug (PostgreSQL only)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	rest := fs.Args()
	if *projectName == "" || len(rest) != 3 || (rest[0] != "add" && rest[0] != "rm") {
		emitErr(stderr, "usage: internal-dep --project P add|rm <issue> <depends-on>  (the issue waits on depends-on)")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	bc := newBeadsClientFn()
	targetDir := project.Path
	if *targetName != "" {
		if !beads.PostgresConfigured() {
			emitErr(stderr, "qualified dependencies require PostgreSQL")
			return 1
		}
		target, err := resolveProject(*targetName)
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		targetDir = target.Path
	}
	for i, id := range rest[1:] {
		checkDir := project.Path
		if i == 1 {
			checkDir = targetDir
		}
		if err := checkNotGateEdge(ctx, bc, checkDir, id, "internal-dep "+rest[0], rest[0] == "rm"); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
	}
	if rest[0] == "add" {
		if rest[1] == rest[2] || dependsOn(ctx, bc, targetDir, rest[2], rest[1]) {
			emitErr(stderr, fmt.Sprintf("%s cannot wait on %s: %s already waits on %s (or they are the same bead), so the edge would be a cycle and neither could ever start", rest[1], rest[2], rest[2], rest[1]))
			return 1
		}
	}
	if rest[0] == "rm" {
		plugged, err := workflowtemplate.IsPluggedBlock(ctx, freshReader(bc), project.Path, project.Name, rest[2], rest[1])
		if err != nil {
			emitErr(stderr, fmt.Sprintf("could not read %s to check internal-dep rm (%v); nothing written, retry in a minute", rest[2], err))
			return 1
		}
		if plugged {
			emitErr(stderr, fmt.Sprintf("refused: %s is the review block plugged in front of %s, and removing the edge would let %s close unreviewed. Finish the block; if it is wrong, tell your Manager or Commander (cmdr internal-mail --to <role> --subject ... --body ...)", rest[2], rest[1], rest[1]))
			return 1
		}
		waiter := rest[1]
		owed, err := requireHeldAfter(ctx, bc, project.Path, waiter, "internal-dep rm "+waiter+" "+rest[2], depRmFix,
			map[string]holding.Edit{waiter: func(b *holding.Bead) {
				b.BlockedBy = slices.DeleteFunc(slices.Clone(b.BlockedBy), func(x string) bool { return x == rest[2] })
			}})
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		if _, err := owed.settle(ctx, rest[2]); err != nil {
			emitErr(stderr, err.Error()+"; nothing was removed")
			return 1
		}
		if err := writeDependency(ctx, bc, project.Path, targetDir, *targetName != "", rest[0], rest[1], rest[2]); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		fmt.Fprintf(stdout, "%s no longer waits on %s\n", rest[1], rest[2])
		return 0
	}
	if err := writeDependency(ctx, bc, project.Path, targetDir, *targetName != "", rest[0], rest[1], rest[2]); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "%s now waits on %s\n", rest[1], rest[2])
	return 0
}

// writeDependency keeps legacy Client identities unchanged; only the explicit
// qualified command carries expected project paths to the guarded PG writer.
func writeDependency(ctx context.Context, bc beads.Client, source, target string, qualified bool, op, from, to string) error {
	if qualified {
		return beads.QualifiedDependency(ctx, source, target, op, from, to)
	}
	if op == "rm" {
		return bc.RemoveDep(ctx, source, from, to)
	}
	return bc.DependOn(ctx, source, from, to)
}

// runInternalPhase sets a Task's commander phase (`bd set-state <id> cmdr=<phase>`).
func runInternalPhase(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-phase", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "task bead id")
	phase := fs.String("phase", "", "the commander phase: brainstorming, planning, executing, reviewing, done, blocked, needs-input, idle, working")
	reason := fs.String("reason", "", "why (recorded with the state change)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *bead == "" || strings.TrimSpace(*phase) == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead, --phase")
		return 1
	}
	if !validPhase(strings.TrimSpace(*phase)) {
		emitErr(stderr, fmt.Sprintf("unknown phase %q: use one of %s", strings.TrimSpace(*phase), strings.Join(phases, ", ")))
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	if err := newBeadsClientFn().SetState(ctx, project.Path, *bead, "cmdr", strings.TrimSpace(*phase), *reason); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "%s phase: %s\n", *bead, strings.TrimSpace(*phase))
	return 0
}

// checkNotGateEdge refuses an edge that touches a gate or a track (the stage
// the graph is laid out on): those edges carry the gate chain, so they are made
// by internal-gate / internal-create-objective --before/--after, which check
// the order, not by a bare dependency write. verb names the refused call.
// Add and rm differ for a nested workstream: internal-gate link adds its edge
// but no other agent verb removes one, so removing passes it (a gate or track
// is refused either way, internal-gate has its own removal for a gate).
func checkNotGateEdge(ctx context.Context, bc beads.Client, dir, id, verb string, removing bool) error {
	b, err := bc.Show(ctx, dir, id)
	if err != nil {
		return fmt.Errorf("could not read %s to check %s (%v); retry in a minute", id, verb, err)
	}
	if b == nil {
		return nil
	}
	for _, l := range b.Labels {
		if l == beads.WorkstreamLabel && (b.ParentID == "" || removing) {
			continue // a top-level workstream's edges are not a gate chain
		}
		if l == beads.GateLabel || l == beads.TrackLabel || l == beads.WorkstreamLabel {
			return fmt.Errorf("%s is a %s bead: its edges are the gate chain and are not written one by one with %s. Use cmdr internal-gate (add|rm|apply|link) for gates, cmdr internal-gate unlink --project <project> --bead <gate> --waiter <superseded step> --reason ... to drop a superseded step's wait on a gate, cmdr internal-create-objective --gate --after <ids> --before <ids> to place a gate between work, or cmdr internal-gate link --project <project> --bead <id> --after <ids> to add a nested workstream's edge. Add and rm differ: internal-dep rm still drops a nested workstream's edge, and only a gate or kind:track bead refuses it", id, l, verb)
		}
	}
	return nil
}

// phases are the values internal-phase accepts (the --phase help text).
var phases = []string{"brainstorming", "planning", "executing", "reviewing", "done", "blocked", "needs-input", "idle", "working"}

func validPhase(p string) bool {
	for _, v := range phases {
		if p == v {
			return true
		}
	}
	return false
}

// reservedLabelPrefixes are labels other cmdr verbs own (they carry checks, mail
// or a state machine): internal-label refuses them and names the verb.
var reservedLabelPrefixes = []struct{ prefix, verb string }{
	{"pushed:", "cmdr internal-set-objective --push-reset"},
	{"pushed-first:", "cmdr internal-set-objective --push-reset"},
	{"pushed-last:", "cmdr internal-set-objective --push-reset"},
	{"pushed-flagged", "cmdr internal-set-objective --push-reset"},
	{"waiting:", "cmdr internal-set-objective --waiting"},
	{"until:", "cmdr internal-set-objective --waiting until:..."},
	{"wait-checked:", "cmdrd (it stamps this)"},
	{"parked", "cmdr internal-set-objective --park|--unpark"},
	{"hold", "cmdr internal-set-objective --hold|--no-hold"},
	{"viz:", "cmdr internal-set-objective --viz"},
	{"lane:", "cmdr internal-set-objective --lane"},
	{"max-operators:", "cmdr internal-set-objective --max-operators"},
	{"kind:track", "cmdr internal-set-objective --workstream|--no-workstream or internal-track convert"},
	{"kind:workstream", "cmdr internal-set-objective --workstream|--no-workstream or internal-track convert"},
	{"kind:gate", "cmdr internal-gate add"},
	{"kind:objective", "cmdr internal-create-objective"},
	{"gate", "cmdr internal-gate add|rm"},
	{"verifier:", "cmdr internal-gate add"},
	{"approver:", "cmdr internal-gate add"},
	{"decision:", "cmdr internal-set-objective --state needs-user --decision"},
	{"needs-user", "cmdr internal-set-objective --state needs-user --decision"},
	{ownercompat.LegacyNeedsLabel, "cmdr internal-set-objective --state needs-user --decision"},
	{"owner:", "cmdr internal-set-objective --owner"},
	{"proposed:", "cmdr proposals accept|overrule (Commander)"},
	{"prod-gate:", "cmdr internal-spawn-operator --prod-gate"},
	{"auto-respawn:", "cmdrd"},
	{"crash-respawn:", "cmdrd"},
	{"operator-died", "cmdrd"},
	{"cmdr:", "cmdr internal-phase"},
	{"workflow-", "cmdr workflow plug <template> (the workflow-* labels are written only by it)"},
	{"pr:", "cmdr internal-set-objective --review <pr>"},
	{"review:", "cmdr internal-set-objective --review <pr>"},
	{"superseded", "cmdr internal-close-objective --reason superseded"},
	{beads.WithdrawnLabel, "cmdr internal-gate withdraw"},
	{"retired", "cmdr internal-gate rm (a retired gate) or internal-close-objective --reason superseded"},
	{"wait-nudged", "cmdrd (it stamps this)"},
	{"reviewed", "cmdr internal-gate pass (the reviewed gate)"},
	{"approved", "cmdr internal-resolve-needs / internal-gate pass (an approval is a gate)"},
	{"waiting", "cmdr internal-set-objective --waiting"},
	{"owner", "cmdr internal-set-objective --owner"},
}

func reservedLabel(label string) (verb string, ok bool) {
	l := strings.ToLower(strings.TrimSpace(label))
	if ownercompat.IsLegacyNeeds(strings.TrimSpace(label)) {
		return "cmdr internal-set-objective --state needs-user --decision", true
	}
	for _, r := range reservedLabelPrefixes {
		if l == r.prefix || strings.HasPrefix(l, r.prefix) && (strings.HasSuffix(r.prefix, ":") || strings.HasSuffix(r.prefix, "-") || l[len(r.prefix)] == ':' || l[len(r.prefix)] == '-') {
			return r.verb, true
		}
	}
	return "", false
}

// runInternalLabel adds or removes a plain label (`bd label add|remove`). Labels
// that carry state have their own verbs and are refused here.
func runInternalLabel(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-label", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "bead id")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	rest := fs.Args()
	if *projectName == "" || *bead == "" || len(rest) != 2 || (rest[0] != "add" && rest[0] != "rm") {
		emitErr(stderr, "usage: internal-label --project P --bead B add|rm <label>")
		return 1
	}
	label := strings.TrimSpace(rest[1])
	if strings.HasPrefix(label, "-") {
		emitErr(stderr, fmt.Sprintf("invalid label %q: a label cannot start with '-' (it would be read as a flag)", label))
		return 1
	}
	if label == "" || strings.ContainsAny(label, " \t,.") {
		emitErr(stderr, fmt.Sprintf("invalid label %q (letters, digits, hyphen, underscore, colon only)", label))
		return 1
	}
	if verb, ok := reservedLabel(label); ok {
		emitErr(stderr, fmt.Sprintf("label %q carries state and is not set by hand: use %s", label, verb))
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	on := rest[0] == "add"
	if err := newBeadsClientFn().SetLabel(ctx, project.Path, *bead, label, on); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if on {
		fmt.Fprintf(stdout, "%s: label %s added\n", *bead, label)
	} else {
		fmt.Fprintf(stdout, "%s: label %s removed\n", *bead, label)
	}
	return 0
}

// runInternalUpdate sets the plain fields of a bead (`bd update --title|--description|--notes|--acceptance|--priority`).
func runInternalUpdate(args []string, in io.Reader, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "bead id")
	title := fs.String("title", "", "new title")
	description := fs.String("description", "", "new description (replaces it)")
	descFile := fs.String("description-file", "", "read the description from this file (- = stdin)")
	notes := fs.String("notes", "", "new notes (replaces them)")
	acceptance := fs.String("acceptance", "", "acceptance criteria")
	priority := fs.Int("priority", -1, "priority 0-4 (0 = highest)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *bead == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	var f beads.Fields
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	if set["title"] {
		f.Title = title
	}
	if set["description"] && set["description-file"] {
		emitErr(stderr, "use --description or --description-file, not both")
		return 1
	}
	if set["description"] {
		f.Description = description
	}
	if set["description-file"] {
		var raw []byte
		var err error
		if *descFile == "-" {
			raw, err = io.ReadAll(in)
		} else {
			raw, err = os.ReadFile(*descFile)
		}
		if err != nil {
			emitErr(stderr, fmt.Sprintf("read %s: %v", *descFile, err))
			return 1
		}
		d := string(raw)
		f.Description = &d
	}
	if set["notes"] {
		f.Notes = notes
	}
	if set["acceptance"] {
		f.Acceptance = acceptance
	}
	if set["priority"] {
		if *priority < 0 || *priority > 4 {
			emitErr(stderr, "--priority must be 0-4")
			return 1
		}
		f.Priority = priority
	}
	if f.Empty() {
		emitErr(stderr, "nothing to update: pass --title, --description, --notes, --acceptance or --priority")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	bc := newBeadsClientFn()
	if err := checkUpdateAllowed(ctx, bc, project.Path, *bead, f); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := bc.UpdateFields(ctx, project.Path, *bead, f); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "updated %s\n", *bead)
	return 0
}

// checkUpdateAllowed guards internal-update: a gate bead's description IS its
// check command and its acceptance its criteria, so neither is edited here
// (internal-gate add redefines it with its checks); a retitle may not make a
// plain step read as an approval (those are gates); a closed bead stays as it
// closed.
func checkUpdateAllowed(ctx context.Context, bc beads.Client, dir, id string, f beads.Fields) error {
	b, err := bc.Show(ctx, dir, id)
	if err != nil {
		return fmt.Errorf("could not read %s to check the update (%v); retry in a minute", id, err)
	}
	if b == nil {
		return nil
	}
	if b.Status == "closed" {
		return fmt.Errorf("%s is closed: it is not edited. Reopen it with cmdr internal-set-objective --status open if it needs more work, or comment with cmdr internal-comment", id)
	}
	isGate := hasLabel(b.Labels, beads.GateLabel)
	if isGate && (f.Description != nil || f.Acceptance != nil) {
		return fmt.Errorf("%s is a gate bead: its description is the check and its acceptance the criteria, so they are not edited with internal-update. Redefine the gate with cmdr internal-gate add", id)
	}
	if f.Title != nil && !isGate && looksLikeApproval(*f.Title) {
		return fmt.Errorf("title %q reads as an approval (Greenlight, Approve, Workflow approved, Sign-off...): an approval is a gate, not a plain step. Create it with cmdr internal-create-objective --gate --approver user, or pick a title that is not an approval", *f.Title)
	}
	return nil
}
