package beads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// countingClient counts List calls through to a MemClient.
type countingClient struct {
	*MemClient
	lists, comments int
}

func (c *countingClient) Comments(ctx context.Context, dir, id string) ([]Comment, error) {
	c.comments++
	return c.MemClient.Comments(ctx, dir, id)
}

func (c *countingClient) List(ctx context.Context, dir string, opts ListOptions) ([]Bead, error) {
	c.lists++
	return c.MemClient.List(ctx, dir, opts)
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(time.Now().String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCachedClientRereadsOnlyWhenBeadsFilesChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, ".beads", "beads.db"))
	inner := &countingClient{MemClient: &MemClient{}}
	c := NewCached(inner)

	for i := 0; i < 3; i++ {
		if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
			t.Fatal(err)
		}
	}
	if inner.lists != 1 {
		t.Fatalf("unchanged beads: want 1 br call, got %d", inner.lists)
	}
	// One comments read per bead per snapshot was most of cmdrd's br traffic.
	for i := 0; i < 3; i++ {
		_, _ = c.Comments(ctx, dir, "mem-1")
	}
	if inner.comments != 1 {
		t.Fatalf("unchanged beads: want 1 comments call, got %d", inner.comments)
	}

	// Another process (an agent's br) writes: the WAL moves.
	touch(t, filepath.Join(dir, ".beads", "beads.db-wal"))
	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 2 {
		t.Fatalf("after a write: want a fresh read, got %d calls", inner.lists)
	}

	// A write through this client drops the cache even within one mtime tick.
	if _, err := c.Create(ctx, dir, "new", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.List(ctx, dir, ListOptions{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if inner.lists != 3 || len(got) != 1 {
		t.Fatalf("after Create: want a fresh read with the new bead, got %d calls, %d beads", inner.lists, len(got))
	}
}

func TestCachedClientCallersGetTheirOwnCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inner := &countingClient{MemClient: &MemClient{}}
	c := NewCached(inner)
	_, _ = c.List(ctx, dir, ListOptions{})
	got, _ := c.List(ctx, dir, ListOptions{})
	got = append(got, Bead{ID: "x"}) // callers append: the cached slice must not change
	again, _ := c.List(ctx, dir, ListOptions{})
	if len(again) != 0 {
		t.Fatalf("a caller's append leaked into the cache: %v", again)
	}
}

// cmdrd's readers share one cache. A slow read that started before a write
// must not, when it returns, wipe what a faster reader cached after the write.
func TestCachedClientLateStaleReadKeepsNewerEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	wal := filepath.Join(dir, ".beads", "beads.db-wal")
	touch(t, wal)
	inner := &countingClient{MemClient: &MemClient{}}
	c := NewCached(inner)
	slow := &blockingClient{countingClient: inner, started: make(chan struct{}), release: make(chan struct{})}
	c.Client = slow
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Comments(ctx, dir, "slow") // reads at the old fingerprint
	}()
	<-slow.started
	if err := os.WriteFile(wal, []byte("an agent's write moves the size"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = c.List(ctx, dir, ListOptions{Status: "all"}) // cached at the new one
	close(slow.release)
	<-done
	_, _ = c.List(ctx, dir, ListOptions{Status: "all"})
	if inner.lists != 1 {
		t.Fatalf("the late stale read dropped the newer cache: %d list calls, want 1", inner.lists)
	}
}

// blockingClient holds its first Comments call until released.
type blockingClient struct {
	*countingClient
	started, release chan struct{}
	once             sync.Once
}

func (c *blockingClient) Comments(ctx context.Context, dir, id string) ([]Comment, error) {
	c.once.Do(func() { close(c.started); <-c.release })
	return c.countingClient.Comments(ctx, dir, id)
}

func TestDetailsIsOneShowPerID(t *testing.T) {
	var calls [][]string
	out := `[{"id":"p-1.2","dependencies":[{"id":"p-1","dependency_type":"parent-child"},{"id":"p-1.1","dependency_type":"blocks"}],
	  "comments":[{"author":"a","text":"gate passed by auto at 1: ok","created_at":"2026-10-22T00:00:00Z"}]},
	 {"id":"p-1.1.1","dependencies":[{"id":"p-2","dependency_type":"parent-child"}]}]`
	c := &cmdClient{run: func(_ context.Context, _, name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string{name}, args...))
		var records []json.RawMessage
		if err := json.Unmarshal([]byte(out), &records); err != nil {
			t.Fatal(err)
		}
		if args[1] == "p-1.2" {
			return append(append([]byte{'['}, records[0]...), ']'), nil, nil
		}
		return append(append([]byte{'['}, records[1]...), ']'), nil, nil
	}}
	got, err := c.Details(context.Background(), "/p", []string{"p-1.2", "p-1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("want one show per ID, got %v", calls)
	}
	for i, id := range []string{"p-1.2", "p-1.1.1"} {
		if len(calls[i]) != 4 || calls[i][1] != "show" || calls[i][2] != id || calls[i][3] != "--json" {
			t.Fatalf("single-ID calls: %v", calls)
		}
	}
	if got["p-1.2"].Parent != "p-1" || len(got["p-1.2"].Deps) != 2 || got["p-1.2"].Deps[1] != (Dep{IssueID: "p-1.2", DependsOnID: "p-1.1", Type: "blocks"}) {
		t.Fatalf("p-1.2 = %+v", got["p-1.2"])
	}
	if len(got["p-1.2"].Comments) != 1 || got["p-1.2"].Comments[0].Text != "gate passed by auto at 1: ok" {
		t.Fatalf("p-1.2 comments = %+v", got["p-1.2"].Comments)
	}
	if got["p-1.1.1"].Parent != "p-2" {
		t.Fatalf("moved bead's parent = %q, want p-2 (its edge, not its dotted id)", got["p-1.1.1"].Parent)
	}
}

