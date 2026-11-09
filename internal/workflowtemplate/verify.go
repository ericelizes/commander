package workflowtemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/example-user/commander/internal/reviewrun"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GHRunner is injected so verification never needs GitHub in tests.
type GHRunner func(context.Context, ...string) ([]byte, error)

func RunGH(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh unavailable or failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

var prPath = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)/?$`)

func CanonicalPR(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil {
		return "", fmt.Errorf("invalid GitHub PR URL %q", raw)
	}
	m := prPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", fmt.Errorf("invalid GitHub PR URL %q", raw)
	}
	return "https://github.com/" + strings.ToLower(m[1]+"/"+m[2]) + "/pull/" + m[3], nil
}

type Head struct {
	SHA    string `json:"headRefOid"`
	Base   string `json:"baseRefOid"`
	State  string `json:"state"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Checks  []Check `json:"statusCheckRollup"`
	Commits []struct {
		OID string `json:"oid"`
	} `json:"commits"`
}

// Check is one statusCheckRollup entry: a CheckRun (Name, Status,
// Conclusion) or a StatusContext (Context, State).
type Check struct {
	Kind        string    `json:"__typename"`
	Name        string    `json:"name"`
	Workflow    string    `json:"workflowName"`
	Context     string    `json:"context"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	State       string    `json:"state"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
	DetailsURL  string    `json:"detailsUrl"`
	TargetURL   string    `json:"targetUrl"`
}

// key names the check a run belongs to; "" (no name) is never merged with
// another entry.
func (c Check) key() string {
	if c.Kind == "StatusContext" || c.Context != "" {
		if c.Context == "" {
			return ""
		}
		return "context\x00" + c.Context
	}
	if c.Name == "" {
		return ""
	}
	return "run\x00" + c.Workflow + "\x00" + c.Name
}

var trailingID = regexp.MustCompile(`(\d+)/?$`)

// runID is the number ending the check's URL (GitHub's job or run id), 0
// when there is none.
func (c Check) runID() int64 {
	u := c.DetailsURL
	if u == "" {
		u = c.TargetURL
	}
	m := trailingID.FindStringSubmatch(u)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return n
}

// newer orders two runs of one check: started, then completed time, then run
// id; on a full tie the later entry wins.
func (c Check) newer(o Check) bool {
	if !c.StartedAt.Equal(o.StartedAt) {
		return c.StartedAt.After(o.StartedAt)
	}
	if !c.CompletedAt.Equal(o.CompletedAt) {
		return c.CompletedAt.After(o.CompletedAt)
	}
	return c.runID() >= o.runID()
}

// latestChecks keeps only the latest run per check name (workflow + job for a
// CheckRun, the context for a StatusContext), as gh pr checks shows them: an
// older failed run that was re-run is no longer the check's state.
func latestChecks(checks []Check) []Check {
	at := map[string]int{}
	var out []Check
	for _, c := range checks {
		k := c.key()
		if i, ok := at[k]; ok && k != "" {
			if c.newer(out[i]) {
				out[i] = c
			}
			continue
		}
		at[k] = len(out)
		out = append(out, c)
	}
	return out
}

func ReadHead(ctx context.Context, run GHRunner, pr string) (Head, error) {
	var h Head
	if run == nil {
		return h, fmt.Errorf("GitHub runner unavailable")
	}
	if _, err := CanonicalPR(pr); err != nil {
		return h, err
	}
	out, err := run(ctx, "pr", "view", pr, "--json", "headRefOid,baseRefOid,state,author,statusCheckRollup,commits")
	if err != nil {
		return h, err
	}
	if err = json.Unmarshal(out, &h); err != nil {
		return h, err
	}
	if h.SHA == "" || h.State == "" {
		return h, fmt.Errorf("GitHub returned no PR head/state")
	}
	return h, nil
}

const EvidencePrefix = "workflow-check@v1 "
const ProofPrefix = "workflow-review-proof@v1 "

// needsProof names the verifiers that read a review proof comment on the step.
func needsProof(verifier string) bool {
	return verifier == "review-proof" || verifier == "review-clean" || verifier == "recheck-clean"
}

