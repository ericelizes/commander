package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
	"github.com/example-user/commander/internal/wsmgr"
)

// operatorSpawnDeps is what internal-spawn-operator needs; tests replace it.
type operatorSpawnDeps struct {
	Spawner interface {
		Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error)
	}
	Project      *store.Project
	DefaultModel string
	// CheckCap refuses one more Operator under step when its workstream
	// already runs its cap (spawn.CheckWorkstreamCap). nil = no cap.
	CheckCap func(ctx context.Context, bc beads.Client, project *store.Project, step string) error
	// RunningOn names an Operator running on a task bead ("" = none). nil = none known.
	RunningOn func(ctx context.Context, taskID string) (string, error)
	Cleanup   func()
}

var spawnOperatorDeps = realSpawnOperatorDeps

type internalSpawnOperatorResult struct {
	Project string `json:"project"`
	Step    string `json:"step"`
	TaskID  string `json:"task_bead_id"`
	Branch  string `json:"branch,omitempty"`
	Agent   string `json:"agent"`
	Mail    string `json:"mail_role"`
	// MailAlias is the dashed spelling; internal-mail resolves it to Mail.
	MailAlias string `json:"mail_role_alias,omitempty"`
	// Runtime and Account are what the Operator runs on; Moved says why that
	// is not what it was headed for (a usage limit), omitted when it is.
	Runtime string `json:"runtime,omitempty"`
	Account string `json:"account,omitempty"`
	Moved   string `json:"moved,omitempty"`
}

// runInternalSpawnOperator is how a Manager delegates work: it creates a
// kind:task bead under a step, spawns an Operator for it (own worktree, mail
// role operator-<flat-branch>) and marks the step in progress, owned by that
// Operator. The step reads "● … → operator-x" in the Workstreams panel while
// the Operator is live.
func runInternalSpawnOperator(args []string, stdout, stderr *os.File) int {
	return spawnOperatorAttempt(args, stdout, stderr, false)
}

// spawnRetryAfter is how long a spawn that failed for a passing reason (a busy
// or timed-out bead read, a killed br) waits before its one retry.
var spawnRetryAfter = 20 * time.Second

// transientSpawnFailure: the failure says the beads were busy, not that the
// spawn is wrong, so the same command may well work a little later.
func transientSpawnFailure(msg string) bool {
	return isBusyRead(errors.New(msg)) || strings.Contains(strings.ToLower(msg), "timed out")
}

// spawnStepBackoff is the wait before each retry of one spawn step (3 tries).
var spawnStepBackoff = []time.Duration{5 * time.Second, 15 * time.Second}

// retryStep runs one spawn step, repeating it while br stays busy. A create is
// repeated only for beads.IsBusy (the call never started, so no duplicate); an
// idempotent write (idempotent=true) also for a killed or timed-out br. Steps
// after the task bead exists retry on their own, so a retry continues on the
// bead already made and never leaves a second half task.
// Each try gets its own context (spawnStepTimeout), so a late retry never
// runs on a caller context that has run out meanwhile.
func retryStep(stderr *os.File, what string, idempotent bool, fn func(ctx context.Context) error) error {
	var err error
	for try := 0; ; try++ {
		tctx, cancel := context.WithTimeout(context.Background(), spawnStepTimeout)
		err = fn(tctx)
		cancel()
		if err == nil {
			return nil
		}
		busy := beads.IsBusy(err) || (idempotent && isBusyRead(err))
		if !busy || try >= len(spawnStepBackoff) {
			return err
		}
		fmt.Fprintf(stderr, "%s: br is busy; trying again in %s (%d/%d)\n", what, spawnStepBackoff[try], try+2, len(spawnStepBackoff)+1)
		time.Sleep(spawnStepBackoff[try])
	}
}

// spawnStepTimeout bounds one try of a spawn step: the door's worst case, as
// for the bead-write verbs. Less gives up on a create cmdrd then applies.
var spawnStepTimeout = beadWriteTimeout

