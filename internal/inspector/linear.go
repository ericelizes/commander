package inspector

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/example-user/commander/internal/linearapi"
	"net/url"
	"strings"
	"time"
)

const (
	linearAPI   = linearapi.Endpoint
	linearTools = "mcp__linear__get_issue,mcp__linear__list_comments"

	linearQuery = `query($id: String!) { issue(id: $id) { identifier title url updatedAt description priorityLabel
  state { name } assignee { name } comments { nodes { body resolvedAt user { name } } } } }`
)

// ParseLinearIssue pulls the identifier (TEAM-102) out of a Linear issue URL.
func ParseLinearIssue(target string) (string, bool) {
	u, err := url.Parse(target)
	if err != nil {
		return "", false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if linearIssueRe.MatchString(seg) {
			return strings.ToUpper(seg), true
		}
	}
	return "", false
}

// linear fetches a Linear issue: GraphQL when LINEAR_API_KEY is set, else (or
// if the API fails) the user's Claude connector.
func (f Fetcher) linear(ctx context.Context, run Exec, t Target) (Card, error) {
	id, ok := ParseLinearIssue(t.Target)
	if !ok {
		return degrade(t, "not a Linear issue URL"), fmt.Errorf("no issue identifier in %s", t.Target)
	}
	var apiErr error
	if key := f.getenv("LINEAR_API_KEY"); key != "" {
		card, err := f.linearAPI(ctx, t, id, key)
		if err == nil {
			return card, nil
		}
		apiErr = err
	}
	card, err := f.claudeDigest(ctx, run, t, linearTools, linearPrompt(id))
	if err == nil {
		return card, nil
	}
	if apiErr != nil {
		err = fmt.Errorf("linear api: %v; %v", apiErr, err)
	}
	return degrade(t, linearHint), err
}

func linearPrompt(id string) string {
	return "Use the Linear tools to fetch issue " + id + " and its comments. " +
		"Reply with ONLY a markdown rendering of exactly this shape, no preamble, no code fence:\n" +
		"first line `# " + id + " <issue title>`\n" +
		"then `Status: <workflow state name>`\n" +
		"then `Assignee: <name or none>`\n" +
		"then `Priority: <priority label or none>`\n" +
		"then `## Body` followed by the issue description as markdown\n" +
		"then `## Open comments` followed by one `author: text` line per unresolved comment, or `none`."
}

func (f Fetcher) linearAPI(ctx context.Context, t Target, id, key string) (Card, error) {
	payload, _ := json.Marshal(map[string]any{"query": linearQuery, "variables": map[string]string{"id": id}})
	body, status := linearapi.Query(ctx, linearapi.HTTPDo(f.HTTP), linearAPI, key, payload)
	if status/100 != 2 {
		return Card{}, fmt.Errorf("Linear request failed (HTTP %d)", status)
	}
	return ParseLinearJSON(body, t)
}

type linearNamed struct {
	Name string `json:"name"`
}

// ParseLinearJSON turns the GraphQL issue response into a card.
func ParseLinearJSON(data []byte, t Target) (Card, error) {
	var resp struct {
		Data struct {
			Issue *struct {
				Identifier    string       `json:"identifier"`
				Title         string       `json:"title"`
				UpdatedAt     string       `json:"updatedAt"`
				Description   string       `json:"description"`
				PriorityLabel string       `json:"priorityLabel"`
				State         linearNamed  `json:"state"`
				Assignee      *linearNamed `json:"assignee"`
				Comments      struct {
					Nodes []struct {
						Body       string       `json:"body"`
						ResolvedAt string       `json:"resolvedAt"`
						User       *linearNamed `json:"user"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"issue"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return Card{}, fmt.Errorf("parse linear response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return Card{}, fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	is := resp.Data.Issue
	if is == nil {
		return Card{}, fmt.Errorf("linear: issue not found")
	}
	c := Card{
		Title:    strings.TrimSpace(is.Identifier + "  " + is.Title),
		State:    strings.ToUpper(is.State.Name),
		URL:      t.Target,
		Markdown: strings.TrimSpace(is.Description),
	}
	if is.Assignee != nil && is.Assignee.Name != "" {
		c.Meta = append(c.Meta, "@"+is.Assignee.Name)
	}
	if is.PriorityLabel != "" && is.PriorityLabel != "No priority" {
		c.Meta = append(c.Meta, is.PriorityLabel)
	}
	if ts, err := time.Parse(time.RFC3339, is.UpdatedAt); err == nil {
		c.UpdatedAt = ts
	}
	var lines []string
	for _, cm := range is.Comments.Nodes {
		if cm.ResolvedAt != "" {
			continue
		}
		author := "someone"
		if cm.User != nil && cm.User.Name != "" {
			author = cm.User.Name
		}
		lines = append(lines, author+": "+strings.Join(strings.Fields(cm.Body), " "))
	}
	if len(lines) > 0 {
		c.Sections = append(c.Sections, Section{Title: "COMMENTS", Lines: lines})
	}
	return c, nil
}