func TestDetailsReadsTheExportAndShowsOnlyWhatItLacks(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".beads"), 0o755)
	os.WriteFile(filepath.Join(dir, ".beads", "beads.db"), nil, 0o644)
	export := `{"id":"p-1.2","dependencies":[{"issue_id":"p-1.2","depends_on_id":"p-1","type":"parent-child"},{"issue_id":"p-1.2","depends_on_id":"p-1.1","type":"blocks"}],"comments":[{"id":1,"author":"a","text":"hi","created_at":"2026-10-22T00:00:00Z"}]}
{"id":"p-1.1.1","dependencies":[{"issue_id":"p-1.1.1","depends_on_id":"p-2","type":"parent-child"}]}
`
	os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(export), 0o644)
	var calls [][]string
	c := &cmdClient{run: func(_ context.Context, _, name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, args)
		return []byte(`[{"id":"p-9","dependencies":[{"id":"p-3","dependency_type":"parent-child"}]}]`), nil, nil
	}}
	got, err := c.Details(context.Background(), dir, []string{"p-1.2", "p-1.1.1", "p-9"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) != 3 || calls[0][1] != "p-9" {
		t.Fatalf("want one show of only p-9, got %v", calls)
	}
	if got["p-1.2"].Parent != "p-1" || len(got["p-1.2"].Deps) != 2 || got["p-1.2"].Deps[1] != (Dep{IssueID: "p-1.2", DependsOnID: "p-1.1", Type: "blocks"}) || got["p-1.2"].Comments[0].Text != "hi" {
		t.Fatalf("p-1.2 = %+v", got["p-1.2"])
	}
	if got["p-1.1.1"].Parent != "p-2" || got["p-9"].Parent != "p-3" {
		t.Fatalf("parents = %q %q", got["p-1.1.1"].Parent, got["p-9"].Parent)
	}

	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), old, old)
	calls = nil
	c.Details(context.Background(), dir, []string{"p-1.2"})
	if len(calls) != 1 {
		t.Fatalf("an expired display export must use br show; calls = %v", calls)
	}
}

// br 0.1.44 on Linux rewrites beads.db's header (change counter,
// version-valid-for) on every read. That alone must not count as a change,
// or ReadConsistent fails every snapshot ("changed during List/Details read").
func TestFingerprintIgnoresSQLiteHeaderCounterBump(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, ".beads", "beads.db")
	raw := make([]byte, 4096)
	copy(raw, "SQLite format 3\x00")
	write := func(mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(db, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(db, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write(now)
	before := fingerprint(dir)

	raw[27]++
	raw[95]++
	write(now.Add(time.Second))
	if got := fingerprint(dir); got != before {
		t.Fatalf("header-only bump changed the fingerprint: %q -> %q", before, got)
	}
	reads := 0
	if err := ReadConsistent(context.Background(), dir, func() error {
		reads++
		raw[27]++
		raw[95]++
		write(now.Add(time.Duration(reads+1) * time.Second))
		return nil
	}); err != nil {
		t.Fatalf("ReadConsistent with a header-bumping reader: %v", err)
	}

	raw[2000] = 1
	write(now.Add(time.Minute))
	if got := fingerprint(dir); got == before {
		t.Fatal("a page change kept the fingerprint")
	}
}
