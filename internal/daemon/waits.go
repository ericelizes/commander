package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/holdread"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

const (
	waitPollInterval = time.Minute
	// untilPollInterval is how often time waits are checked: they need no gh
	// call, and a Manager should wake within about a minute of the target.
	untilPollInterval = time.Minute

	defaultProdTagPrefix = "prod-"
	// prodTagsChecked bounds the compare calls per poll: the newest tags only.
	prodTagsChecked = 15
	// mainCIMissingGrace is how long a main-ci sha GitHub does not know may stay
	// unknown: a wait can be recorded just before its commit is pushed.
	mainCIMissingGrace = 30 * time.Minute
)

// ghRunner runs `gh <args>` and returns stdout. `gh pr checks` exits non-zero
// while checks are pending or failing but still prints its JSON, so a failed
// run with output is not an error.
type ghRunner func(ctx context.Context, args ...string) ([]byte, error)

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil && len(out) > 0 {
		return out, nil
	}
	return out, err
}

// waitWatcher wakes a Manager whose turn ended waiting on CI or a review. Waits
// are bead labels (beads.Wait); the watcher only reads GitHub (gh, read-only).
// An event wakes once: the wait that saw it is cleared. "Last checked" is the
// last poll, kept in memory (checkedAt): stamping it on every waiting bead
// wrote to the shared beads db twice per bead per poll.
type waitWatcher struct {
	prMu     sync.Mutex
	prStates map[string]string
	projects interface {
		ListProjects(ctx context.Context) ([]store.Project, error)
	}
	beads beads.Client
	gh    ghRunner
	// mail sends from Commander to a role.
	mail func(ctx context.Context, toRole, subject, body string) error
	// repoOf is the GitHub owner/name of a project path.
	repoOf func(path string) string
	now    func() time.Time
	// running lists the canonical roles of the Operators running now; nil (or
	// an error) leaves operator waits alone.
	running func(ctx context.Context) (map[string]bool, error)
	// runningNow is running's answer for the poll in progress.
	runningNow map[string]bool
	// prodTagPrefix is the start of a prod tag name ("" = defaultProdTagPrefix).
	prodTagPrefix string
	// woken remembers events already mailed whose wait could not be cleared,
	// so a failed clear never wakes twice.
	// A cleared wait stays here too: reads can show its label for minutes after
	// the clear (the list comes from an export), and it must not wake again.
	woken map[string]bool
	// checkedAt is when the last poll finished (unix s); every waiting bead is checked each poll.
	checkedAt atomic.Int64
	// facts is each bead's review waits as GitHub had them at the last poll.
	facts atomic.Pointer[map[string]livedata.ReviewFact]
}

// CheckedAt is when the waits were last checked; zero before the first poll.
func (w *waitWatcher) CheckedAt() time.Time {
	if s := w.checkedAt.Load(); s > 0 {
		return time.Unix(s, 0)
	}
	return time.Time{}
}

func (w *waitWatcher) Run(ctx context.Context) {
	t := time.NewTicker(waitPollInterval)
	defer t.Stop()
	u := time.NewTicker(untilPollInterval)
	defer u.Stop()
	for {
		w.poll(ctx)
		for wait := true; wait; {
			select {
			case <-ctx.Done():
				return
			case <-u.C:
				w.pollUntil(ctx)
			case <-t.C:
				wait = false
			}
		}
	}
}

// pollUntil wakes the Manager of every bead whose until wait has reached its
// target time. It makes no gh call and never stamps "last checked".
func (w *waitWatcher) pollUntil(ctx context.Context) {
	projects, err := w.projects.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, p := range projects {
		all, err := w.beads.List(ctx, p.Path, beads.ListOptions{Status: "all"})
		if err != nil {
			continue
		}
		w.unparkDue(ctx, p)
		for _, b := range all {
			if b.Status == "closed" {
				continue
			}
			for _, wt := range beads.WaitsOf(b.Labels) {
				if wt.Kind != beads.WaitUntil {
					continue
				}
				if subject, body, fired := w.event(ctx, "", b, wt); fired {
					if w.dropBlockedStart(ctx, p, b, wt) {
						continue
					}
					w.wake(ctx, p, b, wt, subject, body)
				}
			}
		}
	}
}

