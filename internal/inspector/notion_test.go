package inspector

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const notionURL = "https://www.notion.so/example-app/Launch-Plan-0123456789abcdef0123456789abcdef?v=abc#frag"
const notionID = "01234567-89ab-cdef-0123-456789abcdef"

func TestParseNotionID(t *testing.T) {
	cases := map[string]string{
		notionURL:                           notionID,
		"https://www.notion.so/" + notionID: notionID,
		"https://team.notion.site/Spec-0123456789ABCDEF0123456789abcdef":                            notionID,
		"https://www.notion.so/0123456789abcdef0123456789abcdef?v=fedcba9876543210fedcba9876543210": notionID,
	}
	for in, want := range cases {
		if got, ok := ParseNotionID(in); !ok || got != want {
			t.Errorf("ParseNotionID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := ParseNotionID("https://www.notion.so/"); ok {
		t.Error("expected no id")
	}
}

// fakeHTTP serves canned JSON keyed by "path?query" and records requests.
func fakeHTTP(routes map[string]string, seen *[]*http.Request) HTTPDo {
	return func(req *http.Request) (*http.Response, error) {
		*seen = append(*seen, req)
		key := req.URL.Path
		if req.URL.RawQuery != "" {
			key += "?" + req.URL.RawQuery
		}
		body, ok := routes[key]
		code := 200
		if !ok {
			body, code = `{"message":"nope"}`, 404
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func rt(s string) string {
	return `[{"plain_text":"` + s + `","annotations":{}}]`
}

func TestFetch_NotionAPI(t *testing.T) {
	routes := map[string]string{
		"/v1/pages/" + notionID: `{"last_edited_time":"2026-10-20T10:00:00Z","properties":{
			"Name":{"type":"title","title":` + rt("Launch Plan") + `},
			"Status":{"type":"status","status":{"name":"In review"}}}}`,
		"/v1/blocks/" + notionID + "/children?page_size=100": `{"has_more":true,"next_cursor":"c2","results":[
			{"id":"b1","type":"heading_1","heading_1":{"rich_text":` + rt("Goals") + `}},
			{"id":"b2","type":"paragraph","paragraph":{"rich_text":[{"plain_text":"Ship it","annotations":{"bold":true}}]}}]}`,
		"/v1/blocks/" + notionID + "/children?page_size=100&start_cursor=c2": `{"has_more":false,"results":[
			{"id":"b3","type":"toggle","has_children":true,"toggle":{"rich_text":` + rt("Details") + `}},
			{"id":"b4","type":"column_list","has_children":true,"column_list":{}},
			{"id":"b5","type":"heading_2","heading_2":{"rich_text":` + rt("Risks") + `}}]}`,
		"/v1/blocks/b3/children?page_size=100": `{"results":[
			{"id":"b3a","type":"bulleted_list_item","has_children":true,"bulleted_list_item":{"rich_text":` + rt("inner") + `}}]}`,
		"/v1/blocks/b4/children?page_size=100": `{"results":[{"id":"col1","type":"column","has_children":true,"column":{}}]}`,
		"/v1/blocks/col1/children?page_size=100": `{"results":[
			{"id":"b6","type":"to_do","to_do":{"checked":true,"rich_text":` + rt("ship") + `}}]}`,
		"/v1/comments?block_id=" + notionID: `{"results":[{"display_name":{"resolved_name":"Ana"},"rich_text":` + rt("Looks good") + `}]}`,
	}
	var seen []*http.Request
	f := Fetcher{
		HTTP:   fakeHTTP(routes, &seen),
		Getenv: envOf(map[string]string{"NOTION_TOKEN": "secret"}),
		Exec: func(context.Context, string, ...string) ([]byte, error) {
			t.Error("claude must not run when the API works")
			return nil, errors.New("unexpected")
		},
	}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: notionURL})
	if err != nil {
		t.Fatal(err)
	}
	if card.Renderer != RendererNotion || card.Title != "Launch Plan" || card.State != "IN REVIEW" || card.UpdatedAt.IsZero() {
		t.Errorf("header = %+v", card)
	}
	for _, want := range []string{"# Goals", "**Ship it**", "- Details", "  - inner", "- [x] ship", "## Risks"} {
		if !strings.Contains(card.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, card.Markdown)
		}
	}
	if len(card.Sections) != 2 || card.Sections[0].Title != "OUTLINE" || card.Sections[0].Lines[0] != "Goals" || card.Sections[0].Lines[1] != "  Risks" {
		t.Errorf("sections = %+v", card.Sections)
	}
	if card.Sections[1].Title != "COMMENTS" || card.Sections[1].Lines[0] != "Ana: Looks good" {
		t.Errorf("comments = %+v", card.Sections[1])
	}
	if got := seen[0].Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("auth = %q", got)
	}
	if got := seen[0].Header.Get("Notion-Version"); got != "2022-06-28" {
		t.Errorf("version = %q", got)
	}
}

const digest = "Sure, here it is:\n\n# Launch Plan\nStatus: In review\n## Outline\n- Goals\n- Risks\n## Body\nIntro\n\n## Goals\nShip it\n## Open comments\nAna: Looks good\nBo: needs a date\n"

func TestFetch_NotionViaClaude(t *testing.T) {
	var name string
	var args []string
	f := Fetcher{
		Getenv: envOf(nil),
		Exec: func(_ context.Context, n string, a ...string) ([]byte, error) {
			name, args = n, a
			return []byte(digest), nil
		},
	}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: notionURL})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if name != "env" || !strings.HasPrefix(joined, "-u ANTHROPIC_API_KEY -u CLAUDE_CONFIG_DIR claude -p ") || !strings.HasSuffix(joined, " --output-format text --allowedTools mcp__notion__notion-fetch,mcp__notion__notion-get-comments") {
		t.Errorf("exec = %s %v", name, args)
	}
	if !strings.Contains(joined, notionURL) {
		t.Errorf("prompt lacks url: %s", joined)
	}
	if card.Title != "Launch Plan" || card.State != "IN REVIEW" {
		t.Errorf("header = %+v", card)
	}
	if card.Markdown != "Intro\n\n## Goals\nShip it" {
		t.Errorf("markdown = %q", card.Markdown)
	}
	if len(card.Sections) != 2 || len(card.Sections[0].Lines) != 2 || len(card.Sections[1].Lines) != 2 || card.Sections[1].Lines[1] != "Bo: needs a date" {
		t.Errorf("sections = %+v", card.Sections)
	}
}

