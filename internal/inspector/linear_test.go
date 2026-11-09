package inspector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

const linearURL = "https://linear.app/example-app/issue/team-102/fix-the-thing"

const linearResp = `{"data":{"issue":{"identifier":"TEAM-102","title":"Fix the thing","updatedAt":"2026-10-20T10:00:00Z",
 "description":"Steps to repro","priorityLabel":"High","state":{"name":"In Progress"},"assignee":{"name":"the user"},
 "comments":{"nodes":[
  {"body":"first\nline","resolvedAt":null,"user":{"name":"Ana"}},
  {"body":"old","resolvedAt":"2026-10-01T00:00:00Z","user":{"name":"Bo"}}]}}}}`

func TestParseLinearIssue(t *testing.T) {
	if id, ok := ParseLinearIssue(linearURL); !ok || id != "TEAM-102" {
		t.Errorf("got %q %v", id, ok)
	}
	if _, ok := ParseLinearIssue("https://linear.app/example-app/project/x"); ok {
		t.Error("expected no identifier")
	}
}

func TestFetch_LinearAPI(t *testing.T) {
	var gotBody, gotAuth, gotURL string
	f := Fetcher{
		Getenv: envOf(map[string]string{"LINEAR_API_KEY": "lin_key"}),
		HTTP: func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			gotBody, gotAuth, gotURL = string(b), req.Header.Get("Authorization"), req.URL.String()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(linearResp))}, nil
		},
	}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: linearURL})
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://api.linear.app/graphql" || gotAuth != "lin_key" || !strings.Contains(gotBody, `"id":"TEAM-102"`) {
		t.Errorf("request = %s %s %s", gotURL, gotAuth, gotBody)
	}
	if card.Renderer != RendererLinear || card.Title != "TEAM-102  Fix the thing" || card.State != "IN PROGRESS" || card.Markdown != "Steps to repro" {
		t.Errorf("card = %+v", card)
	}
	if strings.Join(card.Meta, "|") != "@the user|High" {
		t.Errorf("meta = %v", card.Meta)
	}
	if len(card.Sections) != 1 || len(card.Sections[0].Lines) != 1 || card.Sections[0].Lines[0] != "Ana: first line" {
		t.Errorf("sections = %+v (resolved comments must be dropped)", card.Sections)
	}
}

func TestFetch_LinearViaClaude(t *testing.T) {
	var args []string
	f := Fetcher{
		Getenv: envOf(nil),
		Exec: func(_ context.Context, _ string, a ...string) ([]byte, error) {
			args = a
			return []byte("# TEAM-102 Fix the thing\nStatus: Todo\nAssignee: none\nPriority: Urgent\n## Body\nrepro\n## Open comments\nnone\n"), nil
		},
	}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: linearURL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "mcp__linear__get_issue,mcp__linear__list_comments") {
		t.Errorf("args = %v", args)
	}
	if card.State != "TODO" || strings.Join(card.Meta, "|") != "Urgent" || card.Markdown != "repro" {
		t.Errorf("card = %+v", card)
	}
}

func TestFetch_LinearGraphQLErrorDegrades(t *testing.T) {
	f := Fetcher{
		Getenv: envOf(map[string]string{"LINEAR_API_KEY": "k"}),
		HTTP: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errors":[{"message":"bad key"}]}`))}, nil
		},
		Exec: func(context.Context, string, ...string) ([]byte, error) { return []byte("nope"), nil },
	}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: linearURL})
	if err == nil || !strings.Contains(card.Err, "bad key") || card.Meta[0] != "set LINEAR_API_KEY or connect Linear in Claude" {
		t.Errorf("card = %+v err = %v", card, err)
	}
}
