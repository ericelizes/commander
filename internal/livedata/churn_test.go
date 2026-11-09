package livedata

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// churnBD is a bd on a Dolt server as cmdrd meets it on a busy project: every
// read costs time, an export costs more, and each write moves the Dolt journal.
// One unit of `lat` is one tenth of the measured cost on a 5,600-bead project
// (bd list --all 1.3s idle, bd export 5s, br show ~0.25s a bead).
type churnBD struct {
	*beads.MemClient
	dir                   string
	list, show, export    time.Duration
	mu                    sync.Mutex
	updated               map[string]int
	lists, shows, flushes atomic.Int64
	listTime, flushTime   atomic.Int64
}

func (c *churnBD) stamp(bs []beads.Bead) []beads.Bead {
	c.mu.Lock()
	defer c.mu.Unlock()
	edges := map[string]int{}
	dependents := map[string]int{}
	for _, e := range c.MemClient.Edges {
		edges[e.IssueID]++
		dependents[e.DependsOnID]++
	}
	for i := range bs {
		bs[i].UpdatedAt = strconv.Itoa(c.updated[bs[i].ID])
		bs[i].DependencyCount = edges[bs[i].ID]
		bs[i].DependentCount = dependents[bs[i].ID]
	}
	return bs
}

func (c *churnBD) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	c.lists.Add(1)
	start := time.Now()
	defer func() { c.listTime.Add(int64(time.Since(start))) }()
	got, err := c.MemClient.List(ctx, dir, opts) // the state when bd took its snapshot
	time.Sleep(c.list)
	return c.stamp(got), err
}

func (c *churnBD) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c.shows.Add(int64(len(ids)))
	got, err := c.MemClient.Details(ctx, dir, ids)
	time.Sleep(time.Duration(len(ids)) * c.show)
	return got, err
}

func (c *churnBD) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	c.shows.Add(1)
	time.Sleep(c.show)
	return c.MemClient.Show(ctx, dir, id)
}

// FlushExport writes the state at its start, after the export's cost.
func (c *churnBD) FlushExport(ctx context.Context, dir string) error {
	c.flushes.Add(1)
	start := time.Now()
	defer func() { c.flushTime.Add(int64(time.Since(start))) }()
	data := c.exportBytes(ctx)
	time.Sleep(c.export)
	tmp := filepath.Join(dir, ".beads", "issues.jsonl.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ".beads", "issues.jsonl"))
}

func (c *churnBD) exportBytes(ctx context.Context) []byte {
	all, _ := c.MemClient.List(ctx, c.dir, beads.ListOptions{Status: "all"})
	all = c.stamp(all)
	ids := make([]string, len(all))
	for i, b := range all {
		ids[i] = b.ID
	}
	details, _ := c.MemClient.Details(ctx, c.dir, ids)
	var out []byte
	for _, b := range all {
		line, _ := json.Marshal(map[string]any{
			"id": b.ID, "title": b.Title, "status": b.Status, "priority": b.Priority, "labels": b.Labels,
			"created_at": b.CreatedAt, "updated_at": b.UpdatedAt, "dependencies": details[b.ID].Deps, "comments": details[b.ID].Comments,
		})
		out = append(append(out, line...), '\n')
	}
	return out
}

// write changes one bead and moves the Dolt journal, as a bd commit does.
func (c *churnBD) write(journal string, change func()) {
	change()
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_, _ = f.WriteString("+commit")
		_ = f.Close()
	}
}

type churnResult struct {
	loads, kept     int
	load            []time.Duration
	visible         []time.Duration
	unseen          int
	lists, shows    int64
	flushes         int64
	listTime, flush time.Duration
}

func churnPct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

