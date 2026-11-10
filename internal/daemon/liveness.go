package daemon

import (
	"cmp"
	"context"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// liveEvery is how stale the snapshot's liveness fact may be: a claim on an
// Operator that died reads as stopped within it, not at the next reap.
const liveEvery = 10 * time.Second

// liveOperators is the snapshot's liveness fact: the Operators whose process
// is alive (a running run whose session is neither gone nor back at a shell),
// looked up at most every liveEvery.
type liveOperators struct {
	every   time.Duration                                       // zero retains the legacy interval
	list    func(ctx context.Context) ([]store.AgentRun, error) // spawn.LiveRuns over the store and tmux
	now     func() time.Time
	mu      sync.Mutex
	at      time.Time
	names   []string
	ok      bool
	looking chan struct{} // closed when the look under way ends; nil = none
	// finished lists the running rows the reaper will close as exited, report
	// reads whether one left a final report, reported keeps those that did, and
	// running lists every running row, so an entry whose row closed goes. A nil
	// finished looks for none.
	finished func(ctx context.Context) ([]store.AgentRun, error)
	report   func(ctx context.Context, run store.AgentRun) (string, error)
	running  func(ctx context.Context) ([]store.AgentRun, error)
	reported *reportedRuns
	unsaid   map[int64]bool // runs read and found to have no report; only the look touches it
}

// get returns the live Operators' names and when they were looked up; ok is
// false when it could not tell (the claims are then taken as recorded). The look
// reads tmux and comments with no lock held: others meanwhile answer from the
// last look, and wait only when there is none. Its names and its reports appear
// together, so no snapshot sees a reported run's end before its report.
func (l *liveOperators) get() ([]string, time.Time, bool) {
	l.mu.Lock()
	now := l.now()
	every := l.every
	if every <= 0 {
		every = liveEvery
	}
	if l.ok && (now.Sub(l.at) < every || l.looking != nil) {
		defer l.mu.Unlock()
		return l.names, l.at, true
	}
	if l.looking != nil {
		done := l.looking
		l.mu.Unlock()
		<-done
		l.mu.Lock()
		defer l.mu.Unlock()
		if !l.ok {
			return nil, time.Time{}, false
		}
		return l.names, l.at, true
	}
	done := make(chan struct{})
	l.looking = done
	l.mu.Unlock()

	names, err := l.look(now)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.looking = nil
	close(done)
	if err != nil {
		log.Printf("daemon: live operators: %v", err)
		l.ok = false
		return nil, time.Time{}, false
	}
	l.names, l.at, l.ok = names, now, true
	return names, now, true
}

// look reads who runs now, then notes the runs that ended.
func (l *liveOperators) look(now time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runs, err := l.list(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(runs))
	for _, r := range runs {
		names = append(names, r.AgentName)
	}
	l.noteFinished(now)
	return names, nil
}

// noteFinished records the finished runs that left a report. Each is read once;
// a failed read leaves the row as it stands and is tried again at the next look.
// It then drops the entries whose row has closed with no settle coming.
func (l *liveOperators) noteFinished(now time.Time) {
	if l.finished == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runs, err := l.finished(ctx)
	if err != nil {
		log.Printf("daemon: finished operators: %v", err)
		return
	}
	unsaid := map[int64]bool{}
	for _, r := range runs {
		if l.unsaid[r.ID] {
			unsaid[r.ID] = true
			continue
		}
		if l.reported.seen(r.ID) {
			continue
		}
		report, err := l.report(ctx, r)
		if err != nil {
			log.Printf("daemon: report of %s: %v", r.TaskBeadID, err)
			continue
		}
		if report == "" {
			unsaid[r.ID] = true
			continue
		}
		l.reported.add(r, now)
	}
	l.unsaid = unsaid
	// Read after the adds, so a row closed since the list above is caught now.
	open, err := l.running(ctx)
	if err != nil {
		log.Printf("daemon: running operators: %v", err)
		return
	}
	ids := make(map[int64]bool, len(open))
	for _, r := range open {
		ids[r.ID] = true
	}
	l.reported.prune(ids, now)
}

// reportedMaxAge bounds how long a run's end speaks for its step: a settle still
// not landed then has failed (its retries stop at maxSettleAge), so the step reads
// as its bead says, and the board flags it.
const reportedMaxAge = maxSettleAge

// reportedRuns is what the board reads of runs that ended with a final report:
// noted at the first look that sees one, or when its settle starts, and over when
// the settle has landed or the run's row closed with no settle coming (a switch of
// runtime or account closes the row itself). An entry stays while its row runs
// and a settled one until it ages out, so neither a stuck sweep nor a look that
// read the run before the settle can open it again with a new time.
type reportedRuns struct {
	mu     sync.Mutex
	now    func() time.Time
	runs   map[int64]*reportedRun
	sweeps int // sweeps under way: a row they closed has its settle still to start
}

type reportedRun struct {
	livedata.SnapReported
	claimed time.Time // when a settle took it; a closed row then has a settle coming
	settled bool
}

func newReportedRuns(now func() time.Time) *reportedRuns {
	return &reportedRuns{now: now, runs: map[int64]*reportedRun{}}
}

func (r *reportedRuns) add(run store.AgentRun, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(run, at)
}

func (r *reportedRuns) addLocked(run store.AgentRun, at time.Time) *reportedRun {
	e, ok := r.runs[run.ID]
	if !ok {
		e = &reportedRun{SnapReported: livedata.SnapReported{
			Operator: beads.OwnerLabelName(run.AgentName), ProjectID: run.ProjectID, Bead: run.TaskBeadID, Since: at.Unix(),
		}}
		r.runs[run.ID] = e
	}
	return e
}

// claim marks an entry of run, if it has one, as having its settle under way.
func (r *reportedRuns) claim(run store.AgentRun, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.runs[run.ID]; ok {
		e.claimed = at
	}
}

// settling is a settle of a finished run starting: the window opens if no look
// opened it, and lasts through failed attempts.
func (r *reportedRuns) settling(run store.AgentRun, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(run, at).claimed = at
}

func (r *reportedRuns) done(run store.AgentRun, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(run, at).settled = true
}

func (r *reportedRuns) seen(id int64) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[id]
	return ok
}

// beginSweep and endSweep bracket a sweep from before it closes rows until each
// closed run's settle has started.
func (r *reportedRuns) beginSweep() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweeps++
}

func (r *reportedRuns) endSweep() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweeps--
}

// prune forgets the entries whose run is not in running: at once when no settle is
// coming, a settled one once aged, a claimed one once its retries are long over.
func (r *reportedRuns) prune(running map[int64]bool, now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-reportedMaxAge).Unix()
	for id, run := range r.runs {
		switch {
		case running[id]:
		case run.settled:
			if run.Since <= cutoff {
				delete(r.runs, id)
			}
		case !run.claimed.IsZero():
			if now.Sub(run.claimed) >= 2*reportedMaxAge {
				delete(r.runs, id)
			}
		case r.sweeps == 0:
			delete(r.runs, id)
		}
	}
}

// list is the runs still between their end and their settle, oldest first.
func (r *reportedRuns) list() []livedata.SnapReported {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-reportedMaxAge).Unix()
	var out []livedata.SnapReported
	for _, run := range r.runs {
		if run.Since > cutoff && !run.settled {
			out = append(out, run.SnapReported)
		}
	}
	slices.SortFunc(out, func(a, b livedata.SnapReported) int {
		return cmp.Or(cmp.Compare(a.Since, b.Since), cmp.Compare(a.Bead, b.Bead))
	})
	return out
}
