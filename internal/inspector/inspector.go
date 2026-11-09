// Package inspector turns an inspector tab (a URL or a command) into a Card the
// right-hand column can draw. It is UI-agnostic: no Bubble Tea, no lipgloss, no
// store. Fetches take an injectable Exec so tests feed canned output.
package inspector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Renderer kinds, as returned by Detect and stored in a tab's renderer column.
const (
	RendererGitHubPR = "github-pr"
	RendererLinear   = "linear"
	RendererNotion   = "notion"
	RendererWeb      = "web"
	RendererBrowser  = "browser" // explicit opt-in to the screenshot renderer
	RendererLink     = "link"
	RendererCmd      = "cmd"
)

// Tab kinds (inspector_tabs.kind).
const (
	KindURL = "url"
	KindCmd = "cmd"
	// KindDecision is a ⚑ decision card; its target is the flagged step's bead id
	// and the inspector draws it as a form (never fetched).
	KindDecision = "decision"
)

// Target is what a tab asks us to show.
type Target struct {
	Kind     string // url | cmd
	Target   string // URL or shell command
	Renderer string // "" = auto-detect
	Title    string
	// Cols x Rows is the cell box a picture renderer should fill (0 = default).
	Cols, Rows int
}

// Section is a labelled block of short lines under the card header
// (CHECKS, REVIEW, ...). Lines start with a status glyph the UI may color.
type Section struct {
	Title string
	Lines []string
}

// Card is everything the UI needs to draw one tab.
type Card struct {
	Renderer string
	Title    string
	State    string // OPEN | MERGED | CLOSED | DONE | ... ("" for none)
	URL      string
	Meta     []string
	Sections []Section
	Markdown string
	// Preformatted marks Markdown as raw terminal output (ANSI kept, no wrapping).
	Preformatted bool
	// Attn is true when the card needs the user (review requested, a failing check).
	Attn bool
	// UpdatedAt is when the source last changed (zero when unknown).
	UpdatedAt time.Time
	// FetchedAt is when this card was produced.
	FetchedAt time.Time
	// Err is set on a fallback card when the fetch failed.
	Err string
	// Image is a PNG of the page (browser renderer) sized for ImageCols x
	// ImageRows cells; Markdown stays the text fallback.
	Image                string
	ImageCols, ImageRows int
}

var (
	ghPRRe        = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)
	linearRe      = regexp.MustCompile(`^https?://linear\.app/`)
	notionRe      = regexp.MustCompile(`^https?://([^/]*\.)?(notion\.so|notion\.site|notion\.com)(/|$)`)
	linearIssueRe = regexp.MustCompile(`^[A-Za-z]+-\d+$`)
	httpRe        = regexp.MustCompile(`^https?://`)
	linkOnlyRe    = regexp.MustCompile(`^https?://([^/]*\.)?(datadoghq\.(com|eu)|grafana\.[^/]+|localhost|127\.0\.0\.1|0\.0\.0\.0)(:\d+)?(/|$)`)
)

// Detect picks the renderer for a tab. A kind of "cmd" is always the command
// renderer; URLs go to the most specific source adapter, then web, then link.
// Authenticated or JS-only hosts (Datadog, Grafana, localhost) and non-http
// targets are link cards.
func Detect(kind, target string) string {
	if kind == KindCmd {
		return RendererCmd
	}
	switch {
	case ghPRRe.MatchString(target):
		return RendererGitHubPR
	case linearRe.MatchString(target):
		return RendererLinear
	case notionRe.MatchString(target):
		return RendererNotion
	case linkOnlyRe.MatchString(target):
		return RendererLink
	case httpRe.MatchString(target):
		return RendererWeb
	}
	return RendererLink
}

// ParseGitHubPR splits a pull request URL into owner, repo and number.
func ParseGitHubPR(target string) (owner, repo string, number int, ok bool) {
	m := ghPRRe.FindStringSubmatch(target)
	if m == nil {
		return "", "", 0, false
	}
	n, _ := strconv.Atoi(m[3])
	return m[1], m[2], n, true
}

// Exec runs a program and returns its combined output. It is the test seam for
// every fetch.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// RunExec is the real Exec.
func RunExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Fetcher produces cards.
type Fetcher struct {
	Exec    Exec          // nil = RunExec
	Timeout time.Duration // per fetch; 0 = 15s (90s for notion/linear)
	Now     func() time.Time
	HTTP    HTTPDo              // nil = http.DefaultClient.Do
	Getenv  func(string) string // nil = os.Getenv
	Browser BrowserConfig       // picture renderer for web/browser tabs
}

