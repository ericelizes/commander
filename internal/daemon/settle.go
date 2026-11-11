package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/worktreelock"
	"github.com/example-user/commander/internal/wsmgr"
)

// runSettler moves an Operator's task bead on when its process ends, so the
// bead always says what is happening:
//
//	exited (finished on its own) -> in review, owned by the workstream Manager
//	died (session gone)          -> open, restarted once on the same bead
//	died again                   -> open again, owned by nobody, Manager mailed
//	died in a restart            -> open, then respawned on the same bead
//
// Nothing else moves a bead out of working.
type runSettler struct {
	projects projectByID
	beads    beads.Client
	// mail sends from cmdrd to a role; nil when the mail server is not configured.
	mail func(ctx context.Context, toRole, subject, body string) error
	// spawner and model respawn an Operator a restart killed; nil spawner
	// turns auto-respawn off.
	spawner interface {
		Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error)
	}
	model string
	// lockWorktree freezes a closed run's worktree; nil uses worktreelock.Lock.
	lockWorktree func(dir string) error
	// running lists the running runs, which the freeze and a stop's settle check
	// for a newer run; nil reads as unreadable, so nothing is frozen.
	running func(ctx context.Context) ([]store.AgentRun, error)
	// branchOf names the branch checked out in a worktree (git by default).
	branchOf func(worktree string) (string, error)
	now      func() time.Time
	// events records how a run ended (operator-exited / operator-died cost
	// events), which the app reads to keep it visible for a while; nil skips it.
	events interface {
		InsertCostEvent(ctx context.Context, e store.CostEvent) (int64, error)
	}
	// comments lists a bead's comments; nil reads them with `br comments`.
	comments func(ctx context.Context, projectPath, beadID string) ([]beadComment, error)
	// restarts bounds crash restarts per step and per hour; nil allows each
	// bead's one.
	restarts *restartBudget
	// lastOutput is when a run's pane last printed; nil stats spawn.RunOutputPath.
	lastOutput func(run store.AgentRun, projectPath string) time.Time
	// mailShownAt is when mail was last shown to a run's Operator; nil reads its mail cursor.
	mailShownAt func(run store.AgentRun) time.Time
	// outputTail is the end of a run's pane output; nil reads spawn.OutputTail.
	outputTail func(run store.AgentRun, projectPath string) string
	// reported holds what the board reads of a finished run until its settle has landed; nil = not tracked.
	reported *reportedRuns
}

