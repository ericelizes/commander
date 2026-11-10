package beads

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// CachedClient answers List, Deps, Details, Show and Comments from memory until the
// project's beads files change. cmdrd rebuilds its snapshot every 3s, and each
// rebuild shelled out to br for every project and step: ~3 br processes a
// second on one SQLite lock, all day, mostly to re-read what had not changed.
// Any write moves at least one of these files. Some br builds (0.1.44 on
// Linux) also rewrite beads.db's header on a plain read, so beads.db is
// compared by content, not mtime (dbContent).
type CachedClient struct {
	Client

	mu    sync.Mutex
	stamp map[string]string         // projectDir -> fingerprint the entries were read at
	hits  map[string]map[string]any // projectDir -> call key -> result

	// details is the per-bead Detail of display reads, keyed by what br list
	// showed of the bead when it was read (see detailsSalvaging). It outlives
	// fingerprint changes and writes: a changed bead changes its own key.
	details map[string]map[string]detailEntry
	// reread is one slot per project: one incremental read at a time (see acquireReread).
	reread map[string]chan struct{}
	// flight is each cache miss being loaded now (see cached).
	flight map[string]*flight
}

// NewCached wraps c. Writes go straight through (and drop that project's cache).
func NewCached(c Client) *CachedClient {
	return &CachedClient{Client: c, stamp: map[string]string{}, hits: map[string]map[string]any{}}
}

var beadsFiles = []string{"beads.db", "beads.db-wal", "last-touched", "issues.jsonl"}

// fingerprint is the size and mtime of each beads file (beads.db: its
// content, see dbContent); "" when unreadable, which never matches, so
// nothing is served stale.
//
// On a Dolt project (a journal under .beads/dolt) it is the journals alone:
// every bd write appends to one, and the SQLite files there are leftovers
// nobody writes. issues.jsonl moves on every export flush (ours included), so
// counting it made a busy project's own flush look like a write: the cache
// dropped and ReadConsistent re-read the whole project, up to 3 times a load.
func fingerprint(projectDir string) string {
	if PostgresConfigured() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s, e := postgresStore()
		if e != nil {
			return ""
		}
		pid, e := projectID(ctx, s.DB(), projectDir)
		if e != nil {
			return ""
		}
		var revision int64
		if e = s.DB().QueryRowContext(ctx, `SELECT state_revision FROM projects WHERE id=?`, pid).Scan(&revision); e != nil {
			return ""
		}
		return fmt.Sprint(revision)
	}
	journals, err := filepath.Glob(filepath.Join(projectDir, ".beads", "dolt", "*", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"))
	if err != nil {
		return ""
	}
	var b strings.Builder
	if len(journals) == 0 {
		for _, name := range beadsFiles {
			path := filepath.Join(projectDir, ".beads", name)
			fi, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					b.WriteString("-;")
					continue
				}
				return ""
			}
			if name == "beads.db" {
				sum, err := dbContent(path, fi)
				if err != nil {
					return ""
				}
				fmt.Fprintf(&b, "%d:%x;", fi.Size(), sum)
				continue
			}
			fmt.Fprintf(&b, "%d:%d;", fi.Size(), fi.ModTime().UnixNano())
		}
	}
	for _, path := range journals {
		fi, err := os.Stat(path)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&b, "%s:%d:%d;", path, fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String()
}

type cacheEntry[T any] struct {
	value T
	at    time.Time
}

var (
	dbSumMu sync.Mutex
	dbSums  = map[string]dbSum{} // path -> last hash, keyed by the stat it was taken at
)

type dbSum struct {
	size, mtime int64
	sum         uint64
}

// dbContent hashes a SQLite file without its header's file change counter
// (bytes 24-27) and version-valid-for (92-95): a br read that only bumps
// them changed nothing. Rehashed only when size or mtime moved.
func dbContent(path string, fi os.FileInfo) (uint64, error) {
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	dbSumMu.Lock()
	if s, ok := dbSums[path]; ok && s.size == size && s.mtime == mtime {
		dbSumMu.Unlock()
		return s.sum, nil
	}
	dbSumMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(raw) >= 100 {
		clear(raw[24:28])
		clear(raw[92:96])
	}
	h := fnv.New64a()
	h.Write(raw)
	sum := h.Sum64()
	dbSumMu.Lock()
	dbSums[path] = dbSum{size: size, mtime: mtime, sum: sum}
	dbSumMu.Unlock()
	return sum, nil
}