func TestParseDigest_NoneAndFence(t *testing.T) {
	card, err := ParseDigest("```markdown\n# T\nStatus: none\n## Outline\nnone\n## Body\nhi\n## Open comments\nnone\n```", Target{Target: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if card.Title != "T" || card.State != "" || len(card.Sections) != 0 || card.Markdown != "hi" {
		t.Errorf("card = %+v", card)
	}
	if _, err := ParseDigest("I could not access that page.", Target{}); err == nil {
		t.Error("expected error for unshaped output")
	}
}

func TestFetch_NotionDegradesToLinkCard(t *testing.T) {
	var seen []*http.Request
	f := Fetcher{
		HTTP:   fakeHTTP(nil, &seen), // every call 404s
		Getenv: envOf(map[string]string{"NOTION_TOKEN": "secret"}),
		Exec: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Not logged in"), errors.New("exit status 1")
		},
	}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: notionURL})
	if err == nil {
		t.Fatal("expected error")
	}
	if card.Renderer != RendererNotion || len(card.Meta) != 1 || card.Meta[0] != "set NOTION_TOKEN or connect Notion in Claude" {
		t.Errorf("card = %+v", card)
	}
	if !strings.Contains(card.Err, "notion api") || !strings.Contains(card.Err, "claude -p") || !strings.Contains(card.Markdown, "Not previewed") {
		t.Errorf("err/markdown = %q / %q", card.Err, card.Markdown)
	}
}

func TestDetectRenderer_NotionHosts(t *testing.T) {
	for _, u := range []string{
		"https://app.notion.com/p/Some-Spec-0123456789abcdef0123456789abcdef?source=copy_link",
		"https://www.notion.so/Spec-0123456789abcdef0123456789abcdef",
		"https://team.notion.site/Spec-0123456789abcdef0123456789abcdef",
	} {
		if !notionRe.MatchString(u) {
			t.Errorf("notion renderer should match %s", u)
		}
		if _, ok := ParseNotionID(u); !ok {
			t.Errorf("page id should parse from %s", u)
		}
	}
}
