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
}

// Review is a review bead as the proof reads it. Comments are oldest first.
type Review struct {
	ID, Status, Owner string
	Comments          []string
	Runs              map[int64]string
	Recorded          map[string]reviewrun.Recorded
	ValidateAddendum  func(context.Context, Head, *Proof) error
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
		err = reviewProof(ctx, run, read, pr, h, proof)
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
func reviewProof(ctx context.Context, run GHRunner, read ReviewReader, pr string, h Head, p *Proof) error {
	if p == nil || p.ReviewBead == "" || p.Head != h.SHA || (!p.NoFindings && !p.NoFixRequired && len(p.FixCommits) == 0) {
		return fmt.Errorf("review proof required for current head: review_bead, head and fix_commits (or no_findings)")
	}
	if read == nil {
		return fmt.Errorf("review reader unavailable")
	}
	r, err := read(ctx, p.ReviewBead)
	if err != nil {
		return err
	}
	if r.ID != p.ReviewBead || r.Status != "closed" || !strings.HasPrefix(r.Owner, "operator-") {
		return fmt.Errorf("proof must name a closed separate review Operator bead")
	}
	if p.Addendum != nil {
		if err := p.Addendum.Validate(); err != nil {
			return err
		}
		if r.ValidateAddendum == nil {
			return fmt.Errorf("review addendum resolver unavailable")
		}
		if err := r.ValidateAddendum(ctx, h, p); err != nil {
			return err
		}
	} else if err := reviewVerdict(r, h.SHA); err != nil {
		return err
	}
	if p.NoFixRequired {
		typed, err := resultVerdict(r, h.SHA)
		if !typed || err != nil {
			return fmt.Errorf("no_fix_required needs a complete typed review with no blocker or major findings")
		}
	}
	for _, sha := range p.FixCommits {
		on, err := onPR(ctx, run, pr, h, sha)
		if err != nil {
			return fmt.Errorf("fix commit %s: %w", sha, err)
		}
		if !on {
			return fmt.Errorf("fix commit %s is not on the PR", sha)
		}
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
