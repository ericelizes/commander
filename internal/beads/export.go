package beads

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return &exportClient{ctx: ctx, cancel: cancel, Client: c, files: map[string]*exportFile{}, flushed: map[string]time.Time{}, flushing: map[string]bool{}, seen: map[string]map[string]fileStamp{}}
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
	// flushing marks a background flush running for the project (Dolt only).
	flushing map[string]bool
	// seen is each project's Dolt journals as stat'ed right before this
	// client's last successful flush: the export holds at least that state.
	seen map[string]map[string]fileStamp
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
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errNoExport
	}
	if c.exportBehind(dir, fi) {
		c.mu.Lock()
		_, warm := c.seen[dir]
		c.mu.Unlock()
		if len(doltJournals(dir)) > 0 && warm {
			// bd export of a big Dolt project takes seconds (5s for 5,600 beads),
			// and on a busy one a write lands meanwhile, so the export it makes is
			// behind again. Waiting for it made every load pay the export and
			// then read bd anyway. Refresh it in the background for later loads
			// and answer this one from bd now. Not on the first load (a cmdrd
			// start): the detail cache is cold too, and with no fresh export it
			// reads every bead through bd show (6 min for 5,600 beads,
			// il9.202.29), so that one waits for the export below.
			c.flushInBackground(dir)
			return nil, errNoExport
		}
		if !c.flush(ctx, dir) {
			return nil, errNoExport
		}
		if fi, err = os.Stat(path); err != nil || c.exportBehind(dir, fi) {
			return nil, errNoExport
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.files[dir]; f != nil && f.size == fi.Size() && f.mtime.Equal(fi.ModTime()) {
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
	c.files[dir] = f
	return f, nil
}

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

// flushInBackground starts flush in its own goroutine unless one runs for dir.
// It is still at most one per flushEvery (flush checks).
func (c *exportClient) flushInBackground(dir string) {
	c.mu.Lock()
	if c.flushing[dir] || time.Since(c.flushed[dir]) < flushEvery {
		c.mu.Unlock()
		return
	}
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.flushing[dir] = true
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.flushing, dir)
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(c.ctx, backgroundFlushBudget)
		defer cancel()
		c.flush(ctx, dir)
	}()
}

// StopBackground ends the background flushes of an ExportReads client and waits
// for them (cmdrd shutdown, tests); other clients have none.
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

// flush asks br to rewrite dir's export, at most once per flushEvery.
func (c *exportClient) flush(ctx context.Context, dir string) bool {
	f, ok := c.Client.(exportFlusher)
	if !ok {
		return false
	}
	c.mu.Lock()
	if time.Since(c.flushed[dir]) < flushEvery {
		c.mu.Unlock()
		return false
	}
	c.flushed[dir] = time.Now()
	c.mu.Unlock()
	before := doltJournals(dir)
	if f.FlushExport(ctx, dir) != nil {
		return false
	}
	c.mu.Lock()
	c.seen[dir] = before
	c.mu.Unlock()
	return true
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
	if err != nil {
		return c.Client.Details(ctx, dir, ids)
	}
	out := make(map[string]Detail, len(ids))
	for _, id := range ids {
		is := f.byID[id]
		if is == nil || is.Status == "tombstone" {
			continue
		}
		d := Detail{Deps: slices.Clone(is.Dependencies), Comments: slices.Clone(is.Comments)}
		for _, dep := range is.Dependencies {
			if dep.Type == "parent-child" {
				d.Parent = dep.DependsOnID
				break
			}
		}
		out[id] = d
	}
	return out, nil
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