// beadComment is one `br comments list --json` entry.
type beadComment struct {
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// maxAutoRespawns caps how often one bead's Operator is respawned for dying in
// a restart; the count lives on the bead as auto-respawn:<n> labels.
const maxAutoRespawns = 2

const autoRespawnLabel = beads.AutoRespawnLabelPrefix

// An Operator that dies on its own (no restart, no report) is restarted once
// per bead (crash-respawn:<unix> label); over a step's beads and the fleet
// the restarts are capped per hour, so a cause that kills every run cannot
// turn into a loop.
const (
	crashRestartsPerStep = 3
	crashRestartsPerHour = 20
)

// noteMark starts every comment cmdrd writes on a bead: never an Operator's report.
const noteMark = "cmdrd: "

// asleepWhy is the died reason of a run found gone after a sleep.
const asleepWhy = "session gone after the Mac slept"

const managerCloseWithin = beads.ManagerCloseWithin

// exited settles an Operator whose process ended on its own. A final report
// (a bead comment written since the run began) means it finished: the bead goes
// to review under its Manager, who is mailed the report's first line. Without
// one it did not finish, so it is settled as died.
func (s runSettler) exited(ctx context.Context, run store.AgentRun) error {
	manager := s.managerOf(ctx, run)
	report := s.finalReport(ctx, run)
	if report == "" {
		if waiting, e := s.questionWait(ctx, run); e != nil {
			return e
		} else if waiting {
			s.freeze(ctx, run)
			return nil
		}
		return s.diedBecause(ctx, run, false, "exited without writing a final report on the bead")
	}
	s.reported.settling(run, s.clock())
	moved, err := s.move(ctx, run, "in_progress", manager, true, s.handoffWait(beads.ManagerCloseWait))
	if err != nil {
		return err
	}
	_ = moved
	s.freeze(ctx, run)
	s.record(ctx, run, store.EventOperatorExited, report)
	return s.tell(ctx, manager, fmt.Sprintf("[operator] %s finished %s: %s", run.AgentName, run.TaskBeadID, report),
		fmt.Sprintf("%s exited at %s. Its report is on the bead (bd show %s). Review it, then close the bead (cmdr internal-close-objective) or reopen it with feedback (--state not-started --owner none) and spawn again. The step waits on you until %s; past that the board flags it.", run.AgentName, s.clock().Format(time.RFC3339), run.TaskBeadID, s.clock().Add(managerCloseWithin).Format(time.RFC3339)))
}

// managerOf is the Manager of the workstream that owns the run's task bead.
func (s runSettler) managerOf(ctx context.Context, run store.AgentRun) string {
	return runManager(ctx, s.projects, s.beads, run)
}

// projectByID is the store lookup runManager needs.
type projectByID interface {
	GetProjectByID(ctx context.Context, id int64) (*store.Project, error)
}

// runManager is the Manager of the workstream that owns run's task bead, by its
// real parent chain in the run's project.
func runManager(ctx context.Context, projects projectByID, bc beads.Client, run store.AgentRun) string {
	dir := ""
	if p, err := projects.GetProjectByID(ctx, run.ProjectID); err == nil && p != nil {
		dir = p.Path
	}
	return wsmgr.Role(wsmgr.Owner(ctx, bc, dir, run.TaskBeadID))
}

// finalReport is the first line of the newest comment written on the bead since
// the run was spawned, "" when there is none.
func (s runSettler) finalReport(ctx context.Context, run store.AgentRun) string {
	report, err := s.readReport(ctx, run)
	if err != nil {
		log.Printf("daemon: comments of %s: %v", run.TaskBeadID, err)
	}
	return report
}

// readReport is finalReport that tells a failed read from no report.
func (s runSettler) readReport(ctx context.Context, run store.AgentRun) (string, error) {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", nil
	}
	list := s.comments
	if list == nil {
		list = brComments
	}
	cs, err := list(ctx, p.Path, run.TaskBeadID)
	if err != nil {
		return "", err
	}
	for i := len(cs) - 1; i >= 0; i-- {
		t, err := time.Parse(time.RFC3339, cs[i].CreatedAt)
		if err != nil || t.Unix() < run.SpawnTime || strings.HasPrefix(cs[i].Text, noteMark) {
			continue
		}
		if line := firstLine(cs[i].Text, 200); line != "" {
			return line, nil
		}
	}
	return "", nil
}

// record notes the end of a run (when, and the report's first line or why it
// died); best effort.
func (s runSettler) record(ctx context.Context, run store.AgentRun, kind, reason string) {
	if s.events == nil {
		return
	}
	_, err := s.events.InsertCostEvent(ctx, store.CostEvent{
		ProjectID:  run.ProjectID,
		TaskBeadID: sql.NullString{String: run.TaskBeadID, Valid: true},
		AgentName:  run.AgentName,
		EventType:  kind,
		Reason:     sql.NullString{String: reason, Valid: reason != ""},
		OccurredAt: s.clock().Unix(),
	})
	if err != nil {
		log.Printf("daemon: record %s for %s: %v", kind, run.AgentName, err)
	}
}

func (s runSettler) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// brComments reads a bead's comments. br prints a banner before the JSON.
func brComments(ctx context.Context, projectPath, beadID string) ([]beadComment, error) {
	cmd := exec.CommandContext(ctx, beads.Binary(), brCommentsArgs(beadID)...)
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	i := strings.Index(string(out), "[")
	if i < 0 {
		return nil, nil
	}
	var cs []beadComment
	return cs, json.Unmarshal(out[i:], &cs)
}

// died settles an Operator whose session is gone. restart is true when the run
// was found dead in the first sweep after cmdrd started: the restart (or a
// reboot) killed it, it did not crash, so it is respawned on its bead.
func (s runSettler) died(ctx context.Context, run store.AgentRun, restart bool) error {
	return s.diedBecause(ctx, run, restart, "ended without the process finishing (killed or crashed)")
}

func (s runSettler) diedBecause(ctx context.Context, run store.AgentRun, restart bool, why string) error {
	return s.diedWith(ctx, run, restart, why, fmt.Sprintf("Operator died on %s", run.TaskBeadID),
		fmt.Sprintf("%s's session %s. %s is open again and waits on you (Manager to recover). Read what it left on the bead (bd show %s), then spawn again or take another path.", run.AgentName, why, run.TaskBeadID, run.TaskBeadID))
}

