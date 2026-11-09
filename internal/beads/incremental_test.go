package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// incClient is a br stand-in: a list with change keys and a show that records
// the ids it was asked for.
type incClient struct {
	Client
	beads map[string]Bead
	edges map[string]Detail
	lists int
	shown [][]string
	fail  map[string]bool
}

func (f *incClient) List(context.Context, string, ListOptions) ([]Bead, error) {
	f.lists++
	var out []Bead
	for _, b := range f.beads {
		out = append(out, b)
	}
	return out, nil
}

func (f *incClient) Details(_ context.Context, _ string, ids []string) (map[string]Detail, error) {
	f.shown = append(f.shown, slices.Clone(ids))
	out := map[string]Detail{}
	var missing []string
	for _, id := range ids {
		if f.fail[id] {
			missing = append(missing, id)
			continue
		}
		out[id] = f.edges[id]
	}
	if len(missing) > 0 {
		return out, &PartialDetailsError{Missing: missing, Err: errors.New("killed")}
	}
	return out, nil
}

func (f *incClient) set(id string, edit func(*Bead)) {
	b := f.beads[id]
	b.ID = id
	edit(&b)
	f.beads[id] = b
}

func newIncremental(t *testing.T, n int) (*incClient, *CachedClient, string, []string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &incClient{beads: map[string]Bead{}, edges: map[string]Detail{}, fail: map[string]bool{}}
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("p-%d", i)
		ids = append(ids, id)
		f.beads[id] = Bead{ID: id, Status: "open", UpdatedAt: "t0"}
		f.edges[id] = Detail{Parent: "root"}
	}
	return f, NewCached(f), dir, ids
}

// bump moves the beads files, as any br write does.
func bump(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".beads", "last-touched"), []byte(strings.Repeat("w", n)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lastShown(f *incClient) []string {
	if len(f.shown) == 0 {
		return nil
	}
	return f.shown[len(f.shown)-1]
}

func TestIncrementalDetailsOneBeadUpdateReadsExactlyThatBead(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 50)
	ctx := context.Background()
	if got, err := c.Details(ctx, dir, ids); err != nil || len(got) != 50 {
		t.Fatalf("first pass: %d err %v", len(got), err)
	}
	if len(f.shown) != 1 || len(f.shown[0]) != 50 {
		t.Fatalf("first pass is the full read, got %v", f.shown)
	}
	f.set("p-7", func(b *Bead) { b.UpdatedAt = "t1" })
	f.edges["p-7"] = Detail{Parent: "moved"}
	bump(t, dir, 1)
	listsBefore := f.lists
	got, err := c.Details(ctx, dir, ids)
	if err != nil || len(got) != 50 || got["p-7"].Parent != "moved" || got["p-8"].Parent != "root" {
		t.Fatalf("second pass: err %v p-7=%v", err, got["p-7"])
	}
	if len(f.shown) != 2 || !slices.Equal(f.shown[1], []string{"p-7"}) {
		t.Fatalf("show calls = %v, want one more for exactly p-7", f.shown)
	}
	if f.lists-listsBefore != 1 {
		t.Fatalf("list calls = %d, want 1", f.lists-listsBefore)
	}
}

func TestIncrementalDetailsNewBeadIsReadAndRemovedIsDropped(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 3)
	ctx := context.Background()
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.beads["p-new"] = Bead{ID: "p-new", Status: "open", UpdatedAt: "t1"}
	f.edges["p-new"] = Detail{Parent: "p-0"}
	delete(f.beads, "p-1")
	bump(t, dir, 1)
	want := []string{"p-0", "p-2", "p-new"}
	got, err := c.Details(ctx, dir, want)
	if err != nil || len(got) != 3 || got["p-new"].Parent != "p-0" {
		t.Fatalf("got %v err %v", got, err)
	}
	if !slices.Equal(lastShown(f), []string{"p-new"}) {
		t.Fatalf("shown %v, want only the new bead", lastShown(f))
	}
	if _, ok := c.details[dir]["p-1"]; ok {
		t.Fatal("removed bead stays cached")
	}
}

