package beads

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeShow answers `br show <ids> --json` like br: it is killed when asked for
// more than bigLimit ids, and may fail chosen ids' batches.
func fakeShow(bigLimit int, failID string, calls *int, mu *sync.Mutex) execFunc {
	return func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		mu.Lock()
		*calls++
		mu.Unlock()
		ids := args[1 : len(args)-1]
		if len(ids) > bigLimit {
			return nil, nil, errors.New("signal: killed")
		}
		var recs []string
		for _, id := range ids {
			if id == failID {
				return nil, nil, errors.New("signal: killed")
			}
			recs = append(recs, fmt.Sprintf(`{"id":%q,"dependencies":[{"id":"root","dependency_type":"parent-child"}],"comments":[]}`, id))
		}
		return []byte("[" + strings.Join(recs, ",") + "]"), nil, nil
	}
}

func idsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("p-%d", i)
	}
	return out
}

// PostgreSQL's bead command accepts exactly one ID per show.
func TestDetailsSingleIDBackend(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprint(fresh), func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			c := &cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
				if len(args) != 3 || args[0] != "show" || args[2] != "--json" {
					return nil, []byte("show requires one bead"), errors.New("exit status 1")
				}
				mu.Lock()
				asked = append(asked, args[1])
				mu.Unlock()
				return []byte(fmt.Sprintf(`{"id":%q,"dependencies":[{"depends_on_id":"root","type":"parent-child"}],"comments":[{"text":%q}]}`, args[1], args[1])), nil, nil
			}}
			ids := []string{"p-3", "p-1", "p-2", "p-0"}
			got, err := c.details(context.Background(), t.TempDir(), ids, fresh, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(asked) != len(ids) || len(got) != len(ids) {
				t.Fatalf("asked %v, got %v", asked, got)
			}
			for _, id := range ids {
				if got[id].Parent != "root" || len(got[id].Comments) != 1 || got[id].Comments[0].Text != id {
					t.Fatalf("%s: incorrect detail: %+v", id, got[id])
				}
			}
			if !fresh && !slices.Equal(asked, ids) {
				t.Fatalf("display order %v, want %v", asked, ids)
			}
		})
	}
}

// A cold start asks for a whole project (more ids than one br show survives):
// smaller batches still return every bead.
func TestColdStartShowBatchesSmallerThanTheKillLimit(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := &cmdClient{run: fakeShow(50, "", &calls, &mu)}
	ids := idsN(215)
	for _, fresh := range []bool{false, true} {
		got, err := c.details(context.Background(), t.TempDir(), ids, fresh, fresh)
		if err != nil || len(got) != len(ids) {
			t.Fatalf("fresh=%v: got %d details, err %v; want %d", fresh, len(got), err, len(ids))
		}
		if got["p-7"].Parent != "root" {
			t.Fatalf("parent = %q", got["p-7"].Parent)
		}
	}
}

// A batch that keeps failing costs only its own beads for display (partial map
// + PartialDetailsError, one retry), while enforcement reads fail whole.
func TestShowBatchFailureIsPartialForDisplayAndFatalWhenFresh(t *testing.T) {
	var calls int
	var mu sync.Mutex
	c := &cmdClient{run: fakeShow(50, "p-45", &calls, &mu)}
	ids := idsN(100)
	got, err := c.details(context.Background(), t.TempDir(), ids, false, false)
	var partial *PartialDetailsError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want PartialDetailsError", err)
	}
	if len(partial.Missing) != showBatch || len(got) != len(ids)-showBatch {
		t.Fatalf("missing %d, read %d; want %d unread", len(partial.Missing), len(got), showBatch)
	}
	if _, ok := got["p-0"]; !ok {
		t.Fatal("beads from healthy batches must be returned")
	}
	if _, err := c.details(context.Background(), t.TempDir(), ids, true, true); err == nil {
		t.Fatal("fresh read must fail when ids stay unread")
	}
}

// A killed batch is retried once, and a later tick starts clean (no sticky failure).
func TestShowBatchRetriesOnceThenRecovers(t *testing.T) {
	var calls int
	var mu sync.Mutex
	failing := true
	c := &cmdClient{run: func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if failing && calls == 1 {
			return nil, nil, errors.New("signal: killed")
		}
		return []byte(`[{"id":"p-0","dependencies":[],"comments":[]}]`), nil, nil
	}}
	got, err := c.details(context.Background(), t.TempDir(), []string{"p-0"}, true, true)
	if err != nil || len(got) != 1 || calls != 2 {
		t.Fatalf("got %v err %v calls %d; want 1 detail after a retry", got, err, calls)
	}
}

type flakyDetails struct {
	Client
	asked [][]string
	fail  map[string]bool
}

func (f *flakyDetails) List(context.Context, string, ListOptions) ([]Bead, error) {
	return []Bead{{ID: "a", UpdatedAt: "t"}, {ID: "b", UpdatedAt: "t"}, {ID: "c", UpdatedAt: "t"}}, nil
}

func (f *flakyDetails) Details(_ context.Context, _ string, ids []string) (map[string]Detail, error) {
	f.asked = append(f.asked, ids)
	out := map[string]Detail{}
	var missing []string
	for _, id := range ids {
		if f.fail[id] {
			missing = append(missing, id)
			continue
		}
		out[id] = Detail{Parent: "root"}
	}
	if len(missing) > 0 {
		return out, &PartialDetailsError{Missing: missing, Err: errors.New("killed")}
	}
	return out, nil
}

// Each retry of a partial display read asks only for what is still unread.
func TestColdStartPartialReadsConvergeAcrossTicks(t *testing.T) {
	dir := t.TempDir()
	flaky := &flakyDetails{fail: map[string]bool{"b": true}}
	c := NewCached(flaky)
	ids := []string{"a", "b", "c"}
	if _, err := c.Details(context.Background(), dir, ids); err == nil {
		t.Fatal("first read must be partial")
	}
	flaky.fail = nil
	got, err := c.Details(context.Background(), dir, ids)
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d err %v; want all 3", len(got), err)
	}
	if last := flaky.asked[len(flaky.asked)-1]; len(last) != 1 || last[0] != "b" {
		t.Fatalf("retry asked %v, want only [b]", last)
	}
}