// stoppedOnRequest settles a run someone stopped (spawn.StopHostRun): it is not
// a crash, so it is never restarted, and the step goes back to its Manager.
func (s runSettler) stoppedOnRequest(ctx context.Context, stop spawn.StoppedRun) error {
	run := stop.Run
	// A respawn under the old name may already hold the step, and its owner label
	// reads as this run's: the settle must not reopen it. The Manager spawned it,
	// so there is nothing to tell. Unreadable = retry.
	runs, err := s.runningRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.ID != run.ID && r.ProjectID == run.ProjectID && r.TaskBeadID == run.TaskBeadID {
			log.Printf("daemon: settle stopped run %d: %s already runs %s, nothing to hand back", run.ID, r.AgentName, run.TaskBeadID)
			return nil
		}
	}
	manager := s.managerOf(ctx, run)
	moved, err := s.move(ctx, run, "open", manager, false, s.handoffWait(beads.ManagerRecoverWait))
	if err != nil {
		return err
	}
	why := "stopped by " + stop.By
	return s.stopped(ctx, run, moved, manager, why, fmt.Sprintf("Operator stopped on %s", run.TaskBeadID),
		fmt.Sprintf("%s was %s and has ended. %s is open again and waits on you (Manager to recover). It did not read any mail sent with the stop: to go on, spawn again with the new direction in the brief.", run.AgentName, why, run.TaskBeadID), "")
}

// diedWith reopens the bead and starts the Operator again within the caps
// (restart: killed by a cmdrd restart or a sleep; else a crash, restarted
// once); otherwise, or when that fails, the step goes back to the Manager.
func (s runSettler) diedWith(ctx context.Context, run store.AgentRun, restart bool, why, subject, body string) error {
	if waiting, e := s.questionWait(ctx, run); e != nil {
		return e
	} else if waiting {
		s.freeze(ctx, run)
		return nil
	}
	manager := s.managerOf(ctx, run)
	// Make before break: the Manager to recover wait (and the Manager's name) replaces the dead
	// claim in the same write; a respawn replaces both with the new run.
	moved, err := s.move(ctx, run, "open", manager, false, s.handoffWait(beads.ManagerRecoverWait))
	if err != nil {
		return err
	}
	stop := func(c context.Context, fact string) error {
		return s.stopped(c, run, moved, manager, why, subject, body, fact)
	}
	if s.spawner == nil {
		return stop(ctx, "")
	}
	return s.respawn(ctx, run, manager, why, !restart, stop)
}

// stopped hands the step back to its Manager: marked died (the board reads it
// as the Manager's move), its worktree frozen, the end written on the bead and
// recorded, and the Manager mailed what cmdrd saw. Each part runs once: a
// failure retries only what is left.
func (s runSettler) stopped(ctx context.Context, run store.AgentRun, moved bool, manager, why, subject, body, fact string) error {
	var steps []func() error
	if moved {
		// The move already wrote the Manager to recover wait with the reopen (one write); the died
		// marker follows.
		steps = append(steps, func() error { return s.setLabel(ctx, run, beads.OperatorDiedLabel, true) })
	}
	steps = append(steps,
		func() error {
			s.freeze(ctx, run)
			s.record(ctx, run, store.EventOperatorDied, why)
			if moved {
				note := fmt.Sprintf("%s%s %s at %s; not restarted", noteMark, run.AgentName, why, s.clock().Format(time.RFC3339))
				if fact != "" {
					note += " (" + fact + ")"
				}
				s.note(ctx, run, note+"; the step is "+manager+"'s.")
			}
			return nil
		},
		func() error { return s.tell(ctx, manager, subject, body+s.facts(ctx, run, fact)) },
	)
	return runSteps(steps)
}

// holdForManager records the Manager to recover wait (and its owner label) on the run's bead.
func (s runSettler) holdForManager(ctx context.Context, run store.AgentRun, manager string) error {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return fmt.Errorf("hold %s: project %d: %v", run.TaskBeadID, run.ProjectID, err)
	}
	if manager != "" {
		if err := s.beads.SetLabel(ctx, p.Path, run.TaskBeadID, beads.OwnerLabelPrefix+beads.OwnerLabelName(manager), true); err != nil {
			return err
		}
	}
	now := s.clock()
	w := beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(managerCloseWithin), Ref: beads.ManagerRecoverWait}
	return s.beads.SetLabel(ctx, p.Path, run.TaskBeadID, w.Label(), true)
}

