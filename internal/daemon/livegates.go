package daemon

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
	"github.com/example-user/commander/internal/wsmgr"
)

const (
	// gateSHAInterval is how often PR heads and main are polled; a change
	// re-checks the gates that read them at once.
	gateSHAInterval = time.Minute
	// gateRecheckEvery re-checks a gate with no push or main move in between.
	gateRecheckEvery = 3 * time.Minute
	// gateChecksPerPoll bounds the shell checks one poll starts; the rest wait for the next minute.
	gateChecksPerPoll = 5
	// gateFailsToReopen is how many failed checks in a row reopen a passed gate.
	gateFailsToReopen = 2
)

// prInCheck finds the PRs an auto gate reads (`gh pr checks|view <n> [--repo o/r]`).
var prInCheck = regexp.MustCompile(`gh pr (?:checks|view) #?(\d+)(?: --repo (\S+))?`)

// gateWatcher keeps condition gates (verifier auto) true to their condition:
// each one is re-run when a PR it reads gets a new head, when main moves, and
// every gateRecheckEvery, until its stage closes. A gate that stops holding is
// reopened (gates.SetResult); the stage's Manager is mailed once per
// passed<->failed transition. Decision gates (user, reviewer, operator) latch:
// the daemon never touches them. A gate passed by hand while its condition is
// false is reopened within a minute of a restart (intended); the per-poll
// check cap means with many gates the later ones wait for a later poll. Read-only against GitHub, like waitWatcher.
type gateWatcher struct {
	projects interface {
		ListProjects(ctx context.Context) ([]store.Project, error)
	}
	beads  beads.Client
	gh     ghRunner
	run    gates.RunFn
	mail   func(ctx context.Context, toRole, subject, body string) error
	repoOf func(path string) string
	now    func() time.Time
	// degraded reports an unreadable store; nil = never.
	degraded func() bool

	// per gate bead: what the last check saw, so a change triggers the next.
	last map[string]gateSeen
	// consecutive failed checks of a still-passed gate.
	failing map[string]int
}

type gateSeen struct {
	at   time.Time
	pins string // PR head shas + main sha, as last seen
}