// cached returns the result for key, calling load on a miss.
func cached[T any](c *CachedClient, ctx context.Context, dir, key string, load func() (T, error)) (T, error) {
	fp := fingerprint(dir)
	c.mu.Lock()
	if fp != "" && c.stamp[dir] == fp {
		if v, ok := c.hits[dir][key]; ok {
			entry := v.(cacheEntry[T])
			if !strings.HasPrefix(key, "details:") || time.Since(entry.at) <= maxExportAge {
				c.mu.Unlock()
				return entry.value, nil
			}
		}
	}
	// Loads that miss at the same fingerprint share one call: the poll, the
	// health loop and a settle each launched their own bd list of a busy project.
	flightKey := dir + "\x00" + fp + "\x00" + key
	if fp != "" {
		if f, ok := c.flight[flightKey]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
				// The caller that ran it may have been cancelled, or may have
				// panicked before it had a result; this one was not, so it reads
				// on its own.
				if f.finished && !f.cancelled {
					return f.value.(T), f.err
				}
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			}
			return cached(c, ctx, dir, key, load)
		}
		f := &flight{done: make(chan struct{})}
		if c.flight == nil {
			c.flight = map[string]*flight{}
		}
		c.flight[flightKey] = f
		defer func() {
			c.mu.Lock()
			delete(c.flight, flightKey)
			c.mu.Unlock()
			close(f.done)
		}()
		c.mu.Unlock()
		v, err := cachedLoad(c, dir, key, fp, load)
		f.value, f.err, f.finished, f.cancelled = v, err, true, ctx.Err() != nil
		return v, err
	}
	c.mu.Unlock()
	return cachedLoad(c, dir, key, fp, load)
}

// flight is one load in progress, shared by the callers that missed with it.
type flight struct {
	done  chan struct{}
	value any
	err   error
	// finished is false when the leader panicked, cancelled when its own
	// context ended: followers then load for themselves.
	finished, cancelled bool
}

// cachedLoad runs load and stores its result when the files stood still.
func cachedLoad[T any](c *CachedClient, dir, key, fp string, load func() (T, error)) (T, error) {
	v, err := load()
	// Readers share this cache: one that started before a write would, on
	// return, re-stamp the old state and drop what a later reader cached.
	if err != nil || fp == "" || fingerprint(dir) != fp {
		return v, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stamp[dir] != fp {
		c.stamp[dir] = fp
		c.hits[dir] = map[string]any{}
	}
	at := time.Now()
	if strings.HasPrefix(key, "details:") {
		if fi, err := os.Stat(filepath.Join(dir, ".beads", "issues.jsonl")); err == nil && fi.ModTime().Before(at) && at.Sub(fi.ModTime()) <= maxExportAge {
			at = fi.ModTime()
		}
	}
	c.hits[dir][key] = cacheEntry[T]{value: v, at: at}
	return v, nil
}

func (c *CachedClient) forget(dir string) {
	c.mu.Lock()
	delete(c.stamp, dir)
	delete(c.hits, dir)
	c.mu.Unlock()
}

// Callers append to and edit what they get back, so each gets its own copy.

func (c *CachedClient) List(ctx context.Context, dir string, opts ListOptions) ([]Bead, error) {
	if PostgresConfigured() {
		return c.Client.List(ctx, dir, opts)
	}
	if labelsOnly(opts) {
		// One `bd list --status all` answers every label filter of all statuses
		// (same beads, same order: bd filters after it sorts). The board asks for
		// three, and each was a 1-3s bd call on a busy project, every load.
		all, err := c.List(ctx, dir, ListOptions{Status: "all"})
		if err != nil {
			return nil, err
		}
		return slices.DeleteFunc(all, func(b Bead) bool { return !hasAll(b.Labels, opts.Labels) || !hasAny(b.Labels, opts.LabelAny) }), nil
	}
	key := fmt.Sprintf("list:%q:%q:%s:%s:%d:%q", opts.Labels, opts.LabelAny, opts.Status, opts.Type, opts.Limit, opts.IDs)
	v, err := cached(c, ctx, dir, key, func() ([]Bead, error) { return c.Client.List(ctx, dir, opts) })
	return cloneBeads(v), err
}

// labelsOnly is a list of every status filtered by labels alone.
func labelsOnly(opts ListOptions) bool {
	return opts.Status == "all" && opts.Type == "" && opts.Limit == 0 && len(opts.IDs) == 0 && (len(opts.Labels) > 0 || len(opts.LabelAny) > 0)
}

// cloneBeads copies the list and each bead's labels: callers edit what they get.
func cloneBeads(v []Bead) []Bead {
	out := slices.Clone(v)
	for i := range out {
		out[i].Labels = slices.Clone(out[i].Labels)
	}
	return out
}

func (c *CachedClient) Deps(ctx context.Context, dir, id string) ([]Dep, error) {
	if PostgresConfigured() {
		return c.Client.Deps(ctx, dir, id)
	}
	v, err := cached(c, ctx, dir, "deps:"+id, func() ([]Dep, error) { return c.Client.Deps(ctx, dir, id) })
	return slices.Clone(v), err
}

func (c *CachedClient) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	if PostgresConfigured() {
		return c.Client.Details(ctx, dir, ids)
	}
	key := "details:" + strings.Join(ids, ",")
	v, err := cached(c, ctx, dir, key, func() (map[string]Detail, error) { return c.detailsSalvaging(ctx, dir, ids) })
	if v == nil {
		return nil, err
	}
	out := make(map[string]Detail, len(v))
	for id, d := range v {
		out[id] = Detail{Parent: d.Parent, Deps: slices.Clone(d.Deps), Comments: slices.Clone(d.Comments)}
	}
	return out, err
}

