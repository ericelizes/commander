package beads

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ExportReads answers the board's reads (List, Details, Deps, Comments) from
// the project's .beads/issues.jsonl, which br rewrites in full on every write.
// Parsing it takes milliseconds; the br calls it replaces queued for minutes
// behind the agents' writes on one SQLite lock, so the board ran minutes
// behind. Writes, Show and any read the export cannot answer exactly go to c.
// A missing, half-written or lagging export also falls back to c.
func ExportReads(c Client) Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &exportClient{ctx: ctx, cancel: cancel, Client: c, files: map[string]*exportFile{}, flushed: map[string]time.Time{}, inflight: map[string]*flushCall{}, failed: map[string]flushFailure{}, seen: map[string]map[string]fileStamp{}, seeded: map[string]time.Time{}}
}

type exportClient struct {
	Client
	// ctx owns the background flushes: Close cancels them and waits.
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	files   map[string]*exportFile // projectDir -> last parse
	flushed map[string]time.Time   // projectDir -> when this client last asked br to export
	// inflight is the project's flush now running: every load that waits for
	// an export waits for this one, and no second one starts beside it.
	inflight map[string]*flushCall
	// failed counts the project's flushes failing in a row and when to retry.
	failed map[string]flushFailure
	// seen is each project's Dolt journals as stat'ed right before this
	// client's last successful flush: the export holds at least that state.
	seen map[string]map[string]fileStamp
	// seeded is the export mtime each project's detail cache was last offered.
	seeded map[string]time.Time
}

// flushCall is one flush of a project; done closes once err is set.
type flushCall struct {
	done chan struct{}
	err  error
}

// flushFailure is a run of failed flushes of one project.
type flushFailure struct {
	n       int
	retryAt time.Time
	err     error
}

// fileStamp is a file's size and mtime: a Dolt journal grows on every write.
type fileStamp struct {
	size  int64
	mtime time.Time
}

// exportFlusher can rewrite a project's export now (br sync --flush-only).
// br skips its own export after a write when another process holds the file,
// so under many writers the export can lag the database for minutes.
type exportFlusher interface {
	FlushExport(ctx context.Context, dir string) error
}

// flushEvery bounds the exports this client asks for: one per project per interval.
const flushEvery = 3 * time.Second

