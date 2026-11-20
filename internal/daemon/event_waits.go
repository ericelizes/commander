package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// Event waits (deploy, pr, event) are resolved by cmdrd itself on the wait poll
// (about once a minute), with no model call: the moment the deploy succeeds,
// the PR settles or the message appears, the wait clears and the step continues
// (its own agent is woken, or auto-dispatch starts it). Nobody polls on a timer.

// deployRun is a successful deploy workflow run.
type deployRun struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	HeadSha string `json:"head_sha"`
	Branch  string `json:"head_branch"`
	Updated string `json:"updated_at"`
	Event   string `json:"event"`
}

// deployJob is a deploy run's job that ships the code ("deploy").
var deployJob = regexp.MustCompile(`(?i)^deploy\b`)

// deployedLine is the resolve job's log line naming what it deployed.
var deployedLine = regexp.MustCompile(`Deploying (prod-\S+) \(([0-9a-f]{7,40})\)`)

// defaultDeployWorkflow matches a prod deploy workflow's name ("Deploy orchestrator to prod").
var defaultDeployWorkflow = regexp.MustCompile(`(?i)\bdeploy\b.*\bprod`)

// deployRunsChecked bounds the compare calls per deploy wait per poll: the newest runs only.
const deployRunsChecked = 15

// deployFor finds the newest successful prod deploy run whose commit contains sha.
// Read-only gh api GET calls; the run list is read once per repo per poll, and a
// compare result (immutable) is remembered.
func (w *waitWatcher) deployFor(ctx context.Context, repo, sha string) (deployRun, bool) {
	runs, ok := w.deployRuns[repo]
	if !ok {
		// Each deploy workflow's own successful runs. The repo-wide list of
		// recent successful runs is mostly CI in a busy repo: Nov 20 the prod
		// deploy fell out of its newest 50 and a deploy that shipped was missed.
		out, err := w.gh(ctx, "api", "repos/"+repo+"/actions/workflows?per_page=100", "--method", "GET", "--jq", `.workflows[]|[.id,.name]|@tsv`)
		if err != nil {
			return deployRun{}, false
		}
		re := w.deployWorkflow
		if re == nil {
			re = defaultDeployWorkflow
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			id, name, found := strings.Cut(line, "\t")
			if !found || !re.MatchString(name) {
				continue
			}
			out, err := w.gh(ctx, "api", "repos/"+repo+"/actions/workflows/"+id+"/runs?status=success&per_page=20", "--method", "GET", "--jq", ".workflow_runs")
			if err != nil {
				return deployRun{}, false
			}
			var all []deployRun
			if json.Unmarshal(out, &all) != nil {
				return deployRun{}, false
			}
			for _, r := range all {
				if r.HeadSha != "" {
					runs = append(runs, r)
				}
			}
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID }) // newest first
		if w.deployRuns == nil {
			w.deployRuns = map[string][]deployRun{}
		}
		w.deployRuns[repo] = runs
	}
	for i, r := range runs {
		if i >= deployRunsChecked {
			break
		}
		shipped := w.deployedSha(ctx, repo, r)
		if shipped == "" {
			continue
		}
		key := repo + "|" + sha + "|" + shipped
		status, seen := w.compared[key]
		if !seen {
			out, err := w.gh(ctx, "api", "repos/"+repo+"/compare/"+sha+"..."+shipped, "--method", "GET", "--jq", ".status")
			if err != nil {
				continue
			}
			status = strings.TrimSpace(string(out))
			if w.compared == nil {
				w.compared = map[string]string{}
			}
			w.compared[key] = status
		}
		if status == "ahead" || status == "identical" {
			return r, true
		}
	}
	return deployRun{}, false
}

// deployedSha is the commit a successful deploy run actually shipped, or ""
// when it shipped nothing (or that is not known yet). A run succeeds even when
// it stands down outside the deploy window with its deploy job skipped, and a
// scheduled run's head is main, not the tag it deploys. Nov 20: three
// tag-only runs cleared deploy waits for code that was not on prod.
func (w *waitWatcher) deployedSha(ctx context.Context, repo string, r deployRun) string {
	if s, ok := w.deployedShas[r.ID]; ok {
		return s
	}
	s, known := w.readDeployedSha(ctx, repo, r)
	if known {
		if w.deployedShas == nil {
			w.deployedShas = map[int64]string{}
		}
		w.deployedShas[r.ID] = s
	}
	return s
}