func (r churnResult) String() string {
	return fmt.Sprintf("loads=%d kept-last-good=%d load p50=%v p90=%v max=%v | close visible p50=%v p90=%v max=%v (n=%d, never seen=%d) | bd lists=%d (%v) shows=%d flushes=%d (%v)",
		r.loads, r.kept, churnPct(r.load, .5).Round(time.Millisecond), churnPct(r.load, .9).Round(time.Millisecond), churnPct(r.load, 1).Round(time.Millisecond),
		churnPct(r.visible, .5).Round(time.Millisecond), churnPct(r.visible, .9).Round(time.Millisecond), churnPct(r.visible, 1).Round(time.Millisecond), len(r.visible), r.unseen,
		r.lists, r.listTime.Round(time.Millisecond), r.shows, r.flushes, r.flush.Round(time.Millisecond))
}

// runChurn seeds a synthetic project (workstreams x steps), starts a writer that
// changes one bead every writeEvery (every other write closes a step), and two
// loaders (the API poll and the health loop) reading through the client stack
// cmdrd uses, for d. It reports load times and how long each close took to show.
func runChurn(t *testing.T, workstreams, steps int, lat, writeEvery, d time.Duration) churnResult {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	journal := filepath.Join(dir, ".beads", "dolt", "example", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "ex"}
	var open []string
	for w := 0; w < workstreams; w++ {
		ws, _ := mem.Create(ctx, dir, fmt.Sprintf("Workstream %d", w), beads.CreateOptions{Labels: []string{"kind:objective"}})
		for s := 0; s < steps; s++ {
			id, _ := mem.Create(ctx, dir, fmt.Sprintf("Step %d.%d", w, s), beads.CreateOptions{Parent: ws, Labels: []string{"kind:objective"}})
			open = append(open, id)
		}
	}
	bd := &churnBD{MemClient: mem, dir: dir, updated: map[string]int{}}
	if err := os.WriteFile(journal, []byte("journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(journal, old, old)
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), bd.exportBytes(ctx), 0o600); err != nil {
		t.Fatal(err)
	}
	project := store.Project{ID: 1, Name: "example", Path: dir}
	cached := beads.NewCached(bd)
	client := beads.ExportReads(cached)
	cache := &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}
	// A cmdrd that has run a while holds every bead's Detail already: warm the
	// per-bead store and the rows at no cost, then charge the live costs.
	all, _ := mem.List(ctx, dir, beads.ListOptions{Status: "all"})
	ids := make([]string, len(all))
	for i, b := range all {
		ids[i] = b.ID
	}
	if _, err := beads.Incremental(cached).Details(ctx, dir, ids); err != nil {
		t.Fatalf("warm details: %v", err)
	}
	if _, err := objectiveRowsFromBeadsCached(ctx, client, []store.Project{project}, cache); err != nil {
		t.Fatalf("warm read: %v", err)
	}
	bd.list, bd.show, bd.export = 13*lat, 25*lat/10, 50*lat
	bd.lists.Store(0)
	bd.shows.Store(0)
	bd.flushes.Store(0)
	bd.listTime.Store(0)
	bd.flushTime.Store(0)

	var mu sync.Mutex
	var res churnResult
	pending := map[string]time.Time{}
	stop := time.Now().Add(d)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the agents' writes
		defer wg.Done()
		rng := rand.New(rand.NewPCG(1, 2))
		for n := 0; time.Now().Before(stop); n++ {
			time.Sleep(writeEvery/2 + time.Duration(rng.Int64N(int64(writeEvery))))
			id := open[rng.IntN(len(open))]
			bd.write(journal, func() {
				if n%2 == 1 {
					_ = mem.Close(ctx, dir, id)
					mu.Lock()
					if _, seen := pending[id]; !seen {
						pending[id] = time.Now()
					}
					mu.Unlock()
				} else {
					_ = mem.SetLabel(ctx, dir, id, fmt.Sprintf("tick-%d", n), true)
				}
				bd.mu.Lock()
				bd.updated[id]++
				bd.mu.Unlock()
			})
		}
	}()
	grace := 150 * lat
	unseen := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pending) > 0
	}
	for _, pause := range []time.Duration{0, 10 * lat} { // poll and health loop
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Past stop the writer is done; read on until every close showed,
			// for at most grace.
			for now := time.Now(); now.Before(stop) || (now.Before(stop.Add(grace)) && unseen()); now = time.Now() {
				start := time.Now()
				rows, took, kept, err := objectiveRowsTimed(ctx, client, []store.Project{project}, cache)
				done := time.Now()
				mu.Lock()
				res.loads++
				res.load = append(res.load, took["example"])
				if kept {
					res.kept++
				}
				if err == nil && !kept {
					for _, r := range rows {
						if at, ok := pending[r.BeadID]; ok && r.Status == "closed" && at.Before(start) {
							res.visible = append(res.visible, done.Sub(at))
							delete(pending, r.BeadID)
						}
					}
				}
				mu.Unlock()
				time.Sleep(pause)
			}
		}()
	}
	wg.Wait()
	res.unseen = len(pending)
	res.lists, res.shows, res.flushes = bd.lists.Load(), bd.shows.Load(), bd.flushes.Load()
	res.listTime, res.flush = time.Duration(bd.listTime.Load()), time.Duration(bd.flushTime.Load())
	return res
}