// dropBlockedStart clears a due "Manager to start" receipt from a step that
// now waits on an open blocker (a gate linked after the step was made or
// released): the step is not ready, so it is nobody's move and its Manager is
// not mailed. A failed read or write leaves it for the next tick.
func (w *waitWatcher) dropBlockedStart(ctx context.Context, p store.Project, b beads.Bead, wt beads.Wait) bool {
	if wt.Ref != beads.ManagerStartWait {
		return false
	}
	behind, err := beads.BehindOpenBlocker(ctx, w.beads, p.Path, b.ID, "")
	if err != nil {
		log.Printf("daemon: waits: read blockers of %s: %v", b.ID, err)
		return true
	}
	if !behind {
		return false
	}
	if err := w.beads.SetLabel(ctx, p.Path, b.ID, storedLabel(b.Labels, wt), false); err != nil {
		log.Printf("daemon: waits: clear %s on blocked %s: %v", beads.ManagerStartWait, b.ID, err)
	}
	return true
}

// maxDueListed caps the beads one batched due mail lists.
const maxDueListed = 10

// unparkDue brings back every parked bead (beads.Park) whose time has passed
// and mails its Manager once per bead, one mail per Manager per tick. The mail
// goes first and is remembered (in memory and as a parked-notified label), so a
// failed un-park or a cmdrd restart never mails twice.
func (w *waitWatcher) unparkDue(ctx context.Context, p store.Project) {
	parked, err := w.beads.List(ctx, p.Path, beads.ListOptions{Labels: []string{beads.ParkedLabel}, Status: "all"})
	if err != nil {
		return
	}
	var due []beads.Bead
	byManager := map[string][]beads.Bead{}
	var managers []string
	for _, b := range parked {
		at := beads.ParkedUntil(b.Labels)
		if b.Status != beads.StatusDeferred || at == 0 || at > w.now().Unix() {
			continue
		}
		due = append(due, b)
		if w.woken[b.ID+"|unpark"] || hasLabelPrefix(b.Labels, beads.ParkedNotifiedPrefix) {
			continue
		}
		manager := wsmgr.Role(wsmgr.Owner(ctx, w.beads, p.Path, b.ID))
		if _, ok := byManager[manager]; !ok {
			managers = append(managers, manager)
		}
		byManager[manager] = append(byManager[manager], b)
	}
	for _, manager := range managers {
		group := byManager[manager]
		subject := fmt.Sprintf("[parked] parked ask %s is due again", group[0].ID)
		if len(group) > 1 {
			subject = fmt.Sprintf("[parked] %d parked asks are due again", len(group))
		}
		var lines []string
		for i, b := range group {
			if i == maxDueListed {
				lines = append(lines, fmt.Sprintf("+%d more", len(group)-maxDueListed))
				break
			}
			lines = append(lines, fmt.Sprintf("- %s (%s)", b.ID, b.Title))
		}
		body := fmt.Sprintf("Parked time passed, so cmdrd un-parked these asks; each is back with the status it had (and its card, if it had one):\n%s\nRe-park one with: cmdr internal-set-objective --project %s --bead <id> --park --until <time>.", strings.Join(lines, "\n"), p.Name)
		if err := w.mail(ctx, manager, subject, body); err != nil {
			log.Printf("daemon: waits: mail %s: %v", manager, err)
			continue
		}
		stamp := beads.ParkedNotifiedPrefix + strconv.FormatInt(w.now().Unix(), 10)
		for _, b := range group {
			w.woken[b.ID+"|unpark"] = true
			if err := w.beads.SetLabel(ctx, p.Path, b.ID, stamp, true); err != nil {
				log.Printf("daemon: waits: stamp %s: %v", b.ID, err)
			}
		}
	}
	for _, b := range due {
		key := b.ID + "|unpark"
		if !w.woken[key] && !hasLabelPrefix(b.Labels, beads.ParkedNotifiedPrefix) {
			continue // its Manager's mail failed: try again next tick
		}
		// The Manager was told it is due: unless something of its own holds it (a card, a wait, an
		// Operator), it comes out of the park held by the Manager dispatch wait, in the same write.
		var hold []string
		if w.onlyHolder(b, "") {
			now := w.now()
			hold = append(hold, beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(2 * time.Minute), Ref: beads.ManagerDispatchWait}.Label())
		}
		if _, err := beads.Unpark(ctx, w.beads, p.Path, b.ID, "cmdrd", hold...); err != nil {
			log.Printf("daemon: waits: unpark %s: %v", b.ID, err)
			continue
		}
		delete(w.woken, key)
	}
}