func (w *gateWatcher) Run(ctx context.Context) {
	t := time.NewTicker(gateSHAInterval)
	defer t.Stop()
	for {
		w.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *gateWatcher) poll(ctx context.Context) {
	if w.degraded != nil && w.degraded() {
		return
	}
	projects, err := w.projects.ListProjects(ctx)
	if err != nil {
		log.Printf("daemon: gates: list projects: %v", err)
		return
	}
	for _, p := range projects {
		if p.Type != "code" && p.Type != "" {
			continue
		}
		w.pollProject(ctx, p)
	}
}

func (w *gateWatcher) pollProject(ctx context.Context, p store.Project) {
	nodes, err := gates.All(ctx, w.beads, p.Path, p.ID)
	if err != nil {
		return // store unreadable: skip, never guess
	}
	closed, err := w.closedStages(ctx, p)
	if err != nil {
		log.Printf("daemon: gates: list stages of %s: %v", p.Path, err)
		return // cannot tell which stages closed: skip, never guess
	}
	live := map[string]bool{} // stage -> verified open by a direct read, this poll
	repo := w.repoOf(p.Path)
	// One sha lookup per PR/main per poll, however many gates read it.
	shas := map[string]string{}
	checks := 0
	for _, n := range nodes {
		g := n.Gate
		if g.Verifier != store.VerifierAuto || closed[g.BeadID] {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if checks >= gateChecksPerPoll {
			return
		}
		if !w.stageOpen(ctx, p, g.BeadID, closed, live) {
			continue
		}
		pins, pinsOK := w.pins(ctx, repo, g.Check, shas)
		seen, known := w.last[n.ID]
		if !pinsOK && known {
			pins = seen.pins // gh failed: keep what was seen, no phantom move
		}
		now := w.now()
		if known && seen.pins == pins && now.Sub(seen.at) < gateRecheckEvery {
			continue
		}
		w.last[n.ID] = gateSeen{at: now, pins: pins}
		if w.protectedByWorkflow(ctx, p, n) {
			continue
		}
		checks++
		w.recheck(ctx, p, n)
	}
}

// closedStages is the set of stage beads already closed or parked (deferred): their gates are not re-checked.
// A List error is returned: an empty set would re-run (and reopen) closed stages' gates.
func (w *gateWatcher) closedStages(ctx context.Context, p store.Project) (map[string]bool, error) {
	closed := map[string]bool{}
	all, err := w.beads.List(ctx, p.Path, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		return nil, err
	}
	for _, b := range all {
		if b.Status == "closed" || b.Status == beads.StatusDeferred {
			closed[b.ID] = true
		}
	}
	return closed, nil
}

// stageOpen reads the stage and its ancestors directly (a stage without
// kind:objective is not in the listed set) and reports false when any is
// closed or parked, or cannot be read. The answer is kept in live for the poll.
func (w *gateWatcher) stageOpen(ctx context.Context, p store.Project, stage string, closed, live map[string]bool) bool {
	if ok, done := live[stage]; done {
		return ok
	}
	ok := true
	for _, id := range beads.Lineage(ctx, w.beads, p.Path, nil, stage) {
		if closed[id] {
			ok = false
			break
		}
		b, err := w.beads.Show(ctx, p.Path, id)
		if err != nil || (b != nil && (b.Status == "closed" || b.Status == beads.StatusDeferred)) {
			ok = false
			break
		}
	}
	live[stage] = ok
	return ok
}

// pins is the fingerprint of what a gate's condition reads: the head sha of
// each PR named in its check and the default branch's sha. false = a lookup
// failed (gh error), so the fingerprint says nothing and is not a move.
func (w *gateWatcher) pins(ctx context.Context, projectRepo, check string, cache map[string]string) (string, bool) {
	var parts []string
	unknown := false
	lookup := func(key string, args ...string) {
		sha, ok := cache[key]
		if !ok {
			out, err := w.gh(ctx, args...)
			if err == nil {
				sha = strings.TrimSpace(string(out))
			}
			cache[key] = sha
		}
		if sha == "" {
			unknown = true
		}
		parts = append(parts, key+"="+sha)
	}
	repos := map[string]bool{}
	for _, m := range prInCheck.FindAllStringSubmatch(check, -1) {
		repo := m[2]
		if repo == "" {
			repo = projectRepo
		}
		if repo == "" {
			continue
		}
		repos[repo] = true
		lookup(repo+"#"+m[1], "pr", "view", m[1], "--repo", repo, "--json", "headRefOid", "--jq", ".headRefOid")
	}
	for repo := range repos {
		lookup(repo+"@main", "api", "repos/"+repo+"/commits/HEAD", "--method", "GET", "--jq", ".sha")
	}
	return strings.Join(parts, " "), !unknown
}

// protectedByWorkflow is a gate the merge rule protects whose check cmdrd may not
// run to a close: anything but the standard merged template, or that template naming
// other PRs than the ones its target is judged on (its plugged blocks', its own and
// its stage's pr: labels), such as an old merged PR. cmdrd has no completion door;
// `internal-gate check` still runs it through the door. A failed read counts as
// protected: never guess.
func (w *gateWatcher) protectedByWorkflow(ctx context.Context, p store.Project, n gates.Node) bool {
	b, err := w.beads.Show(ctx, p.Path, n.ID)
	if err != nil {
		return true
	}
	if b == nil {
		return false
	}
	blocks, err := workflowtemplate.BlocksPluggedBefore(ctx, w.beads, p.Path, p.Name, *b)
	if err != nil {
		return true
	}
	if !workflowtemplate.MergeTarget(workflowtemplate.Node{Bead: *b}) && len(blocks) == 0 {
		return false
	}
	if !gates.IsMergedCheck(n.Gate.Check) {
		return true
	}
	repo := w.repoOf(p.Path)
	var prs []string
	for _, block := range blocks {
		prs = append(prs, workflowtemplate.BlockPR(block))
	}
	prs = append(prs, workflowtemplate.LabelPRs(*b, repo)...)
	if stage := beads.UnescapeLabel(workflowtemplate.Label(*b, "gate-of:")); stage != "" {
		s, err := w.beads.Show(ctx, p.Path, stage)
		if err != nil {
			return true
		}
		if s != nil {
			prs = append(prs, workflowtemplate.LabelPRs(*s, repo)...)
		}
	}
	if len(prs) == 0 {
		return true // no PR to hold the check to
	}
	named := map[string]bool{}
	for _, m := range prInCheck.FindAllStringSubmatch(n.Gate.Check, -1) {
		r := m[2]
		if r == "" {
			r = repo
		}
		if pr, err := workflowtemplate.CanonicalPR("https://github.com/" + r + "/pull/" + m[1]); err == nil {
			named[pr] = true
		}
	}
	for _, pr := range prs {
		if !named[pr] {
			return true
		}
	}
	return false
}

// recheck runs the gate's command and records a changed outcome. The Manager
// is mailed only on passed->failed and failed->passed.
func (w *gateWatcher) recheck(ctx context.Context, p store.Project, n gates.Node) {
	g := n.Gate
	status, evidence := gates.EvalAuto(ctx, w.run, p.Path, g.Check)
	if status == store.GateFailed && g.Status == store.GatePassed {
		// A transient gh error must not reopen a passed gate: it takes two
		// failures in a row (the next check is the following poll).
		if w.failing[n.ID] < gateFailsToReopen-1 {
			w.failing[n.ID]++
			delete(w.last, n.ID) // re-run on the next poll, not in 3 min
			return
		}
	}
	delete(w.failing, n.ID)
	if status == g.Status {
		return
	}
	if err := gates.SetResult(ctx, w.beads, p.Path, n, status, evidence, "auto", w.now()); err != nil {
		log.Printf("daemon: gates: record %s: %v", n.ID, err)
		return
	}
	// Passing for the first time closes the gate and releases what waited on it: that work has no
	// holder until its Manager dispatches it, so the Manager is told as for any other change.
	released := status == store.GatePassed && g.Status != store.GatePassed && g.Status != store.GateFailed
	if !(g.Status == store.GatePassed && status == store.GateFailed) && !(g.Status == store.GateFailed && status == store.GatePassed) && !released {
		return
	}
	verb := "stopped holding"
	switch {
	case released:
		verb = "passed; the work behind it is released, dispatch it"
	case status == store.GatePassed:
		verb = "holds again"
	}
	subject := fmt.Sprintf("[gate] %s: %s %s", g.BeadID, g.Name, verb)
	body := fmt.Sprintf("Gate %q on %s (%s) %s. Latest check: %s", g.Name, g.BeadID, n.ID, verb, evidence)
	if err := w.mail(ctx, wsmgr.Role(wsmgr.Owner(ctx, w.beads, p.Path, g.BeadID)), subject, body); err != nil {
		log.Printf("daemon: gates: mail for %s: %v", n.ID, err)
	}
}