// FlushExport runs br sync --flush-only in dir, through the same queue as every br call.
func (c *cmdClient) FlushExport(ctx context.Context, dir string) error {
	args := []string{"sync", "--flush-only"}
	if isBD() {
		// bd in server mode never rewrites the export on its own.
		args = []string{"export", "-o", filepath.Join(dir, ".beads", "issues.jsonl")}
	}
	_, stderr, err := c.run(ctx, dir, beadsBinary(), args...)
	if err != nil {
		return fmt.Errorf("br sync --flush-only: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// FlushExport forwards to the client the cache wraps.
func (c *CachedClient) FlushExport(ctx context.Context, dir string) error {
	f, ok := c.Client.(exportFlusher)
	if !ok {
		return errNoExport
	}
	return f.FlushExport(ctx, dir)
}

type exportFile struct {
	size    int64
	mtime   time.Time
	issues  []exportIssue
	byID    map[string]*exportIssue
	parents map[string]int // id -> how many live issues depend on it
}

type exportIssue struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Acceptance   string    `json:"acceptance_criteria"`
	Status       string    `json:"status"`
	CloseReason  string    `json:"close_reason"`
	Priority     int       `json:"priority"`
	Labels       []string  `json:"labels"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
	Dependencies []Dep     `json:"dependencies"`
	Comments     []Comment `json:"comments"`
}

// exportLagMax is how far br's beads.db may be ahead of the export before it
// is treated as stale: br writes the export right after the database. bd's Dolt
// journal gets no allowance: in server mode bd never writes the export, only
// our flush does, so a write within 2s of the last flush (a close right after
// its --then-* hold, il9.172.31) was served stale until the next write.
const exportLagMax = 2 * time.Second

var errNoExport = errors.New("beads export unusable")

// load returns the parsed export of dir, re-parsing only when the file changed.
// An export behind the database is refreshed once (br sync --flush-only) first.
func (c *exportClient) load(ctx context.Context, dir string) (*exportFile, error) {
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	// Before the stat: a flush ending between the two must not make this
	// stat's (older) export look like one a warm client may skip waiting for.
	c.mu.Lock()
	_, warm := c.seen[dir]
	c.mu.Unlock()
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errNoExport
	}
	if c.exportBehind(dir, fi) {
		switch {
		case len(doltJournals(dir)) > 0 && warm:
			// bd export of a big Dolt project takes seconds (5s for 5,600 beads),
			// and on a busy one a write lands meanwhile, so the export it makes is
			// behind again. Waiting for it made every load pay the export and
			// then read bd anyway. Refresh it in the background for later loads
			// and answer this one from bd now.
			c.startFlush(dir, backgroundFlushBudget, true)
			return nil, errNoExport
		case len(doltJournals(dir)) > 0:
			// Not on a cold client (a cmdrd start): the detail cache is cold
			// too, and with no fresh export it reads every bead through bd show
			// (6 min for 5,600 beads, il9.202.29). Wait for the export instead;
			// if it fails, answer from the export on disk, flagged stale.
			if err := c.coldFlush(ctx, dir); err != nil {
				if errors.Is(err, errNoExport) {
					return nil, errNoExport // no flush to wait for: this client cannot, or is stopped
				}
				if fi, serr := os.Stat(path); serr == nil {
					if f, perr := c.parsed(path, fi); perr == nil {
						return f, &staleExportError{err: err, at: fi.ModTime()}
					}
				}
				return nil, errNoExport
			}
		default:
			call := c.startFlush(dir, coldFlushBudget, true)
			if call == nil || call.wait(ctx) != nil {
				return nil, errNoExport
			}
		}
		if fi, err = os.Stat(path); err != nil || c.exportBehind(dir, fi) {
			return nil, errNoExport
		}
	}
	return c.parsed(path, fi)
}

// parsed returns the export at path as stat'ed in fi, re-parsing only when it changed.
func (c *exportClient) parsed(path string, fi os.FileInfo) (*exportFile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.files[path]; f != nil && f.size == fi.Size() && f.mtime.Equal(fi.ModTime()) {
		return f, nil
	}
	f, err := parseExport(path)
	if err != nil {
		return nil, err
	}
	// A file that changed while it was read may be half-written: use it next time.
	if after, err := os.Stat(path); err != nil || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		return nil, errNoExport
	}
	f.size, f.mtime = fi.Size(), fi.ModTime()
	c.files[path] = f
	return f, nil
}

// staleExportError is a cold Dolt project whose export could not be refreshed:
// Details answers from the export on disk and says so (a partial read, so the
// board shows it degraded), rather than reading every bead through bd show.
type staleExportError struct {
	err error
	at  time.Time
}

func (e *staleExportError) Error() string {
	return fmt.Sprintf("beads export not refreshed (%v): details are from the export of %s", e.err, e.at.Format(time.RFC3339))
}
func (e *staleExportError) Unwrap() error { return e.err }

func (c *exportClient) exportBehind(dir string, export os.FileInfo) bool {
	// Only beads.db, not its -wal: the WAL's mtime runs 10-40s ahead of the export on a
	// busy project (checkpoints, readers), which rejected a good export.
	if d, err := os.Stat(filepath.Join(dir, ".beads", "beads.db")); err == nil && d.ModTime().Sub(export.ModTime()) > exportLagMax {
		return true
	}
	// bd's Dolt journal: every write to a bd database appends to it. After our
	// own flush, any change since the stat taken before it is a write the export
	// may lack (it can have committed while bd export ran, so the export's mtime
	// proves nothing); otherwise a journal newer than the export is behind.
	c.mu.Lock()
	seen, flushed := c.seen[dir]
	c.mu.Unlock()
	for path, now := range doltJournals(dir) {
		if was, ok := seen[path]; flushed && ok {
			if was.size != now.size || !was.mtime.Equal(now.mtime) {
				return true
			}
			continue
		}
		if now.mtime.After(export.ModTime()) {
			return true
		}
	}
	return false
}

// doltJournals stats each Dolt journal of dir.
func doltJournals(dir string) map[string]fileStamp {
	paths, _ := filepath.Glob(filepath.Join(dir, ".beads", "dolt", "*", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"))
	out := map[string]fileStamp{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fileStamp{size: fi.Size(), mtime: fi.ModTime()}
		}
	}
	return out
}

// backgroundFlushBudget bounds a flush no load waits for.
const backgroundFlushBudget = 2 * time.Minute

// coldFlushBudget bounds a flush loads wait for: bd export takes 5s for 5,600
// beads; the rest is room for the br queue under load.
const coldFlushBudget = 90 * time.Second

// flushRetryMax caps the backoff between retries of a failing flush.
const flushRetryMax = 2 * time.Minute

// coldFlush gets a cold Dolt project's export refreshed. The first load waits
// for one flush and every load beside it waits for the same one. Once a flush
// has failed no load waits again: the export is retried in the background with
// backoff and the error is returned at once.
func (c *exportClient) coldFlush(ctx context.Context, dir string) error {
	c.mu.Lock()
	failed := c.failed[dir]
	_, flushed := c.seen[dir]
	c.mu.Unlock()
	if flushed {
		return nil // a flush succeeded since the caller looked
	}
	if failed.n > 0 {
		c.startFlush(dir, backgroundFlushBudget, false)
		return fmt.Errorf("%d failed in a row: %w", failed.n, failed.err)
	}
	call := c.startFlush(dir, coldFlushBudget, false)
	if call == nil {
		return errNoExport
	}
	return call.wait(ctx)
}

// startFlush returns dir's flush in flight, else starts one owned by this
// client (a load that stops waiting does not end it) under budget. It starts
// none while a failing project waits for its retry, after Close, when the
// client cannot flush, or with throttle within flushEvery of the last.
func (c *exportClient) startFlush(dir string, budget time.Duration, throttle bool) *flushCall {
	f, ok := c.Client.(exportFlusher)
	if !ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if call := c.inflight[dir]; call != nil {
		return call
	}
	if c.ctx.Err() != nil || time.Now().Before(c.failed[dir].retryAt) || (throttle && time.Since(c.flushed[dir]) < flushEvery) {
		return nil
	}
	call := &flushCall{done: make(chan struct{})}
	c.inflight[dir] = call
	c.flushed[dir] = time.Now()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, budget)
		defer cancel()
		before := doltJournals(dir)
		err := f.FlushExport(ctx, dir)
		c.mu.Lock()
		if err == nil {
			c.seen[dir] = before
			if n := c.failed[dir].n; n > 0 {
				log.Printf("beads: export of %s refreshed again after %d failed flushes", dir, n)
			}
			delete(c.failed, dir)
		} else {
			fail := c.failed[dir]
			fail.n++
			fail.err = err
			wait := min(flushEvery<<min(fail.n-1, 10), flushRetryMax)
			fail.retryAt = time.Now().Add(wait)
			c.failed[dir] = fail
			log.Printf("beads: export of %s failed (%d in a row), retrying in %v; cold loads answer from the export on disk meanwhile: %v", dir, fail.n, wait, err)
		}
		delete(c.inflight, dir)
		call.err = err
		c.mu.Unlock()
		close(call.done)
	}()
	return call
}

// wait waits for the flush or ctx, whichever ends first.
func (f *flushCall) wait(ctx context.Context) error {
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopBackground ends the flushes of an ExportReads client and waits for them
// (cmdrd shutdown, tests); other clients have none.
func StopBackground(c Client) {
	if e, ok := c.(*exportClient); ok {
		e.stop()
	}
}

func (c *exportClient) stop() {
	c.mu.Lock()
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}

func parseExport(path string) (*exportFile, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, errNoExport
	}
	defer fh.Close()
	f := &exportFile{byID: map[string]*exportIssue{}, parents: map[string]int{}}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var is exportIssue
		if err := json.Unmarshal(sc.Bytes(), &is); err != nil || is.ID == "" {
			return nil, errNoExport
		}
		f.issues = append(f.issues, is)
	}
	if sc.Err() != nil {
		return nil, errNoExport
	}
	for i := range f.issues {
		is := &f.issues[i]
		f.byID[is.ID] = is
		if is.Status == "tombstone" {
			continue
		}
		for _, d := range is.Dependencies {
			f.parents[d.DependsOnID]++
		}
	}
	return f, nil
}

// List answers the label-filtered reads of every status, in br's order
// (priority, then newest first); other filters go to br.
func (c *exportClient) List(ctx context.Context, dir string, opts ListOptions) ([]Bead, error) {
	if opts.Status != "all" || opts.Type != "" || opts.Limit > 0 || len(opts.IDs) > 0 {
		return c.Client.List(ctx, dir, opts)
	}
	f, err := c.load(ctx, dir)
	if err != nil {
		return c.Client.List(ctx, dir, opts)
	}
	var out []exportIssue
	for _, is := range f.issues {
		if is.Status != "tombstone" && hasAll(is.Labels, opts.Labels) && hasAny(is.Labels, opts.LabelAny) {
			out = append(out, is)
		}
	}
	slices.SortStableFunc(out, func(a, b exportIssue) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		switch {
		case a.CreatedAt > b.CreatedAt:
			return -1
		case a.CreatedAt < b.CreatedAt:
			return 1
		}
		return 0
	})
	beads := make([]Bead, 0, len(out))
	for _, is := range out {
		beads = append(beads, Bead{
			ID: is.ID, Title: is.Title, Description: is.Description, AcceptanceCriteria: is.Acceptance, Status: is.Status, CloseReason: is.CloseReason, Labels: slices.Clone(is.Labels), Priority: is.Priority,
			CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt,
			DependencyCount: len(is.Dependencies), DependentCount: f.parents[is.ID],
		})
	}
	return beads, nil
}

func hasAll(labels, want []string) bool {
	for _, w := range want {
		if !slices.Contains(labels, w) {
			return false
		}
	}
	return true
}

func hasAny(labels, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if slices.Contains(labels, w) {
			return true
		}
	}
	return false
}

func (c *exportClient) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	f, err := c.load(ctx, dir)
	var stale *staleExportError
	if errors.As(err, &stale) {
		// The beads the old export lacks stay unread: reading them through bd
		// show is the storm this avoids. The partial read flags the project.
		out, missing := f.details(ids)
		return out, &PartialDetailsError{Missing: missing, Err: err}
	}
	if err != nil {
		return c.Client.Details(ctx, dir, ids)
	}
	out, _ := f.details(ids)
	c.seedCache(ctx, dir, f)
	return out, nil
}

// seedCache offers f to an empty detail cache under this client (il9.202.29.5).
// A cold load answers from the export alone and left the cache empty, so once
// the project was quiet past maxExportAge, the first write's load (warm branch:
// answer from bd) made the cache's first read and sent every bead to bd show
// (6 min for 5,600 beads). Tried once per export file, and only on a Dolt
// project this client flushed, where the journals say whether the database
// has moved past the export.
func (c *exportClient) seedCache(ctx context.Context, dir string, f *exportFile) {
	inner, ok := c.Client.(*CachedClient)
	if !ok || inner.hasDetails(dir) {
		return
	}
	c.mu.Lock()
	seen := c.seen[dir]
	tried := c.seeded[dir].Equal(f.mtime)
	c.seeded[dir] = f.mtime
	c.mu.Unlock()
	if len(seen) == 0 || tried {
		return
	}
	inner.seedFromExport(ctx, dir, f, seen)
}

// details answers ids from the export, listing those it lacks.
func (f *exportFile) details(ids []string) (map[string]Detail, []string) {
	out := make(map[string]Detail, len(ids))
	var missing []string
	for _, id := range ids {
		if d, ok := f.detail(id); ok {
			out[id] = d
		} else {
			missing = append(missing, id)
		}
	}
	return out, missing
}

// detail is one bead's Detail from the export; false when it lacks the bead.
func (f *exportFile) detail(id string) (Detail, bool) {
	is := f.byID[id]
	if is == nil || is.Status == "tombstone" {
		return Detail{}, false
	}
	d := Detail{Deps: slices.Clone(is.Dependencies), Comments: slices.Clone(is.Comments)}
	for _, dep := range is.Dependencies {
		if dep.Type == "parent-child" {
			d.Parent = dep.DependsOnID
			break
		}
	}
	return d, true
}

func (c *exportClient) Deps(ctx context.Context, dir, id string) ([]Dep, error) {
	f, err := c.load(ctx, dir)
	if err != nil {
		return c.Client.Deps(ctx, dir, id)
	}
	if is := f.byID[id]; is != nil {
		return slices.Clone(is.Dependencies), nil
	}
	return c.Client.Deps(ctx, dir, id)
}

func (c *exportClient) Comments(ctx context.Context, dir, id string) ([]Comment, error) {
	f, err := c.load(ctx, dir)
	if err != nil {
		return c.Client.Comments(ctx, dir, id)
	}
	if is := f.byID[id]; is != nil {
		return slices.Clone(is.Comments), nil
	}
	return c.Client.Comments(ctx, dir, id)
}