// facts is what cmdrd saw of a run's end, for the Manager: why it was not
// restarted and the end of its output.
func (s runSettler) facts(ctx context.Context, run store.AgentRun, fact string) string {
	var b strings.Builder
	if fact != "" {
		b.WriteString("\n\nNot restarted: " + fact + ".")
	}
	if tail := s.tail(ctx, run); tail != "" {
		b.WriteString("\n\nLast output:\n" + tail)
	}
	return b.String()
}

func (s runSettler) tail(ctx context.Context, run store.AgentRun) string {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return ""
	}
	if s.outputTail != nil {
		return s.outputTail(run, p.Path)
	}
	return spawn.OutputTail(run, p.Path, 12)
}

// note writes a comment on the run's bead: the event as cmdrd saw it, for
// whoever reads the bead next (the Manager, the Operator it restarts). Best
// effort.
func (s runSettler) note(ctx context.Context, run store.AgentRun, text string) {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return
	}
	if err := s.beads.AddComment(ctx, p.Path, run.TaskBeadID, text); err != nil {
		log.Printf("daemon: note on %s: %v", run.TaskBeadID, err)
	}
}

// respawn starts the Operator again on the same bead, branch and worktree,
// resuming its claude session. Without a restart left (cap reached, not a code
// project, no worktree) it calls stop with the reason instead. A spawn that
// fails is tried once more at the next sweep; a second failure is stop's too.
func (s runSettler) respawn(ctx context.Context, run store.AgentRun, manager, why string, crash bool, stop func(context.Context, string) error) error {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil || p.Type != "code" {
		return stop(ctx, "")
	}
	b, err := s.beads.Show(ctx, p.Path, run.TaskBeadID)
	if err != nil {
		return err
	}
	if !settlable(b, run, false) {
		return stop(ctx, "")
	}
	n, crashed := 0, false
	for _, l := range b.Labels {
		n += b2i(strings.HasPrefix(l, autoRespawnLabel))
		crashed = crashed || strings.HasPrefix(l, beads.CrashRespawnLabelPrefix)
	}
	switch {
	case crash && crashed:
		return stop(ctx, "it already died once and was restarted; it died again")
	case !crash && n >= maxAutoRespawns:
		return stop(ctx, fmt.Sprintf("restarted %d times already", n))
	}
	branchOf := s.branchOf
	if branchOf == nil {
		branchOf = gitBranchOf
	}
	branch, err := branchOf(filepath.Join(p.Path, ".worktrees", strings.TrimPrefix(run.AgentName, "operator-")))
	if err != nil || branch == "" {
		return stop(ctx, "")
	}
	if branch == spawn.DetachedHEAD {
		return stop(ctx, "its worktree is on a detached HEAD, so there is no branch to restart it on")
	}
	if crash {
		if full := s.restarts.take(beads.ParentOf(run.TaskBeadID), s.clock()); full != "" {
			return stop(ctx, full)
		}
	}
	req := spawn.SpawnRequest{
		ProjectID: p.ID, ProjectName: p.Name, ProjectPath: p.Path, ProjectType: p.Type, RepoPath: p.RepoPath,
		TaskBeadID: run.TaskBeadID, Branch: branch, Name: strings.TrimPrefix(run.AgentName, "operator-"), Model: s.model, Resume: true,
	}
	if crash {
		req.ResumeNote = "your process ended before it wrote its final report on the bead; finish the task and write that report before you exit"
	}
	started := func(c context.Context, res *spawn.SpawnResult) error {
		return runSteps(s.afterRespawn(c, p, run, res, manager, why, branch, crash, n))
	}
	res, err := s.spawner.Spawn(ctx, req)
	if err == nil {
		return started(ctx, res)
	}
	log.Printf("daemon: respawn %s: %v (one more try next sweep)", run.TaskBeadID, err)
	return &partialErr{err: fmt.Errorf("respawn %s: %w", run.TaskBeadID, err), rest: func(c context.Context) error {
		if !s.stillSettling(c, run) {
			return nil
		}
		res, err := s.spawner.Spawn(c, req)
		if err != nil {
			return stop(c, "starting it again failed twice: "+firstLine(err.Error(), 200))
		}
		return started(c, res)
	}}
}

