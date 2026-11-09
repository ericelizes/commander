package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/store"
)

func TestInspector_FailedRefreshKeepsLastGoodCard(t *testing.T) {
	c, st := newInspectorForTest(t, 100, 24)
	addTabs(t, st, "https://github.com/o/r/pull/4109")
	drive(t, c, c.loadCmd()())
	c.fetcher.Exec = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("rate limited"), errors.New("exit status 1")
	}
	drive(t, c, key("r"))
	view := ansi.Strip(c.View())
	if !strings.Contains(view, "ship worker") || !strings.Contains(view, "✗ gh pr view") {
		t.Errorf("want last good card plus error line:\n%s", view)
	}
	// The errored fetch must not overwrite the store cache.
	if js, _, _ := st.GetTabCache(context.Background(), 0, ""); !strings.Contains(js, "ship worker") {
		t.Errorf("cache lost the good card: %q", js)
	}
	// A later success clears the error.
	c.fetcher.Exec = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"title":"third","state":"OPEN"}`), nil
	}
	drive(t, c, key("r"))
	if view := ansi.Strip(c.View()); !strings.Contains(view, "third") || strings.Contains(view, "✗ gh pr view") {
		t.Errorf("success should clear the error:\n%s", view)
	}
}

func TestInspector_SlowSourcesShowFetchingPlaceholder(t *testing.T) {
	c, _ := newInspectorForTest(t, 100, 24)
	tab := store.InspectorTab{Kind: inspector.KindURL, Target: "https://www.notion.so/Spec-0123456789abcdef0123456789abcdef"}
	if got := c.cardFor(tab).Markdown; got != "fetching… (Notion via Claude)" && got != "fetching… (Notion API)" {
		t.Errorf("notion placeholder = %q", got)
	}
	tab.Target = "https://example.com/x"
	if got := c.cardFor(tab).Markdown; got != "Loading…" {
		t.Errorf("web placeholder = %q", got)
	}
}