// Evidence is written only after a successful fresh check. A changed head
// makes every prior result stale, including the final check.
type Evidence struct {
	Binding  string `json:"writer_binding,omitempty"`
	Instance string `json:"instance"`
	Step     string `json:"step"`
	Verifier string `json:"verifier"`
	PR       string `json:"pr"`
	Head     string `json:"head"`
	Finding  string `json:"finding"`
	Proof    *Proof `json:"proof,omitempty"`
	// FollowUps are the beads filed for the minor findings this review left
	// unfixed (MinorPolicy): the step passes with them, never with them dropped.
	FollowUps []string `json:"follow_ups,omitempty"`
}

func (e Evidence) Comment() string { b, _ := json.Marshal(e); return EvidencePrefix + string(b) }
func ParseEvidence(text string) (Evidence, bool) {
	var e Evidence
	s, ok := strings.CutPrefix(text, EvidencePrefix)
	if !ok || json.Unmarshal([]byte(s), &e) != nil {
		return e, false
	}
	return e, e.Instance != "" && e.Step != "" && e.Head != "" && e.Finding != ""
}

// Proof points at an independently completed review bead. FixCommits can be
// empty only when the review explicitly reports no findings.
type Proof struct {
	Addendum      *AddendumRef `json:"addendum,omitempty"`
	ReviewBead    string       `json:"review_bead"`
	Head          string       `json:"head"`
	FixCommits    []string     `json:"fix_commits"`
	NoFindings    bool         `json:"no_findings"`
	NoFixRequired bool         `json:"no_fix_required,omitempty"`
	// PriorReview and PriorHead make this a delta recheck: ReviewBead reviewed
	// only the fix delta PriorHead..Head (its typed receipt's base is PriorHead),
	// and PriorReview's complete review of PriorHead is the retained coverage.
	PriorReview string `json:"prior_review,omitempty"`
	PriorHead   string `json:"prior_head,omitempty"`
	// DeltaRenewal carries ReviewBead's review of PriorHead to Head with no new
	// review, when the verifier measures the fix delta as tiny documentation
	// (renewDelta). PriorReview stays empty: ReviewBead is the renewed review.
	DeltaRenewal bool `json:"delta_renewal,omitempty"`
}

// MinorPolicy is the review loop's bound (decision d78): blockers and majors are
// fixed and rechecked; minors get at most one fix pass; minors a review leaves
// unfixed on the passing head are filed as follow-up beads held by the owning
// Manager, and the step passes. A recheck reviews only the fix delta.
const MinorPolicy = "one-minor-fix-pass@v1"

// FollowUpFiler files (idempotently) one follow-up bead per finding left
// unfixed on head by review, returning their ids in finding order.
type FollowUpFiler func(ctx context.Context, review, head string, findings []ReviewFinding) ([]string, error)

// Review is a review bead as the proof reads it. Comments are oldest first.
type Review struct {
	ID, Status, Owner string
	Comments          []string
	Runs              map[int64]string
	Recorded          map[string]reviewrun.Recorded
	ValidateAddendum  func(context.Context, Head, *Proof) error
	// FileFollowUps is set by the close door; nil refuses a review that leaves minors.
	FileFollowUps FollowUpFiler
}
type ReviewReader func(context.Context, string) (Review, error)