// afterRespawn marks the bead worked on by the restarted Operator and says so
// on the bead; a restart after a cmdrd restart or a sleep also tells the
// Manager (a crash restart needs nothing from it until it dies again).
func (s runSettler) afterRespawn(ctx context.Context, p *store.Project, run store.AgentRun, res *spawn.SpawnResult, manager, why, branch string, crash bool, n int) []func() error {
	cause := "was killed by a restart"
	if why == asleepWhy {
		cause = "died while the Mac slept"
	}
	label := fmt.Sprintf("%s%d", autoRespawnLabel, n+1)
	if crash {
		label = fmt.Sprintf("%s%d", beads.CrashRespawnLabelPrefix, s.clock().Unix())
	}
	// The Operator is running now: a failure below must not run diedWith (and
	// so Spawn) again, so only the steps not yet done are retried (partialErr).
	steps := []func() error{
		func() error {
			// The new run replaces the Manager's recover hold in one write (never a moment with neither).
			b, err := s.beads.Show(ctx, p.Path, run.TaskBeadID)
			if err != nil {
				return err
			}
			if b == nil {
				return nil
			}
			return s.moveBead(ctx, p.Path, *b, "in_progress", res.AgentName, false, label)
		},
		func() error {
			s.note(ctx, run, fmt.Sprintf("%s%s %s at %s; restarted on branch %s, same worktree, session resumed.", noteMark, run.AgentName, why, s.clock().Format(time.RFC3339), branch))
			return nil
		},
	}
	if !crash {
		steps = append(steps, func() error {
			return s.tell(ctx, manager, fmt.Sprintf("Operator respawned on %s", run.TaskBeadID),
				fmt.Sprintf("%s %s. cmdrd respawned it on %s (branch %s, same worktree, session resumed): auto-respawn %d of %d.", run.AgentName, cause, run.TaskBeadID, branch, n+1, maxAutoRespawns))
		})
	}
	return steps
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// restartBudget counts recent crash restarts by step and in all. It lives in
// memory: a cmdrd restart starts a fresh hour, and each bead's one restart is
// on the bead regardless.
type restartBudget struct {
	mu     sync.Mutex
	byStep map[string][]time.Time
	all    []time.Time
}

// take records a restart under step at now and returns "", or why the hour's
// budget has none left. A nil budget allows every restart.
func (r *restartBudget) take(step string, now time.Time) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	recent := func(ts []time.Time) []time.Time {
		return slices.DeleteFunc(ts, func(t time.Time) bool { return now.Sub(t) >= time.Hour })
	}
	if r.byStep == nil {
		r.byStep = map[string][]time.Time{}
	}
	r.all = recent(r.all)
	inStep := recent(r.byStep[step])
	switch {
	case len(inStep) >= crashRestartsPerStep:
		r.byStep[step] = inStep
		return fmt.Sprintf("%d Operators under %s were restarted in the last hour", len(inStep), step)
	case len(r.all) >= crashRestartsPerHour:
		r.byStep[step] = inStep
		return fmt.Sprintf("%d Operators were restarted in the last hour", len(r.all))
	}
	r.byStep[step] = append(inStep, now)
	r.all = append(r.all, now)
	return ""
}

// partialErr is a settle that failed after part of it took effect (an Operator
// was already respawned): rest redoes only what is left.
type partialErr struct {
	err  error
	rest func(context.Context) error
}

func (e *partialErr) Error() string { return e.err.Error() }
func (e *partialErr) Unwrap() error { return e.err }

// runSteps runs steps in order; a failure returns a partialErr that resumes at
// the failed step.
func runSteps(steps []func() error) error {
	for i, f := range steps {
		if err := f(); err != nil {
			rest := steps[i:]
			return &partialErr{err: err, rest: func(context.Context) error { return runSteps(rest) }}
		}
	}
	return nil
}

// freeze write-protects the closed run's worktree. Spawning on the bead again
// (a reopen, or respawn below) unlocks it first (spawn.gitWorktreeAdd). Best
// effort: a failure is logged, never blocks settling.
func (s runSettler) freeze(ctx context.Context, run store.AgentRun) {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return
	}
	lock := s.lockWorktree
	if lock == nil {
		lock = worktreelock.Lock
	}
	if err := spawn.FreezeWorktree(ctx, s.runningRuns, lock, p.Path, run); err != nil {
		log.Printf("daemon: lock worktree of run %d: %v", run.ID, err)
	}
}