// detailKey includes the bead's own edges: bd can swap blockers without moving
// updated_at or either count. Sort a copy so backend edge order is immaterial.
func detailKey(b Bead) string {
	edges := make([]string, 0, len(b.Dependencies))
	for _, d := range b.Dependencies {
		edges = append(edges, fmt.Sprintf("%q:%q:%q", d.IssueID, d.DependsOnID, d.Type))
	}
	slices.Sort(edges)
	return fmt.Sprintf("%s|%s|%s|%d|%d|%d|%s|%s", b.Parent, b.UpdatedAt, b.Status, b.DependencyCount, b.DependentCount, b.CommentCount, strings.Join(b.Labels, ","), strings.Join(edges, ","))
}

type detailEntry struct {
	key    string
	detail Detail
	// export is the mtime of the issues.jsonl the Detail may have come from (a
	// cold read accepts an export up to maxExportAge old); zero for br show.
	// Such an entry is trusted only while that file is unchanged, so display lag
	// stays bounded; a newer export replaces it (see detailsIncremental).
	export time.Time
	// owesShow marks a first read made past maxExportAge: it came from br show, so
	// the next export may be older than it and must not replace it, but a change
	// its list key cannot see (bd leaves updated_at alone on an edge swap) is only
	// learned by reading again. Once that file moves it goes to br show, not the file.
	owesShow bool
}

