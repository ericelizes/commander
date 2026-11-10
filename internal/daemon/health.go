package daemon

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/wsmgr"
)

// healthBoard is what keeps failing in cmdrd. Every failure that repeats is
// reported here under a key (mail:<role>, start:<role>); a key still failing
// after healthGrace is a problem: the app shows it, Commander is mailed once
// and the check-in lists it, until a success for the key clears it. Errors
// that only ever reached the log are how mail sat undeliverable for hours.
type healthBoard struct {
	mu     sync.Mutex
	issues map[string]*healthIssue
	now    func() time.Time
	// alert tells Commander about a new problem (nil: not wired).
	alert func(key, detail string, since time.Time) error
}

type healthIssue struct {
	first, last time.Time
	detail      string
	alerted     bool
	// alertAfter holds back the next alert after alertFails failed sends.
	alertAfter time.Time
	alertFails int
}

// healthGrace is how long a failure may repeat before it is a problem: long
// enough to ride out a restart, short enough that nobody finds it by asking.
const healthGrace = 2 * time.Minute

// timedHealthLoad builds the board for the health loop. Its load holds the same
// per-project read slot as the API's, so a slow one is what an API load waits behind.
func timedHealthLoad(ctx context.Context, load func(context.Context) livedata.Snapshot, slow time.Duration) {
	start := time.Now()
	snap := load(beads.WithReadCaller(ctx, "health"))
	if took := time.Since(start); took > slow {
		log.Printf("daemon: health loop load took %s (%s)", took.Round(time.Millisecond), snap.LoadTimes())
	}
}

// healthEvery is how often cmdrd checks the board's health with nobody watching.
const healthEvery = 10 * time.Second

// healthStale drops a key nothing has reported for this long (its source stopped trying).
const healthStale = 10 * time.Minute

func newHealthBoard(now func() time.Time) *healthBoard {
	return &healthBoard{issues: map[string]*healthIssue{}, now: now}
}

// fail records one more failure for key.
func (h *healthBoard) fail(key, detail string) {
	if h == nil {
		return
	}
	now := h.now()
	h.mu.Lock()
	is, ok := h.issues[key]
	if !ok {
		is = &healthIssue{first: now}
		h.issues[key] = is
	}
	is.last, is.detail = now, detail
	h.mu.Unlock()
}

// ok clears key: whatever failed works again.
func (h *healthBoard) ok(key string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	is, found := h.issues[key]
	delete(h.issues, key)
	h.mu.Unlock()
	if found && is.alerted {
		log.Printf("daemon: health: %s recovered", key)
	}
}

// list is every current problem, oldest first. Source.Snapshot calls it, so it
// never sends: alertDue does, on its own ticker.
func (h *healthBoard) list() []livedata.SnapHealth {
	if h == nil {
		return nil
	}
	now := h.now()
	var out []livedata.SnapHealth
	h.mu.Lock()
	for key, is := range h.issues {
		if now.Sub(is.last) > healthStale {
			delete(h.issues, key)
			continue
		}
		if now.Sub(is.first) < healthGrace {
			continue
		}
		out = append(out, livedata.SnapHealth{Key: key, Detail: is.detail, Since: is.first.Unix()})
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Since < out[j].Since || (out[i].Since == out[j].Since && out[i].Key < out[j].Key)
	})
	return out
}

// alertDue tells whoever needs to hear about each problem not yet told, or whose
// failed send is due again. One at a time, each up to healthAlertBudget.
func (h *healthBoard) alertDue() {
	if h == nil {
		return
	}
	now := h.now()
	var fresh []livedata.SnapHealth
	h.mu.Lock()
	for key, is := range h.issues {
		if is.alerted || now.Sub(is.last) > healthStale || now.Sub(is.first) < healthGrace || now.Before(is.alertAfter) {
			continue
		}
		is.alerted = true
		fresh = append(fresh, livedata.SnapHealth{Key: key, Detail: is.detail, Since: is.first.Unix()})
	}
	h.mu.Unlock()
	for _, p := range fresh {
		log.Printf("daemon: HEALTH: %s: %s (since %s)", p.Key, p.Detail, time.Unix(p.Since, 0).Format(time.Kitchen))
		if h.alert != nil {
			if err := h.alert(p.Key, p.Detail, time.Unix(p.Since, 0)); err != nil {
				h.alertFailed(p.Key)
			}
		}
	}
}