// br bumps updated_at on the bead that owns a label, comment or edge; the other
// end of an edge only changes its dependent count. Each is noticed.
func TestIncrementalDetailsNoticesLabelEdgeAndCommentChanges(t *testing.T) {
	for name, edit := range map[string]func(*Bead){
		"label":         func(b *Bead) { b.UpdatedAt = "t1"; b.Labels = []string{"x"} },
		"comment":       func(b *Bead) { b.UpdatedAt = "t1"; b.CommentCount = 1 },
		"dep add":       func(b *Bead) { b.UpdatedAt = "t1"; b.DependencyCount = 1 },
		"dep add far":   func(b *Bead) { b.DependentCount = 1 }, // other end: no updated_at bump
		"status":        func(b *Bead) { b.UpdatedAt = "t1"; b.Status = "closed" },
		"label no bump": func(b *Bead) { b.Labels = []string{"y"} },
	} {
		t.Run(name, func(t *testing.T) {
			f, c, dir, ids := newIncremental(t, 4)
			ctx := context.Background()
			if _, err := c.Details(ctx, dir, ids); err != nil {
				t.Fatal(err)
			}
			f.set("p-2", edit)
			bump(t, dir, 1)
			if _, err := c.Details(ctx, dir, ids); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(lastShown(f), []string{"p-2"}) || len(f.shown) != 2 {
				t.Fatalf("show calls = %v, want a second one for p-2 only", f.shown)
			}
		})
	}
}

func TestIncrementalDetailsFailedBatchKeepsCachedData(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 5)
	ctx := context.Background()
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.set("p-3", func(b *Bead) { b.UpdatedAt = "t1" })
	f.edges["p-3"] = Detail{Parent: "new"}
	f.fail["p-3"] = true
	bump(t, dir, 1)
	got, err := c.Details(ctx, dir, ids)
	var partial *PartialDetailsError
	if !errors.As(err, &partial) || !slices.Equal(partial.Missing, []string{"p-3"}) {
		t.Fatalf("err = %v, want partial for p-3", err)
	}
	if len(got) != 5 || got["p-3"].Parent != "root" {
		t.Fatalf("previous data not kept: %v", got["p-3"])
	}
	f.fail["p-3"] = false
	bump(t, dir, 2)
	got, err = c.Details(ctx, dir, ids)
	if err != nil || got["p-3"].Parent != "new" || !slices.Equal(lastShown(f), []string{"p-3"}) {
		t.Fatalf("retry: err %v p-3=%v shown %v", err, got["p-3"], lastShown(f))
	}
}

func TestIncrementalDetailsBurstOfWritesIsOneIncrementalRead(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 30)
	ctx := context.Background()
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		f.set(ids[i], func(b *Bead) { b.UpdatedAt = "t1" })
		bump(t, dir, i)
	}
	listsBefore, showsBefore := f.lists, len(f.shown)
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if f.lists-listsBefore != 1 || len(f.shown)-showsBefore != 1 || len(lastShown(f)) != 20 {
		t.Fatalf("lists +%d, shows +%d (%d ids)", f.lists-listsBefore, len(f.shown)-showsBefore, len(lastShown(f)))
	}
	// Nothing moved since: no br call at all.
	listsBefore, showsBefore = f.lists, len(f.shown)
	if _, err := c.Details(ctx, dir, ids); err != nil || f.lists != listsBefore || len(f.shown) != showsBefore {
		t.Fatalf("unchanged board called br: lists +%d shows +%d err %v", f.lists-listsBefore, len(f.shown)-showsBefore, err)
	}
}

// expClient tells a DB-backed incremental read (changedDetails) from a plain
// Details read, which may be served from the export.
type expClient struct {
	*incClient
	viaChanged int
}

func (f *expClient) changedDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	f.viaChanged++
	return f.incClient.Details(ctx, dir, ids)
}

