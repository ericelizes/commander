package livedata

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// SnapLive is what cmdrd saw running when it built the snapshot. A bead's
// claim says who is on a step; this says whether that agent is alive, and the
// board believes it over the claim.
type SnapLive struct {
	At        int64    `json:"-"`         // unix s cmdrd looked (not served: it moves every look, the snapshot would change with it)
	Operators []string `json:"operators"` // live Operators, by owner-label name
	// Reported are runs that ended with a final report and whose settle has not landed.
	Reported []SnapReported `json:"reported,omitempty"`
}

// SnapReported is one Operator run that ended on its own with a final report,
// from the moment cmdrd saw that until its settle has written the step over to
// the Manager. The claim on the bead still names the Operator (or is half
// rewritten), so the step reads as the settle will leave it.
type SnapReported struct {
	Operator  string `json:"operator"` // by owner-label name
	ProjectID int64  `json:"project_id"`
	Bead      string `json:"bead"`
	Since     int64  `json:"since"` // unix s it was seen ended: the Manager's timer runs from here
}

// holds reports whether the run's end still speaks for a step in this state. A
// step someone else has claimed, or one back on a live Operator (a same-name
// respawn), a closed or parked one, a user card or a recorded handoff is not
// its to answer for: each reads as its own markers say.
func (r SnapReported) holds(status, state, owner, manager, waiting string, live LiveOperators) bool {
	switch {
	case status == "closed", status == beads.StatusDeferred, state == beads.StateNeedsUser, state == beads.StateDone:
		return false
	case live.has(r.Operator), strings.Contains(" · "+waiting+" · ", " · "+beads.ManagerCloseWait+" · "):
		return false
	}
	// No owner is a settle between its label writes, which leave the step in
	// progress; on a step not started it is a reopen or a death, and holds nothing.
	if owner == "" {
		return status == "in_progress"
	}
	name := beads.OwnerLabelName(owner)
	return name == r.Operator || name == beads.OwnerLabelName(manager)
}

// wait is the Manager-to-close wait the settle will write.
func (r SnapReported) wait() beads.Wait {
	since := time.Unix(r.Since, 0)
	return beads.Wait{Kind: beads.WaitUntil, Since: since, Until: since.Add(beads.ManagerCloseWithin), Ref: beads.ManagerCloseWait}
}

// MarkReported reads each reported run's step as its settle will leave it: in
// review, on its Manager, with the time wait. Run after MarkLiveOwners, which
// took the ended Operator for a death. The loaded rows are not edited.
func MarkReported(rows []ObjectiveRow, reported []SnapReported, live LiveOperators) []ObjectiveRow {
	if len(reported) == 0 {
		return rows
	}
	parents := make(map[store.WorkstreamKey]ObjectiveRow, len(rows))
	for _, r := range rows {
		parents[store.WorkstreamKey{ProjectID: r.ProjectID, BeadID: r.BeadID}] = r
	}
	rows = slices.Clone(rows)
	for _, rep := range reported {
		for i := range rows {
			r := &rows[i]
			if r.ProjectID != rep.ProjectID || r.BeadID != rep.Bead {
				continue
			}
			// The settle hands it to its owner: the nearest nested workstream's
			// Manager, else the root's.
			root := r.BeadID
			for cur := *r; ; {
				if cur.Owns {
					root = cur.BeadID
					break
				}
				root = cur.BeadID
				next, ok := parents[store.WorkstreamKey{ProjectID: r.ProjectID, BeadID: cur.ParentBeadID}]
				if cur.ParentBeadID == "" || !ok {
					if cur.ParentBeadID != "" {
						root = cur.ParentBeadID
					}
					break
				}
				cur = next
			}
			manager := store.WorkstreamManagerRole(root)
			if rep.holds(r.Status, r.State, r.Owner, manager, beads.WaitsText(r.Waits), live) {
				r.Owner, r.OwnerState, r.OwnerWhy, r.State = manager, "", "", beads.StateReview
				r.Waits = append(slices.Clone(r.Waits), rep.wait())
			}
		}
	}
	return rows
}

// LiveOperators is the live Operators by owner-label name; nil = not known.
type LiveOperators map[string]bool

func (l LiveOperators) has(role string) bool { return l[beads.OwnerLabelName(role)] }