func (s runSettler) runningRuns(ctx context.Context) ([]store.AgentRun, error) {
	if s.running == nil {
		return nil, errors.New("no reader for running runs")
	}
	return s.running(ctx)
}

func gitBranchOf(worktree string) (string, error) {
	out, err := exec.Command("git", "-C", worktree, "rev-parse", "--abbrev-ref", "HEAD").Output()
	return strings.TrimSpace(string(out)), err
}

// handoffWait is the label of a Manager handoff wait (Manager to close, Manager to recover) from now.
func (s runSettler) handoffWait(kind string) string {
	now := s.clock()
	return beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(managerCloseWithin), Ref: kind}.Label()
}

func (s runSettler) setLabel(ctx context.Context, run store.AgentRun, label string, on bool) error {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return fmt.Errorf("settle %s: project %d: %v", run.TaskBeadID, run.ProjectID, err)
	}
	if on && !s.stillOpen(ctx, p.Path, run.TaskBeadID) {
		return nil // closed meanwhile: no label onto a closed bead
	}
	return s.beads.SetLabel(ctx, p.Path, run.TaskBeadID, label, on)
}

// move sets the bead's status, its one owner (none when owner is ""), and the
// review marker, and clears an earlier run's died marker or Manager wait.
// moved is false when the bead is gone or already closed.
func (s runSettler) move(ctx context.Context, run store.AgentRun, status, owner string, review bool, hold ...string) (moved bool, err error) {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return false, fmt.Errorf("settle %s: project %d: %v", run.TaskBeadID, run.ProjectID, err)
	}
	b, err := s.beads.Show(ctx, p.Path, run.TaskBeadID)
	if err != nil {
		return false, fmt.Errorf("settle %s: %w", run.TaskBeadID, err)
	}
	if !settlable(b, run, review) {
		return false, nil // closed, accepted, claimed by another run or already handed on
	}
	return true, s.moveBead(ctx, p.Path, *b, status, owner, review, hold...)
}

// settlable reports whether the end of run may still be written onto b. A late
// exit event must not touch a bead that is closed or parked, that another
// Operator has claimed since this run began, or that this run already handed to
// its Manager (in review): the Manager's close or reopen is the newer fact.
// finished is true for an exit with a report, which may redo a handoff whose
// Manager-to-close wait is missing.
func settlable(b *beads.Bead, run store.AgentRun, finished bool) bool {
	if b == nil || b.Status == "closed" || b.Status == beads.StatusDeferred {
		return false
	}
	state, owner := beads.StepState(b.Status, b.Labels)
	if strings.HasPrefix(owner, "operator-") && owner != beads.OwnerLabelName(run.AgentName) {
		return false
	}
	if state == beads.StateReview && !strings.HasPrefix(owner, "operator-") {
		if !finished {
			return false
		}
		for _, l := range b.Labels {
			if w, ok := beads.ParseWaitLabel(l); ok && w.Kind == beads.WaitUntil && w.Ref == beads.ManagerCloseWait {
				return false
			}
		}
	}
	return true
}

// stillOpen re-reads the bead right before a write: the Manager may have
// closed it since the settle began.
func (s runSettler) stillOpen(ctx context.Context, dir, id string) bool {
	b, err := s.beads.Show(ctx, dir, id)
	return err != nil || b == nil || (b.Status != "closed" && b.Status != beads.StatusDeferred)
}

func (s runSettler) moveBead(ctx context.Context, dir string, b beads.Bead, status, owner string, review bool, hold ...string) error {
	if !s.stillOpen(ctx, dir, b.ID) {
		return nil
	}
	// One update (one transaction): the next holder (owner, hold waits) goes in with the old
	// claim going out, so no read ever sees the bead with nobody on it (I3, make before break).
	f := beads.Fields{Status: &status, AddLabels: append([]string(nil), hold...)}
	want := ""
	if owner != "" {
		want = beads.OwnerLabelPrefix + beads.OwnerLabelName(owner)
		f.AddLabels = append(f.AddLabels, want)
	}
	if review {
		f.AddLabels = append(f.AddLabels, beads.ReviewLabel)
	}
	for _, l := range b.Labels {
		if (strings.HasPrefix(l, beads.OwnerLabelPrefix) && l != want) || beads.IsSettleMarker(l) || (!review && l == beads.ReviewLabel) {
			f.RemoveLabels = append(f.RemoveLabels, l)
		}
	}
	return s.beads.UpdateFields(ctx, dir, b.ID, f)
}