func TestInvariantIncrementalUnchangedBoardReadsNothingAndOneChangeReadsOneBead(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 30)
	inv := Incremental(c)
	ctx := context.Background()
	if got, err := inv.Details(ctx, dir, ids); err != nil || len(got) != 30 {
		t.Fatalf("cold pass: %d err %v", len(got), err)
	}
	shown := len(f.shown)
	if _, err := inv.Details(ctx, dir, ids); err != nil || len(f.shown) != shown {
		t.Fatalf("unchanged board issued br show: %v err %v", f.shown[shown:], err)
	}
	// An owner label just added moves that bead's key: exactly it is re-read.
	f.set("p-3", func(b *Bead) { b.Labels = []string{"owner:operator-x"} })
	f.edges["p-3"] = Detail{Parent: "other"}
	bump(t, dir, 1)
	got, err := inv.Details(ctx, dir, ids)
	if err != nil || got["p-3"].Parent != "other" || got["p-4"].Parent != "root" {
		t.Fatalf("changed bead not seen: %v err %v", got["p-3"], err)
	}
	if len(f.shown) != shown+1 || !slices.Equal(lastShown(f), []string{"p-3"}) {
		t.Fatalf("show calls = %v, want one more for exactly p-3", f.shown[shown:])
	}
	// A just-closed bead is re-read too.
	f.set("p-5", func(b *Bead) { b.Status = "closed" })
	bump(t, dir, 2)
	if _, err := inv.Details(ctx, dir, ids); err != nil || !slices.Equal(lastShown(f), []string{"p-5"}) {
		t.Fatalf("closed bead not re-read: %v err %v", f.shown, err)
	}
}

func TestInvariantIncrementalColdStartSkipsExportAndDistrustsExportEntries(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 5)
	e := &expClient{incClient: f}
	c.Client = e
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Incremental(c).Details(ctx, dir, ids); err != nil || e.viaChanged != 1 || len(f.shown) != 1 {
		t.Fatalf("cold invariant read must be DB-backed: viaChanged %d shown %v err %v", e.viaChanged, f.shown, err)
	}
	// A display read on a fresh store may take the export; the invariant then re-reads those.
	c2 := NewCached(e)
	if _, err := c2.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	before := e.viaChanged
	if _, err := Incremental(c2).Details(ctx, dir, ids); err != nil || e.viaChanged != before+1 {
		t.Fatalf("export-sourced entries were trusted by the invariant read (viaChanged %d -> %d, err %v)", before, e.viaChanged, err)
	}
}

func TestInvariantIncrementalActionVerbsStillReadFresh(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 4)
	inv := Incremental(c)
	ctx := context.Background()
	if _, err := inv.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	shown := len(f.shown)
	if _, err := FreshDetails(ctx, ForActions(c), dir, ids); err != nil || len(f.shown) != shown+1 {
		t.Fatalf("FreshDetails must bypass the incremental store: shown %v err %v", f.shown, err)
	}
}

// gatedClient holds every Details call until released and records what each saw.
type gatedClient struct {
	*incClient
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	calls   int
}

func (g *gatedClient) List(ctx context.Context, dir string, o ListOptions) ([]Bead, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.incClient.List(ctx, dir, o)
}

func (g *gatedClient) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.incClient.Details(ctx, dir, ids)
}

func newGated(t *testing.T, n int) (*gatedClient, *CachedClient, string, []string) {
	t.Helper()
	f, _, dir, ids := newIncremental(t, n)
	g := &gatedClient{incClient: f, entered: make(chan struct{}, 16), release: make(chan struct{})}
	return g, NewCached(g), dir, ids
}

func waitDetails(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("a read never returned")
		return nil
	}
}

