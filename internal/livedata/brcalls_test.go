package livedata

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
)

// brCounter counts the reads that reach br, per kind.
type brCounter struct {
	*beads.MemClient
	mu    sync.Mutex
	calls map[string]int
}

func (c *brCounter) count(kind string) {
	c.mu.Lock()
	c.calls[kind]++
	c.mu.Unlock()
}

func (c *brCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

func (c *brCounter) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	c.count("list")
	return c.MemClient.List(ctx, dir, o)
}

func (c *brCounter) Deps(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	c.count("deps")
	return c.MemClient.Deps(ctx, dir, id)
}

func (c *brCounter) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c.count("details")
	return c.MemClient.Details(ctx, dir, ids)
}

func (c *brCounter) Comments(ctx context.Context, dir, id string) ([]beads.Comment, error) {
	c.count("comments")
	return c.MemClient.Comments(ctx, dir, id)
}

func (c *brCounter) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	c.count("show")
	return c.MemClient.Show(ctx, dir, id)
}

// The snapshot reads a project in a fixed handful of br calls however many
// steps and decided gates it has (it was one `br dep list` per step and one
// `br comments list` per gate), and none at all while nothing changed.
func TestSnapshotBrCallsAreBatchedAndCached(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "beads.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "p"}
	ws, _ := mem.Create(ctx, dir, "Workstream", beads.CreateOptions{Labels: []string{"kind:objective"}})
	var steps []string
	for _, title := range []string{"Build", "Review", "Ship"} {
		id, _ := mem.Create(ctx, dir, title, beads.CreateOptions{Parent: ws, Labels: []string{"kind:objective"}})
		steps = append(steps, id)
	}
	_ = mem.DependOn(ctx, dir, steps[2], steps[1])
	for _, name := range []string{"spec", "tests"} {
		gid, err := gates.Upsert(ctx, mem, dir, store.Gate{ProjectID: 1, BeadID: steps[0], Name: name, Check: "true", Verifier: store.VerifierAuto})
		if err != nil {
			t.Fatal(err)
		}
		ns, _ := gates.ForStage(ctx, mem, dir, 1, steps[0])
		for _, n := range ns {
			if n.ID == gid {
				if err := gates.SetResult(ctx, mem, dir, n, store.GatePassed, "exit 0", "auto", time.Unix(1, 0)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Built after the writes above: the files only move when br writes.
	counter := &brCounter{MemClient: mem, calls: map[string]int{}}
	bc := beads.NewCached(counter)
	projects := []store.Project{{ID: 1, Name: "p", Path: dir}}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	read := func() []ObjectiveRow {
		rows, err := objectiveRowsFromBeads(ctx, bc, projects)
		if err != nil {
			t.Fatal(err)
		}
		attachGates(ctx, st, bc, projects, rows)
		return rows
	}
	rows := read()
	// One list of every status answers the objectives, approval cards, tasks and
	// stage gates filters and the change keys (it was 5 calls, il9.202.25.1);
	// details: edges, gate evidence.
	want := map[string]int{"list": 1, "details": 2}
	if !maps.Equal(counter.calls, want) {
		t.Fatalf("cold read br calls = %v, want %v", counter.calls, want)
	}
	byID := map[string]ObjectiveRow{}
	for _, r := range rows {
		byID[r.BeadID] = r
	}
	if !slices.Equal(byID[steps[2]].BlockedBy, []string{steps[1]}) {
		t.Fatalf("Ship BlockedBy = %v, want [%s]", byID[steps[2]].BlockedBy, steps[1])
	}
	if g := byID[steps[0]].Gates; len(g) != 2 || g[0].Evidence != "exit 0" || g[0].Status != store.GatePassed {
		t.Fatalf("Build gates = %+v, want two passed with evidence", g)
	}

	before := counter.total()
	for i := 0; i < 3; i++ {
		read()
	}
	if n := counter.total() - before; n != 0 {
		t.Fatalf("unchanged project: %d br calls over 3 reads, want 0", n)
	}
}