func spawnOperatorAttempt(args []string, stdout, stderr *os.File, retried bool) int {
	fs := flag.NewFlagSet("internal-spawn-operator", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	step := fs.String("step", "", "step (objective) bead id the work belongs to")
	title := fs.String("title", "", "task title")
	prompt := fs.String("prompt", "", "task body: what the Operator should do and how to know it's done")
	branch := fs.String("branch", "", "git branch name only (code projects; default op/<slug>-<task>); the Operator's role and worktree are always named from the task bead")
	model := fs.String("model", "", "model id (default: configured operator model)")
	runtime := fs.String("runtime", "", "agent CLI: claude or codex (default: claude; a configured [runtime] codex_operator_share sends that part of new beads to Codex)")
	account := fs.String("account", "", "Claude account (cmdr account list; default: the Manager's account)")
	writesWorkflow := fs.Bool("writes-workflow", false, "the Operator's brief is writing the workflow itself: skip the workflow-approved check")
	asParent := fs.Bool("as-parent", false, "a parent Manager runs an Operator under a nested workstream itself (its Manager is down); recorded on the step")
	prodGate := fs.String("prod-gate", "", "closed user-approved gate bead whose description is the one production action this Operator may take (Managers only, after approving that action)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *step == "" || *title == "" {
		emitErr(stderr, "missing required flag(s): --project, --step, --title")
		return 1
	}
	if *runtime != "" && !chat.ValidRuntime(*runtime) {
		emitErr(stderr, fmt.Sprintf("unknown --runtime %q (claude or codex)", *runtime))
		return 1
	}
	if *account != "" && !claudeacct.Exists(commanderHomeDir(), *account) {
		emitErr(stderr, fmt.Sprintf("unknown --account %q (cmdr account list)", *account))
		return 1
	}
	// Before the task bead is made: a named runtime or account with no usage left.
	if err := spawn.RequestedUsage(*runtime, *account); err != nil {
		emitErr(stderr, fmt.Sprintf("spawn operator: %v", err))
		return 1
	}

	// fail reports a failed spawn: on stderr for the Manager, and by mail to
	// Commander so the failure can never stay inside a Manager's chat.
	// manager is the step's Manager once the project is known; until then the
	// step's dotted id is all there is.
	var bc beads.Client
	projectDir := ""
	manager := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return wsmgr.Role(wsmgr.Owner(ctx, bc, projectDir, *step))
	}
	// left names a task this attempt leaves behind, for the report and its mail.
	left := ""
	report := func(msg string) int {
		msg += left
		emitErr(stderr, msg)
		mailSpawnFailure(manager(), *projectName, *step, *title, msg, args, stderr)
		return 1
	}
	// repeatable goes false once a task bead may be left behind: another attempt
	// would create a second one.
	repeatable := true
	fail := func(msg string) int {
		if repeatable && !retried && transientSpawnFailure(msg) {
			fmt.Fprintf(stderr, "spawn failed (%s); trying once more in %s\n", msg, spawnRetryAfter)
			time.Sleep(spawnRetryAfter)
			return spawnOperatorAttempt(args, stdout, stderr, true)
		}
		return report(msg)
	}
	var taskHolder func(id string) string
	removeTask := func(id, reason string) {
		keep := taskHolder(id)
		if keep == "" {
			dctx, dcancel := context.WithTimeout(context.Background(), spawnStepTimeout)
			defer dcancel()
			// What the task held (its step, when nothing else does) goes to the Manager first.
			holdReleased(dctx, bc, projectDir, id, map[string]holding.Edit{id: func(h *holding.Bead) { h.Status = holding.StatusClosed }}, manager(), beads.ManagerDispatchWait)
			err := bc.Delete(dctx, projectDir, id, reason)
			if err == nil {
				return
			}
			keep = fmt.Sprintf("could not be removed (%v)", err)
			// It stays, open with nobody on it: its Manager holds it.
			_ = holdWithManager(dctx, bc, projectDir, id, manager(), beads.ManagerRecoverWait)
		}
		repeatable = false
		left = fmt.Sprintf("; task %s %s and stays: look at it before spawning again", id, keep)
		fmt.Fprintf(stderr, "warning: task %s %s, so the spawn is not repeated\n", id, keep)
	}

	deps, err := spawnOperatorDeps(*projectName)
	if err != nil {
		return fail(err.Error())
	}
	if deps.Cleanup != nil {
		defer deps.Cleanup()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	project := deps.Project
	bc = newBeadsClientFn()
	projectDir = project.Path
	taskHolder = func(id string) string {
		hctx, hcancel := context.WithTimeout(context.Background(), spawnStepTimeout)
		defer hcancel()
		return taskHeldBy(hctx, bc, deps.RunningOn, projectDir, id)
	}

	// Validate the target before deriving a Manager role or sending failure mail.
	// A failed jq lookup often arrives as the literal id "null".
	target, err := bc.Show(ctx, projectDir, *step)
	if errors.Is(err, beads.ErrNotFound) || (err == nil && target == nil) {
		emitErr(stderr, fmt.Sprintf("cannot spawn: step %q does not name a readable bead", *step))
		return 1
	}
	if err != nil {
		// A busy or timed-out read is not a missing bead: keep the retry and the mail.
		return fail(fmt.Sprintf("cannot read step %q: %v", *step, err))
	}

	if deps.CheckCap != nil {
		if err := deps.CheckCap(ctx, bc, project, *step); err != nil {
			return fail(err.Error())
		}
	}
	var parentNote string
	if role := os.Getenv("COMMANDER_ROLE"); strings.HasPrefix(role, "manager-") {
		owners, err := wsmgr.OwnerChain(ctx, bc, project.Path, *step)
		switch {
		case err != nil && beads.NestedManagersOn(project.Path):
			return fail(fmt.Sprintf("could not read who owns %s: %v; retry in a minute", *step, err))
		case err != nil:
			// Switch off: the one-Manager tree this check guards cannot split.
			fmt.Fprintf(stderr, "warning: could not read who owns %s (%v); spawning as asked\n", *step, err)
		default:
			record, err := parentDispatch(role, owners, *step, *asParent)
			if err != nil {
				// A policy refusal, not a failure: no Commander mail.
				emitErr(stderr, err.Error())
				return 1
			}
			if record {
				parentNote = fmt.Sprintf("Operator spawned by parent %s with --as-parent: %s owns this step.", role, wsmgr.Role(owners[0]))
			}
		}
	}

	if !*writesWorkflow {
		gate, err := openGateAbove(ctx, bc, project.Path, *step)
		if err != nil {
			return fail(err.Error())
		}
		if gate != "" {
			return fail(fmt.Sprintf("workflow not approved: %s", gate))
		}
		if err := requireWorkflowGate(ctx, bc, project.Path, *step, stderr); err != nil {
			return fail(err.Error())
		}
	}
	prodScope := ""
	if *prodGate != "" {
		scope, err := checkProdGate(ctx, bc, project.Path, *step, *prodGate, os.Getenv("COMMANDER_ROLE"))
		if err != nil {
			return fail(err.Error())
		}
		prodScope = scope
	}
	if !*writesWorkflow {
		if err := checkReached(ctx, bc, project.Path, project.Name, *step, "spawning an Operator"); err != nil {
			return fail(err.Error())
		}
	}
	// Before the task bead is made: no Operator starts on a nearly full disk.
	if err := spawn.CheckDisk(spawn.SpawnRequest{ProjectPath: project.Path, RepoPath: project.RepoPath}); err != nil {
		emitErr(stderr, fmt.Sprintf("spawn operator: %v; no task was created", err))
		return 1
	}
	// Before the task bead is made: what the run would really start on (the
	// Manager's account, a share draw either way) has usage left.
	if err := spawn.CheckUsage(spawn.SpawnRequest{TaskText: *title + "\n" + *prompt, Runtime: *runtime, Account: *account},
		"", spawn.ManagerRole(ctx, bc, project.Path, *step)); err != nil {
		emitErr(stderr, fmt.Sprintf("spawn operator: %v; no task was created", err))
		return 1
	}
	var taskID string
	createStarted := time.Now()
	marker := spawnAttemptLabel()
	err = retryStep(stderr, "create task", false, func(ctx context.Context) error {
		id, cerr := bc.Create(ctx, project.Path, *title, beads.CreateOptions{
			Parent:      *step,
			Labels:      []string{"kind:task", marker},
			Description: *prompt,
			Silent:      true,
		})
		taskID = id
		return cerr
	})
	if err != nil && taskID == "" && beads.MayHaveApplied(err) {
		// The create may have landed, so it is never repeated: carry on with this
		// attempt's own bead if it shows, else report it. Another spawn's task of
		// the same title has another marker, so it is never taken for this one.
		taskID = findLandedCreate(bc, project.Path, *title, *step, marker, createStarted)
		if taskID == "" {
			return report(fmt.Sprintf("create task under %s: %v; no such task shows yet and the create was not repeated: look at the step's children before spawning again", *step, err))
		}
		if keep := taskHolder(taskID); keep != "" {
			return report(fmt.Sprintf("create task under %s: %v; task %s landed but %s, so it was neither taken over nor removed: look at it before spawning again", *step, err, taskID, keep))
		}
		err = nil
	}
	if err != nil {
		if taskID != "" {
			removeTask(taskID, "inherited labels could not be cleaned")
		}
		return fail(fmt.Sprintf("create task under %s: %v", *step, err))
	}

	if *prodGate != "" {
		if err := retryStep(stderr, "record prod-gate", true, func(ctx context.Context) error {
			return bc.SetLabel(ctx, project.Path, taskID, prodGateLabelPrefix+beads.EscapeLabel(*prodGate), true)
		}); err != nil {
			removeTask(taskID, "prod-gate label failed")
			return fail(fmt.Sprintf("record prod-gate on %s: %v", taskID, err))
		}
	}

	// The role and worktree are always op-<slug>-<task id>; --branch only
	// names the git branch.
	name := strings.ReplaceAll(operatorBranch(*title, taskID), "/", "-")
	taskBranch := *branch
	if taskBranch == "" && project.Type == "code" {
		taskBranch = operatorBranch(*title, taskID)
	}
	useModel := *model
	if useModel == "" {
		useModel = deps.DefaultModel
	}
	// Its own budget: the create and its retries may have used up ctx's.
	spawnCtx, spawnCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer spawnCancel()
	res, err := deps.Spawner.Spawn(spawnCtx, spawn.SpawnRequest{
		ProjectID:     project.ID,
		ProjectName:   project.Name,
		ProjectPath:   project.Path,
		ProjectType:   project.Type,
		RepoPath:      project.RepoPath,
		TaskBeadID:    taskID,
		Branch:        taskBranch,
		Name:          name,
		Model:         useModel,
		Runtime:       *runtime,
		TaskText:      *title + "\n" + *prompt,
		Account:       *account,
		ProdGate:      *prodGate,
		ProdGateScope: prodScope,
	})
	if err != nil {
		// The task never ran: delete it, so neither a retry duplicates it nor
		// does a closed bead read as finished work.
		removeTask(taskID, "spawn failed")
		return fail(fmt.Sprintf("spawn operator: %v", err))
	}

	// The task bead is the work: working, owned by its Operator. The step's
	// state follows from its children, so it is not touched here.
	if err := retryStep(stderr, "mark task in progress", true, func(ctx context.Context) error {
		return bc.SetStatus(ctx, project.Path, taskID, "in_progress")
	}); err != nil {
		emitErr(stderr, fmt.Sprintf("operator %s spawned for %s but marking the task failed: %v", res.AgentName, taskID, err))
		return 1
	}
	if err := retryStep(stderr, "set task owner", true, func(ctx context.Context) error {
		return bc.SetLabel(ctx, project.Path, taskID, beads.OwnerLabelPrefix+beads.OwnerLabelName(res.AgentName), true)
	}); err != nil {
		emitErr(stderr, fmt.Sprintf("operator %s spawned for %s but setting the task owner failed: %v", res.AgentName, taskID, err))
		return 1
	}

	if parentNote != "" {
		// Recorded once the Operator runs, so a failed spawn leaves no note.
		if err := retryStep(stderr, "record --as-parent", false, func(ctx context.Context) error {
			return bc.AddComment(ctx, project.Path, *step, parentNote)
		}); err != nil {
			fmt.Fprintf(stderr, "warning: operator %s spawned but recording --as-parent on %s failed: %v; add it with cmdr internal-comment --project %s --bead %s --text %q\n",
				res.AgentName, *step, err, project.Name, *step, parentNote)
		}
	}

	if err := json.NewEncoder(stdout).Encode(internalSpawnOperatorResult{
		Project: project.Name, Step: *step, TaskID: taskID, Branch: taskBranch,
		Agent: res.AgentName, Mail: res.AgentName, MailAlias: spawn.CanonicalRole(res.AgentName),
		Runtime: res.Runtime, Account: res.Account, Moved: res.Moved,
	}); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// spawnAttemptLabel is one attempt's mark on the task bead it creates: when the
// create's answer is lost, it tells that bead from any other of the same title.
func spawnAttemptLabel() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails on the platforms Commander runs on
	return "spawn-attempt:" + hex.EncodeToString(b)
}

// taskHeldBy says why a task bead may not be deleted or taken over: it has an
// owner, an Operator is running on it, or that could not be read (in doubt it
// stays). "" = free.
func taskHeldBy(ctx context.Context, bc beads.Client, runningOn func(context.Context, string) (string, error), dir, id string) string {
	task, err := freshReader(bc).Show(ctx, dir, id)
	if err != nil {
		return fmt.Sprintf("could not be read (%v)", err)
	}
	if task != nil {
		for _, l := range task.Labels {
			if who, ok := strings.CutPrefix(l, beads.OwnerLabelPrefix); ok && who != "" {
				return "is owned by " + who
			}
		}
	}
	if runningOn == nil {
		return ""
	}
	op, err := runningOn(ctx, id)
	if err != nil {
		return fmt.Sprintf("could not be checked for a running Operator (%v)", err)
	}
	if op != "" {
		return "has Operator " + op + " running on it"
	}
	return ""
}

// prodGateLabelPrefix marks a task whose Operator may take the production
// action its gate covers.
const prodGateLabelPrefix = "prod-gate:"

// checkProdGate validates a --prod-gate bead and returns its description, the
// exact action allowed: only a Manager may pass it, the bead is a gate (kind:gate),
// closed, approved by the user, and linked to the step (gate-of: the step or an
// ancestor, a blocks edge from one of them, or its approval covering the step
// on the board).
func checkProdGate(ctx context.Context, bc beads.Client, dir, step, gateID, caller string) (string, error) {
	if !strings.HasPrefix(caller, "manager-") {
		return "", fmt.Errorf("--prod-gate: only a Manager may pass it (COMMANDER_ROLE=%q)", caller)
	}
	gate, err := freshReader(bc).Show(ctx, dir, gateID)
	if err != nil {
		return "", fmt.Errorf("--prod-gate %s: %w", gateID, err)
	}
	if gate == nil || !(slices.Contains(gate.Labels, gates.KindLabel) || slices.Contains(gate.Labels, beads.GateLabel)) {
		return "", fmt.Errorf("--prod-gate %s: not a gate bead (label gate or kind:gate)", gateID)
	}
	if !strings.Contains(strings.ToLower(gate.Status), "closed") {
		return "", fmt.Errorf("--prod-gate %s: gate is not closed (the user has not approved it)", gateID)
	}
	approved := false
	for _, l := range gate.Labels {
		if who, ok := strings.CutPrefix(l, beads.ApproverLabelPrefix); ok && ownercompat.Normalize(who) == "user" {
			approved = true
		}
	}
	if !approved {
		return "", fmt.Errorf("--prod-gate %s: gate is not approved by the user (needs approver:user)", gateID)
	}
	if strings.TrimSpace(gate.Description) == "" {
		return "", fmt.Errorf("--prod-gate %s: gate has no description naming the exact action it covers", gateID)
	}
	chain := beads.Lineage(ctx, bc, dir, nil, step)
	linked := false
	for _, id := range chain {
		if slices.Contains(gate.Labels, "gate-of:"+beads.EscapeLabel(id)) {
			linked = true
		}
	}
	if !linked {
		details, err := beads.FreshDetails(ctx, bc, dir, chain)
		if err != nil {
			return "", fmt.Errorf("--prod-gate %s: read step links: %w", gateID, err)
		}
		for _, id := range chain {
			for _, d := range details[id].Deps {
				if d.Type == "blocks" && d.DependsOnID == gateID {
					linked = true
				}
			}
		}
	}
	if !linked {
		if s := boardSnapshot(); s != nil {
			linked = slices.Contains(livedata.CoveringApprovals(*s, step), gateID)
		}
	}
	if !linked {
		return "", fmt.Errorf("--prod-gate %s: gate is not linked to step %s (gate-of or blocks edge, or its approval covering the step on the board)", gateID, step)
	}
	return gate.Description, nil
}

// openGateAbove returns the first open gate bead that blocks the step or one of
// its ancestors ("" = none): no Operator starts work under an unapproved
// workflow gate. Ancestors are the step's real parent chain, so a step promoted
// to its own workstream is not held by the old workstream's gate. (Show leaves
// Status as br prints it: "● P2 · CLOSED".)
func openGateAbove(ctx context.Context, bc beads.Client, dir, step string) (string, error) {
	chain := beads.Lineage(ctx, bc, dir, nil, step)
	details, err := readOnceMore(ctx, func() (map[string]beads.Detail, error) {
		return beads.FreshDetails(ctx, bc, dir, chain)
	})
	if err != nil {
		return "", gateReadError(step, err)
	}
	fresh := freshReader(bc) // a cached Show could still read the gate as open after it closed
	for _, id := range chain {
		for _, d := range details[id].Deps {
			if d.Type != "blocks" {
				continue
			}
			blocker, err := readOnceMore(ctx, func() (*beads.Bead, error) {
				return fresh.Show(ctx, dir, d.DependsOnID)
			})
			if err != nil {
				return "", gateReadError(step, err)
			}
			if blocker != nil && !strings.Contains(strings.ToLower(blocker.Status), "closed") && slices.Contains(blocker.Labels, beads.GateLabel) {
				return d.DependsOnID, nil
			}
		}
	}
	return "", nil
}

// mailSpawnFailure mails Commander (high importance) from the step's Manager
// when internal-spawn-operator fails. A mail failure only warns on stderr; it
// never changes the exit code.
func mailSpawnFailure(manager, project, step, title, errMsg string, args []string, stderr *os.File) {
	subject := fmt.Sprintf("spawn failed: %s (%s)", title, project)
	body := fmt.Sprintf("Spawning an Operator failed.\n\nError: %s\nStep: %s\nCommand: cmdr internal-spawn-operator %s\n\n"+
		"The Manager was told to try to unblock itself and to set the step needs-user if it cannot.",
		errMsg, step, quoteArgs(args))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, client, key, cleanup, err := mailDeps()
	if err == nil {
		defer cleanup()
		err = sendRoleMail(ctx, st, client, key, manager, []string{"commander"}, subject, body, agentmail.SendOptions{Importance: "high"})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: spawn failed and mailing Commander failed too: %v\n", err)
	}
}

// quoteArgs renders args as a copy-pasteable command line.
func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'$`\\") {
			quoted[i] = strconv.Quote(a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}

var branchSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// operatorBranch names a worktree branch op/<slug>-<task-id>; the task id keeps
// concurrent operators on similar titles from colliding.
func operatorBranch(title, taskID string) string {
	slug := strings.Trim(branchSlugRE.ReplaceAllString(strings.ToLower(title), "-"), "-")
	// The owner label "owner:operator-op-<slug>-<task-id>" must fit br's 50
	// characters, so the slug gets whatever room the task id leaves (max 32).
	room := 50 - len("owner:operator-op--") - len(taskID)
	room = max(0, min(32, room))
	if len(slug) > room {
		slug = strings.Trim(slug[:room], "-")
	}
	if slug == "" {
		return "op/" + taskID
	}
	return "op/" + slug + "-" + taskID
}

func realSpawnOperatorDeps(projectName string) (*operatorSpawnDeps, error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, fmt.Errorf("resolve state dir: %w", err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	project, err := st.GetProjectByName(ctx, projectName)
	if err != nil || project == nil {
		_ = st.Close()
		if err == nil {
			err = fmt.Errorf("project %q not added", projectName)
		}
		return nil, err
	}
	cfg, err := config.Load(filepath.Join(filepath.Dir(statePath), "config.toml"))
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("load config: %w", err)
	}
	server, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("agent-mail client: %w", err)
	}
	repo := resolveCommanderRepo()
	am := &agentmail.ServerRegistrar{Server: server, Store: st, Key: repo}
	return &operatorSpawnDeps{
		Spawner:      spawn.New(st, opencode.New(), beads.New(), am, docker.New(), tmuxctl.New(), repo).WithDockerSandbox(cfg.OperatorSandbox == "docker"),
		Project:      project,
		DefaultModel: cfg.Models.DefaultOperator,
		CheckCap: func(ctx context.Context, bc beads.Client, p *store.Project, step string) error {
			// Dead Operators (session gone or only a shell) must not count. Count
			// past them without closing them: closing here skipped cmdrd's settle,
			// so their beads stayed in progress and their Managers never heard.
			return spawn.CheckWorkstreamCap(ctx, spawn.NewLiveRuns(st), bc, p.ID, p.Path, step, cfg.Concurrency.MaxOperatorsPerWorkstream)
		},
		RunningOn: func(ctx context.Context, taskID string) (string, error) {
			runs, err := st.ListAgentRunsByTaskBeadID(ctx, taskID, "running")
			if err != nil || len(runs) == 0 {
				return "", err
			}
			return runs[0].AgentName, nil
		},
		Cleanup: func() { _ = st.Close() },
	}, nil
}

