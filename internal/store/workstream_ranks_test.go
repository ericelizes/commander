package store

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

func TestWorkstreamOrderRoundTripAndReplace(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	a, b, c := WorkstreamKey{1, "a"}, WorkstreamKey{2, "b"}, WorkstreamKey{1, "c"}

	if err := st.SetWorkstreamOrder(ctx, []WorkstreamKey{b, a, c}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListWorkstreamRanks(ctx)
	if err != nil || got[b] != 1 || got[a] != 2 || got[c] != 3 {
		t.Fatalf("ranks = %v, err %v", got, err)
	}

	if err := st.SetWorkstreamOrder(ctx, []WorkstreamKey{c}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListWorkstreamRanks(ctx)
	if len(got) != 1 || got[c] != 1 {
		t.Fatalf("replace should drop unlisted: %v", got)
	}
}

func keysOf(ids ...string) []WorkstreamKey {
	out := make([]WorkstreamKey, 0, len(ids))
	for _, id := range ids {
		out = append(out, WorkstreamKey{ProjectID: 1, BeadID: id})
	}
	return out
}

func orderOf(t *testing.T, st *Store) []string {
	t.Helper()
	ranks, err := st.ListWorkstreamRanks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(ranks))
	for k, r := range ranks {
		out[r-1] = k.BeadID
	}
	return out
}

func TestWorkstreamRanks_MovesCompose(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	present := keysOf("a", "b", "c", "d")
	if _, err := st.MoveWorkstream(ctx, present, present[2], 1); err != nil { // c to top
		t.Fatal(err)
	}
	// The second caller still holds the original order: the store's wins.
	if _, err := st.MoveWorkstream(ctx, present, present[3], 2); err != nil { // d to 2
		t.Fatal(err)
	}
	if got := orderOf(t, st); len(got) != 4 || got[0] != "c" || got[1] != "d" || got[2] != "a" || got[3] != "b" {
		t.Fatalf("order = %v", got)
	}
}

func TestWorkstreamRanks_NewWorkstreamDoesNotReorderExisting(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if err := st.SetWorkstreamOrder(ctx, keysOf("b", "a")); err != nil {
		t.Fatal(err)
	}
	// "n" is new (unranked); moving it to the end keeps b before a.
	present := keysOf("a", "b", "n")
	if _, err := st.MoveWorkstream(ctx, present, present[2], 3); err != nil {
		t.Fatal(err)
	}
	if got := orderOf(t, st); got[0] != "b" || got[1] != "a" || got[2] != "n" {
		t.Fatalf("order = %v", got)
	}
}

func TestWorkstreamRanks_ConcurrentMovesDoNotLoseOne(t *testing.T) {
	st, err := Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	present := keysOf("a", "b", "c", "d", "e", "f")
	var wg sync.WaitGroup
	// Each mover sends its own bead to the top; every one must end up ranked and the list stays whole.
	for _, k := range present {
		wg.Add(1)
		go func(k WorkstreamKey) {
			defer wg.Done()
			if _, err := st.MoveWorkstream(ctx, present, k, 1); err != nil {
				t.Error(err)
			}
		}(k)
	}
	wg.Wait()
	got := orderOf(t, st)
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	if len(got) != 6 || len(seen) != 6 {
		t.Fatalf("order = %v", got)
	}
}

func TestMoveBead_RanksOnlyItsSiblings(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	k := func(id string) WorkstreamKey { return WorkstreamKey{ProjectID: 1, BeadID: id} }
	if _, err := s.MoveBead(ctx, []WorkstreamKey{k("x.1"), k("x.2")}, k("x.2"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveBead(ctx, []WorkstreamKey{k("w.1"), k("w.2"), k("w.3")}, k("w.3"), 1); err != nil {
		t.Fatal(err)
	}
	ranks, err := s.ListBeadRanks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"w.3": 1, "w.1": 2, "w.2": 3, "x.2": 1, "x.1": 2} {
		if ranks[k(id)] != want {
			t.Errorf("%s = %d, want %d (%v)", id, ranks[k(id)], want, ranks)
		}
	}
	// Workstream order lives apart and survives.
	if got, _ := s.ListWorkstreamRanks(ctx); len(got) != 0 {
		t.Errorf("workstream ranks %v", got)
	}
}

func TestPostgresWorkstreamMovesCompose(t *testing.T) {
	st := postgresTestStore(t)
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, Project{Name: "ranks", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	present := []WorkstreamKey{{pid, "a"}, {pid, "b"}, {pid, "c"}, {pid, "d"}}
	if err := st.SetWorkstreamOrder(ctx, present); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveWorkstream(ctx, present, present[2], 1); err != nil {
		t.Fatal(err)
	}
	got, err := st.MoveWorkstream(ctx, present, present[3], 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkstreamKey{present[2], present[3], present[0], present[1]}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	ranks, err := st.ListWorkstreamRanks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range want {
		if ranks[k] != i+1 {
			t.Fatalf("ranks = %v, want %v", ranks, want)
		}
	}
}

func TestPostgresWorkstreamConcurrentMovesCompose(t *testing.T) {
	st := postgresTestStore(t)
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, Project{Name: "ranks", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	present := []WorkstreamKey{{pid, "a"}, {pid, "b"}, {pid, "c"}, {pid, "d"}}
	// Include the empty-table case: row locks alone cannot serialize these moves.
	for _, seeded := range []bool{false, true} {
		if seeded {
			if err := st.SetWorkstreamOrder(ctx, present); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() { <-start; _, err := st.MoveWorkstream(ctx, present, present[2], 1); errs <- err }()
		go func() { <-start; _, err := st.MoveWorkstream(ctx, present, present[3], 2); errs <- err }()
		close(start)
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		got := orderOf(t, st)
		// Either serial ordering preserves both moves. A lost write produces neither.
		if !reflect.DeepEqual(got, []string{"c", "d", "a", "b"}) && !reflect.DeepEqual(got, []string{"c", "a", "d", "b"}) {
			t.Fatalf("moves did not compose (seeded=%v): %v", seeded, got)
		}
	}
}