func exportMTime(dir string) time.Time {
	if fi, err := os.Stat(filepath.Join(dir, ".beads", "issues.jsonl")); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// detailsSalvaging answers a display read incrementally. It keeps each bead's
// Detail with the list key it was read at; one `br list` finds the beads whose
// key moved (or that are new) and only those go to br show. Beads that left
// the list are dropped. A failed batch keeps the previous Detail of its beads
// (key unchanged, so the next read retries) and the read reports a
// PartialDetailsError. The first read of a project is the plain full read.
func (c *CachedClient) detailsSalvaging(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return c.detailsIncremental(ctx, dir, ids, false)
}

// detailsIncremental is detailsSalvaging; noExport never serves or stores an
// export-sourced Detail (the first read is DB-backed too), for decisions that
// mail people: it sends every export-sourced entry to br show, once, and they are
// DB-backed for all readers from then on.
//
// An export-sourced entry whose list key did not move but whose export did takes
// its Detail from the newer file, under the first read's age limit, instead of br
// show: for all of them that is a full re-read (72 batches, 1m42s). A bead the file
// cannot answer for, a file past the limit, or an entry that owes a show goes to br
// show.
func (c *CachedClient) detailsIncremental(ctx context.Context, dir string, ids []string, noExport bool) (map[string]Detail, error) {
	if PostgresConfigured() {
		return c.Client.Details(ctx, dir, ids)
	}
	start := time.Now()
	caller := readCaller(ctx, noExport)
	trace := &detailTrace{}
	ctx = context.WithValue(ctx, detailTraceKey{}, trace)
	release, err := c.acquireReread(ctx, dir)
	waited := time.Since(start)
	if err != nil {
		log.Printf("beads: details caller=%s project=%s requested=%d noExport=%t wait=%s cancelled=%v", caller, dir, len(ids), noExport, waited.Round(time.Millisecond), err)
		return nil, err
	}
	acquired := time.Now()
	staleCount := 0
	reason := "warm"
	defer func() {
		release()
		held := time.Since(acquired)
		if waited >= time.Second || held >= time.Second || noExport || err != nil {
			log.Printf("beads: details caller=%s project=%s requested=%d stale=%d show_batches=%d reason=%s noExport=%t wait=%s hold=%s err=%v", caller, dir, len(ids), staleCount, trace.batches.Load(), reason, noExport, waited.Round(time.Millisecond), held.Round(time.Millisecond), err)
		}
	}()

	c.mu.Lock()
	store := c.details[dir]
	c.mu.Unlock()

	var keys map[string]string // nil when the list could not be read
	if all, err := c.List(ctx, dir, ListOptions{Status: "all"}); err == nil {
		keys = make(map[string]string, len(all))
		for _, b := range all {
			keys[b.ID] = detailKey(b)
		}
	}
	first := len(store) == 0
	if first {
		reason = "cold"
	}
	exportNow := exportMTime(dir)

	out := make(map[string]Detail, len(ids))
	var stale, moved []string
	for _, id := range ids {
		e, ok := store[id]
		switch {
		case !ok || keys == nil || keys[id] != e.key:
			stale = append(stale, id)
		case e.export.IsZero() || (!noExport && e.export.Equal(exportNow)):
			out[id] = e.detail
		case noExport || e.owesShow:
			stale = append(stale, id)
		default:
			moved = append(moved, id)
		}
	}
	var retaken map[string]Detail
	if len(moved) > 0 {
		retaken = exportDetails(dir, moved)
		for _, id := range moved {
			if d, ok := retaken[id]; ok {
				out[id] = d
			} else {
				stale = append(stale, id)
			}
		}
	}

	staleCount = len(stale)
	var got map[string]Detail
	var usedExport time.Time
	var owesShow bool
	if len(stale) > 0 {
		if first && !noExport {
			usedExport = exportNow
			// Past the limit the cold read goes to br show (exportDetails).
			owesShow = time.Since(exportNow) > maxExportAge
			got, err = c.Client.Details(ctx, dir, stale)
		} else {
			got, err = ChangedDetails(ctx, c.Client, dir, stale)
		}
	}
	var partial *PartialDetailsError
	if err != nil && !errors.As(err, &partial) {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.details == nil {
		c.details = map[string]map[string]detailEntry{}
	}
	next := map[string]detailEntry{}
	for id, e := range c.details[dir] {
		if _, listed := keys[id]; listed || keys == nil {
			next[id] = e
		}
	}
	for id, d := range retaken {
		next[id] = detailEntry{key: keys[id], detail: d, export: exportNow}
	}
	for id, d := range got {
		out[id] = d
		next[id] = detailEntry{key: keys[id], detail: d, export: usedExport, owesShow: owesShow}
	}
	if partial != nil {
		for _, id := range partial.Missing {
			if e, ok := store[id]; ok {
				out[id] = e.detail
			}
		}
	}
	c.details[dir] = next
	return out, err
}

// acquireReread queues a read behind the one running for dir. Overlapping loads
// (the poll, a settle, the 10s health loop) then share one re-read: the first does
// it, each later one finds the result in the per-bead store and goes to br show
// only for what still moved. No read is handed to another's result or context, so
// a caller's cancel ends its own read and the next in line reads on its own, and a
// write made while one read ran is still seen by the read that waited.
func (c *CachedClient) acquireReread(ctx context.Context, dir string) (func(), error) {
	c.mu.Lock()
	if c.reread == nil {
		c.reread = map[string]chan struct{}{}
	}
	slot, ok := c.reread[dir]
	if !ok {
		slot = make(chan struct{}, 1)
		c.reread[dir] = slot
	}
	c.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// tryAcquireReread takes dir's reread slot only when it is free.
func (c *CachedClient) tryAcquireReread(dir string) (func(), bool) {
	c.mu.Lock()
	if c.reread == nil {
		c.reread = map[string]chan struct{}{}
	}
	slot, ok := c.reread[dir]
	if !ok {
		slot = make(chan struct{}, 1)
		c.reread[dir] = slot
	}
	c.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, true
	default:
		return nil, false
	}
}

// seedFromExport offers f, an export holding at least the state of the Dolt
// journals seen (stat'ed before the flush that wrote it), to dir's detail cache.
// Each bead is keyed by one bd list, read only while the journals and the file
// still stand where they were: then the list, the export and the database are
// one state. An empty cache is filled with export-sourced entries (export = f's
// mtime), so the rules for those hold: trusted while that file stands, re-taken
// from a newer one under maxExportAge, else shown. In a filled cache an
// export-sourced entry from an older file whose list key did not move takes its
// Detail and stamp from f; an entry read through br show (no export stamp, or
// owing a show) is never replaced. A write anywhere in the meantime changes
// nothing. It returns false when another read holds the cache (not waited for),
// so the caller offers f again later.
func (c *CachedClient) seedFromExport(ctx context.Context, dir string, f *exportFile, seen map[string]fileStamp) bool {
	release, ok := c.tryAcquireReread(dir)
	if !ok {
		return false
	}
	defer release()
	export := filepath.Join(dir, ".beads", "issues.jsonl")
	standing := func() bool {
		fi, err := os.Stat(export)
		return err == nil && fi.Size() == f.size && fi.ModTime().Equal(f.mtime) &&
			maps.EqualFunc(doltJournals(dir), seen, func(a, b fileStamp) bool { return a.size == b.size && a.mtime.Equal(b.mtime) })
	}
	if !standing() {
		return true
	}
	all, err := c.List(ctx, dir, ListOptions{Status: "all"})
	if err != nil || !standing() {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.details == nil {
		c.details = map[string]map[string]detailEntry{}
	}
	store := c.details[dir]
	if len(store) == 0 {
		store = make(map[string]detailEntry, len(all))
		for _, b := range all {
			if d, ok := f.detail(b.ID); ok {
				store[b.ID] = detailEntry{key: detailKey(b), detail: d, export: f.mtime}
			}
		}
		c.details[dir] = store
		return true
	}
	// A new map: a read holding an earlier copy of it reads on unlocked.
	store = maps.Clone(store)
	c.details[dir] = store
	for _, b := range all {
		e, ok := store[b.ID]
		if !ok || e.export.IsZero() || e.owesShow || !e.export.Before(f.mtime) || e.key != detailKey(b) {
			continue
		}
		if d, ok := f.detail(b.ID); ok {
			store[b.ID] = detailEntry{key: e.key, detail: d, export: f.mtime}
		}
	}
	return true
}

// Incremental reads Details through c's shared per-bead store like a display
// read (only beads whose list key moved go to br show) but never from an export,
// so a cold first read is DB-backed. Action verbs keep FreshDetails/ForActions.
func Incremental(c *CachedClient) Client { return incrementalClient{c} }

// IsolatedIncremental keeps a persistent DB-backed detail cache and reread slot
// separate from display hydration. Construct once, not once per pass. The backing
// client still shares the process/cross-process DB limiter and write priority.
// Action reads continue to bypass both caches through FreshDetails/ForActions.
func IsolatedIncremental(c *CachedClient) Client {
	return Incremental(NewCached(c.Client))
}

type incrementalClient struct{ *CachedClient }

func (c incrementalClient) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return c.detailsIncremental(ctx, dir, ids, true)
}

func (c *CachedClient) Show(ctx context.Context, dir, id string) (*Bead, error) {
	if PostgresConfigured() {
		return c.Client.Show(ctx, dir, id)
	}
	v, err := cached(c, ctx, dir, "show:"+id, func() (*Bead, error) { return c.Client.Show(ctx, dir, id) })
	if v == nil {
		return nil, err
	}
	b := *v
	return &b, err
}

func (c *CachedClient) Comments(ctx context.Context, dir, id string) ([]Comment, error) {
	if PostgresConfigured() {
		return c.Client.Comments(ctx, dir, id)
	}
	v, err := cached(c, ctx, dir, "comments:"+id, func() ([]Comment, error) { return c.Client.Comments(ctx, dir, id) })
	return slices.Clone(v), err
}

func (c *CachedClient) SetState(ctx context.Context, dir, id, dimension, value, reason string) error {
	defer c.forget(dir)
	return c.Client.SetState(ctx, dir, id, dimension, value, reason)
}

func (c *CachedClient) Create(ctx context.Context, dir, title string, opts CreateOptions) (string, error) {
	defer c.forget(dir)
	return c.Client.Create(ctx, dir, title, opts)
}

func (c *CachedClient) Link(ctx context.Context, dir, fromID, toID, linkType string) error {
	defer c.forget(dir)
	return c.Client.Link(ctx, dir, fromID, toID, linkType)
}

func (c *CachedClient) Rename(ctx context.Context, dir, id, title string) error {
	defer c.forget(dir)
	return c.Client.Rename(ctx, dir, id, title)
}

func (c *CachedClient) Close(ctx context.Context, dir, id string) error {
	defer c.forget(dir)
	return c.Client.Close(ctx, dir, id)
}

func (c *CachedClient) Delete(ctx context.Context, dir, id, reason string) error {
	defer c.forget(dir)
	return c.Client.Delete(ctx, dir, id, reason)
}

func (c *CachedClient) SetStatus(ctx context.Context, dir, id, status string) error {
	defer c.forget(dir)
	return c.Client.SetStatus(ctx, dir, id, status)
}

func (c *CachedClient) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	defer c.forget(dir)
	return c.Client.SetLabel(ctx, dir, id, label, on)
}

func (c *CachedClient) SetParent(ctx context.Context, dir, id, parent string) error {
	defer c.forget(dir)
	return c.Client.SetParent(ctx, dir, id, parent)
}

func (c *CachedClient) DependOn(ctx context.Context, dir, id, dependsOn string) error {
	defer c.forget(dir)
	return c.Client.DependOn(ctx, dir, id, dependsOn)
}

func (c *CachedClient) RemoveDep(ctx context.Context, dir, id, dependsOn string) error {
	defer c.forget(dir)
	return c.Client.RemoveDep(ctx, dir, id, dependsOn)
}

func (c *CachedClient) UpdateFields(ctx context.Context, dir, id string, f Fields) error {
	defer c.forget(dir)
	return c.Client.UpdateFields(ctx, dir, id, f)
}

func (c *CachedClient) CloseWithReason(ctx context.Context, dir, id, reason string) error {
	defer c.forget(dir)
	return c.Client.CloseWithReason(ctx, dir, id, reason)
}

func (c *CachedClient) AddComment(ctx context.Context, dir, id, text string) error {
	defer c.forget(dir)
	return c.Client.AddComment(ctx, dir, id, text)
}

// ErrChangedDuringRead means the beads files kept moving through every attempt
// of ReadConsistent: a busy writer, not a failed read.
var ErrChangedDuringRead = errors.New("beads changed during List/Details read")

// ReadRetryBackoff is the jittered pause (200-500ms) before re-reading a project
// whose files moved mid-read; a var so tests can shorten it.
var ReadRetryBackoff = func() time.Duration {
	return 200*time.Millisecond + time.Duration(rand.Int64N(int64(300*time.Millisecond)))
}

// ReadConsistent retries the entire List/Details read if any beads file moved
// between those calls. Individual cache hits alone cannot guarantee that pair.
// When every attempt overlaps a write it returns ErrChangedDuringRead.
func ReadConsistent(ctx context.Context, dir string, read func() error) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(ReadRetryBackoff()):
			}
		}
		before := fingerprint(dir)
		err := read()
		if before != "" && before == fingerprint(dir) {
			return err
		}
	}
	return fmt.Errorf("beads in %s: %w", dir, ErrChangedDuringRead)
}