// alertFailed makes key's alert due again, later each time: nobody may have heard
// of the problem, but a send that timed out may still land and mail has no
// idempotency key, so a retry can repeat it.
func (h *healthBoard) alertFailed(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if is, found := h.issues[key]; found {
		delay := healthAlertRetry << min(is.alertFails, 4) // up to 16 min, capped below
		is.alerted, is.alertAfter = false, h.now().Add(min(delay, healthAlertRetryMax))
		is.alertFails++
	}
}

// mailHistoryKey is the one issue for all mail the Waker holds back.
const mailHistoryKey = "mail-history"

// healthHistoryRoles bounds the roles the mail-history issue names.
const healthHistoryRoles = 10

// reportMailHistory keeps old mail on roles with no read cursor on the board. One
// issue however many roles hold it: one alert to Commander, none to the roles.
func reportMailHistory(h *healthBoard, held map[string]int, answers map[string][]string) {
	if len(held) == 0 {
		h.ok(mailHistoryKey)
		return
	}
	roles := make([]string, 0, len(held))
	total := 0
	for role, n := range held {
		roles = append(roles, role)
		total += n
	}
	sort.Strings(roles)
	var named []string
	for _, role := range roles[:min(len(roles), healthHistoryRoles)] {
		named = append(named, fmt.Sprintf("%s (%d)", role, held[role]))
	}
	if more := len(roles) - len(named); more > 0 {
		named = append(named, fmt.Sprintf("+%d more", more))
	}
	listed := strings.Join(roles[:min(len(roles), healthHistoryRoles)], " ")
	h.fail(mailHistoryKey, fmt.Sprintf("Unread mail nobody was shown: %d mail(s) on %d role(s)\n%s\n%sIt was old when cmdrd first saw the role, which has no read cursor, so it was not delivered. Read it with cmdr internal-inbox --agent <role>. Once dealt with, clear the listed roles (run it again for any more): for r in %s; do cmdr internal-inbox --agent $r --mark-read >/dev/null; done", total, len(roles), strings.Join(named, ", "), heldAnswerLines(answers), listed))
}