func hasLabelPrefix(labels []string, prefix string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// wake mails the bead's Manager once for wt. Manager handoff receipts stay
// until an actual state write accepts custody; other waits clear. A failed
// clear is remembered so the next poll does not mail again. The label was read
// from the list, which can lag the database, so the database decides whether
// the wait still stands: a wait already replaced or cleared mails nothing, and
// a failed read leaves the decision to the next poll.
func (w *waitWatcher) wake(ctx context.Context, p store.Project, b beads.Bead, wt beads.Wait, subject, body string) {
	label := storedLabel(b.Labels, wt)
	key := b.ID + "|" + label
	digest := sha256.Sum256([]byte(label))
	receipt := fmt.Sprintf("handoff-notified:%x", digest[:16])
	handoff := beads.IsManagerHandoff(wt)
	if handoff && slices.Contains(b.Labels, receipt) {
		return
	}
	if !w.woken[key] {
		fresh, err := beads.FreshShow(ctx, w.beads, p.Path, b.ID)
		if err != nil {
			log.Printf("daemon: waits: read %s: %v", b.ID, err)
			return
		}
		if fresh == nil {
			return
		}
		if fresh.Status == "closed" || !slices.Contains(fresh.Labels, label) || (handoff && slices.Contains(fresh.Labels, receipt)) {
			return
		}
		manager := wsmgr.Role(wsmgr.Owner(ctx, w.beads, p.Path, b.ID))
		if err := w.mail(ctx, manager, subject, body); err != nil {
			log.Printf("daemon: waits: mail %s: %v", manager, err)
			return
		}
		w.woken[key] = true
	}
	if handoff {
		if err := w.beads.SetLabel(ctx, p.Path, b.ID, receipt, true); err != nil {
			log.Printf("daemon: waits: retain handoff notification %s: %v", b.ID, err)
		}
		return
	}
	// Clear and hand on in one write: a wait that was the bead's only holder is replaced by the
	// Manager dispatch wait (the Manager was just mailed), so no read sees it unheld (I3).
	f := beads.Fields{RemoveLabels: []string{label}}
	if w.onlyHolder(b, label) {
		now := w.now()
		f.AddLabels = []string{beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(2 * time.Minute), Ref: beads.ManagerDispatchWait}.Label()}
	}
	if err := w.beads.UpdateFields(ctx, p.Path, b.ID, f); err != nil {
		log.Printf("daemon: waits: clear %s on %s: %v", label, b.ID, err)
	}
}

// onlyHolder reports whether nothing of b's own holds it once label is gone, judged by the one
// predicate (holding.Held over the bead's own fields: a gate, a user card, a wait that still holds
// (an expired handoff does not, beads.WaitHolds), a running Operator). Open children or blockers
// may still hold it; a dispatch wait beside them is never wrong (two holders are legal, none is not).
func (w *waitWatcher) onlyHolder(b beads.Bead, label string) bool {
	after := b
	after.Labels = slices.DeleteFunc(slices.Clone(b.Labels), func(l string) bool { return l == label })
	if after.Status == beads.StatusDeferred {
		after.Status = "open" // judged as it comes out of the park
	}
	_, held, _ := holding.Held(holdread.Bead(&after, "", nil), ownOnly{running: w.runningNow})
	return !held
}

// ownOnly is a holding.World that knows only who runs: onlyHolder judges a bead by what it carries.
type ownOnly struct{ running map[string]bool }