// Overlapping loads of one project share one re-read of a flushed export.
func TestIncrementalDetailsOverlappingReadsShareOneReread(t *testing.T) {
	g, c, dir, ids := newGated(t, 40)
	ctx := context.Background()
	done := make(chan error, 4)
	var results [4]map[string]Detail
	for i := range results {
		go func() {
			var err error
			results[i], err = c.detailsSalvaging(ctx, dir, ids)
			done <- err
		}()
	}
	<-g.entered
	time.Sleep(20 * time.Millisecond) // let the others reach the read too
	close(g.release)
	for range results {
		if err := waitDetails(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if g.calls != 1 {
		t.Fatalf("%d re-reads for 4 overlapping loads, want 1", g.calls)
	}
	for i, r := range results {
		if len(r) != len(ids) {
			t.Fatalf("load %d got %d of %d", i, len(r), len(ids))
		}
	}
}

// A read that waited behind another still sees a bead that moved meanwhile.
func TestIncrementalDetailsAWaitingReadSeesAWriteMadeDuringTheFirst(t *testing.T) {
	g, c, dir, ids := newGated(t, 3)
	ctx := context.Background()
	first := make(chan error, 1)
	go func() { _, err := c.detailsSalvaging(ctx, dir, ids); first <- err }()
	<-g.entered

	second := make(chan error, 1)
	var got map[string]Detail
	g.mu.Lock()
	g.set("p-1", func(b *Bead) { b.UpdatedAt = "t1" })
	g.edges["p-1"] = Detail{Parent: "moved"}
	g.mu.Unlock()
	bump(t, dir, 1)
	go func() {
		var err error
		got, err = c.detailsSalvaging(ctx, dir, ids)
		second <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(g.release)
	if err := waitDetails(t, first); err != nil {
		t.Fatal(err)
	}
	if err := waitDetails(t, second); err != nil {
		t.Fatal(err)
	}
	if got["p-1"].Parent != "moved" {
		t.Fatalf("the waiting read returned %+v, from before the write", got["p-1"])
	}
}

// One caller's cancel ends only its own read: the next in line reads on its own
// live context and gets the answer, and nothing runs on after both return.
func TestIncrementalDetailsACancelledCallerDoesNotFailTheNext(t *testing.T) {
	g, c, dir, ids := newGated(t, 3)
	cancelled, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.detailsSalvaging(cancelled, dir, ids); first <- err }()
	<-g.entered

	second := make(chan error, 1)
	var got map[string]Detail
	go func() {
		var err error
		got, err = c.detailsSalvaging(context.Background(), dir, ids)
		second <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := waitDetails(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled caller got %v", err)
	}
	<-g.entered // the next caller's own read
	close(g.release)
	if err := waitDetails(t, second); err != nil {
		t.Fatalf("the next caller inherited the cancel: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d of %d", len(got), len(ids))
	}
}

// A caller that gives up while queued returns at once and leaves the running read alone.
func TestIncrementalDetailsAQueuedCallerCanGiveUp(t *testing.T) {
	g, c, dir, ids := newGated(t, 3)
	first := make(chan error, 1)
	go func() { _, err := c.detailsSalvaging(context.Background(), dir, ids); first <- err }()
	<-g.entered

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.detailsSalvaging(gone, dir, ids); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued caller got %v", err)
	}
	close(g.release)
	if err := waitDetails(t, first); err != nil {
		t.Fatalf("the running read was disturbed: %v", err)
	}
}

func setExport(t *testing.T, dir string, at time.Time, lines ...string) {
	t.Helper()
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func exportLine(id, parent string) string {
	return fmt.Sprintf(`{"id":%q,"dependencies":[{"depends_on_id":%q,"type":"parent-child"}]}`, id, parent)
}

// The once-per-restart full re-read: entries a first read took from the export
// went stale when a flush moved that file, and all of them went to br show (72
// batches, 1m42s). A bead whose list key did not move re-takes its Detail from the
// newer export; only moved beads go to br show.
func TestIncrementalDetailsMovedExportSendsNoUnchangedBeadToShow(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 5)
	ctx := context.Background()
	now := time.Now()
	setExport(t, dir, now.Add(-2*time.Second), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil

	var lines []string
	for _, id := range ids {
		lines = append(lines, exportLine(id, "from-export"))
	}
	setExport(t, dir, now.Add(-time.Second), lines...)
	got, err := c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 0 {
		t.Fatalf("unchanged beads went to br show: %v", f.shown)
	}
	for _, id := range ids {
		if got[id].Parent != "from-export" {
			t.Fatalf("%s not re-taken: %+v", id, got[id])
		}
	}

	// A moved bead goes to br show, alone, and is trusted from then on.
	f.set("p-2", func(b *Bead) { b.UpdatedAt = "t1" })
	f.edges["p-2"] = Detail{Parent: "from-show"}
	lines[0] = exportLine("p-0", "newer-export")
	setExport(t, dir, now, lines...)
	got, err = c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || !slices.Equal(f.shown[0], []string{"p-2"}) {
		t.Fatalf("shown = %v, want only the moved bead", f.shown)
	}
	if got["p-2"].Parent != "from-show" || got["p-0"].Parent != "newer-export" {
		t.Fatalf("p-2=%+v p-0=%+v", got["p-2"], got["p-0"])
	}
	setExport(t, dir, now.Add(time.Second), exportLine("p-2", "stale-export"))
	got, _ = c.Details(ctx, dir, []string{"p-2"})
	if got["p-2"].Parent != "from-show" {
		t.Fatalf("a br show entry was replaced by the export: %+v", got["p-2"])
	}
}

// The re-take keeps the first read's bound: an export past maxExportAge may lag
// the db by any amount, so its beads go to br show, once.
func TestIncrementalDetailsMovedExportPastTheLimitGoesToShow(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 3)
	ctx := context.Background()
	now := time.Now()
	setExport(t, dir, now.Add(-time.Second), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil
	setExport(t, dir, now.Add(-2*maxExportAge), exportLine("p-0", "lagging"), exportLine("p-1", "lagging"), exportLine("p-2", "lagging"))
	got, err := c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || len(f.shown[0]) != 3 || got["p-0"].Parent != "root" {
		t.Fatalf("shown = %v p-0=%+v, want all from br show", f.shown, got["p-0"])
	}
}

// An export that cannot answer for a bead is no answer: it goes to br show.
func TestIncrementalDetailsMovedExportMissingABeadFallsBackToShow(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 3)
	ctx := context.Background()
	now := time.Now()
	setExport(t, dir, now.Add(-time.Second), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil
	setExport(t, dir, now, exportLine("p-0", "root"), exportLine("p-1", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || !slices.Equal(f.shown[0], []string{"p-2"}) {
		t.Fatalf("shown = %v, want the bead the export lacks", f.shown)
	}
	f.shown = nil
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(path, []byte("{half a line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || len(f.shown[0]) != 2 {
		t.Fatalf("shown = %v, want the two export-sourced beads from a broken export", f.shown)
	}
}

// What the invariant pass does: it never trusts an export, so its first read after
// a display read that stamped entries shows those whole, once, and they are
// DB-backed for everyone after. Pinned because the re-take leaves entries stamped
// where the old re-read cleared them.
func TestIncrementalDetailsNoExportReadStillShowsExportEntriesOnce(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 4)
	ctx := context.Background()
	setExport(t, dir, time.Now().Add(-time.Second), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil
	inc := Incremental(c)
	if _, err := inc.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || len(f.shown[0]) != 4 {
		t.Fatalf("shown = %v, want every export-sourced bead", f.shown)
	}
	f.shown = nil
	setExport(t, dir, time.Now(), exportLine("p-0", "root"))
	bump(t, dir, 1)
	if _, err := inc.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 0 {
		t.Fatalf("DB-backed entries were read again: %v", f.shown)
	}
}

// A move between parents keeps every count and, in real projects, updated_at:
// only the parent says a cached Detail is out of date.
func TestDetailKeyNoticesAMove(t *testing.T) {
	var before, after Bead
	if err := json.Unmarshal([]byte(`{"id":"x","status":"open","parent":"a","dependency_count":1}`), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"x","status":"open","parent":"b","dependency_count":1}`), &after); err != nil {
		t.Fatal(err)
	}
	if detailKey(before) == detailKey(after) {
		t.Fatalf("the key did not change: %q", detailKey(after))
	}
}

// A first read past the age limit got its Details from br show, whatever the file
// holds. A flush that began before a write can land after it (newer mtime, older
// content, same list key): it must not replace what br show said. The moved file
// sends the entry to br show, once, and then it is trusted.
func TestIncrementalDetailsShownFirstReadIsNotRetaken(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 1)
	ctx := context.Background()
	setExport(t, dir, time.Now().Add(-time.Hour), exportLine("p-0", "root"))
	f.edges["p-0"] = Detail{Parent: "root", Comments: []Comment{{Text: "gate passed by auto"}}}
	if got, err := c.Details(ctx, dir, ids); err != nil || len(got["p-0"].Comments) != 1 {
		t.Fatalf("first read: %+v err %v", got["p-0"], err)
	}
	f.shown = nil
	setExport(t, dir, time.Now(), exportLine("p-0", "root"))
	got, err := c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["p-0"].Comments) != 1 {
		t.Fatalf("a Detail from br show was replaced by an older export: %+v", got["p-0"])
	}
	if len(f.shown) != 1 || !slices.Equal(f.shown[0], ids) {
		t.Fatalf("shown = %v, want the owed read", f.shown)
	}
	f.shown = nil
	setExport(t, dir, time.Now().Add(time.Second), exportLine("p-0", "root"))
	got, err = c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["p-0"].Comments) != 1 || len(f.shown) != 0 {
		t.Fatalf("after the owed read: %+v shown = %v", got["p-0"], f.shown)
	}
}

// bd leaves updated_at alone when an edge is swapped for another, so the list key
// stands. A first read past the limit still learns of it when the export next moves.
func TestIncrementalDetailsPastLimitFirstReadLearnsAKeyStableChange(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 1)
	ctx := context.Background()
	setExport(t, dir, time.Now().Add(-time.Hour), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.edges["p-0"] = Detail{Parent: "new-parent"}

	// While the file stands the entry stands.
	bump(t, dir, 1)
	f.shown = nil
	got, err := c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got["p-0"].Parent != "root" || len(f.shown) != 0 {
		t.Fatalf("file unchanged: %+v shown = %v", got["p-0"], f.shown)
	}

	setExport(t, dir, time.Now(), exportLine("p-0", "new-parent"))
	got, err = c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got["p-0"].Parent != "new-parent" || len(f.shown) != 1 {
		t.Fatalf("missed a key-stable change: %+v shown = %v", got["p-0"], f.shown)
	}
}

// A failed owed read keeps the old Detail and is owed again, not lost.
func TestIncrementalDetailsFailedOwedShowIsRetried(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 1)
	ctx := context.Background()
	setExport(t, dir, time.Now().Add(-time.Hour), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.edges["p-0"] = Detail{Parent: "new-parent"}
	f.fail["p-0"] = true
	setExport(t, dir, time.Now(), exportLine("p-0", "from-export"))
	got, err := c.detailsSalvaging(ctx, dir, ids)
	var partial *PartialDetailsError
	if !errors.As(err, &partial) || got["p-0"].Parent != "root" {
		t.Fatalf("failed read: %+v err %v", got["p-0"], err)
	}
	f.fail = map[string]bool{}
	got, err = c.detailsSalvaging(ctx, dir, ids)
	if err != nil || got["p-0"].Parent != "new-parent" {
		t.Fatalf("not retried: %+v err %v", got["p-0"], err)
	}
}

// The owed read is the only extra one: the invariant pass after it reads nothing,
// and a first read inside the limit keeps the re-take.
func TestIncrementalDetailsOwedShowHappensOnce(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 5)
	ctx := context.Background()
	setExport(t, dir, time.Now().Add(-time.Hour), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil
	setExport(t, dir, time.Now(), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := Incremental(c).Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || len(f.shown[0]) != 5 {
		t.Fatalf("shown = %v, want one read of all five", f.shown)
	}
}

// The invariant pass mails people: a moved export inside the age limit, which a
// display read would re-take, is no answer for it. Every entry goes to br show.
func TestIncrementalDetailsNoExportReadSkipsTheRetake(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 3)
	ctx := context.Background()
	setExport(t, dir, time.Now().Add(-time.Second), exportLine("p-0", "root"))
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil
	setExport(t, dir, time.Now(), exportLine("p-0", "from-export"), exportLine("p-1", "from-export"), exportLine("p-2", "from-export"))
	got, err := Incremental(c).Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || len(f.shown[0]) != 3 {
		t.Fatalf("shown = %v, want every bead from br show", f.shown)
	}
	if got["p-0"].Parent != "root" {
		t.Fatalf("p-0 = %+v, read from the export", got["p-0"])
	}
}

func TestIncrementalDetailsNoticesKeyStableBlockerSwap(t *testing.T) {
	f, c, dir, ids := newIncremental(t, 1)
	ctx := context.Background()
	set := func(on string) {
		var b Bead
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":"p-0","status":"open","updated_at":"t0","dependency_count":1,"dependencies":[{"issue_id":"p-0","depends_on_id":%q,"type":"blocks"}]}`, on)), &b); err != nil {
			t.Fatal(err)
		}
		f.beads[b.ID] = b
		f.edges[b.ID] = Detail{Deps: []Dep{{IssueID: b.ID, DependsOnID: on, Type: "blocks"}}}
	}
	set("p-old")
	if _, err := c.Details(ctx, dir, ids); err != nil {
		t.Fatal(err)
	}
	f.shown = nil
	set("p-new")
	bump(t, dir, 1)
	got, err := c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.shown) != 1 || got["p-0"].Deps[0].DependsOnID != "p-new" {
		t.Fatalf("stale blockers: %+v, reads %v", got, f.shown)
	}
}