func (w *waitWatcher) readDeployedSha(ctx context.Context, repo string, r deployRun) (sha string, known bool) {
	out, err := w.gh(ctx, "api", fmt.Sprintf("repos/%s/actions/runs/%d/jobs", repo, r.ID), "--method", "GET", "--jq", `.jobs[]|[.id,.name,.conclusion]|@tsv`)
	if err != nil {
		return "", false
	}
	var resolveJob string
	hasDeployJob, deployed := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		switch {
		case deployJob.MatchString(f[1]):
			hasDeployJob = true
			deployed = deployed || f[2] == "success"
		case strings.EqualFold(f[1], "resolve"):
			resolveJob = f[0]
		}
	}
	if hasDeployJob && !deployed {
		return "", true
	}
	if resolveJob != "" {
		if log, err := w.gh(ctx, "api", "repos/"+repo+"/actions/jobs/"+resolveJob+"/logs", "--method", "GET"); err == nil {
			if m := deployedLine.FindSubmatch(log); m != nil {
				return string(m[2]), true
			}
		}
	}
	if r.Event == "schedule" {
		return "", false // its head is main; what it deployed is still unknown
	}
	return r.HeadSha, true
}

// prOutcome reports whether a pr wait's condition has settled: merged (or
// closed), green (checks settled, any result), approved (approved or changes
// requested). result says how it settled.
func (w *waitWatcher) prOutcome(ctx context.Context, repo string, pr int, cond string) (result string, settled bool) {
	switch cond {
	case beads.PRGreen:
		failing, done := w.ciResult(ctx, repo, pr)
		if !done {
			return "", false
		}
		if failing > 0 {
			return fmt.Sprintf("checks finished, %d failing", failing), true
		}
		return "checks all green", true
	case beads.PRMerged, beads.PRApproved:
		out, err := w.gh(ctx, "pr", "view", strconv.Itoa(pr), "--repo", repo, "--json", "state,reviewDecision")
		if err != nil {
			return "", false
		}
		var v struct{ State, ReviewDecision string }
		if json.Unmarshal(out, &v) != nil {
			return "", false
		}
		switch {
		case v.State == "MERGED":
			return "merged", true
		case v.State == "CLOSED":
			return "closed without merging", true
		case cond == beads.PRApproved && v.ReviewDecision == "APPROVED":
			return "approved", true
		case cond == beads.PRApproved && v.ReviewDecision == "CHANGES_REQUESTED":
			return "changes requested", true
		}
	}
	return "", false
}

// slackMessage is one message in a channel's history.
type slackMessage struct {
	TS   string
	Text string
}

// slackHistoryFunc reads a channel's messages after oldest.
type slackHistoryFunc func(ctx context.Context, channel string, oldest time.Time) ([]slackMessage, error)

