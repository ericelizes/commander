package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/example-user/commander/internal/binstage"
	"github.com/example-user/commander/internal/updatecheck"
)

// Install-from-the-app (GET /api/update, POST /api/update/install).
//
// Threat model: agents run as the same OS user. They can read the API token,
// copy any header and drive any client, so no token, header or ticket can tell
// the app from an agent. The only protection that holds is an OUT-OF-BAND human
// act: any authenticated POST /api/update/install makes cmdrd itself show a
// native confirmation on the machine (Prompt) and it builds and installs only
// when the person presses Install. An agent can still POST the endpoint; the
// worst it does is pop a dialog the person declines (a global floor of one
// prompt per five minutes and three per hour, then 429; the floor is applied
// before any git work and survives a cmdrd restart). The X-Commander-Role
// refusal is a convenience for well-behaved clients, not a barrier: the dialog
// is. The confirmation names the exact target revision; the build runs in a
// detached export of exactly that commit (never the live checkout) and the
// built binaries must carry exactly that revision.
// Honest limit: a process with screen control (computer use, accessibility) can
// click the dialog. The guards on agent shells are a speed bump only. The job
// file is forgeable by the same user: it is status only (a fake "done" or
// "failed", or a 5 minute block) and is used in one direction only: to make
// the prompt floor MORE restrictive.

const (
	// RoleHeader is set by clients that know they are an agent; its presence
	// refuses the call. Nothing sends it today, so it protects nothing.
	RoleHeader = "X-Commander-Role"
	// OriginHeader "app" only changes the dialog wording ("from the Commander
	// app" vs "via the local API"); it is not protection, any client can send it.
	OriginHeader = "X-Commander-Origin"

	updateCacheTTL = 60 * time.Second
	promptEvery    = 5 * time.Minute
	promptsPerHour = 3
	maxJobEvents   = 300
	// defaultRestartWatch is how long the old daemon waits to be replaced.
	defaultRestartWatch = 3 * time.Minute
)

// Job states.
const (
	JobConfirming = "confirming" // waiting for the person at the native dialog
	JobDeclined   = "declined"   // cancelled, timed out or no dialog; nothing was built
	JobRunning    = "running"
	JobRestarting = "restarting" // the daemon is about to be replaced; the new one completes the job
	JobDone       = "done"
	JobFailed     = "failed"
)

// JobEvent is one line of install progress.
type JobEvent struct {
	Seq   int       `json:"seq"`
	Time  time.Time `json:"time"`
	Step  string    `json:"step"` // check, stage, install, rollback, restart, done
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

// Job is the current or last install/rollback, persisted in update-job.json so
// the daemon that comes up after the restart can report its result. The file is
// status only: nothing in it is ever executed or acted on.
type Job struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"` // install or rollback
	State   string    `json:"state"`
	Target  string    `json:"target,omitempty"` // revision expected to run afterwards
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
	// Prompted is when this job's dialog was shown; a new daemon seeds the
	// prompt floor from it (forgeable, but only ever to be more restrictive).
	Prompted time.Time  `json:"prompted,omitempty"`
	Events   []JobEvent `json:"events"`
}

// UpdateStatus is the response of GET /api/update.
type UpdateStatus struct {
	updatecheck.Status
	Repo     string    `json:"repo,omitempty"`
	Commands []string  `json:"commands"` // what to type in a terminal (the web build shows these)
	Job      *Job      `json:"job,omitempty"`
	Checked  time.Time `json:"checked"`
	// CanRollback is true when the install dir holds the previous binaries.
	CanRollback bool `json:"can_rollback"`
	// Host is the machine cmdrd runs on and NoDialog why it cannot show the
	// confirmation there ("" = it can); the desktop app on another machine
	// then confirms and installs over SSH itself (app/electron/remote-update.ts).
	Host     string `json:"host,omitempty"`
	NoDialog string `json:"no_dialog,omitempty"`
}

