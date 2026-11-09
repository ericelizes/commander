package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// wakeGap is how far apart two minute ticks must be before the second one is
// treated as a wake from sleep (or a stall): a missed tick is a minute, so
// three leaves room for a slow sweep.
const wakeGap = 3 * time.Minute

// sleepWatch measures the wall-clock time the minute loop spent waiting for its
// ticker: from done (end of an iteration) to tick (the ticker fired), so a slow
// sweep or snapshot load is not mistaken for a sleep. It compares wall time
// only (Round(0) strips the monotonic reading): on macOS the monotonic clock
// does not advance while the Mac sleeps, so time.Since after a 10-hour sleep
// reads about a minute.
type sleepWatch struct {
	now  func() time.Time
	last time.Time
}

// done marks the end of an iteration.
func (w *sleepWatch) done() { w.last = w.now().Round(0) }

// tick returns the wait since done, and whether it is long enough to be a
// sleep. Before the first done there is no gap.
func (w *sleepWatch) tick() (time.Duration, bool) {
	if w.last.IsZero() {
		return 0, false
	}
	gap := w.now().Round(0).Sub(w.last)
	return gap, gap > wakeGap
}

// diedWhileAsleep settles a run found dead after a sleep: the bead opens with
// no owner, is respawned within the auto-respawn cap, else the Manager is
// asked to decide.
func (s runSettler) diedWhileAsleep(ctx context.Context, run store.AgentRun, gap time.Duration) error {
	subject := fmt.Sprintf("Operator %s died while the Mac slept (gap %d min): %s", run.AgentName, int(gap.Minutes()), run.TaskBeadID)
	body := fmt.Sprintf("%s's session is gone after the Mac slept for %d min. %s is open again with no owner. Read what it left on the bead (bd show %s); decide: respawn or drop.", run.AgentName, int(gap.Minutes()), run.TaskBeadID, run.TaskBeadID)
	return s.diedWith(ctx, run, true, asleepWhy, subject, body)
}

// afterWake settles the runs a sweep closed right after a wake. Runs whose
// session vanished died; so did runs left at a bare shell without a final
// report (the sleep killed claude, the launcher's shell stayed). Runs that did
// write their report finished normally. It returns the number that died, and
// mails Commander one summary when more than two did. A failed settle is
// handed to retry.
func (s runSettler) afterWake(ctx context.Context, res spawn.HostReapResult, gap time.Duration, retry func(func(context.Context) error)) int {
	died := 0
	s.settleStopped(ctx, res, retry)
	for _, run := range res.Died {
		died++
		s.settleOrRetry(ctx, retry, "died", run, func(c context.Context) error { return s.diedWhileAsleep(c, run, gap) })
	}
	for _, run := range res.Exited {
		if s.finalReport(ctx, run) != "" {
			s.settleOrRetry(ctx, retry, "exited", run, func(c context.Context) error { return s.exited(c, run) })
			continue
		}
		died++
		s.settleOrRetry(ctx, retry, "died", run, func(c context.Context) error { return s.diedWhileAsleep(c, run, gap) })
	}
	if died > 2 {
		if err := s.tell(ctx, "commander", fmt.Sprintf("%d Operators died while the Mac slept", died),
			fmt.Sprintf("The Mac was asleep or stalled for %d min. %d Operator runs ended; each Manager was mailed to decide: respawn or drop.", int(gap.Minutes()), died)); err != nil {
			log.Printf("daemon: wake summary: %v", err)
		}
	}
	return died
}

// settleReaped settles the runs a normal (no wake) sweep closed; failures are
// queued on retry. restart is true in the first sweep after cmdrd started.
func (s runSettler) settleReaped(ctx context.Context, res spawn.HostReapResult, restart bool, retry func(func(context.Context) error)) {
	s.settleStopped(ctx, res, retry)
	for _, run := range res.Exited {
		run := run
		s.settleOrRetry(ctx, retry, "exited", run, func(c context.Context) error { return s.exited(c, run) })
	}
	for _, run := range res.Died {
		run := run
		s.settleOrRetry(ctx, retry, "died", run, func(c context.Context) error { return s.died(c, run, restart) })
	}
}

// settleStopped settles the runs a sweep found stopped on request.
func (s runSettler) settleStopped(ctx context.Context, res spawn.HostReapResult, retry func(func(context.Context) error)) {
	for _, stop := range res.Stopped {
		stop := stop
		s.settleOrRetry(ctx, retry, "stopped", stop.Run, func(c context.Context) error { return s.stoppedOnRequest(c, stop) })
	}
}