func (ownOnly) Bead(string) (holding.Bead, bool) { return holding.Bead{}, false }
func (ownOnly) HasOpenChild(string) bool         { return false }
func (o ownOnly) OwnerLive(owner string) (string, bool) {
	if holding.IsManager(owner) {
		return "idle", true // a Manager's name holds nothing between turns
	}
	if o.running == nil {
		return "", false // unknown: taken as live
	}
	if o.running[spawn.CanonicalRole(owner)] {
		return "working", true
	}
	return "", true
}

// storedLabel is the label on the bead that wt was parsed from. Label() may
// render another spelling (a long-form operator wait over the label limit
// re-renders compact), and clearing that one removes nothing, so the wait
// fires again at every poll.
func storedLabel(labels []string, wt beads.Wait) string {
	for _, l := range labels {
		if p, ok := beads.ParseWaitLabel(l); ok && p.Kind == wt.Kind && p.Ref == wt.Ref && p.Repo == wt.Repo && p.PR == wt.PR && p.Sha == wt.Sha && p.Since.Equal(wt.Since) {
			return l
		}
	}
	return wt.Label()
}

func (w *waitWatcher) poll(ctx context.Context) {
	projects, err := w.projects.ListProjects(ctx)
	if err != nil {
		log.Printf("daemon: waits: list projects: %v", err)
		return
	}
	facts := map[string]livedata.ReviewFact{}
	defer func() { w.facts.Store(&facts) }()
	w.runningNow = nil
	if w.running != nil {
		if r, err := w.running(ctx); err == nil {
			w.runningNow = r
		}
	}
	for _, p := range projects {
		all, err := w.beads.List(ctx, p.Path, beads.ListOptions{Status: "all"})
		if err != nil {
			continue
		}
		repo := w.repoOf(p.Path)
		for _, b := range all {
			if b.Status == "closed" {
				continue
			}
			if waits := beads.WaitsOf(b.Labels); len(waits) > 0 {
				w.checkBead(ctx, p, repo, b, waits)
				if f, ok := w.reviewFact(ctx, repo, b, waits); ok {
					facts[b.ID] = f
				}
			}
			w.clearStamps(ctx, p, b)
		}
	}
	w.checkedAt.Store(w.now().Unix())
}

// checkBead handles one bead's waits: events wake the Manager; the rest are
// only stamped as checked.
func (w *waitWatcher) checkBead(ctx context.Context, p store.Project, repo string, b beads.Bead, waits []beads.Wait) {
	for _, wt := range waits {
		subject, body, fired := w.event(ctx, repo, b, wt)
		if !fired {
			continue
		}
		w.wake(ctx, p, b, wt, subject, body)
	}
}

// clearStamps removes the per-bead "checked" and old nudge labels earlier
// versions wrote; after one pass there are none and this writes nothing.
func (w *waitWatcher) clearStamps(ctx context.Context, p store.Project, b beads.Bead) {
	for _, l := range b.Labels {
		_, stamp := beads.ParseCheckedLabel(l)
		if stamp || l == beads.WaitNudgedLabel {
			if err := w.beads.SetLabel(ctx, p.Path, b.ID, l, false); err != nil {
				log.Printf("daemon: waits: clear %s on %s: %v", l, b.ID, err)
			}
		}
	}
}