// Updater implements the update endpoints. Every side effect is a seam, so
// tests run no dialog, build, install or restart.
type Updater struct {
	Home       string // COMMANDER_HOME: job file, install log
	Repo       string
	InstallDir string
	LocalBin   string
	Check      func(ctx context.Context) updatecheck.Status
	// PromptAvailable returns why no native dialog can be shown, or nil.
	PromptAvailable func() error
	// Prompt shows the native confirmation (default button Cancel; a timeout
	// counts as Cancel) and reports whether the person pressed Install.
	Prompt func(ctx context.Context, title, text string) (bool, error)
	// Export checks out exactly the commit target (tracked files only) into
	// the new directory dir, apart from the live checkout; cleanup removes it.
	Export func(ctx context.Context, repo, target, dir string) (cleanup func(), err error)
	// Build builds src (the export) into stageDir with a clean environment;
	// out gets its output.
	Build func(ctx context.Context, src, stageDir string, out io.Writer) error
	// BinaryRevision reads the vcs.revision and vcs.modified a built binary carries.
	BinaryRevision func(path string) (rev string, modified bool, err error)
	// Install and Rollback are binstage.Install / binstage.Rollback.
	Install  func(binstage.Options) error
	Rollback func(binstage.Options) error
	// Restart replaces cmdrd in a process that outlives it (the installed
	// `cmdr install --restart-only --yes`). The channel (may be nil) gets that
	// process's result once it ends: nil = it reported success.
	Restart func(out io.Writer) (<-chan error, error)
	// RestartWatch is how long this daemon waits to be replaced before the job
	// fails; 0 = 3 minutes.
	RestartWatch time.Duration
	// Busy returns why an install must wait (a ship verify or build slot is
	// held), or "".
	Busy func() string
	// Branch returns the branch checked out in the repo.
	Branch func(ctx context.Context, repo string) (string, error)
	Now    func() time.Time

	mu      sync.Mutex
	reqMu   sync.Mutex // serializes Request, so the prompt floor holds before any git work
	cached  *UpdateStatus
	cacheAt time.Time
	job     *Job
	// prompts are the times dialogs were shown in the last hour (memory; seeded
	// at boot from the job file's Prompted).
	prompts []time.Time
	once    sync.Once
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) init() {
	u.once.Do(func() {
		u.job = u.loadJob()
		if u.job != nil && !u.job.Prompted.IsZero() && u.now().Sub(u.job.Prompted) < promptEvery {
			u.prompts = []time.Time{u.job.Prompted}
		}
	})
}

// Boot settles the job a previous daemon left. cmdrd calls it once its API
// serves, so a restarting job turns done only from a daemon that is up.
func (u *Updater) Boot() {
	u.init()
	_ = prepareBuildRoot(filepath.Join(u.Home, "update-build"), "", u.now())
}

func (u *Updater) restartWatch() time.Duration {
	if u.RestartWatch > 0 {
		return u.RestartWatch
	}
	return defaultRestartWatch
}

// recoveryText is what the person types when an install did not come back.
func (u *Updater) recoveryText(action string) string {
	if action == "rollback" {
		return "Restart cmdrd with `cmdr install --restart-only`."
	}
	return "To go back run `cmdr install --rollback`. If cmdr itself does not start, re-point the links: " +
		"`ln -sf " + filepath.Join(u.InstallDir, "cmdr.prev") + " " + filepath.Join(u.LocalBin, "cmdr") + "` and the same for cmdrd (cmdrd.prev), then restart cmdrd. " +
		"If cmdrd keeps crashing (a crash loop) on the new build, these are the way back."
}

func (u *Updater) jobPath() string { return filepath.Join(u.Home, "update-job.json") }