// settleOrRetry runs a settle; on failure it queues a retry. A failure after
// part of the settle took effect (partialErr) queues only the rest; otherwise
// the retry first re-reads the bead and gives up when its state moved on.
func (s runSettler) settleOrRetry(ctx context.Context, retry func(func(context.Context) error), what string, run store.AgentRun, f func(context.Context) error) {
	s.reported.claim(run, s.clock())
	err := f(ctx)
	if err == nil {
		s.reported.done(run, s.clock())
		return
	}
	log.Printf("daemon: settle %s run %d: %v (will retry)", what, run.ID, err)
	var pe *partialErr
	if errors.As(err, &pe) {
		s.reported.done(run, s.clock()) // its bead's move landed: only the rest is left
		retry(pe.rest)
		return
	}
	retry(func(c context.Context) error {
		if !s.stillSettling(c, run) {
			log.Printf("daemon: settle %s run %d: bead %s moved on, dropped", what, run.ID, run.TaskBeadID)
			s.reported.done(run, s.clock())
			return nil
		}
		err := f(c)
		if err == nil {
			s.reported.done(run, s.clock())
		}
		return err
	})
}

// stillSettling reports whether the run's bead is still as the closed run left
// it: present, not closed, and not claimed by another Operator since. A bead it
// cannot read counts as settling (the retry then fails again and ages out).
func (s runSettler) stillSettling(ctx context.Context, run store.AgentRun) bool {
	p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		return true
	}
	b, err := s.beads.Show(ctx, p.Path, run.TaskBeadID)
	if err != nil {
		return true
	}
	if b == nil || b.Status == "closed" {
		return false
	}
	if b.Status == "in_progress" {
		self := beads.OwnerLabelName(run.AgentName)
		for _, l := range b.Labels {
			o := strings.TrimPrefix(l, beads.OwnerLabelPrefix)
			if o != l && strings.HasPrefix(o, "operator-") && o != self {
				return false
			}
		}
	}
	return true
}

// Retries stop after this many attempts or this long: a settle that still fails
// (project removed, bead gone) is the Manager's to find, not a loop forever.
const (
	maxSettleAttempts = 10
	maxSettleAge      = 30 * time.Minute
)

// settleRetries holds settles that failed (br busy, locked, killed after a
// wake) so the next sweeps run them again: the run row is already closed, so
// nothing else would move its bead.
type settleRetries struct {
	pending []*settleRetry
	now     func() time.Time // nil = time.Now
}

type settleRetry struct {
	f        func(context.Context) error
	attempts int
	added    time.Time
}

func (r *settleRetries) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *settleRetries) add(f func(context.Context) error) {
	r.pending = append(r.pending, &settleRetry{f: f, added: r.clock()})
}

func (r *settleRetries) run(ctx context.Context) {
	todo := r.pending
	r.pending = nil
	for _, it := range todo {
		if r.clock().Sub(it.added) > maxSettleAge {
			log.Printf("daemon: settle retry dropped after %s", maxSettleAge)
			continue
		}
		err := it.f(ctx)
		if err == nil {
			continue
		}
		var pe *partialErr
		if errors.As(err, &pe) {
			it.f = pe.rest
		}
		it.attempts++
		if it.attempts >= maxSettleAttempts {
			log.Printf("daemon: settle retry dropped after %d attempts: %v", it.attempts, err)
			continue
		}
		log.Printf("daemon: settle retry: %v", err)
		r.pending = append(r.pending, it)
	}
}

// Low battery: on macOS, Operators die when the Mac sleeps, and it sleeps on a
// flat battery.
const (
	lowBatteryAt   = 10
	lowBatteryOver = 20
)

// batteryWatcher mails Commander once per discharge cycle when the battery is
// at or below lowBatteryAt on battery power. The latch clears on charging or
// above lowBatteryOver.
type batteryWatcher struct {
	// read returns `pmset -g batt` output; nil means no battery reading here.
	read    func(ctx context.Context) (string, error)
	mail    func(ctx context.Context, toRole, subject, body string) error
	latched bool
}

func newBatteryWatcher(mail func(ctx context.Context, toRole, subject, body string) error) *batteryWatcher {
	w := &batteryWatcher{mail: mail}
	if runtime.GOOS == "darwin" {
		w.read = func(ctx context.Context) (string, error) {
			out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
			return string(out), err
		}
	}
	return w
}

var batteryPct = regexp.MustCompile(`(\d+)%`)

// parseBattery reads pmset output: the percentage, and whether the Mac runs on
// battery power (not AC). ok is false when there is no battery line.
func parseBattery(out string) (pct int, onBattery, ok bool) {
	m := batteryPct.FindStringSubmatch(out)
	if m == nil {
		return 0, false, false
	}
	pct, _ = strconv.Atoi(m[1])
	return pct, strings.Contains(out, "Battery Power") && !strings.Contains(out, "AC Power"), true
}

func (w *batteryWatcher) check(ctx context.Context) {
	if w == nil || w.read == nil || w.mail == nil {
		return
	}
	out, err := w.read(ctx)
	if err != nil {
		return
	}
	pct, onBattery, ok := parseBattery(out)
	if !ok {
		return
	}
	if !onBattery || pct > lowBatteryOver {
		w.latched = false
		return
	}
	if pct > lowBatteryAt || w.latched {
		return
	}
	w.latched = true
	if err := w.mail(ctx, "commander", fmt.Sprintf("Battery at %d%%", pct),
		fmt.Sprintf("Battery at %d%%, Operators die when the Mac sleeps; plug in.", pct)); err != nil {
		log.Printf("daemon: low battery mail: %v", err)
		w.latched = false
	}
}