func (s runSettler) tell(ctx context.Context, role, subject, body string) error {
	if s.mail == nil {
		return nil
	}
	return s.mail(ctx, role, subject, body)
}

// runsByProject is the store reads sweepOrphans needs.
type runsByProject interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	ListAgentRunsByProject(ctx context.Context, projectID int64, status string) ([]store.AgentRun, error)
}

// sweepOrphans settles open steps still claimed by an Operator commander spawned
// whose process is gone: runs closed before the settler existed, or by a path
// that did not settle them. A step in review (or with a report since its run
// began) is settled as finished, any other as died; a needs-user step is the
// user's turn and is left alone. cmdrd runs it once at startup.
func (s runSettler) sweepOrphans(ctx context.Context, st runsByProject) {
	projects, err := st.ListProjects(ctx)
	if err != nil {
		log.Printf("daemon: orphan sweep: list projects: %v", err)
		return
	}
	for _, p := range projects {
		runs, err := st.ListAgentRunsByProject(ctx, p.ID, "")
		if err != nil || len(runs) == 0 {
			continue
		}
		running := map[string]bool{}
		latest := map[string]store.AgentRun{}
		for _, r := range runs {
			name := beads.OwnerLabelName(r.AgentName)
			if r.Status == "running" {
				running[name] = true
			}
			if l, ok := latest[name]; !ok || r.SpawnTime > l.SpawnTime {
				latest[name] = r
			}
		}
		all, err := s.beads.List(ctx, p.Path, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
		if err != nil {
			log.Printf("daemon: orphan sweep: list %s: %v", p.Name, err)
			continue
		}
		for _, b := range all {
			if b.Status == "closed" || b.Status == beads.StatusDeferred {
				continue
			}
			state, owner := beads.StepState(b.Status, b.Labels)
			last, known := latest[owner]
			if !strings.HasPrefix(owner, "operator-") || !known || running[owner] || state == beads.StateNeedsUser {
				continue
			}
			run := last
			run.TaskBeadID, run.ProjectID = b.ID, p.ID
			if err := s.settleOrphan(ctx, p.Path, b, run, state == beads.StateReview); err != nil {
				log.Printf("daemon: orphan sweep: %s: %v", b.ID, err)
			}
		}
	}
}

func (s runSettler) settleOrphan(ctx context.Context, dir string, b beads.Bead, run store.AgentRun, review bool) error {
	manager := s.managerOf(ctx, run)
	report := s.finalReport(ctx, run)
	if review || report != "" {
		if err := s.moveBead(ctx, dir, b, "in_progress", manager, true, s.handoffWait(beads.ManagerCloseWait)); err != nil {
			return err
		}
		if report == "" {
			report = "its report is on the bead"
		}
		return s.tell(ctx, manager, fmt.Sprintf("[operator] %s finished %s: %s", run.AgentName, b.ID, report),
			fmt.Sprintf("cmdrd found %s still claimed by %s, which is no longer running, and handed it to you for review. Read the report (bd show %s), then close the bead (cmdr internal-close-objective) or reopen it with feedback (--state not-started --owner none). The step waits on you until %s; past that the board flags it.", b.ID, run.AgentName, b.ID, s.clock().Add(managerCloseWithin).Format(time.RFC3339)))
	}
	// One write: reopened, the Manager's recover hold and the died marker in, the dead claim out.
	if err := s.moveBead(ctx, dir, b, "open", manager, false, s.handoffWait(beads.ManagerRecoverWait), beads.OperatorDiedLabel); err != nil {
		return err
	}
	return s.tell(ctx, manager, fmt.Sprintf("Operator died on %s", b.ID),
		fmt.Sprintf("cmdrd found %s still claimed by %s, which is no longer running and left no report. %s is open again and waits on you (Manager to recover). Read what it left on the bead (bd show %s), then spawn again or take another path.", b.ID, run.AgentName, b.ID, b.ID))
}

// brCommentsArgs is the comments read for the configured CLI: bd takes
// `comments <id>`, br `comments list <id>`.
func brCommentsArgs(beadID string) []string {
	if filepath.Base(beads.Binary()) == "bd" {
		return []string{"comments", beadID, "--json"}
	}
	return []string{"comments", "list", beadID, "--json"}
}