// loadJob reads the job a previous daemon left and settles its status: a
// restarting job is done when this build is the target, failed when it never
// arrived. It only reads; it never runs anything the file names.
func (u *Updater) loadJob() *Job {
	raw, err := os.ReadFile(u.jobPath())
	if err != nil {
		return nil
	}
	var j Job
	if json.Unmarshal(raw, &j) != nil {
		return nil
	}
	was := j.State
	if j.State == JobRunning || j.State == JobConfirming { // the daemon died mid-job
		j.State, j.Error = JobFailed, "cmdrd stopped while the job was running"
	}
	if j.State == JobRestarting {
		running := strings.TrimSuffix(updatecheck.RunningStamp(), "-dirty")
		switch {
		case j.Target != "" && running != "" && strings.TrimSuffix(j.Target, "-dirty") == running:
			j.State = JobDone
			j.Events = append(j.Events, JobEvent{Seq: len(j.Events) + 1, Time: u.now(), Step: "done", Level: "info", Text: "cmdrd is running " + short(running)})
		case u.now().Sub(j.Started) > u.restartWatch():
			j.State, j.Error = JobFailed, "cmdrd did not come back on the new build. "+u.recoveryText(j.Action)
		}
	}
	if j.State != was {
		u.job = &j
		u.mu.Lock()
		u.saveLocked()
		u.mu.Unlock()
	}
	return &j
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// Status is the check, cached for 60s, with the job (never cached).
func (u *Updater) Status(ctx context.Context, fresh bool) UpdateStatus {
	u.init()
	u.mu.Lock()
	if u.cached == nil || fresh || u.now().Sub(u.cacheAt) > updateCacheTTL {
		u.mu.Unlock()
		st := u.Check(ctx)
		u.mu.Lock()
		u.cached = &UpdateStatus{Status: st, Repo: u.Repo, Checked: u.now(), Commands: commandsFor(u.Repo),
			CanRollback: fileExists(filepath.Join(u.InstallDir, "cmdr.prev"))}
		u.cacheAt = u.now()
	}
	out := *u.cached
	out.Host = hostName()
	if u.PromptAvailable != nil {
		if err := u.PromptAvailable(); err != nil {
			out.NoDialog = err.Error()
		}
	}
	if u.job != nil {
		j := *u.job
		j.Events = append([]JobEvent(nil), u.job.Events...)
		out.Job = &j
	}
	u.mu.Unlock()
	return out
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func commandsFor(repo string) []string {
	if repo == "" {
		repo = "<commander checkout>"
	}
	return []string{"cd " + repo, "just build", "just install --yes --restart-cmdrd"}
}

func (u *Updater) invalidate() {
	u.mu.Lock()
	u.cached = nil
	u.mu.Unlock()
}

// ---- audit log ----

func (u *Updater) audit(action, target, result, reason string) {
	line, _ := json.Marshal(map[string]string{
		"time": u.now().UTC().Format(time.RFC3339), "action": action, "target": target, "result": result, "reason": reason,
	})
	path := filepath.Join(u.Home, "logs", "update-install.jsonl")
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ---- jobs ----

func (u *Updater) emit(step, level, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job == nil {
		return
	}
	u.job.Events = append(u.job.Events, JobEvent{Seq: len(u.job.Events) + 1, Time: u.now(), Step: step, Level: level, Text: text})
	if len(u.job.Events) > maxJobEvents {
		u.job.Events = u.job.Events[len(u.job.Events)-maxJobEvents:]
	}
	u.saveLocked()
}

func (u *Updater) saveLocked() {
	raw, err := json.Marshal(u.job)
	if err != nil {
		return
	}
	tmp := u.jobPath() + ".tmp"
	if os.MkdirAll(filepath.Dir(tmp), 0o755) == nil && os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, u.jobPath())
	}
}

func (u *Updater) setState(state, errText string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job != nil {
		u.job.State, u.job.Error = state, errText
		u.saveLocked()
	}
}

// lineWriter turns what a step prints into job events.
type lineWriter struct {
	u    *Updater
	step string
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		w.emitLine(strings.TrimSpace(string(w.buf[:i])))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.emitLine(strings.TrimSpace(string(w.buf)))
	w.buf = nil
}

// emitLine records one output line. stage.sh keeps going when codesign fails
// (the binaries are then ad-hoc signed and macOS privacy grants may reset), so
// that line is a warning, not info.
func (w *lineWriter) emitLine(line string) {
	if line == "" {
		return
	}
	level := "info"
	if w.step == "stage" && strings.HasPrefix(line, "could not sign ") {
		level = "warn"
	}
	w.u.emit(w.step, level, line)
}

// Refusal is why a job did not start.
type Refusal struct {
	Status int
	Msg    string
}

func (r *Refusal) Error() string { return r.Msg }

// Request checks the preconditions and starts a job that first asks the person
// at the machine; nothing is built unless they confirm. Callers naming an agent
// role are refused before anything is shown. origin "app" only changes the
// dialog wording.
func (u *Updater) Request(ctx context.Context, action, agentRole, origin string) (*Job, error) {
	u.init()
	if agentRole != "" {
		return nil, &Refusal{http.StatusForbidden, "refused for agent role " + agentRole + "; installing needs the user's OK at the machine"}
	}
	if action != "install" && action != "rollback" {
		return nil, &Refusal{http.StatusBadRequest, "action must be install or rollback"}
	}
	// One request at a time, and the floor first: a flood costs no git work.
	u.reqMu.Lock()
	defer u.reqMu.Unlock()
	if why := u.floorRefusal(); why != "" {
		return nil, &Refusal{http.StatusTooManyRequests, why}
	}
	if u.PromptAvailable != nil {
		if err := u.PromptAvailable(); err != nil {
			return nil, &Refusal{http.StatusConflict, "no confirmation dialog can be shown here (" + err.Error() + "); run the terminal commands instead"}
		}
	}
	if u.active() {
		return nil, &Refusal{http.StatusConflict, "an update is already in progress"}
	}
	if why := u.Busy(); why != "" {
		return nil, &Refusal{http.StatusConflict, why}
	}
	var target, text string
	if action == "install" {
		if branch, err := u.Branch(ctx, u.Repo); err != nil || branch != "main" {
			return nil, &Refusal{http.StatusConflict, "the checkout " + u.Repo + " is not on main; the install builds main"}
		}
		st := u.Check(ctx)
		if st.Error != "" {
			return nil, &Refusal{http.StatusConflict, st.Error}
		}
		if st.Dirty {
			return nil, &Refusal{http.StatusConflict, "the main checkout has uncommitted changes; commit or stash them first"}
		}
		if st.UpToDate || st.Main == "" {
			return nil, &Refusal{http.StatusConflict, "already up to date"}
		}
		target, text = st.Main, installDialogText(st)
	} else {
		if !fileExists(filepath.Join(u.InstallDir, "cmdr.prev")) {
			return nil, &Refusal{http.StatusConflict, "no previous build to roll back to"}
		}
		// The revisions come from the binaries themselves: a planted .prev file
		// must not pass as the version it claims in VERSION.prev.
		claimed := binstage.Revision2(u.InstallDir)
		for _, name := range binstage.Binaries {
			rev, _, err := u.BinaryRevision(filepath.Join(u.InstallDir, name+".prev"))
			if err != nil {
				return nil, &Refusal{http.StatusConflict, "cannot read the revision built into " + name + ".prev: " + err.Error()}
			}
			if rev == "" || rev != claimed {
				return nil, &Refusal{http.StatusConflict, name + ".prev carries revision " + short(rev) + ", not the " + short(claimed) + " that VERSION.prev names; nothing rolled back"}
			}
			target = rev
		}
		text = "Roll back Commander to the previous build " + short(target) + " (read from the binaries)?\n\nCurrently installed: " + short(binstage.Revision(u.InstallDir)) +
			"\nChat hosts and Codex bridges keep running; cmdrd restarts."
	}
	text = requestLine(origin) + "\n\n" + text

	u.mu.Lock()
	if u.activeLocked() {
		u.mu.Unlock()
		return nil, &Refusal{http.StatusConflict, "an update is already in progress"}
	}
	now := u.now()
	u.prompts = append(u.recentPromptsLocked(now), now)
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	job := &Job{ID: hex.EncodeToString(id), Action: action, State: JobConfirming, Target: target, Started: now, Prompted: now}
	u.job = job
	u.saveLocked()
	snapshot := *job
	u.mu.Unlock()

	u.audit(action, target, "asked", "job "+snapshot.ID+" origin "+originWord(origin))
	u.emit("confirm", "info", "waiting for your confirmation on screen")
	go u.run(action, target, text)
	return &snapshot, nil
}

func originWord(origin string) string {
	if origin == "app" {
		return "app"
	}
	return "api"
}

// requestLine tells the person where the dialog came from.
func requestLine(origin string) string {
	if origin == "app" {
		return "Requested from the Commander app."
	}
	return "Requested via the local API (not the app's button)."
}

func (u *Updater) recentPromptsLocked(now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range u.prompts {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	return keep
}

// floorRefusal is why no dialog may be shown now, or "": one per five minutes
// and three per hour, whatever the action or target.
func (u *Updater) floorRefusal() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	recent := u.recentPromptsLocked(now)
	for _, t := range recent {
		if now.Sub(t) < promptEvery {
			return "a confirmation was just shown; try again in a few minutes"
		}
	}
	if len(recent) >= promptsPerHour {
		return "too many confirmations this hour; try again later"
	}
	return ""
}

func (u *Updater) active() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.activeLocked()
}