// event reports whether wait wt has happened, with the mail to send.
func (w *waitWatcher) event(ctx context.Context, projectRepo string, b beads.Bead, wt beads.Wait) (subject, body string, fired bool) {
	switch wt.Kind {
	case beads.WaitCI:
		repo := wt.Repo
		if repo == "" {
			repo = projectRepo
		}
		if repo == "" {
			return
		}
		failing, settled := w.ciResult(ctx, repo, wt.PR)
		if !settled {
			return
		}
		result := "all green"
		if failing > 0 {
			result = fmt.Sprintf("%d failing", failing)
		}
		subject = fmt.Sprintf("[ci] #%d finished: %s", wt.PR, result)
		return subject, fmt.Sprintf("CI for PR #%d in %s finished: %s. You were waiting on it (%s). Pick the work back up.", wt.PR, repo, result, b.ID), true
	case beads.WaitMainCI:
		repo := waitRepo(wt, projectRepo)
		if repo == "" {
			return
		}
		sha := short(wt.Sha)
		full, missing := w.fullCommit(ctx, repo, wt.Sha)
		if missing {
			if w.now().Sub(wt.Since) < mainCIMissingGrace {
				return
			}
			subject = fmt.Sprintf("[main-ci] %s not found in %s", sha, repo)
			return subject, fmt.Sprintf("GitHub has no commit %s in %s, or gh cannot read that repo, so its CI can never be seen. You were waiting on it (%s) and that wait is now cleared. Check the sha and record the wait again.", wt.Sha, repo, b.ID), true
		}
		if full == "" {
			return
		}
		failing, settled := w.mainCIResult(ctx, repo, full)
		if !settled {
			return
		}
		result := "all green"
		if failing > 0 {
			result = fmt.Sprintf("%d failing", failing)
		}
		subject = fmt.Sprintf("[main-ci] %s finished: %s", sha, result)
		return subject, fmt.Sprintf("CI on main for %s in %s finished: %s. You were waiting on it (%s). Pick the work back up.", sha, repo, result, b.ID), true
	case beads.WaitProdTag:
		repo := waitRepo(wt, projectRepo)
		if repo == "" {
			return
		}
		tag, ok := w.prodTagFor(ctx, repo, wt.Sha)
		if !ok {
			return
		}
		sha := short(wt.Sha)
		subject = fmt.Sprintf("[prod-tag] %s is in %s", sha, tag)
		return subject, fmt.Sprintf("Prod tag %s in %s now contains %s. You were waiting on it (%s). Pick the work back up.", tag, repo, sha, b.ID), true
	case beads.WaitUntil:
		if w.now().Before(wt.Until) {
			return
		}
		label := wt.Text()
		if beads.IsManagerHandoff(wt) {
			return "[failure] " + label + " deadline missed", fmt.Sprintf("Automation failure on %s: %s deadline missed. You own recovery. Read the current bead and current-holder/run evidence. Accept the existing result and close it, or dispatch the existing ready work through the canonical guarded door. Do not restart an old actor over a healthy replacement, duplicate workers/reviews, or ask the user to repair this handoff. If blocked, report the concrete cause and recovery target to Commander.", b.ID, label), true
		}
		return "[wait] " + label + " time reached", fmt.Sprintf("%s: time reached. You were waiting on it (%s). Pick the work back up.", label, b.ID), true
	case beads.WaitReview:
		pr, _ := strconv.Atoi(livedata.ReviewPR(b.Labels))
		repo := prRepo(b.Labels, projectRepo)
		if pr == 0 || repo == "" {
			return
		}
		verdict, prState, ok := w.reviewVerdict(ctx, repo, pr, wt)
		if !ok && (prState == "MERGED" || prState == "CLOSED") {
			// The PR is gone: no review can land on it any more.
			done := strings.ToLower(prState)
			subject = fmt.Sprintf("[review] #%d %s without %s's review", pr, done, wt.Ref)
			return subject, fmt.Sprintf("PR #%d in %s was %s before %s reviewed it. You were waiting on that review (%s). Pick the work back up.", pr, repo, done, wt.Ref, b.ID), true
		}
		if !ok {
			return
		}
		subject = fmt.Sprintf("[review] %s %s #%d", wt.Ref, verdict, pr)
		return subject, fmt.Sprintf("%s %s PR #%d in %s. You were waiting on that review (%s). Pick the work back up.", wt.Ref, verdict, pr, repo, b.ID), true
	case beads.WaitOperator:
		role := spawn.CanonicalRole("operator-" + strings.TrimPrefix(wt.Ref, "operator-"))
		if w.runningNow == nil || w.runningNow[role] {
			return
		}
		// The poll's run list predates the bead reads. A spawn can land
		// between them; confirm absence after seeing the recorded wait.
		if w.running != nil {
			fresh, err := w.running(ctx)
			if err != nil || fresh == nil || fresh[role] {
				return
			}
		}
		subject = fmt.Sprintf("[operator] %s is no longer running", wt.Ref)
		return subject, fmt.Sprintf("Operator %s is no longer running. You were waiting on it (%s). Pick the work back up.", wt.Ref, b.ID), true
	}
	return
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// prRepo is the repo a step's PR is in: the one its pr: label names, else the project's.
func prRepo(labels []string, projectRepo string) string {
	if r := livedata.ReviewRepo(labels); r != "" {
		return r
	}
	return projectRepo
}

func waitRepo(wt beads.Wait, projectRepo string) string {
	if wt.Repo != "" {
		return wt.Repo
	}
	return projectRepo
}

// fullCommit resolves a recorded sha to the whole one gh run list needs: its
// --commit filter gives a short sha 0 runs and no error, and a label cannot hold
// a whole sha (beads.MaxLabelLen). missing means GitHub answered that the commit
// or repo is not there; any other failure returns "" to try again next poll.
func (w *waitWatcher) fullCommit(ctx context.Context, repo, sha string) (full string, missing bool) {
	out, err := w.gh(ctx, "api", "repos/"+repo+"/commits/"+sha, "--method", "GET", "--jq", ".sha")
	if err != nil {
		return "", false
	}
	if got := strings.TrimSpace(string(out)); len(got) == 40 && strings.HasPrefix(got, sha) {
		return got, false
	}
	// gh prints an API error's body on stdout, and runGH passes it on.
	var apiErr struct{ Status string }
	if json.Unmarshal(out, &apiErr) != nil {
		return "", false
	}
	return "", apiErr.Status == "404" || apiErr.Status == "422"
}

// mainCIResult counts failed workflow runs on a commit (a full sha); settled is
// true when there is at least one run and none is still queued or running.
func (w *waitWatcher) mainCIResult(ctx context.Context, repo, sha string) (failing int, settled bool) {
	out, err := w.gh(ctx, "run", "list", "--repo", repo, "--commit", sha, "--limit", "50", "--json", "status,conclusion")
	if err != nil {
		return 0, false
	}
	var runs []struct{ Status, Conclusion string }
	if json.Unmarshal(out, &runs) != nil || len(runs) == 0 {
		return 0, false
	}
	for _, r := range runs {
		if r.Status != "completed" {
			return 0, false
		}
		switch r.Conclusion {
		case "failure", "cancelled", "timed_out", "startup_failure":
			failing++
		}
	}
	return failing, true
}

// prodTagFor finds a prod tag (name starting with the prefix) whose commit
// contains sha: GitHub's compare of sha...tag says "ahead" or "identical".
// Read-only: gh api GET calls only.
func (w *waitWatcher) prodTagFor(ctx context.Context, repo, sha string) (string, bool) {
	prefix := w.prodTagPrefix
	if prefix == "" {
		prefix = defaultProdTagPrefix
	}
	out, err := w.gh(ctx, "api", "repos/"+repo+"/git/matching-refs/tags/"+prefix, "--method", "GET")
	if err != nil {
		return "", false
	}
	var refs []struct{ Ref string }
	if json.Unmarshal(out, &refs) != nil {
		return "", false
	}
	for i, n := len(refs)-1, 0; i >= 0 && n < prodTagsChecked; i, n = i-1, n+1 {
		tag := strings.TrimPrefix(refs[i].Ref, "refs/tags/")
		out, err := w.gh(ctx, "api", "repos/"+repo+"/compare/"+sha+"..."+tag, "--method", "GET", "--jq", ".status")
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(out)) {
		case "ahead", "identical":
			return tag, true
		}
	}
	return "", false
}