// parentDispatch judges a Manager spawning under step whose owner chain is owners
// (nearest first, ending at the root; just the root with nested Managers off):
// the owner runs its Operators; an owner above it (a parent) dispatches to the
// nested Manager by mail instead, and with --as-parent may run one itself,
// recorded on the step. A Manager outside the chain (a sibling, another
// workstream's, or a nested Manager after --undo) is refused: it mails the owner.
func parentDispatch(role string, owners []string, step string, asParent bool) (record bool, err error) {
	if len(owners) == 0 || roleAmong(role, []string{wsmgr.Role(owners[0])}) {
		return false, nil
	}
	nearest := wsmgr.Role(owners[0])
	above := make([]string, 0, len(owners)-1)
	for _, o := range owners[1:] {
		above = append(above, wsmgr.Role(o))
	}
	if !roleAmong(role, above) {
		return false, fmt.Errorf("%s belongs to the workstream %s, whose Manager %s runs its Operators, not %s: ask it with cmdr internal-mail --to %s --subject ... --body ...", step, owners[0], nearest, role, nearest)
	}
	if !asParent {
		return false, fmt.Errorf("%s belongs to the nested workstream %s, whose Manager %s runs its Operators: dispatch it with cmdr internal-mail --to %s --subject ... --body ...; only while that Manager is down, add --as-parent (recorded on the step)", step, owners[0], nearest, nearest)
	}
	return true, nil
}
