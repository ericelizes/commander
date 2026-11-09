package beads

import (
	"context"
	"testing"
)

func TestRename_Argv(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, nil, nil)}

	if err := c.Rename(context.Background(), "/tmp/p", "bd-1", "new title"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	assertArgv(t, got, []string{"br", "update", "bd-1", "--title", "new title", "cwd=/tmp/p"})
}

func TestRename_RequiresTitle(t *testing.T) {
	c := &cmdClient{run: captureExec(new([]string), nil, nil, nil)}
	if err := c.Rename(context.Background(), "/tmp/p", "bd-1", ""); err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestClose_Argv(t *testing.T) {
	var got []string
	c := &cmdClient{run: captureExec(&got, nil, nil, nil)}

	if err := c.Close(context.Background(), "/tmp/p", "bd-1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertArgv(t, got, []string{"br", "close", "bd-1", "cwd=/tmp/p"})
}

func TestParentOf(t *testing.T) {
	cases := map[string]string{
		"proj-ab1":     "",
		"proj-ab1.2":   "proj-ab1",
		"proj-ab1.2.3": "proj-ab1.2",
	}
	for id, want := range cases {
		if got := ParentOf(id); got != want {
			t.Errorf("ParentOf(%q) = %q, want %q", id, got, want)
		}
	}
}
