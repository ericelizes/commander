package inspector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Notion and Linear pages render two ways, in order:
//
//  1. the REST/GraphQL API when NOTION_TOKEN / LINEAR_API_KEY is set;
//  2. the user's Claude Code (`claude -p`), which already has the Notion and
//     Linear connectors, asked for a fixed markdown shape that ParseDigest turns
//     into a Card. The child runs with ANTHROPIC_API_KEY unset so the user's
//     plan login is used, never an API key.
//
// If neither works the tab degrades to the link card with a one-line hint.

const (
	// slowFetchTimeout covers the claude -p path, which takes seconds to a minute.
	slowFetchTimeout = 90 * time.Second

	notionHint = "set NOTION_TOKEN or connect Notion in Claude"
	linearHint = "set LINEAR_API_KEY or connect Linear in Claude"
)

// HTTPDo is the test seam for REST/GraphQL calls (nil = http.DefaultClient.Do).
type HTTPDo func(req *http.Request) (*http.Response, error)

func (f Fetcher) getenv(key string) string {
	if f.Getenv != nil {
		return f.Getenv(key)
	}
	return os.Getenv(key)
}

func (f Fetcher) httpDo(req *http.Request) ([]byte, error) {
	do := f.HTTP
	if do == nil {
		do = http.DefaultClient.Do
	}
	resp, err := do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", req.URL.Host, resp.StatusCode, TrimTitle(string(body), 120))
	}
	return body, nil
}

// claudeDigest runs `claude -p` with the given connector tools and parses the
// digest shape it prints.
func (f Fetcher) claudeDigest(ctx context.Context, run Exec, t Target, tools, prompt string) (Card, error) {
	// --allowedTools is variadic and would swallow the prompt as a tool name, so
	// the prompt goes first and the flag is last.
	out, err := run(ctx, "env", "-u", "ANTHROPIC_API_KEY", "-u", "CLAUDE_CONFIG_DIR", "claude", "-p", prompt, "--output-format", "text",
		"--allowedTools", tools)
	if err != nil {
		return Card{}, fmt.Errorf("claude -p: %v: %s", err, TrimTitle(string(out), 160))
	}
	return ParseDigest(string(out), t)
}

// ParseDigest turns the markdown shape requested from Claude into a card:
//
//	# <title>
//	Status: <value or none>
//	[Assignee: <name or none>]  [Priority: <label or none>]
//	## Outline          (bullets; optional)
//	## Body             (markdown, may contain its own ## headings)
//	## Open comments    (author: text per line, or none)
func ParseDigest(text string, t Target) (Card, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	// Drop a wrapping code fence if the model added one.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		lines = lines[1:]
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			lines = lines[:len(lines)-1]
		}
	}
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "# ") {
			start = i
			break
		}
	}
	if start < 0 {
		return Card{}, fmt.Errorf("unexpected claude output: %s", TrimTitle(text, 100))
	}
	lines = lines[start:]

	// Outline and Body are the first of their kind; comments is the LAST
	// "## Open comments" so a body with that heading cannot eat it.
	outlineAt, bodyAt, commentsAt := -1, -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case "## Outline":
			if outlineAt < 0 && bodyAt < 0 {
				outlineAt = i
			}
		case "## Body":
			if bodyAt < 0 {
				bodyAt = i
			}
		case "## Open comments":
			commentsAt = i
		}
	}
	if commentsAt >= 0 && commentsAt < bodyAt {
		commentsAt = -1
	}
	end := func(at int) int { // exclusive end of the section starting at at
		e := len(lines)
		for _, o := range []int{outlineAt, bodyAt, commentsAt} {
			if o > at && o < e {
				e = o
			}
		}
		return e
	}

	c := Card{Title: strings.TrimSpace(strings.TrimPrefix(lines[0], "# ")), URL: t.Target}
	for _, l := range lines[1:end(0)] {
		key, val, ok := strings.Cut(strings.TrimSpace(l), ":")
		val = strings.TrimSpace(val)
		if !ok || isNone(val) {
			continue
		}
		switch strings.ToLower(key) {
		case "status":
			c.State = strings.ToUpper(val)
		case "assignee":
			c.Meta = append(c.Meta, "@"+val)
		case "priority":
			c.Meta = append(c.Meta, val)
		}
	}
	if outlineAt >= 0 {
		if outline := bulletLines(lines[outlineAt+1 : end(outlineAt)]); len(outline) > 0 {
			c.Sections = append(c.Sections, Section{Title: "OUTLINE", Lines: outline})
		}
	}
	if bodyAt >= 0 {
		c.Markdown = strings.TrimSpace(strings.Join(lines[bodyAt+1:end(bodyAt)], "\n"))
	}
	if commentsAt >= 0 {
		if comments := bulletLines(lines[commentsAt+1:]); len(comments) > 0 {
			c.Sections = append(c.Sections, Section{Title: "COMMENTS", Lines: comments})
		}
	}
	return c, nil
}

// bulletLines strips list markers and drops blanks and "none".
func bulletLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*• "))
		if s != "" && !isNone(s) {
			out = append(out, s)
		}
	}
	return out
}

func isNone(s string) bool {
	return strings.EqualFold(strings.Trim(s, " .*_`"), "none")
}

// degrade is the link card for a failed Notion/Linear fetch: the one-line hint
// goes in Meta (the caller sets Err from the returned error).
func degrade(t Target, hint string) Card {
	c := LinkCard(t)
	c.Meta = []string{hint}
	return c
}

// DefaultRefreshSecs is how often a tab's card is refetched when `cmdr tab add`
// is not given --refresh: notion/linear fetches are slow (and rate limited) and
// web pages are screenshotted, so they get 300s; commands 10s; everything else 60s.
func DefaultRefreshSecs(kind, target, renderer string) int {
	if kind == KindCmd {
		return 10
	}
	if renderer == "" {
		renderer = Detect(kind, target)
	}
	switch renderer {
	case RendererNotion, RendererLinear, RendererWeb, RendererBrowser:
		return 300 // web/browser: a Chrome launch per refresh; `r` forces one
	}
	return 60
}

// FetchingLabel is the placeholder shown while a card loads for the first time.
func FetchingLabel(renderer string, getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	var name, tokenVar string
	switch renderer {
	case RendererNotion:
		name, tokenVar = "Notion", "NOTION_TOKEN"
	case RendererLinear:
		name, tokenVar = "Linear", "LINEAR_API_KEY"
	default:
		return "Loading…"
	}
	if getenv(tokenVar) != "" {
		return "fetching… (" + name + " API)"
	}
	return "fetching… (" + name + " via Claude)"
}