func (u *Updater) activeLocked() bool {
	if u.job == nil {
		return false
	}
	switch u.job.State {
	case JobConfirming, JobRunning:
		return true
	case JobRestarting:
		return u.now().Sub(u.job.Started) < u.restartWatch()+time.Minute
	}
	return false
}

func (u *Updater) run(action, target, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	fail := func(step string, err error) {
		u.emit(step, "error", err.Error())
		u.setState(JobFailed, step+": "+err.Error())
		u.audit(action, target, "failed", step+": "+err.Error())
		u.invalidate()
	}
	title := "Install latest Commander from main"
	if action == "rollback" {
		title = "Roll back Commander"
	}
	ok, err := u.Prompt(ctx, title, text)
	if err != nil || !ok {
		reason := "not confirmed"
		if err != nil {
			reason = "no confirmation: " + err.Error()
		}
		u.emit("confirm", "info", reason+"; nothing was installed")
		// audit first, as on the confirmed path: whoever sees the declined state
		// finds its audit line already written
		u.audit(action, target, "declined", reason)
		u.setState(JobDeclined, reason)
		return
	}
	u.audit(action, target, "confirmed", "")
	u.setState(JobRunning, "")

	opts := binstage.Options{InstallDir: u.InstallDir, LocalBin: u.LocalBin, Repo: u.Repo}
	out := &lineWriter{u: u, step: action}
	opts.Out = out
	defer out.flush()

	if action == "install" {
		if step, err := u.stageAndInstall(ctx, target, opts); err != nil {
			fail(step, err)
			return
		}
	} else {
		u.emit("rollback", "info", "restoring the previous binaries")
		if err := u.Rollback(opts); err != nil {
			fail("rollback", err)
			return
		}
		out.flush()
		u.mu.Lock()
		u.job.Target = binstage.Revision(u.InstallDir)
		u.mu.Unlock()
	}
	out.flush()
	u.invalidate()
	// From here the new daemon reports the result; this one may be gone before
	// Restart returns.
	u.setState(JobRestarting, "")
	u.audit(action, target, "installed", "restarting cmdrd")
	u.emit("restart", "info", "restarting cmdrd; chat hosts and Codex bridges keep running")
	restartOut := &lineWriter{u: u, step: "restart"}
	done, err := u.Restart(restartOut)
	restartOut.flush()
	if err != nil {
		fail("restart", fmt.Errorf("installed, but restarting cmdrd failed: %w. %s", err, u.recoveryText(action)))
		return
	}
	u.watchRestart(action, target, done)
}