// heldAnswerLines names held subjects that mention a decision answer: a lost
// answer must not sit inside a count.
func heldAnswerLines(answers map[string][]string) string {
	roles := make([]string, 0, len(answers))
	for role := range answers {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	var b strings.Builder
	shown, total := 0, 0
	for _, role := range roles {
		for _, subject := range answers[role] {
			total++
			if shown < healthHistoryRoles {
				fmt.Fprintf(&b, "May hold a decision answer: %s: %q\n", role, subject)
				shown++
			}
		}
	}
	if total > shown {
		fmt.Fprintf(&b, "+%d more that may hold a decision answer\n", total-shown)
	}
	return b.String()
}

// checkPlans puts every workstream whose plan breaks the rules on the health
// board (key plan:<Manager role>), and clears the ones that are right again.
// It runs on every snapshot, so a bad plan is seen within seconds of the write
// that made it, whoever made it (a Manager, a reviewer on GitHub, CI). A workstream
// whose root is on hold raises none: its findings go on its check-in line.
func checkPlans(h *healthBoard, snap *livedata.Snapshot) {
	if h == nil || snap.Display == nil {
		return
	}
	roles := map[string]string{}
	held := map[string]bool{}
	owned := map[string]map[string]string{} // workstream -> bead -> its owner's role
	for _, w := range snap.Workstreams {
		key := w.Project + ":" + w.BeadID
		roles[key] = w.ManagerRole
		held[w.ManagerRole] = w.Hold
		owned[key] = stepOwners(w.Steps, w.ManagerRole, nil)
	}
	shown := map[string]bool{}
	for _, row := range snap.Display.Workstreams {
		key := row.Project + ":" + row.BeadID
		role := roles[key]
		shown["plan:"+role] = true
		for _, r := range owned[key] {
			// Still on the board: a row with no diagram this time keeps its
			// nested Managers' issues as they are, like the root's.
			shown["plan:"+r] = true
		}
		if role == "" || row.Diagram == nil {
			continue
		}
		// A nested workstream with its own Manager gets its own findings; a hold
		// on the root still quiets the whole tree.
		byRole := map[string][]string{role: nil}
		order := []string{role}
		for _, r := range owned[key] {
			if _, ok := byRole[r]; !ok {
				byRole[r] = nil
				order = append(order, r)
			}
		}
		for _, f := range row.Diagram.Findings {
			to := role
			if r, ok := owned[key][f.Bead]; ok {
				to = r
			}
			// A plan on hold is counted on its check-in line, not raised as a failure.
			if livedata.FindingKind(f) == "error" && !held[role] && !freshlySpawned(snap, to, f.Bead, h.now()) {
				byRole[to] = append(byRole[to], fmt.Sprintf("- %s %q: %s. Fix: %s", f.Bead, f.Title, f.Why, f.Fix))
			}
		}
		for _, r := range order {
			lines := byRole[r]
			if len(lines) == 0 {
				h.ok("plan:" + r)
				continue
			}
			h.fail("plan:"+r, fmt.Sprintf("#%d %s: the plan breaks %d rule(s), so its board and diagram read wrong:\n%s", row.N, row.Title, len(lines), strings.Join(lines, "\n")))
		}
	}
	if snap.Degraded == nil {
		h.dropGonePlans(shown)
	}
}

// stepOwners maps each step under a workstream to the role of the Manager that
// owns it: the nearest step with a manager_role (a nested workstream), else role.
func stepOwners(steps []livedata.SnapStep, role string, out map[string]string) map[string]string {
	if out == nil {
		out = map[string]string{}
	}
	for _, st := range steps {
		r := role
		if st.ManagerRole != "" {
			r = st.ManagerRole
		}
		out[st.BeadID] = r
		stepOwners(st.Children, r, out)
	}
	return out
}

// dropGonePlans clears the plan issues of workstreams off the board: a closed
// workstream has no plan to fix, and its Manager must not be told to fix it when
// the grace runs out. A degraded snapshot may just have missed the row.
func (h *healthBoard) dropGonePlans(shown map[string]bool) {
	h.mu.Lock()
	var gone []string
	for key := range h.issues {
		if strings.HasPrefix(key, "plan:") && !shown[key] {
			gone = append(gone, key)
		}
	}
	h.mu.Unlock()
	for _, key := range gone {
		h.ok(key)
	}
}

// spawnGrace is how long after a spawn a bead's findings stay off the health
// board: the claim, the run and the snapshot settle in that time.
const spawnGrace = 3 * time.Minute

// freshlySpawned reports whether an Operator on bead was spawned under
// spawnGrace ago (per the Manager's watch of its running Operators).
func freshlySpawned(snap *livedata.Snapshot, managerRole, bead string, now time.Time) bool {
	for _, o := range snap.Watch[managerRole].Operators {
		if o.BeadID == bead && now.Sub(time.Unix(o.Since, 0)) < spawnGrace {
			return true
		}
	}
	return false
}

// healthAlertRetry and healthAlertRetryMax bound how long after a failed send an
// alert goes again: the first, doubling per failure, up to the second.
const (
	healthAlertRetry    = time.Minute
	healthAlertRetryMax = 15 * time.Minute
)

// healthAlertBudget bounds one alert: its route reads and its send.
const healthAlertBudget = 20 * time.Second

// healthAlert mails a problem through send and says whether it went.
func healthAlert(send wsmgr.SendFunc) func(key, detail string, since time.Time) error {
	return func(key, detail string, since time.Time) error {
		ctx, cancel := context.WithTimeout(context.Background(), healthAlertBudget)
		defer cancel()
		to, subject, body := healthMail(key, detail, since)
		err := send(ctx, to, subject, body)
		if err != nil {
			log.Printf("daemon: health alert to %s not sent: %v", to, err)
		}
		return err
	}
}

// healthMail is who hears about a problem and what they are told: a plan
// that breaks the rules goes to its Manager to fix; anything else (mail that
// cannot reach an agent, a Manager that cannot start) to Commander.
func healthMail(key, detail string, since time.Time) (to, subject, body string) {
	at := since.Format(time.Kitchen)
	if role, ok := strings.CutPrefix(key, "plan:"); ok {
		first, _, _ := strings.Cut(detail, ":")
		return role, "[health] your plan reads wrong: fix it now",
			detail + "\n\nThis has been true since " + at + ". You are responsible for your plan's state: fix every line above with the command given, then run cmdr graph check on the workstream. The user sees " + first + " flagged until it is clean."
	}
	head, _, _ := strings.Cut(detail, "\n")
	return commanderRole, "[health] " + head,
		detail + "\n\nFailing since " + at + " and still failing. The user's board is not moving for this; tell the user and fix the cause."
}
