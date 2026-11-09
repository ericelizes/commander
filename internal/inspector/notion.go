package inspector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	notionAPI     = "https://api.notion.com/v1"
	notionVersion = "2022-06-28"
	notionTools   = "mcp__notion__notion-fetch,mcp__notion__notion-get-comments"
)

var notionIDRe = regexp.MustCompile(`([0-9a-fA-F]{8})-?([0-9a-fA-F]{4})-?([0-9a-fA-F]{4})-?([0-9a-fA-F]{4})-?([0-9a-fA-F]{12})`)

// ParseNotionID pulls the page id out of a Notion URL: 32 hex chars, with or
// without dashes, after an optional title slug and before ?v= / #. The id is
// returned dashed (8-4-4-4-12).
func ParseNotionID(target string) (string, bool) {
	u, err := url.Parse(target)
	if err != nil {
		return "", false
	}
	all := notionIDRe.FindAllStringSubmatch(u.Path, -1)
	if len(all) == 0 {
		return "", false
	}
	m := all[len(all)-1]
	return strings.ToLower(strings.Join(m[1:], "-")), true
}

// notion fetches a Notion page: the API when NOTION_TOKEN is set, else (or if
// the API fails) the user's Claude connector.
func (f Fetcher) notion(ctx context.Context, run Exec, t Target) (Card, error) {
	var apiErr error
	if tok := f.getenv("NOTION_TOKEN"); tok != "" {
		card, err := f.notionAPI(ctx, t, tok)
		if err == nil {
			return card, nil
		}
		apiErr = err
	}
	card, err := f.claudeDigest(ctx, run, t, notionTools, notionPrompt(t.Target))
	if err == nil {
		return card, nil
	}
	if apiErr != nil {
		err = fmt.Errorf("notion api: %v; %v", apiErr, err)
	}
	return degrade(t, notionHint), err
}

func notionPrompt(target string) string {
	return "Use the Notion tools to fetch the page at " + target + " and its comments. " +
		"Reply with ONLY a markdown rendering of exactly this shape, no preamble, no code fence:\n" +
		"first line `# <page title>`\n" +
		"then `Status: <the page's Status property, or none>`\n" +
		"then `## Outline` followed by a bullet list of the page's headings\n" +
		"then `## Body` followed by the page content as markdown\n" +
		"then `## Open comments` followed by one `author: text` line per unresolved comment, or `none`."
}

func (f Fetcher) notionGet(ctx context.Context, token, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, notionAPI+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", notionVersion)
	return f.httpDo(req)
}

type notionRichText struct {
	PlainText   string `json:"plain_text"`
	Href        string `json:"href"`
	Annotations struct {
		Bold   bool `json:"bold"`
		Code   bool `json:"code"`
		Italic bool `json:"italic"`
	} `json:"annotations"`
}

type notionNamed struct {
	Name string `json:"name"`
}

type notionPage struct {
	LastEdited string `json:"last_edited_time"`
	Properties map[string]struct {
		Type   string           `json:"type"`
		Title  []notionRichText `json:"title"`
		Status *notionNamed     `json:"status"`
		Select *notionNamed     `json:"select"`
	} `json:"properties"`
}

type notionList struct {
	Results    []json.RawMessage `json:"results"`
	HasMore    bool              `json:"has_more"`
	NextCursor string            `json:"next_cursor"`
}

// notionListAll follows next_cursor pagination for a list endpoint.
func (f Fetcher) notionListAll(ctx context.Context, token, path string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	cursor := ""
	for page := 0; page < 50; page++ {
		p := path
		if cursor != "" {
			sep := "?"
			if strings.Contains(p, "?") {
				sep = "&"
			}
			p += sep + "start_cursor=" + url.QueryEscape(cursor)
		}
		body, err := f.notionGet(ctx, token, p)
		if err != nil {
			return nil, err
		}
		var l notionList
		if err := json.Unmarshal(body, &l); err != nil {
			return nil, fmt.Errorf("parse notion list: %w", err)
		}
		all = append(all, l.Results...)
		if !l.HasMore || l.NextCursor == "" {
			break
		}
		cursor = l.NextCursor
	}
	return all, nil
}