// churnScenario is tests/evals/scenarios/load-under-churn.json.
type churnScenario struct {
	UnitMS          int `json:"unit_ms"`
	Workstreams     int `json:"workstreams"`
	Steps           int `json:"steps"`
	WriteEveryUnits int `json:"write_every_units"`
	RunUnits        int `json:"run_units"`
	MaxVisibleUnits int `json:"max_visible_units"`
	MaxLoadUnits    int `json:"max_load_units"`
	MaxKept         int `json:"max_kept_last_good"`
	MinClosesSeen   int `json:"min_closes_seen"`
}

// TestLoadUnderChurnScenario: on a busy Dolt project a close shows on the board
// within seconds and no load keeps last-good rows (il9.202.25.1). Before, every
// load waited for a 5s export it then could not use, the export it wrote moved
// the fingerprint, and ReadConsistent re-read the project up to 3 times.
// COMMANDER_CHURN_BENCH=1 runs it at the live costs (unit 100ms, about a minute)
// and only logs; COMMANDER_CHURN_SCALE=<duration> sets the unit.
func TestLoadUnderChurnScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/load-under-churn.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc churnScenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	unit := time.Duration(sc.UnitMS) * time.Millisecond
	bench := os.Getenv("COMMANDER_CHURN_BENCH") != ""
	if bench {
		unit = 100 * time.Millisecond
		if v := os.Getenv("COMMANDER_CHURN_SCALE"); v != "" {
			if unit, err = time.ParseDuration(v); err != nil {
				t.Fatal(err)
			}
		}
		sc.RunUnits = 600
	}
	// Limits scale with the harness's deadline scale so a busy machine does not fail them.
	scale := 1
	if v, err := strconv.Atoi(os.Getenv("COMMANDER_EVAL_TIMEOUT_SCALE")); err == nil && v >= 1 && v <= 10 {
		scale = v
	}
	res := runChurn(t, sc.Workstreams, sc.Steps, unit, time.Duration(sc.WriteEveryUnits)*unit, time.Duration(sc.RunUnits)*unit)
	t.Logf("churn (unit %v, %d beads): %s", unit, sc.Workstreams*(sc.Steps+1), res)
	if bench {
		return
	}
	if res.kept > sc.MaxKept {
		t.Errorf("%d loads kept last-good rows, want at most %d", res.kept, sc.MaxKept)
	}
	if len(res.visible) < sc.MinClosesSeen || res.unseen > 0 {
		t.Errorf("closes seen %d (want at least %d), never seen %d", len(res.visible), sc.MinClosesSeen, res.unseen)
	}
	if got, limit := churnPct(res.visible, 1), time.Duration(sc.MaxVisibleUnits*scale)*unit; got > limit {
		t.Errorf("a close took %v to show, limit %v", got, limit)
	}
	if got, limit := churnPct(res.load, 1), time.Duration(sc.MaxLoadUnits*scale)*unit; got > limit {
		t.Errorf("a load took %v, limit %v", got, limit)
	}
}
