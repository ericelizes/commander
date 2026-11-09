package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

type swapClient struct {
	beads.MockClient
	current beads.Bead
	shows   int
}

func (c *swapClient) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	return []beads.Bead{c.current}, nil
}
func (c *swapClient) Details(context.Context, string, []string) (map[string]beads.Detail, error) {
	c.shows++
	return map[string]beads.Detail{c.current.ID: {Deps: c.current.Dependencies}}, nil
}

// A warmed DB-backed display read must learn a replacement blocker even when
// bd leaves all timestamps and counts alone. No export may mask the DB read.
func TestBlockerSwapScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/blocker-swap.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct{ Before, After beads.Bead }
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	client := &swapClient{current: sc.Before}
	cached := beads.NewCached(client)
	ctx := context.Background()
	read := func(want string) {
		t.Helper()
		got, err := cached.Details(ctx, dir, []string{sc.Before.ID})
		if err != nil {
			t.Fatal(err)
		}
		if ds := got[sc.Before.ID].Deps; len(ds) != 1 || ds[0].DependsOnID != want {
			t.Fatalf("blocker=%+v want=%s", ds, want)
		}
	}
	read(sc.Before.Dependencies[0].DependsOnID)
	client.current = sc.After
	if err := os.WriteFile(filepath.Join(dir, ".beads", "last-touched"), []byte("edge swapped"), 0600); err != nil {
		t.Fatal(err)
	}
	read(sc.After.Dependencies[0].DependsOnID)
	if client.shows != 2 {
		t.Fatalf("shows=%d", client.shows)
	}
	read(sc.After.Dependencies[0].DependsOnID)
	if client.shows != 2 {
		t.Fatal("unchanged detail unnecessarily read")
	}
}