// watchRestart runs in the OLD daemon: if the restart process fails, or this
// process is still here when the watch ends, the restart did not take and the
// job fails with the way back. A daemon that was replaced simply never gets here.
func (u *Updater) watchRestart(action, target string, done <-chan error) {
	timer := time.NewTimer(u.restartWatch())
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				u.failRestart(action, target, "the restart command failed: "+err.Error())
				return
			}
			done = nil // it reported success; now wait to be replaced
		case <-timer.C:
			if u.settledDone() {
				return
			}
			u.failRestart(action, target, fmt.Sprintf("cmdrd was not replaced within %s", u.restartWatch()))
			return
		}
	}
}

// settledDone is true when a newer daemon already wrote this job as done.
func (u *Updater) settledDone() bool {
	raw, err := os.ReadFile(u.jobPath())
	if err != nil {
		return false
	}
	var j Job
	u.mu.Lock()
	id := ""
	if u.job != nil {
		id = u.job.ID
	}
	u.mu.Unlock()
	return json.Unmarshal(raw, &j) == nil && j.ID == id && j.State == JobDone
}

func (u *Updater) failRestart(action, target, why string) {
	text := "restart: " + why + ". " + u.recoveryText(action)
	u.emit("restart", "error", text)
	u.setState(JobFailed, text)
	u.audit(action, target, "failed", text)
	u.invalidate()
}