// ciResult counts failing checks of a PR; settled is true when none is pending
// (and there is at least one check).
func (w *waitWatcher) ciResult(ctx context.Context, repo string, pr int) (failing int, settled bool) {
	out, err := w.gh(ctx, "pr", "checks", strconv.Itoa(pr), "--repo", repo, "--json", "name,bucket")
	if err != nil {
		return 0, false
	}
	var checks []struct{ Bucket string }
	if json.Unmarshal(out, &checks) != nil || len(checks) == 0 {
		return 0, false
	}
	for _, c := range checks {
		switch c.Bucket {
		case "pending":
			return 0, false
		case "fail", "cancel":
			failing++
		}
	}
	return failing, true
}

// reviewVerdict is "approved", "requested changes on" or "commented on" when the waited-on
// reviewer's latest review, submitted since the wait began, says so.
// ReviewFacts is GitHub's word on each bead's review waits at the last poll.
func (w *waitWatcher) ReviewFacts() map[string]livedata.ReviewFact {
	if m := w.facts.Load(); m != nil {
		return *m
	}
	return nil
}

// reviewFact looks up, for a bead's review waits, what each named reviewer
// has done on its PR since the wait began. A review wait with no PR can never
// clear, so it is reported as such.
func (w *waitWatcher) reviewFact(ctx context.Context, repo string, b beads.Bead, waits []beads.Wait) (livedata.ReviewFact, bool) {
	var reviews []beads.Wait
	for _, wt := range waits {
		if wt.Kind == beads.WaitReview {
			reviews = append(reviews, wt)
		}
	}
	if len(reviews) == 0 {
		return livedata.ReviewFact{}, false
	}
	pr, _ := strconv.Atoi(livedata.ReviewPR(b.Labels))
	repo = prRepo(b.Labels, repo)
	if pr == 0 || repo == "" {
		return livedata.ReviewFact{NoPR: true, Text: "review wait names no PR"}, true
	}
	var acted, waiting []string
	for _, wt := range reviews {
		if verdict, _, ok := w.reviewVerdict(ctx, repo, pr, wt); ok {
			acted = append(acted, fmt.Sprintf("%s %s #%d", wt.Ref, verdict, pr))
		} else {
			waiting = append(waiting, wt.Ref)
		}
	}
	if len(acted) > 0 {
		return livedata.ReviewFact{Acted: true, Text: strings.Join(acted, "; ")}, true
	}
	return livedata.ReviewFact{Text: fmt.Sprintf("waiting on %s (#%d)", strings.Join(waiting, ", "), pr)}, true
}