func (f Fetcher) notionAPI(ctx context.Context, t Target, token string) (Card, error) {
	id, ok := ParseNotionID(t.Target)
	if !ok {
		return Card{}, fmt.Errorf("no Notion page id in %s", t.Target)
	}
	body, err := f.notionGet(ctx, token, "/pages/"+id)
	if err != nil {
		return Card{}, err
	}
	var page notionPage
	if err := json.Unmarshal(body, &page); err != nil {
		return Card{}, fmt.Errorf("parse notion page: %w", err)
	}
	c := Card{URL: t.Target}
	for name, p := range page.Properties {
		switch {
		case p.Type == "title":
			c.Title = richText(p.Title)
		case p.Type == "status" && p.Status != nil:
			c.State = strings.ToUpper(p.Status.Name)
		case p.Type == "select" && p.Select != nil && strings.EqualFold(name, "status") && c.State == "":
			c.State = strings.ToUpper(p.Select.Name)
		}
	}
	if c.Title == "" {
		c.Title = t.Title
	}
	if ts, err := time.Parse(time.RFC3339, page.LastEdited); err == nil {
		c.UpdatedAt = ts
	}

	var md, outline []string
	if err := f.notionBlocks(ctx, token, id, 0, &md, &outline); err != nil {
		return Card{}, err
	}
	c.Markdown = strings.TrimSpace(strings.Join(md, "\n"))
	if len(outline) > 0 {
		c.Sections = append(c.Sections, Section{Title: "OUTLINE", Lines: outline})
	}

	// Comments are best-effort: an integration without comment access gets a 403.
	if raws, err := f.notionListAll(ctx, token, "/comments?block_id="+id); err == nil {
		var lines []string
		for _, r := range raws {
			var cm struct {
				RichText    []notionRichText `json:"rich_text"`
				DisplayName struct {
					ResolvedName string `json:"resolved_name"`
				} `json:"display_name"`
			}
			if json.Unmarshal(r, &cm) != nil {
				continue
			}
			author := cm.DisplayName.ResolvedName
			if author == "" {
				author = "someone"
			}
			lines = append(lines, author+": "+richText(cm.RichText))
		}
		if len(lines) > 0 {
			c.Sections = append(c.Sections, Section{Title: "COMMENTS", Lines: lines})
		}
	}
	return c, nil
}

// notionBlocks appends the markdown for the children of blockID. depth counts
// nesting under top-level blocks: toggles and lists are expanded one level;
// column_list/column are transparent layout and do not count.
func (f Fetcher) notionBlocks(ctx context.Context, token, blockID string, depth int, md, outline *[]string) error {
	raws, err := f.notionListAll(ctx, token, "/blocks/"+blockID+"/children?page_size=100")
	if err != nil {
		return err
	}
	indent := strings.Repeat("  ", depth)
	for _, raw := range raws {
		var b struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			HasChildren bool   `json:"has_children"`
		}
		var all map[string]json.RawMessage
		if json.Unmarshal(raw, &b) != nil || json.Unmarshal(raw, &all) != nil {
			continue
		}
		// The payload lives under a key named after the block type.
		var payload struct {
			RichText []notionRichText `json:"rich_text"`
			Checked  bool             `json:"checked"`
			Language string           `json:"language"`
			Title    string           `json:"title"`
			URL      string           `json:"url"`
		}
		_ = json.Unmarshal(all[b.Type], &payload)
		text := richText(payload.RichText)
		layout := b.Type == "column_list" || b.Type == "column"

		var line string
		blank := false // blank line after the block
		switch b.Type {
		case "heading_1", "heading_2", "heading_3":
			level := int(b.Type[len(b.Type)-1] - '0')
			line = strings.Repeat("#", level) + " " + text
			*outline = append(*outline, strings.Repeat("  ", level-1)+text)
			blank = true
		case "paragraph":
			line, blank = indent+text, true
		case "bulleted_list_item", "toggle":
			line = indent + "- " + text
		case "numbered_list_item":
			line = indent + "1. " + text
		case "to_do":
			box := "[ ]"
			if payload.Checked {
				box = "[x]"
			}
			line = indent + "- " + box + " " + text
		case "quote", "callout":
			line = indent + "> " + text
		case "code":
			line, blank = "```"+payload.Language+"\n"+text+"\n```", true
		case "divider":
			line = "---"
		case "child_page", "child_database":
			line = indent + "- " + payload.Title
		case "bookmark", "embed", "link_preview":
			line = indent + payload.URL
		default:
			if text != "" {
				line = indent + text
			}
		}
		if !layout && line != "" {
			*md = append(*md, line)
			if blank {
				*md = append(*md, "")
			}
		}
		if !b.HasChildren {
			continue
		}
		switch {
		case layout:
			if err := f.notionBlocks(ctx, token, b.ID, depth, md, outline); err != nil {
				return err
			}
		case depth == 0:
			if err := f.notionBlocks(ctx, token, b.ID, depth+1, md, outline); err != nil {
				return err
			}
		}
	}
	return nil
}

// richText flattens rich text to inline markdown (bold, italic, code, links).
func richText(parts []notionRichText) string {
	var sb strings.Builder
	for _, p := range parts {
		s := p.PlainText
		if s == "" {
			continue
		}
		if p.Annotations.Code {
			s = "`" + s + "`"
		}
		if p.Annotations.Bold {
			s = "**" + s + "**"
		}
		if p.Annotations.Italic {
			s = "*" + s + "*"
		}
		if p.Href != "" {
			s = "[" + s + "](" + p.Href + ")"
		}
		sb.WriteString(s)
	}
	return sb.String()
}