// stageAndInstall builds exactly the confirmed commit in a private export and
// installs it. It returns the failing step. The export is removed on every path.
func (u *Updater) stageAndInstall(ctx context.Context, target string, opts binstage.Options) (string, error) {
	if branch, err := u.Branch(ctx, u.Repo); err != nil || branch != "main" {
		return "check", errors.New("the checkout is no longer on main, nothing installed, try again")
	}
	// No longer the pin (the export is), but a moved main means the person approved something else.
	if st := u.Check(ctx); st.Error != "" || st.Main != target {
		return "check", errors.New("main moved, nothing installed, try again")
	}
	root := filepath.Join(u.Home, "update-build")
	src := filepath.Join(root, target)
	if err := prepareBuildRoot(root, target, u.now()); err != nil {
		return "stage", err
	}
	stageDir := src + ".staged"
	_ = os.RemoveAll(stageDir)
	cleanup, err := u.Export(ctx, u.Repo, target, src)
	if err != nil {
		return "stage", fmt.Errorf("could not export %s: %w", short(target), err)
	}
	defer func() {
		cleanup()
		_ = os.RemoveAll(stageDir)
	}()
	stageOut := &lineWriter{u: u, step: "stage"}
	u.emit("stage", "info", "building "+short(target)+" from a clean export of that commit")
	err = u.Build(ctx, src, stageDir, stageOut)
	stageOut.flush()
	if err != nil {
		return "stage", err
	}
	if got := binstage.Revision(stageDir); got != target {
		return "stage", fmt.Errorf("staged revision %q is not the confirmed %s, nothing installed", got, short(target))
	}
	for _, name := range binstage.Binaries {
		rev, modified, err := u.BinaryRevision(filepath.Join(stageDir, name))
		if err != nil {
			return "stage", fmt.Errorf("cannot read the revision built into %s: %w, nothing installed", name, err)
		}
		if rev != target || modified {
			return "stage", fmt.Errorf("%s carries revision %q (modified=%v), not exactly the confirmed %s, nothing installed", name, short(rev), modified, short(target))
		}
	}
	opts.StageDir = stageDir
	u.emit("install", "info", "copying binaries, keeping the previous ones as .prev")
	if err := u.Install(opts); err != nil {
		return "install", err
	}
	return "", nil
}