// reviewVerdict also returns the PR's state (OPEN, MERGED, CLOSED).
func (w *waitWatcher) reviewVerdict(ctx context.Context, repo string, pr int, wt beads.Wait) (string, string, bool) {
	out, err := w.gh(ctx, "pr", "view", strconv.Itoa(pr), "--repo", repo, "--json", "reviews,state")
	if err != nil {
		return "", "", false
	}
	var v struct {
		State   string
		Reviews []struct {
			Author      struct{ Login string }
			State       string
			SubmittedAt time.Time
		}
	}
	if json.Unmarshal(out, &v) != nil {
		return "", "", false
	}
	w.prMu.Lock()
	if w.prStates == nil {
		w.prStates = map[string]string{}
	}
	w.prStates[repo+"#"+strconv.Itoa(pr)] = v.State
	w.prMu.Unlock()
	verdict := ""
	var latest time.Time
	for _, r := range v.Reviews {
		if !strings.EqualFold(r.Author.Login, wt.Ref) || r.SubmittedAt.Before(wt.Since) || !r.SubmittedAt.After(latest) {
			continue
		}
		switch r.State {
		case "APPROVED":
			verdict, latest = "approved", r.SubmittedAt
		case "CHANGES_REQUESTED":
			verdict, latest = "requested changes on", r.SubmittedAt
		case "COMMENTED":
			// A comment-only review is still the reviewer's move made: theirs is done.
			verdict, latest = "commented on", r.SubmittedAt
		}
	}
	return verdict, v.State, verdict != ""
}

// PRStates returns a copy of facts learned by existing review polls, with no gh calls.
func (w *waitWatcher) PRStates() map[string]string {
	w.prMu.Lock()
	defer w.prMu.Unlock()
	out := map[string]string{}
	for k, v := range w.prStates {
		out[k] = v
	}
	return out
}
