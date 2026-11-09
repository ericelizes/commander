package inspector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		kind, target, want string
	}{
		{"url", "https://github.com/example-app/example-app/pull/4109", RendererGitHubPR},
		{"url", "https://github.com/example-app/example-app/issues/12", RendererWeb},
		{"url", "https://team.notion.site/Spec-abc123", RendererNotion},
		{"url", "https://docs.example.com/guide", RendererWeb},
		{"url", "https://app.datadoghq.com/dashboard/abc", RendererLink},
		{"url", "http://localhost:5173/", RendererLink},
		{"url", "file:///tmp/x.html", RendererLink},
		{"cmd", "gh pr checks 4109 --watch", RendererCmd},
	}
	for _, c := range cases {
		if got := Detect(c.kind, c.target); got != c.want {
			t.Errorf("Detect(%q, %q) = %q, want %q", c.kind, c.target, got, c.want)
		}
	}
}

const prJSON = `{
  "title": "feat: ship worker unit",
  "state": "OPEN",
  "author": {"login": "user"},
  "headRefName": "feat/u3-worker",
  "baseRefName": "main",
  "additions": 412, "deletions": 38, "changedFiles": 9,
  "reviewDecision": "REVIEW_REQUIRED",
  "updatedAt": "2026-10-20T10:00:00Z",
  "body": "## Summary\nAdds the worker.",
  "statusCheckRollup": [
    {"name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
    {"name": "lint", "status": "COMPLETED", "conclusion": "FAILURE"},
    {"name": "e2e", "status": "IN_PROGRESS", "conclusion": ""},
    {"context": "ci/legacy", "state": "SUCCESS"}
  ],
  "reviews": [
    {"author": {"login": "ana"}, "state": "APPROVED"},
    {"author": {"login": "bo"}, "state": "COMMENTED"}
  ]
}`

func TestFetch_GitHubPR(t *testing.T) {
	var gotName string
	var gotArgs []string
	f := Fetcher{
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName, gotArgs = name, args
			return []byte(prJSON), nil
		},
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	url := "https://github.com/example-app/example-app/pull/4109"
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: url})
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "gh" || strings.Join(gotArgs[:5], " ") != "pr view 4109 -R example-app/example-app" || gotArgs[5] != "--json" {
		t.Errorf("exec = %s %v", gotName, gotArgs)
	}
	if !strings.Contains(gotArgs[6], "statusCheckRollup") {
		t.Errorf("json fields = %q", gotArgs[6])
	}
	if card.Renderer != RendererGitHubPR || card.State != "OPEN" || card.URL != url {
		t.Errorf("card header = %+v", card)
	}
	if card.Title != "PR #4109  feat: ship worker unit" {
		t.Errorf("title = %q", card.Title)
	}
	if !strings.Contains(strings.Join(card.Meta, "|"), "feat/u3-worker → main") ||
		!strings.Contains(strings.Join(card.Meta, "|"), "+412 −38") ||
		!strings.Contains(strings.Join(card.Meta, "|"), "9 files") {
		t.Errorf("meta = %v", card.Meta)
	}
	if len(card.Sections) != 2 || card.Sections[0].Title != "CHECKS" || card.Sections[0].Lines[0] != "✓ 2  ✗ 1  ○ 1" {
		t.Errorf("sections = %+v", card.Sections)
	}
	if card.Sections[0].Lines[1] != "✗ lint" {
		t.Errorf("failing check not listed: %v", card.Sections[0].Lines)
	}
	if got := strings.Join(card.Sections[1].Lines, "|"); got != "⚑ awaiting review|✓ ana" {
		t.Errorf("review lines = %q", got)
	}
	if !card.Attn {
		t.Error("open PR with failing check / awaiting review should need attention")
	}
	if card.UpdatedAt.IsZero() || card.FetchedAt.Unix() != 1_800_000_000 {
		t.Errorf("times: updated=%v fetched=%v", card.UpdatedAt, card.FetchedAt)
	}
	if !strings.Contains(card.Markdown, "Adds the worker.") {
		t.Errorf("body = %q", card.Markdown)
	}
}