func (s *SnapLive) operators() LiveOperators {
	if s == nil {
		return nil
	}
	out := LiveOperators{}
	for _, o := range s.Operators {
		out[beads.OwnerLabelName(o)] = true
	}
	return out
}

// NewSnapLive is the liveness fact for a snapshot: the Operators cmdrd found
// alive at at, plus runs spawned after that look (a claim on one is newer
// than the check, so it is not taken for dead).
func NewSnapLive(alive []string, at int64, running []OperatorRow) *SnapLive {
	l := &SnapLive{At: at, Operators: []string{}}
	add := func(role string) {
		if n := beads.OwnerLabelName(role); !slices.Contains(l.Operators, n) {
			l.Operators = append(l.Operators, n)
		}
	}
	for _, o := range alive {
		add(o)
	}
	for _, r := range running {
		if r.SpawnTime.Unix() >= at {
			add(r.AgentName)
		}
	}
	slices.Sort(l.Operators)
	return l
}

// The why of a claim the liveness fact corrected, and of a settled death.
const (
	whyNotRunning = "Operator is no longer running and has not reported"
	whyWaitedGone = "The Operator it waited on is no longer running"
)

// liveClaim is a claim's owner state checked against live: an Operator that
// is not running holds nothing, whatever its run row last said. changed is
// true when live overrode the record.
func liveClaim(owner, ownerState, state string, live LiveOperators) (string, bool) {
	if live == nil || !strings.HasPrefix(owner, "operator-") {
		return ownerState, false
	}
	if live.has(owner) {
		return OwnerWorking, false
	}
	if ownerState == OwnerDied || ownerState == OwnerFinished {
		return ownerState, false
	}
	if state == beads.StateReview || state == beads.StateNeedsUser || state == beads.StateDone {
		return OwnerFinished, true
	}
	return OwnerDied, true
}

// MarkLiveOwners corrects rows' claims and Operator waits with live (nil
// leaves them as the store has them), before anything is derived from them.
func MarkLiveOwners(rows []ObjectiveRow, live LiveOperators) []ObjectiveRow {
	if live == nil {
		return rows
	}
	rows = slices.Clone(rows) // the loaded rows are kept as last-good data: never edit them
	for i := range rows {
		r := &rows[i]
		if r.Status == "closed" || r.Status == beads.StatusDeferred {
			continue
		}
		if st, changed := liveClaim(r.Owner, r.OwnerState, r.State, live); changed {
			r.OwnerState, r.OwnerWhy = st, whyNotRunning
		} else {
			r.OwnerState = st
		}
		kept := r.Waits[:0:0]
		for _, w := range r.Waits {
			if w.Kind != beads.WaitOperator || live.has(w.Ref) {
				kept = append(kept, w)
			}
		}
		if len(kept) == len(r.Waits) {
			continue
		}
		r.Waits = kept
		if len(kept) == 0 && r.OwnerState != OwnerWorking {
			r.OwnerState, r.OwnerWhy = OwnerDied, whyWaitedGone
		}
	}
	return rows
}

// LiveRuns keeps the running rows whose Operator live finds alive.
func LiveRuns(ops []OperatorRow, live LiveOperators) []OperatorRow {
	if live == nil {
		return ops
	}
	out := ops[:0:0]
	for _, o := range ops {
		if live.has(o.AgentName) {
			out = append(out, o)
		}
	}
	return out
}

// diedWhy is a settled death's why: what the settler recorded and how often
// cmdrd restarted the Operator before it gave the step back.
func diedWhy(reason string, restarts int) string {
	why := "Operator stopped without a report"
	if strings.Contains(reason, "slept") {
		why = "Operator stopped while the machine slept"
	}
	switch {
	case restarts == 1:
		why += "; restarted once, stopped again"
	case restarts > 1:
		why += "; restarted " + strconv.Itoa(restarts) + " times, stopped again"
	}
	return why
}