func (f Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Renderer is the renderer Fetch uses when the tab names none: Detect, except
// that Notion and Linear URLs become pictures (browser) when the signed-in
// browser profile has a session for them. GitHub PRs keep gh.
func (f Fetcher) Renderer(t Target) string {
	r := Detect(t.Kind, t.Target)
	if (r == RendererNotion || r == RendererLinear) && f.browserFirst(t) {
		return RendererBrowser
	}
	return r
}

// Fetch builds the card for t. It never returns a nil-card: on failure it
// returns a link-card fallback with Err set, plus the error.
func (f Fetcher) Fetch(ctx context.Context, t Target) (Card, error) {
	run := f.Exec
	if run == nil {
		run = RunExec
	}
	renderer := t.Renderer
	if renderer == "" {
		renderer = f.Renderer(t)
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
		if renderer == RendererNotion || renderer == RendererLinear {
			timeout = slowFetchTimeout // claude -p takes seconds to a minute
		}
		if renderer == RendererWeb || renderer == RendererBrowser {
			timeout = browserTimeout
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var card Card
	var err error
	switch renderer {
	case RendererGitHubPR:
		card, err = f.githubPR(ctx, run, t)
	case RendererCmd:
		card = f.command(ctx, run, t)
	case RendererNotion:
		card, err = f.notion(ctx, run, t)
	case RendererLinear:
		card, err = f.linear(ctx, run, t)
	case RendererWeb, RendererBrowser:
		// A picture of the page when the terminal and Chrome allow it; else the
		// link card with a hint (`o` opens the real page).
		card, err = f.browser(ctx, run, t)
	default:
		card = LinkCard(t)
	}
	if err != nil {
		if card.Title == "" { // notion/linear return their own hinted link card
			card = LinkCard(t)
		}
		card.Err = err.Error()
	}
	card.Renderer = renderer
	card.FetchedAt = f.now()
	return card, err
}

// LinkCard is the no-fetch card: a title from the URL's host and path.
func LinkCard(t Target) Card {
	title := t.Title
	if title == "" {
		title = TitleFromURL(t.Target)
	}
	c := Card{Renderer: RendererLink, Title: title, URL: t.Target}
	if t.Kind == KindCmd {
		c.URL = ""
		return c
	}
	c.Markdown = "Not previewed in the pane.\n\nPress o to open it in your browser, y to copy the URL."
	return c
}

// TitleFromURL is a readable title for a URL with no fetched title:
// "github.com/o/r/issues/4" -> "github.com / r / issues / 4"-ish, kept simple as
// host plus the last path segments.
func TitleFromURL(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return target
	}
	var segs []string
	for _, s := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) > 2 {
		segs = segs[len(segs)-2:]
	}
	if len(segs) == 0 {
		return u.Host
	}
	return u.Host + " / " + strings.Join(segs, " / ")
}

// command runs t.Target through sh -c and keeps its ANSI output.
func (f Fetcher) command(ctx context.Context, run Exec, t Target) Card {
	out, err := run(ctx, "sh", "-c", t.Target)
	c := Card{Title: t.Title, State: "", Markdown: strings.TrimRight(string(out), "\n"), Preformatted: true}
	if c.Title == "" {
		c.Title = t.Target
	}
	c.Meta = []string{"$ " + t.Target}
	if err != nil {
		c.State = "FAILED"
		c.Attn = true
		if c.Markdown == "" {
			c.Markdown = err.Error()
		}
	}
	return c
}

type ghPR struct {
	Title             string                 `json:"title"`
	State             string                 `json:"state"`
	HeadRefName       string                 `json:"headRefName"`
	BaseRefName       string                 `json:"baseRefName"`
	Additions         int                    `json:"additions"`
	Deletions         int                    `json:"deletions"`
	ChangedFiles      int                    `json:"changedFiles"`
	ReviewDecision    string                 `json:"reviewDecision"`
	Body              string                 `json:"body"`
	UpdatedAt         string                 `json:"updatedAt"`
	Author            struct{ Login string } `json:"author"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`
		Context    string `json:"context"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
	Reviews []struct {
		Author struct{ Login string } `json:"author"`
		State  string                 `json:"state"`
	} `json:"reviews"`
}

func (f Fetcher) githubPR(ctx context.Context, run Exec, t Target) (Card, error) {
	owner, repo, n, ok := ParseGitHubPR(t.Target)
	if !ok {
		return Card{}, fmt.Errorf("not a pull request URL: %s", t.Target)
	}
	out, err := run(ctx, "gh", "pr", "view", strconv.Itoa(n), "-R", owner+"/"+repo, "--json",
		"title,state,author,headRefName,baseRefName,additions,deletions,changedFiles,statusCheckRollup,reviewDecision,reviews,body,updatedAt")
	if err != nil {
		return Card{}, fmt.Errorf("gh pr view: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return ParseGitHubPRJSON(out, t)
}

// ParseGitHubPRJSON turns `gh pr view --json ...` output into a card.
func ParseGitHubPRJSON(data []byte, t Target) (Card, error) {
	var pr ghPR
	if err := json.Unmarshal(data, &pr); err != nil {
		return Card{}, fmt.Errorf("parse gh output: %w", err)
	}
	c := Card{
		Title:    pr.Title,
		State:    strings.ToUpper(pr.State),
		URL:      t.Target,
		Markdown: strings.TrimSpace(pr.Body),
	}
	_, _, n, _ := ParseGitHubPR(t.Target)
	if n > 0 {
		c.Title = fmt.Sprintf("PR #%d  %s", n, pr.Title)
	}
	if pr.HeadRefName != "" {
		c.Meta = append(c.Meta, pr.HeadRefName+" → "+pr.BaseRefName)
	}
	c.Meta = append(c.Meta, fmt.Sprintf("+%d −%d", pr.Additions, pr.Deletions), fmt.Sprintf("%d files", pr.ChangedFiles))
	if pr.Author.Login != "" {
		c.Meta = append(c.Meta, "by "+pr.Author.Login)
	}
	if ts, err := time.Parse(time.RFC3339, pr.UpdatedAt); err == nil {
		c.UpdatedAt = ts
	}

	var pass, fail, pending int
	var failing []string
	for _, ck := range pr.StatusCheckRollup {
		name := ck.Name
		if name == "" {
			name = ck.Context
		}
		switch strings.ToUpper(firstNonEmpty(ck.Conclusion, ck.State)) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			pass++
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			fail++
			failing = append(failing, "✗ "+name)
		default: // QUEUED / IN_PROGRESS / PENDING / ""
			pending++
		}
	}
	if pass+fail+pending > 0 {
		lines := []string{fmt.Sprintf("✓ %d  ✗ %d  ○ %d", pass, fail, pending)}
		c.Sections = append(c.Sections, Section{Title: "CHECKS", Lines: append(lines, failing...)})
	}

	var rev []string
	switch pr.ReviewDecision {
	case "APPROVED":
		rev = append(rev, "✓ approved")
	case "CHANGES_REQUESTED":
		rev = append(rev, "✗ changes requested")
	case "REVIEW_REQUIRED":
		rev = append(rev, "⚑ awaiting review")
	}
	reviews := latestReviews(pr)
	logins := make([]string, 0, len(reviews))
	for login := range reviews {
		logins = append(logins, login)
	}
	sort.Strings(logins)
	for _, login := range logins {
		switch reviews[login] {
		case "APPROVED":
			rev = append(rev, "✓ "+login)
		case "CHANGES_REQUESTED":
			rev = append(rev, "✗ "+login)
		}
	}
	if len(rev) > 0 {
		c.Sections = append(c.Sections, Section{Title: "REVIEW", Lines: rev})
	}
	c.Attn = c.State == "OPEN" && (fail > 0 || pr.ReviewDecision == "REVIEW_REQUIRED" || pr.ReviewDecision == "CHANGES_REQUESTED")
	return c, nil
}

// latestReviews keeps each reviewer's last approving or blocking review.
func latestReviews(pr ghPR) map[string]string {
	out := map[string]string{}
	for _, r := range pr.Reviews {
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" {
			out[r.Author.Login] = r.State
		} else if r.State == "DISMISSED" {
			delete(out, r.Author.Login)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Stale reports whether a card fetched at fetchedAt should be refreshed.
func Stale(fetchedAt time.Time, refreshSecs int, now time.Time) bool {
	if fetchedAt.IsZero() {
		return true
	}
	if refreshSecs <= 0 {
		refreshSecs = 60
	}
	return now.Sub(fetchedAt) >= time.Duration(refreshSecs)*time.Second
}

// Ago formats an age like "12m ago" ("just now" under a minute).
func Ago(then, now time.Time) string {
	if then.IsZero() {
		return ""
	}
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Marshal / Unmarshal cache a card in inspector_state.cache_json.
func Marshal(c Card) (string, error) {
	b, err := json.Marshal(c)
	return string(b), err
}

func Unmarshal(s string) (Card, error) {
	var c Card
	err := json.Unmarshal([]byte(s), &c)
	return c, err
}

// DefaultTitle is a tab title when --title is omitted.
func DefaultTitle(kind, target string) string {
	if kind == KindDecision {
		return "⚑ " + target
	}
	if kind == KindCmd {
		return TrimTitle(target, 24)
	}
	if _, _, n, ok := ParseGitHubPR(target); ok {
		return fmt.Sprintf("PR #%d", n)
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return TrimTitle(target, 24)
	}
	if linearRe.MatchString(target) {
		for _, seg := range strings.Split(u.Path, "/") {
			if linearIssueRe.MatchString(seg) {
				return strings.ToUpper(seg)
			}
		}
	}
	last := ""
	for _, s := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if s != "" {
			last = s
		}
	}
	if last == "" {
		return u.Host
	}
	return TrimTitle(last, 24)
}

// TrimTitle shortens s to at most n runes with an ellipsis.
func TrimTitle(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