func TestFetch_GitHubPRMergedNeedsNothing(t *testing.T) {
	f := Fetcher{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"title":"x","state":"MERGED","reviewDecision":"APPROVED","statusCheckRollup":[]}`), nil
	}}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://github.com/o/r/pull/7"})
	if err != nil || card.State != "MERGED" || card.Attn || len(card.Sections) != 1 {
		t.Errorf("merged card = %+v err=%v", card, err)
	}
}

func TestFetch_GitHubFailureFallsBackToLinkCard(t *testing.T) {
	f := Fetcher{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("gh: not logged in"), errors.New("exit 1")
	}}
	card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: "https://github.com/o/r/pull/7"})
	if err == nil || card.Err == "" || !strings.Contains(card.Err, "not logged in") {
		t.Fatalf("want an error card, got %+v err=%v", card, err)
	}
	if card.URL != "https://github.com/o/r/pull/7" || card.Title == "" {
		t.Errorf("fallback card = %+v", card)
	}
}

func TestFetch_CmdKeepsANSI(t *testing.T) {
	var gotArgs []string
	f := Fetcher{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("\x1b[32mok\x1b[0m\n"), nil
	}}
	card, err := f.Fetch(context.Background(), Target{Kind: KindCmd, Target: "curl -s localhost:5173/health", Title: "health"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs, " ") != "sh -c curl -s localhost:5173/health" {
		t.Errorf("exec = %v", gotArgs)
	}
	if card.Markdown != "\x1b[32mok\x1b[0m" || !card.Preformatted || card.Title != "health" || card.Renderer != RendererCmd {
		t.Errorf("card = %+v", card)
	}
}

func TestFetch_CmdFailureMarked(t *testing.T) {
	f := Fetcher{Exec: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit status 2")
	}}
	card, _ := f.Fetch(context.Background(), Target{Kind: KindCmd, Target: "false"})
	if card.State != "FAILED" || !card.Attn || card.Markdown != "boom" {
		t.Errorf("card = %+v", card)
	}
}

func TestFetch_LinkAndTodoRenderers(t *testing.T) {
	f := Fetcher{Exec: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("link cards must not exec")
		return nil, nil
	}}
	for _, target := range []string{
		"https://app.datadoghq.com/dashboard/abc",
		"https://docs.example.com/a/b/c",
	} {
		card, err := f.Fetch(context.Background(), Target{Kind: KindURL, Target: target})
		if err != nil || card.URL != target || card.Title == "" || card.Markdown == "" {
			t.Errorf("%s -> %+v err=%v", target, card, err)
		}
	}
}

func TestTitleFromURLAndDefaultTitle(t *testing.T) {
	if got := TitleFromURL("https://github.com/o/r/issues/4"); got != "github.com / issues / 4" {
		t.Errorf("TitleFromURL = %q", got)
	}
	if got := TitleFromURL("https://example.com/"); got != "example.com" {
		t.Errorf("TitleFromURL root = %q", got)
	}
	cases := map[string]string{
		"https://github.com/o/r/pull/4109":                        "PR #4109",
		"https://linear.app/example-app/issue/team-102/some-slug": "TEAM-102",
		"https://example.com/":                                    "example.com",
		"http://localhost:5173/":                                  "localhost:5173",
	}
	for in, want := range cases {
		if got := DefaultTitle(KindURL, in); got != want {
			t.Errorf("DefaultTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := DefaultTitle(KindCmd, "gh pr checks 4109 --watch --interval 5"); got != "gh pr checks 4109 --wat…" {
		t.Errorf("DefaultTitle cmd = %q", got)
	}
}

func TestStaleAndAgo(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if !Stale(time.Time{}, 60, now) {
		t.Error("never fetched is stale")
	}
	if Stale(now.Add(-30*time.Second), 60, now) {
		t.Error("30s old with 60s refresh is fresh")
	}
	if !Stale(now.Add(-61*time.Second), 60, now) {
		t.Error("61s old with 60s refresh is stale")
	}
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", 12 * time.Minute: "12m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago",
	} {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("Ago(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCardCacheRoundTrip(t *testing.T) {
	in := Card{Title: "t", State: "OPEN", Meta: []string{"a"}, Sections: []Section{{Title: "S", Lines: []string{"x"}}}, FetchedAt: time.Unix(5, 0).UTC()}
	js, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Unmarshal(js)
	if err != nil || out.Title != "t" || out.Sections[0].Lines[0] != "x" || !out.FetchedAt.Equal(in.FetchedAt) {
		t.Errorf("round trip = %+v err=%v", out, err)
	}
}