// Verify fails closed, including malformed or unavailable GitHub responses.
// Legacy verifier ids remain supported for instances snapshotted by step 1.
func Verify(ctx context.Context, run GHRunner, read ReviewReader, verifier, pr, instance, step string, proof *Proof) (Evidence, error) {
	e := Evidence{Instance: instance, Step: step, Verifier: verifier, PR: pr, Proof: proof}
	h, err := ReadHead(ctx, run, pr)
	if err != nil {
		return e, err
	}
	e.Head = h.SHA
	var pending *followUps
	switch verifier {
	case "threads-resolved", "devin-comments-resolved":
		err = threads(ctx, run, pr)
	case "ci-green":
		err = ciGreen(h)
	case "merged":
		if h.State != "MERGED" {
			err = fmt.Errorf("PR is not merged")
		}
	case "deploy-ok":
		repo := strings.Split(strings.TrimPrefix(pr, "https://github.com/"), "/pull/")[0]
		var runs []struct {
			Head       string `json:"headSha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		}
		var out []byte
		out, err = run(ctx, "run", "list", "--repo", repo, "--commit", h.SHA, "--workflow", "deploy", "--limit", "1", "--json", "headSha,status,conclusion")
		if err == nil {
			err = json.Unmarshal(out, &runs)
		}
		if err == nil && (len(runs) != 1 || runs[0].Head != h.SHA || runs[0].Status != "completed" || runs[0].Conclusion != "success") {
			err = fmt.Errorf("deploy job has not succeeded on PR head %s", h.SHA)
		}
	case "review-proof", "review-clean", "recheck-clean":
		pending, err = reviewProof(ctx, run, read, pr, h, proof)
	case "head-current":
		if err = threads(ctx, run, pr); err == nil {
			err = ciGreen(h)
		}
	default:
		err = fmt.Errorf("unsupported verifier %q", verifier)
	}
	if err != nil {
		return e, fmt.Errorf("%s: %w", verifier, err)
	}
	after, err := ReadHead(ctx, run, pr)
	if err != nil {
		return e, err
	}
	if after.SHA != h.SHA || (proof != nil && proof.Addendum != nil && after.Base != h.Base) {
		return e, fmt.Errorf("PR head changed during check; retry")
	}
	e.Finding = verifier + " passed"
	if pending != nil && pending.note != "" {
		e.Finding += "; " + pending.note
	}
	if pending != nil && len(pending.findings) > 0 {
		if pending.file == nil {
			return e, fmt.Errorf("%s: review %s left %d minor finding(s) on head %s and no follow-up filer is available; close through cmdr internal-close-objective", verifier, pending.review, len(pending.findings), h.SHA)
		}
		ids, err := pending.file(ctx, pending.review, pending.head, pending.findings)
		if err != nil {
			return e, fmt.Errorf("%s: filing follow-ups for the minor findings: %w", verifier, err)
		}
		if len(ids) != len(pending.findings) {
			return e, fmt.Errorf("%s: filed %d follow-ups for %d minor findings", verifier, len(ids), len(pending.findings))
		}
		e.FollowUps = ids
		e.Finding += fmt.Sprintf("; %s: %d minor finding(s) filed as follow-ups %s", MinorPolicy, len(ids), strings.Join(ids, ", "))
	}
	if proof != nil && proof.Addendum != nil && (verifier == "review-proof" || verifier == "review-clean" || verifier == "recheck-clean") {
		e.Finding += "; original NOT CLEAN retained; conformance addendum accepted"
	}
	return e, nil
}

// ciGreen accepts SKIPPED and NEUTRAL only from a completed CheckRun; a
// StatusContext (the State field) has no such states, so only SUCCESS passes
// there. At least one check must have succeeded: all skipped proves nothing.
// Only the latest run of each check counts (latestChecks). The checks come
// from the PR head's statusCheckRollup, the list gh pr checks reads: GitHub
// leaves workflow_dispatch check suites (a manual deploy) out of it, though the
// commit's check-runs list them, so never judge from check-runs.
func ciGreen(h Head) error {
	if len(h.Checks) == 0 {
		return fmt.Errorf("no CI checks on PR head %s", h.SHA)
	}
	succeeded := false
	for _, c := range latestChecks(h.Checks) {
		switch {
		case c.State == "SUCCESS", c.Status == "COMPLETED" && c.Conclusion == "SUCCESS":
			succeeded = true
		case c.State == "" && c.Status == "COMPLETED" && (c.Conclusion == "SKIPPED" || c.Conclusion == "NEUTRAL"):
		default:
			return fmt.Errorf("CI is not green on PR head %s", h.SHA)
		}
	}
	if !succeeded {
		return fmt.Errorf("CI is not green on PR head %s: no check succeeded (all skipped or neutral)", h.SHA)
	}
	return nil
}

// followUps are the minor findings a passing review leaves on the head, to be
// filed once the head is confirmed unchanged.
type followUps struct {
	review, head string
	findings     []ReviewFinding
	file         FollowUpFiler
	note         string
}

func reviewProof(ctx context.Context, run GHRunner, read ReviewReader, pr string, h Head, p *Proof) (*followUps, error) {
	if p == nil || p.ReviewBead == "" || p.Head != h.SHA || (!p.NoFindings && !p.NoFixRequired && len(p.FixCommits) == 0) {
		return nil, fmt.Errorf("review proof required for current head: review_bead, head and fix_commits (or no_findings)")
	}
	if read == nil {
		return nil, fmt.Errorf("review reader unavailable")
	}
	r, err := read(ctx, p.ReviewBead)
	if err != nil {
		return nil, err
	}
	if r.ID != p.ReviewBead || r.Status != "closed" || !strings.HasPrefix(r.Owner, "operator-") {
		return nil, fmt.Errorf("proof must name a closed separate review Operator bead")
	}
	if p.Addendum != nil {
		if err := p.Addendum.Validate(); err != nil {
			return nil, err
		}
		if r.ValidateAddendum == nil {
			return nil, fmt.Errorf("review addendum resolver unavailable")
		}
		if err := r.ValidateAddendum(ctx, h, p); err != nil {
			return nil, err
		}
	}
	// A renewal judges the review on the head it read; everything else on the current head.
	reviewed, note := h.SHA, ""
	if p.DeltaRenewal {
		if p.Addendum != nil || !hexLength(p.PriorHead, 40) || p.PriorHead == h.SHA || len(p.FixCommits) == 0 {
			return nil, fmt.Errorf("a delta renewal names the renewed review as review_bead, its full prior_head and the fix_commits since, with no addendum")
		}
		reviewed = p.PriorHead
	}
	if p.Addendum == nil {
		if err := reviewVerdict(r, reviewed); err != nil {
			return nil, err
		}
	}
	current := currentResult(r, reviewed)
	if p.DeltaRenewal {
		if current == nil {
			return nil, fmt.Errorf("a delta renewal carries only a complete typed review: review %s has none on head %s", r.ID, p.PriorHead)
		}
		// The renewed review must stand on full coverage: a full review, or a
		// delta recheck whose proof names the full review it retained
		// (prior_review), judged exactly as that recheck was.
		full := fullCoverage(ctx, run, pr, h, r.ID, current)
		if p.PriorReview == "" {
			if full != nil {
				return nil, full
			}
		} else if full == nil {
			return nil, fmt.Errorf("review %s is a full review: a renewal names prior_review only to renew a delta recheck", r.ID)
		} else if err := deltaRecheck(ctx, run, read, pr, h, &Proof{ReviewBead: r.ID, PriorReview: p.PriorReview, PriorHead: current.Base, FixCommits: p.FixCommits}, current); err != nil {
			return nil, fmt.Errorf("renewing delta recheck %s: %w", r.ID, err)
		}
		on, err := onPR(ctx, run, pr, h, p.PriorHead)
		if err != nil {
			return nil, fmt.Errorf("prior head %s: %w", p.PriorHead, err)
		}
		if !on {
			return nil, fmt.Errorf("prior head %s is not on the PR", p.PriorHead)
		}
		if note, err = renewDelta(ctx, run, pr, p.PriorHead, h.SHA); err != nil {
			return nil, err
		}
	}
	if p.NoFixRequired {
		typed, err := resultVerdict(r, reviewed)
		if !typed || err != nil {
			return nil, fmt.Errorf("no_fix_required needs a complete typed review with no blocker or major findings")
		}
	}
	if p.NoFindings && current != nil && len(current.Findings) > 0 {
		return nil, fmt.Errorf("no_findings contradicts review %s, whose typed result on head %s has %d finding(s): use fix_commits or no_fix_required", r.ID, reviewed, len(current.Findings))
	}
	if !p.DeltaRenewal && (p.PriorReview != "" || p.PriorHead != "") {
		if err := deltaRecheck(ctx, run, read, pr, h, p, current); err != nil {
			return nil, err
		}
	} else if !p.DeltaRenewal && p.Addendum == nil && current != nil {
		// A plain proof stands on its own review: one that reviewed only a delta
		// (its base a PR commit) covers nothing before it.
		if err := fullCoverage(ctx, run, pr, h, r.ID, current); err != nil {
			return nil, err
		}
	}
	for _, sha := range p.FixCommits {
		on, err := onPR(ctx, run, pr, h, sha)
		if err != nil {
			return nil, fmt.Errorf("fix commit %s: %w", sha, err)
		}
		if !on {
			return nil, fmt.Errorf("fix commit %s is not on the PR", sha)
		}
	}
	out := &followUps{review: r.ID, head: reviewed, file: r.FileFollowUps, note: note}
	// Any finding left here is minor (reviewVerdict refused the rest): the head
	// passes, and each one becomes a follow-up rather than another review round.
	if current != nil {
		out.findings = current.Findings
	}
	return out, nil
}

// fullCoverage refuses a typed review that reviewed only a delta: its base is
// one of the PR's commits, so it saw nothing between the PR base and that
// commit. A full review's base is the PR base (or a merge-base), never on the PR.
func fullCoverage(ctx context.Context, run GHRunner, pr string, h Head, review string, res *ReviewResult) error {
	on, err := onPR(ctx, run, pr, h, res.Base)
	if err != nil {
		return fmt.Errorf("review %s base %s: %w", review, res.Base, err)
	}
	if on {
		return fmt.Errorf("review %s reviewed only the delta %s..%s (its base is a PR commit): prove it as a delta recheck with prior_review and prior_head naming the full review it builds on", review, res.Base, res.Head)
	}
	return nil
}

// currentResult is the latest valid typed result of r on head, nil when none
// (a legacy CLEAN report carries no findings to file).
func currentResult(r Review, head string) *ReviewResult {
	var latest *ReviewResult
	for _, c := range r.Comments {
		result, ok, err := ParseReviewResult(c)
		if !ok || err != nil || result.Head != head || result.validate(r) != nil {
			continue
		}
		latest = &result
	}
	return latest
}

// deltaRecheck holds a recheck that reviewed only the fix delta to honest
// retained coverage: the recheck's typed receipt has base = the prior head, the
// prior review is a complete full typed review of that head (never itself a
// delta, so coverage never chains), every blocker/major it found is named in the
// recheck's resolved list, the prior head is on the PR and the fix pass between
// them is named.
func deltaRecheck(ctx context.Context, run GHRunner, read ReviewReader, pr string, h Head, p *Proof, current *ReviewResult) error {
	if p.PriorReview == "" || !hexLength(p.PriorHead, 40) || p.PriorHead == h.SHA {
		return fmt.Errorf("a delta recheck proof needs prior_review and the full prior_head it was reviewed at (not the current head)")
	}
	if p.PriorReview == p.ReviewBead {
		return fmt.Errorf("a delta recheck must be a new review of the fix delta, not review %s again", p.ReviewBead)
	}
	if len(p.FixCommits) == 0 {
		return fmt.Errorf("a delta recheck reviews a fix pass: name its fix_commits")
	}
	if current == nil {
		return fmt.Errorf("a delta recheck needs a typed review result on head %s", h.SHA)
	}
	if current.Base != p.PriorHead {
		return fmt.Errorf("delta recheck %s reviewed %s..%s, not the fix delta since the prior reviewed head %s: run review-policy with --base %s", p.ReviewBead, current.Base, h.SHA, p.PriorHead, p.PriorHead)
	}
	prior, err := read(ctx, p.PriorReview)
	if err != nil {
		return fmt.Errorf("prior review %s: %w", p.PriorReview, err)
	}
	if prior.ID != p.PriorReview || prior.Status != "closed" || !strings.HasPrefix(prior.Owner, "operator-") {
		return fmt.Errorf("prior review %s must be a closed separate review Operator bead", p.PriorReview)
	}
	retained := currentResult(prior, p.PriorHead)
	if retained == nil || retained.Execution != "complete" || retained.Coverage != "complete" || retained.Source == "not-assessed" {
		return fmt.Errorf("prior review %s has no complete typed review of head %s to retain", p.PriorReview, p.PriorHead)
	}
	if err := fullCoverage(ctx, run, pr, h, p.PriorReview, retained); err != nil {
		return fmt.Errorf("a delta recheck retains one full review, never another delta (a later recheck names the full review as prior_review and its head as prior_head): %w", err)
	}
	resolved := map[string]bool{}
	for _, id := range current.Resolved {
		resolved[id] = true
	}
	known := map[string]bool{}
	var unverified []string
	for _, f := range retained.Findings {
		known[f.ID] = true
		switch {
		case resolved[f.ID]:
		case f.Severity == "blocker" || f.Severity == "major":
			unverified = append(unverified, f.ID)
		}
	}
	for _, id := range current.Resolved {
		if !known[id] {
			return fmt.Errorf("delta recheck %s resolves %s, which is no finding of prior review %s", p.ReviewBead, id, p.PriorReview)
		}
	}
	if len(unverified) > 0 {
		return fmt.Errorf("delta recheck %s does not verify the prior review's blocker/major finding(s) %s fixed: its receipt must list each one in resolved", p.ReviewBead, strings.Join(unverified, ", "))
	}
	on, err := onPR(ctx, run, pr, h, p.PriorHead)
	if err != nil {
		return fmt.Errorf("prior head %s: %w", p.PriorHead, err)
	}
	if !on {
		return fmt.Errorf("prior head %s is not on the PR", p.PriorHead)
	}
	return nil
}

// onPR says whether sha is one of the PR's commits. `gh pr view --json commits`
// lists only the oldest 100 (the REST list stops at 250), so a list that may be
// cut short is no answer: beyond it the commit is on the PR when it is an
// ancestor of the head and not of the base (compare API, any length).
func onPR(ctx context.Context, run GHRunner, pr string, h Head, sha string) (bool, error) {
	for _, c := range h.Commits {
		if c.OID == sha {
			return true, nil
		}
	}
	if len(h.Commits) < 100 {
		return false, nil
	}
	if h.Base == "" {
		return false, fmt.Errorf("GitHub returned no PR base")
	}
	repo := strings.Split(strings.TrimPrefix(pr, "https://github.com/"), "/pull/")[0]
	status := func(from, to string) (string, error) {
		out, err := run(ctx, "api", "repos/"+repo+"/compare/"+url.PathEscape(from)+"..."+url.PathEscape(to), "--method", "GET", "--jq", ".status")
		if err != nil {
			return "", err
		}
		s := strings.TrimSpace(string(out))
		switch s {
		case "ahead", "behind", "identical", "diverged":
			return s, nil
		}
		return "", fmt.Errorf("GitHub compare returned %q", s)
	}
	toHead, err := status(sha, h.SHA)
	if err != nil {
		return false, err
	}
	if toHead != "ahead" && toHead != "identical" {
		return false, nil
	}
	fromBase, err := status(h.Base, sha)
	if err != nil {
		return false, err
	}
	return fromBase == "ahead" || fromBase == "diverged", nil
}

// reviewVerdict reads the review's report: its latest comment with a verdict line
// (a line that is exactly CLEAN or NOT CLEAN), so notes added later do not unmake
// it. The report must hold no NOT CLEAN and name the head; an earlier NOT CLEAN
// report of this same head stands, a later CLEAN does not undo it.
func reviewVerdict(r Review, head string) error {
	if typed, err := resultVerdict(r, head); typed {
		return err
	}
	report := -1
	for i := len(r.Comments) - 1; i >= 0 && report < 0; i-- {
		if clean, notClean := verdictLines(r.Comments[i]); clean || notClean {
			report = i
		}
	}
	if report < 0 {
		return fmt.Errorf("review %s has no CLEAN or NOT CLEAN line: its review Operator must post its report ending in one", r.ID)
	}
	if _, notClean := verdictLines(r.Comments[report]); notClean {
		return fmt.Errorf("review %s did not end CLEAN: fix its findings, push, then have a new review Operator review the new head", r.ID)
	}
	if !hasHeadLine(r.Comments[report], head) {
		return fmt.Errorf("review %s does not identify current PR head %s: its report needs a line Head: <full-sha>", r.ID, head)
	}
	for _, c := range r.Comments[:report] {
		if _, notClean := verdictLines(c); notClean && strings.Contains(c, head) {
			return fmt.Errorf("review %s reported NOT CLEAN on head %s: push the fix, then have a new review Operator review the new head", r.ID, head)
		}
	}
	return nil
}

// hasHeadLine is a line that is exactly "Head: <head>", not the sha anywhere in the text.
// Markdown around it ("- **Head:** `<sha>`") is dropped first.
func hasHeadLine(comment, head string) bool {
	for _, line := range strings.Split(comment, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(strings.NewReplacer("*", "", "`", "").Replace(line)), "-+ ")
		if v, ok := strings.CutPrefix(line, "Head:"); ok && strings.TrimSpace(v) == head {
			return true
		}
	}
	return false
}

func verdictLines(comment string) (clean, notClean bool) {
	for _, line := range strings.Split(comment, "\n") {
		switch strings.TrimSpace(line) {
		case "CLEAN":
			clean = true
		case "NOT CLEAN":
			notClean = true
		}
	}
	return clean, notClean
}

func threads(ctx context.Context, run GHRunner, pr string) error {
	m := prPath.FindStringSubmatch(strings.TrimPrefix(pr, "https://github.com"))
	if m == nil {
		return fmt.Errorf("invalid PR")
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		query := `query($owner:String!,$repo:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){reviewThreads(first:100,after:$cursor){nodes{isResolved comments(first:1){totalCount nodes{author{login}}}} pageInfo{hasNextPage endCursor}}}}}`
		args := []string{"api", "graphql", "-f", "query=" + query, "-f", "owner=" + m[1], "-f", "repo=" + m[2], "-F", "number=" + m[3]}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		out, err := run(ctx, args...)
		if err != nil {
			return err
		}
		var v struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Repository struct {
					PR struct {
						Threads *struct {
							Nodes []struct {
								Resolved bool `json:"isResolved"`
								Comments struct {
									Count int `json:"totalCount"`
									Nodes []struct {
										Author *struct {
											Login string `json:"login"`
										} `json:"author"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
							Page struct {
								More   bool   `json:"hasNextPage"`
								Cursor string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err = json.Unmarshal(out, &v); err != nil {
			return err
		}
		t := v.Data.Repository.PR.Threads
		if len(v.Errors) > 0 || t == nil {
			return fmt.Errorf("GitHub review threads unreadable")
		}
		for _, n := range t.Nodes {
			if !n.Resolved {
				return fmt.Errorf("unresolved review thread")
			}
			for _, c := range n.Comments.Nodes {
				if c.Author != nil && strings.Contains(strings.ToLower(c.Author.Login), "devin") && n.Comments.Count < 2 {
					return fmt.Errorf("Devin thread has no answer")
				}
			}
		}
		if !t.Page.More {
			return nil
		}
		if t.Page.Cursor == "" || seen[t.Page.Cursor] {
			return fmt.Errorf("invalid review thread pagination")
		}
		cursor = t.Page.Cursor
		seen[cursor] = true
	}
}

// SettledVerifier marks a step closed by `cmdr workflow settle-block` after its
// PR merged outside the review: the step was never checked, and its evidence
// says so. Only that verb writes it; internal-comment refuses the prefix.
const SettledVerifier = "settled"

// SettledFinding is what a settled step records: the merge, never a review.
func SettledFinding(head, reason string) string {
	return "settled: PR merged at " + head + ", review not completed. Reason: " + reason
}