// newSlackHistory reads ~/.commander/slack-token (or COMMANDER_SLACK_TOKEN): a
// Slack token with channels:history (and groups:history for private
// channels). No token: nil, and event:slack waits report "not configured".
func newSlackHistory(home string) slackHistoryFunc {
	token := strings.TrimSpace(os.Getenv("COMMANDER_SLACK_TOKEN"))
	if token == "" {
		if b, err := os.ReadFile(filepath.Join(home, "slack-token")); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	if token == "" {
		return nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	return func(ctx context.Context, channel string, oldest time.Time) ([]slackMessage, error) {
		q := url.Values{"channel": {channel}, "oldest": {strconv.FormatInt(oldest.Unix(), 10)}, "limit": {"200"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://slack.com/api/conversations.history?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var body struct {
			OK       bool   `json:"ok"`
			Error    string `json:"error"`
			Messages []struct {
				TS          string `json:"ts"`
				Text        string `json:"text"`
				Attachments []struct {
					Text, Fallback string
				} `json:"attachments"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, err
		}
		if !body.OK {
			return nil, fmt.Errorf("slack conversations.history: %s", body.Error)
		}
		out := make([]slackMessage, 0, len(body.Messages))
		for _, m := range body.Messages {
			text := m.Text
			for _, a := range m.Attachments {
				text += "\n" + a.Text + "\n" + a.Fallback
			}
			out = append(out, slackMessage{TS: m.TS, Text: text})
		}
		return out, nil
	}
}

// eventFor finds a message matching an event wait's pattern posted after the wait began.
// configured is false when cmdrd has no access to the wait's source.
func (w *waitWatcher) eventFor(ctx context.Context, wt beads.Wait) (match slackMessage, found, configured bool) {
	re, err := regexp.Compile(wt.Ref)
	if err != nil {
		return slackMessage{}, false, true
	}
	switch wt.Cond {
	case beads.EventSlack:
		if w.slack == nil {
			return slackMessage{}, false, false
		}
		msgs, err := w.slack(ctx, wt.Channel, wt.Since)
		if err != nil {
			return slackMessage{}, false, true
		}
		for _, m := range msgs {
			if re.MatchString(m.Text) {
				return m, true, true
			}
		}
		return slackMessage{}, false, true
	}
	// Notify has no delivery-read API cmdrd can call yet.
	return slackMessage{}, false, false
}

func eventLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// eventWaitFired reports a deploy, pr or event wait that has happened, with the message for its owner.
func (w *waitWatcher) eventWaitFired(ctx context.Context, projectRepo string, b beads.Bead, wt beads.Wait) (subject, body string, fired bool) {
	switch wt.Kind {
	case beads.WaitDeploy:
		repo := waitRepo(wt, projectRepo)
		if repo == "" {
			return
		}
		run, ok := w.deployFor(ctx, repo, wt.Sha)
		if !ok {
			return
		}
		at := run.Branch
		subject = fmt.Sprintf("[deploy] %s is live in %s", short(wt.Sha), at)
		return subject, fmt.Sprintf("%q succeeded (run %d, %s, %s) with a commit containing %s. You were waiting on that deploy (%s). Pick the work back up now: run the check it was for.", run.Name, run.ID, at, run.Updated, wt.Sha, b.ID), true
	case beads.WaitPR:
		repo := waitRepo(wt, projectRepo)
		if repo == "" {
			return
		}
		result, ok := w.prOutcome(ctx, repo, wt.PR, wt.Cond)
		if !ok {
			return
		}
		subject = fmt.Sprintf("[pr] #%d %s", wt.PR, result)
		return subject, fmt.Sprintf("PR #%d in %s: %s. You were waiting on it to be %s (%s). Pick the work back up.", wt.PR, repo, result, wt.Cond, b.ID), true
	case beads.WaitEvent:
		m, found, configured := w.eventFor(ctx, wt)
		if !configured {
			key := "unconfigured|" + b.ID + "|" + wt.Label()
			if w.woken[key] {
				return
			}
			w.woken[key] = true
			subject = fmt.Sprintf("[event] cmdrd cannot watch %s events", wt.Cond)
			need := "a Slack token with channels:history in ~/.commander/slack-token (or COMMANDER_SLACK_TOKEN)"
			if wt.Cond == beads.EventNotify {
				need = "a Notify delivery-read API (not built yet); use event:slack:<channel>:<pattern> on the channel Notify posts to"
			}
			return subject, fmt.Sprintf("Step %s waits on a %s event in %s matching %q, but cmdrd has no access to %s events: it needs %s. The wait is cleared so the step is not held by something nobody can see; record a wait cmdrd can watch.", b.ID, wt.Cond, wt.Channel, wt.Ref, wt.Cond, need), true
		}
		if !found {
			return
		}
		subject = fmt.Sprintf("[event] %s in %s", eventLine(m.Text), wt.Channel)
		return subject, fmt.Sprintf("A message matching %q appeared in %s (ts %s):\n\n%s\n\nYou were waiting on it (%s). Pick the work back up now: run the check it was for.", wt.Ref, wt.Channel, m.TS, eventLine(m.Text), b.ID), true
	}
	return
}

// expectOverdue tells the Commander once when a deploy, pr or event wait passed
// its expect-by time without happening. The wait stays: it may still happen.
func (w *waitWatcher) expectOverdue(ctx context.Context, b beads.Bead, wt beads.Wait) {
	if !wt.IsEventWait() || wt.By.IsZero() || w.now().Before(wt.By) {
		return
	}
	key := "overdue|" + b.ID + "|" + wt.Label()
	if w.woken[key] {
		return
	}
	// The board shows it red (invalid-state: wait-overdue); nobody is mailed.
	project := w.projectOf(b)
	w.qmu.Lock()
	if w.overdue == nil {
		w.overdue = map[string]livedata.InvalidState{}
	}
	w.overdue[b.ID] = livedata.InvalidState{Project: project, Rule: "wait-overdue", Beads: []string{b.ID},
		Why: fmt.Sprintf("it expected %s by %s, and it has not happened", wt.Text(), wt.By.UTC().Format("Jan 2 15:04 UTC")),
		Fix: "keep waiting (move the expect-by with ,by=<RFC3339>), or plan the step that acts on why it has not happened"}
	w.qmu.Unlock()
	w.woken[key] = true
}