// markDied fills the why and the time of steps whose Operator cmdrd settled as
// died, from the newest operator-died event on each bead.
func markDied(rows []ObjectiveRow, events []store.CostEvent) []ObjectiveRow {
	latest := map[string]store.CostEvent{}
	for _, e := range events {
		if e.EventType != store.EventOperatorDied || !e.TaskBeadID.Valid {
			continue
		}
		k := strconv.FormatInt(e.ProjectID, 10) + "\x00" + e.TaskBeadID.String
		if old, ok := latest[k]; !ok || e.OccurredAt >= old.OccurredAt {
			latest[k] = e
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.OwnerState != OwnerDied || r.Owner != "" || r.Status == "closed" {
			continue
		}
		e, ok := latest[strconv.FormatInt(r.ProjectID, 10)+"\x00"+r.BeadID]
		r.OwnerWhy = diedWhy(e.Reason.String, r.Restarts)
		if ok {
			r.OwnerSince = e.OccurredAt
		}
	}
	return rows
}

// restartsOf counts the automatic restarts cmdrd made on a bead (its
// auto-respawn:<n> and crash-respawn:<unix> labels).
func restartsOf(labels []string) int {
	n := 0
	for _, l := range labels {
		if strings.HasPrefix(l, beads.AutoRespawnLabelPrefix) || strings.HasPrefix(l, beads.CrashRespawnLabelPrefix) {
			n++
		}
	}
	return n
}

// withLiveness is s with every claim checked against s.Live (unchanged
// without one). Steps are copied, so the caller's snapshot is not edited.
func withLiveness(s Snapshot) Snapshot {
	live := s.Live.operators()
	if live == nil {
		return s
	}
	var fix func(SnapWorkstream, []SnapStep) []SnapStep
	fix = func(w SnapWorkstream, steps []SnapStep) []SnapStep {
		out := make([]SnapStep, len(steps))
		for i, st := range steps {
			st.Children = fix(w, st.Children)
			if !stepClosed(st) && !parked(st) {
				st = liveStep(st, live)
				st = reportedStep(st, w, s.Live.Reported, live)
			}
			out[i] = st
		}
		return out
	}
	ws := make([]SnapWorkstream, len(s.Workstreams))
	for i, w := range s.Workstreams {
		w.Steps = fix(w, w.Steps)
		ws[i] = w
	}
	s.Workstreams = ws
	if s.Watch != nil {
		watch := make(map[string]SnapWatch, len(s.Watch))
		for role, w := range s.Watch {
			ops := []SnapWatchedOp{}
			for _, o := range w.Operators {
				if live.has(o.Role) {
					ops = append(ops, o)
				}
			}
			w.Operators = ops
			watch[role] = w
		}
		s.Watch = watch
	}
	return s
}

// reportedStep is MarkReported for one snapshot step.
func reportedStep(st SnapStep, w SnapWorkstream, reported []SnapReported, live LiveOperators) SnapStep {
	for _, rep := range reported {
		if rep.ProjectID != w.ProjectID || rep.Bead != st.BeadID || !rep.holds(st.Status, st.State, st.Owner, w.ManagerRole, st.Waiting, live) {
			continue
		}
		st.Owner, st.OwnerState, st.OwnerWhy, st.State, st.Working, st.Broken = w.ManagerRole, "", "", beads.StateReview, false, false
		wait := rep.wait()
		if st.Waiting != "" {
			st.Waiting += " · "
		}
		st.Waiting += wait.Text()
		stepWaits(&st, []beads.Wait{wait})
	}
	return st
}

// liveStep is MarkLiveOwners for one snapshot step (its waits are text).
func liveStep(st SnapStep, live LiveOperators) SnapStep {
	if os, changed := liveClaim(st.Owner, st.OwnerState, st.State, live); changed {
		st.OwnerState, st.OwnerWhy, st.Working = os, whyNotRunning, false
	} else {
		st.OwnerState = os
	}
	if st.Waiting == "" {
		return st
	}
	parts := strings.Split(st.Waiting, " · ")
	kept := slices.DeleteFunc(slices.Clone(parts), func(w string) bool { return strings.HasPrefix(w, "operator-") && !live.has(w) })
	if len(kept) == len(parts) {
		return st
	}
	st.Waiting = strings.Join(kept, " · ")
	if len(kept) == 0 && st.OwnerState != OwnerWorking {
		st.OwnerState, st.OwnerWhy, st.WaitSince = OwnerDied, whyWaitedGone, 0
	}
	return st
}
