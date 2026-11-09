package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// noBr fails the test on any read that would reach br.
type noBr struct {
	beads.Client
	t *testing.T
}

func (n noBr) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	n.t.Fatal("ensure managers read br; it should number workstreams from the snapshot")
	return nil, nil
}

// ensureManagers numbers workstreams from the snapshot cmdrd already holds:
// it used to re-read every project from br (uncached, on a second state.db
// pool) every 30s, which under load ran past its 1-minute deadline.
func TestLiveWorkstreamsReadsTheSnapshotNotBr(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	if _, err := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "p"}
	ws, _ := mem.Create(ctx, dir, "Workstream", beads.CreateOptions{Labels: []string{"kind:objective"}})

	live := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: mem}}
	numbered := liveWorkstreams(live)
	if _, err := numbered(ctx); err == nil {
		t.Fatal("before the first snapshot: want an error, not an empty list (that would look like no workstreams)")
	}
	live.Snapshot(ctx)
	live.Deps.Beads = noBr{t: t}
	got, err := numbered(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != ws || got[0].Project != "p" {
		t.Fatalf("numbered = %+v, want the one workstream %s", got, ws)
	}
}
