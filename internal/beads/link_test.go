package beads

import (
	"context"
	"testing"
)

func TestLink_PositionalArgs(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, nil, nil)}

	if err := c.Link(context.Background(), "/code/p", "bd-parent", "bd-child", "parent-child"); err != nil {
		t.Fatalf("Link: %v", err)
	}

	want := []string{"br", "link", "bd-parent", "bd-child", "--type", "parent-child", "cwd=/code/p"}
	assertArgv(t, got, want)

	// Regression for U1 finding: must NOT use --from / --to.
	for _, a := range got {
		if a == "--from" || a == "--to" {
			t.Fatalf("link must use positional args; saw %q in %v", a, got)
		}
	}
}

func TestLink_DefaultTypeIsBlocks(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, nil, nil)}

	if err := c.Link(context.Background(), "/code/p", "bd-1", "bd-2", ""); err != nil {
		t.Fatalf("Link: %v", err)
	}

	want := []string{"br", "link", "bd-1", "bd-2", "--type", "blocks", "cwd=/code/p"}
	assertArgv(t, got, want)
}

func TestLink_RejectsEmptyIDs(t *testing.T) {
	c := &cmdClient{run: func(_ context.Context, _, _ string, _ ...string) ([]byte, []byte, error) {
		t.Fatal("exec should not run with empty IDs")
		return nil, nil, nil
	}}

	if err := c.Link(context.Background(), "/code/p", "", "bd-2", "blocks"); err == nil {
		t.Error("expected error for empty fromID")
	}
	if err := c.Link(context.Background(), "/code/p", "bd-1", "", "blocks"); err == nil {
		t.Error("expected error for empty toID")
	}
}
