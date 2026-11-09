package livedata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// partialClient reads details for every bead but those it is told to leave
// unread, as a cold start whose big `br show` batches were killed would.
type partialClient struct {
	*beads.MemClient
	unread atomic.Bool
}

func (c *partialClient) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	got, err := c.MemClient.Details(ctx, dir, ids)
	if err != nil || !c.unread.Load() || len(ids) < 2 {
		return got, err
	}
	last := ids[len(ids)-1]
	delete(got, last)
	return got, &beads.PartialDetailsError{Missing: []string{last}, Err: errors.New("br show: signal: killed")}
}

type failingClient struct{ *beads.MemClient }

func (c failingClient) Details(context.Context, string, []string) (map[string]beads.Detail, error) {
	return nil, errors.New("br show: signal: killed")
}

func coldProject(t *testing.T, mem *beads.MemClient, id int64, name string) store.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws, _ := mem.Create(context.Background(), dir, name+" workstream", beads.CreateOptions{Labels: []string{"kind:objective"}})
	_, _ = mem.Create(context.Background(), dir, name+" step", beads.CreateOptions{Parent: ws, Labels: []string{"kind:objective"}})
	return store.Project{ID: id, Name: name, Path: dir}
}

// Cold start (no last-good rows): a partly unread project still shows rows and
// is flagged degraded; the next tick, once br answers, clears it.
func TestColdStartPartialDetailsShowRowsThenRecover(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{Prefix: "p"}
	pc := &partialClient{MemClient: mem}
	pc.unread.Store(true)
	projects := []store.Project{coldProject(t, mem, 1, "alpha")}
	cache := &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}

	rows, err := objectiveRowsFromBeadsCached(ctx, pc, projects, cache)
	if len(rows) != 2 {
		t.Fatalf("cold partial read: %d rows, want 2", len(rows))
	}
	if !OnlyProjectReadErrors(err) {
		t.Fatalf("err = %v, want a project read error (degraded)", err)
	}

	pc.unread.Store(false)
	rows, err = objectiveRowsFromBeadsCached(ctx, pc, projects, cache)
	if err != nil || len(rows) != 2 {
		t.Fatalf("recovered read: %d rows, err %v; want 2 rows and no error", len(rows), err)
	}
}

// One project that cannot be read does not take the others down.
func TestColdStartOneProjectFailingReturnsTheOthers(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{Prefix: "p"}
	good := coldProject(t, mem, 1, "alpha")
	bad := coldProject(t, mem, 2, "beta")
	bc := &perProjectClient{MemClient: mem, failDir: bad.Path}
	rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{good, bad}, nil)
	var alpha, beta int
	for _, r := range rows {
		switch r.ProjectName {
		case "alpha":
			alpha++
		case "beta":
			beta++
		}
	}
	if alpha == 0 || beta != 0 {
		t.Fatalf("alpha rows %d, beta rows %d; want the good project's rows only", alpha, beta)
	}
	if !OnlyProjectReadErrors(err) {
		t.Fatalf("err = %v, want only project read errors", err)
	}
	if OnlyProjectReadErrors(errors.Join(err, errors.New("store down"))) {
		t.Fatal("a store error is not a project read error")
	}
}

type perProjectClient struct {
	*beads.MemClient
	failDir string
}

func (c *perProjectClient) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if dir == c.failDir {
		return nil, errors.New("br show: signal: killed")
	}
	return c.MemClient.Details(ctx, dir, ids)
}