// staleBuildAge is how old another target's build dir must be before it is
// treated as a crashed build's leftover.
const staleBuildAge = time.Hour

// prepareBuildRoot makes the update-build dir private (0700 even when it
// already existed) and removes leftovers of crashed builds: entries for another
// target that are older than an hour. Entries for keep are left to the build.
func prepareBuildRoot(root, keep string, now time.Time) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == keep || name == keep+".staged" {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleBuildAge {
			_ = os.RemoveAll(filepath.Join(root, name))
		}
	}
	return nil
}

// installDialogText is what the person reads before pressing Install: the exact
// revision, how far behind, the newest commits and their review markers.
func installDialogText(st updatecheck.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Install revision %s from main?\n%d %s not installed yet (installed: %s).\n\n", short(st.Main), st.Behind,
		map[bool]string{true: "commit is", false: "commits are"}[st.Behind == 1], orNone(short(st.Installed)))
	for i, c := range st.Commits {
		if i == 10 {
			hidden, unconfirmed := st.Behind-10, 0
			for _, h := range st.Commits[10:] {
				if h.Review != updatecheck.Reviewed {
					unconfirmed++
				}
			}
			fmt.Fprintf(&b, "... and %d more", hidden)
			if hidden == len(st.Commits)-10 {
				fmt.Fprintf(&b, " (%d of them not confirmed)\n", unconfirmed)
			} else {
				fmt.Fprintf(&b, " (%d of the next %d not confirmed; the rest are not listed)\n", unconfirmed, len(st.Commits)-10)
			}
			break
		}
		mark := "not confirmed"
		if c.Review == updatecheck.Reviewed {
			mark = "reviewed"
		}
		fmt.Fprintf(&b, "%s  %s  [%s]\n", short(c.Hash), cleanLine(c.Subject), mark)
	}
	b.WriteString("\nReview markers are best effort and can be forged by an agent.\ncmdrd restarts; chat hosts and Codex bridges keep running.")
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// cleanLine keeps commit subjects (untrusted text) printable and short.
func cleanLine(s string) string {
	// Drop control, format (bidi overrides, zero-width), and line/paragraph
	// separator runes; a stripped rune must not glue words together.
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "..."
	}
	return s
}

// ---- HTTP ----

func (s *Server) updateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/update", s.auth(http.HandlerFunc(s.serveUpdate)))
	mux.Handle("POST /api/update/install", s.auth(http.HandlerFunc(s.serveUpdateInstall)))
}

func (s *Server) updater(w http.ResponseWriter) *Updater {
	if s.opts.Update == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "update not available"})
		return nil
	}
	return s.opts.Update
}

func (s *Server) serveUpdate(w http.ResponseWriter, r *http.Request) {
	if u := s.updater(w); u != nil {
		writeJSON(w, http.StatusOK, u.Status(r.Context(), r.URL.Query().Get("fresh") == "1"))
	}
}

// serveUpdateInstall starts an install or rollback. It answers 202 at once; the
// job waits for the person at the native dialog and GET /api/update shows it.
func (s *Server) serveUpdateInstall(w http.ResponseWriter, r *http.Request) {
	u := s.updater(w)
	if u == nil {
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad body"})
			return
		}
	}
	if req.Action == "" {
		req.Action = "install"
	}
	job, err := u.Request(r.Context(), req.Action, r.Header.Get(RoleHeader), r.Header.Get(OriginHeader))
	if err != nil {
		var ref *Refusal
		status := http.StatusInternalServerError
		if errors.As(err, &ref) {
			status = ref.Status
		}
		u.audit(req.Action, "", "refused", err.Error())
		writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "job": job})
}
