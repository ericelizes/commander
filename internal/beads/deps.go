package beads

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Deps invokes `br dep list <issueID> --json` from projectDir.
// DependOn invokes `br dep add <issueID> <dependsOn>` from projectDir.
func (c *cmdClient) DependOn(ctx context.Context, projectDir, issueID, dependsOn string) error {
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), "dep", "add", issueID, dependsOn)
	if err != nil {
		return fmt.Errorf("br dep add: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (c *cmdClient) Deps(ctx context.Context, projectDir, issueID string) ([]Dep, error) {
	stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), "dep", "list", issueID, "--json")
	if err != nil {
		return nil, fmt.Errorf("br dep list: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	// br lists edges {issue_id, depends_on_id, type}; bd lists the issues
	// depended on {id, dependency_type}. Read either.
	var raw []struct {
		IssueID        string `json:"issue_id"`
		DependsOnID    string `json:"depends_on_id"`
		Type           string `json:"type"`
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("br dep list: decode: %w", err)
	}
	deps := make([]Dep, 0, len(raw))
	for _, r := range raw {
		d := Dep{IssueID: r.IssueID, DependsOnID: r.DependsOnID, Type: r.Type}
		if d.DependsOnID == "" {
			d = Dep{IssueID: issueID, DependsOnID: r.ID, Type: r.DependencyType}
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// Dependents invokes `br dep list <issueID> --direction up --json`: the edges that
// point at issueID, so a reverse edge needs no read of the whole project.
func (c *cmdClient) Dependents(ctx context.Context, projectDir, issueID string) ([]Dep, error) {
	stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), "dep", "list", issueID, "--direction", "up", "--json")
	if err != nil {
		return nil, fmt.Errorf("br dep list --direction up: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	// br lists edges {issue_id, depends_on_id, type}; bd lists the issues that
	// depend on it {id, dependency_type}. Read either.
	var raw []struct {
		IssueID        string `json:"issue_id"`
		Type           string `json:"type"`
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("br dep list --direction up: decode: %w", err)
	}
	deps := make([]Dep, 0, len(raw))
	for _, r := range raw {
		d := Dep{IssueID: r.IssueID, DependsOnID: issueID, Type: r.Type}
		if d.IssueID == "" {
			d = Dep{IssueID: r.ID, DependsOnID: issueID, Type: r.DependencyType}
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// showBatch is how many beads one `br show` call asks for. br show costs
// about a quarter second per bead, so a project's whole tree (~200 beads) in one
// call outlives the per-call timeout; small batches finish well inside it and a
// failed one costs only its own beads.
const showBatch = 20

// showConcurrency bounds the parallel `br show` calls of one read.
const showConcurrency = 3

// showBackoffDepth: with more br calls than this already queued for the project,
// a display read runs one batch at a time so it does not crowd out writes.
const showBackoffDepth = 2

// queueDepthFn is how many br calls of a project wait (a seam for tests).
var queueDepthFn = QueueDepth

// yieldFn is YieldToWrites (a seam for tests).
var yieldFn = YieldToWrites

// parallelism: a display read takes one db token at a time, always, so cmdrd's
// warming first pass never holds every shared token; an action read (fresh)
// keeps the queue-depth rule.
func parallelism(dir string, fresh bool) int {
	if !fresh {
		return 1
	}
	return showParallelism(dir)
}

func showParallelism(dir string) int {
	if queueDepthFn(dir) > showBackoffDepth {
		return 1
	}
	return showConcurrency
}

// PartialDetailsError is returned by a display read (Details) that could not
// read every bead: the map holds the beads that were read, Missing the rest.
type PartialDetailsError struct {
	Missing []string
	Err     error
}

func (e *PartialDetailsError) Error() string {
	if len(e.Missing) == 0 {
		return e.Err.Error() // every bead read, from a source that says why it is partial
	}
	return fmt.Sprintf("br show: %d of the requested beads unread: %v", len(e.Missing), e.Err)
}
func (e *PartialDetailsError) Unwrap() error { return e.Err }

// Details is for display: it permits a consistent export at most 30 seconds old
// and CachedClient may reuse it within that bound. Snapshot loaders (including
// gate evidence) use Details; action/enforcement callers must use FreshDetails.
func (c *cmdClient) Details(ctx context.Context, projectDir string, ids []string) (map[string]Detail, error) {
	return c.details(ctx, projectDir, ids, false, false)
}

// ChangedDetails is the display read of beads known to have changed: br show
// (never the export, which can lag behind the change that was just noticed),
// with a partial result on a failed batch like Details.
func ChangedDetails(ctx context.Context, c Client, dir string, ids []string) (map[string]Detail, error) {
	if r, ok := c.(interface {
		changedDetails(context.Context, string, []string) (map[string]Detail, error)
	}); ok {
		return r.changedDetails(ctx, dir, ids)
	}
	return c.Details(ctx, dir, ids)
}

func (c *cmdClient) changedDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return c.details(ctx, dir, ids, false, true)
}
func (w *StateWrite) changedDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return ChangedDetails(ctx, w.Client, dir, ids)
}

// FreshDetails bypasses exports and detail caches for decisions about actions:
// gate enforcement, spawn guards, ownership and writes. Use Details for display
// polls so an unchanged snapshot does not launch br on every refresh. Test
// clients without a separate persistence layer use their normal Details read.
func FreshDetails(ctx context.Context, c Client, dir string, ids []string) (map[string]Detail, error) {
	if fresh, ok := c.(interface {
		freshDetails(context.Context, string, []string) (map[string]Detail, error)
	}); ok {
		return fresh.freshDetails(ctx, dir, ids)
	}
	return c.Details(ctx, dir, ids)
}

// FreshShow is Show for a decision about an action: it bypasses exports and the
// read cache, which on a Dolt backend can lag a write made by another process
// until the export is next rewritten. Test clients use their normal Show.
func FreshShow(ctx context.Context, c Client, dir, id string) (*Bead, error) {
	if fresh, ok := c.(interface {
		freshShow(context.Context, string, string) (*Bead, error)
	}); ok {
		return fresh.freshShow(ctx, dir, id)
	}
	return c.Show(ctx, dir, id)
}

func (c *cmdClient) freshShow(ctx context.Context, dir, id string) (*Bead, error) {
	return c.Show(ctx, dir, id)
}
func (c *CachedClient) freshShow(ctx context.Context, dir, id string) (*Bead, error) {
	return FreshShow(ctx, c.Client, dir, id)
}
func (w *StateWrite) freshShow(ctx context.Context, dir, id string) (*Bead, error) {
	return FreshShow(ctx, w.Client, dir, id)
}
func (c *exportClient) freshShow(ctx context.Context, dir, id string) (*Bead, error) {
	return FreshShow(ctx, c.Client, dir, id)
}

func (c *cmdClient) freshDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return c.details(ctx, dir, ids, true, true)
}
func (c *CachedClient) freshDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return FreshDetails(ctx, c.Client, dir, ids)
}
func (w *StateWrite) freshDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return FreshDetails(ctx, w.Client, dir, ids)
}

func (c *cmdClient) details(ctx context.Context, projectDir string, ids []string, fresh, skipExport bool) (map[string]Detail, error) {
	var out map[string]Detail
	if !skipExport {
		out = exportDetails(projectDir, ids)
	}
	if out == nil {
		out = make(map[string]Detail, len(ids))
	}
	if !fresh {
		ctx = WithYieldBudget(ctx, YieldBudget)
	}
	var missing []string
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			missing = append(missing, id)
		}
	}
	var (
		mu       sync.Mutex
		cond     = sync.NewCond(&mu)
		inflight int
		firstEr  error
		failed   []string
		wg       sync.WaitGroup
	)
	for start := 0; start < len(missing); start += showBatch {
		batch := missing[start:min(start+showBatch, len(missing))]
		if !fresh {
			// display: let a waiting write (any process) go first
			yieldFn(ctx, projectDir)
		}
		mu.Lock()
		for inflight >= parallelism(projectDir, fresh) {
			cond.Wait()
		}
		inflight++
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			shown, err := c.showOnce(ctx, projectDir, batch)
			mu.Lock()
			defer mu.Unlock()
			defer cond.Broadcast()
			inflight--
			if err != nil {
				failed = append(failed, batch...)
				if firstEr == nil {
					firstEr = err
				}
				return
			}
			maps.Copy(out, shown)
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if _, ok := out[id]; !ok && !slices.Contains(failed, id) {
			failed = append(failed, id)
			if firstEr == nil {
				firstEr = fmt.Errorf("br show: missing details for %s", id)
			}
		}
	}
	if len(failed) == 0 {
		return out, nil
	}
	if fresh {
		return nil, firstEr
	}
	return out, &PartialDetailsError{Missing: failed, Err: firstEr}
}

// showOnce runs one `br show` for a batch, retrying once when it was killed or
// failed for a reason other than the caller giving up.
func (c *cmdClient) showOnce(ctx context.Context, projectDir string, batch []string) (map[string]Detail, error) {
	args := append(append([]string{"show"}, batch...), "--json")
	if isBD() {
		args = append(args, "--include-comments") // bd show leaves comments out by default
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			break
		}
		stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
		if err != nil {
			lastErr = fmt.Errorf("br show: %w: %s", err, strings.TrimSpace(string(stderr)))
			if errors.Is(err, context.DeadlineExceeded) {
				break // a hung batch must not cost another full timeout
			}
			continue
		}
		var shown []struct {
			ID           string `json:"id"`
			Dependencies []struct {
				ID   string `json:"id"`
				Type string `json:"dependency_type"`
			} `json:"dependencies"`
			Comments []Comment `json:"comments"`
		}
		if err := json.Unmarshal(stdout, &shown); err != nil {
			return nil, fmt.Errorf("br show: decode: %w", err)
		}
		out := make(map[string]Detail, len(shown))
		for _, s := range shown {
			d := Detail{Comments: s.Comments}
			for _, e := range s.Dependencies {
				d.Deps = append(d.Deps, Dep{IssueID: s.ID, DependsOnID: e.ID, Type: e.Type})
				if e.Type == "parent-child" {
					d.Parent = e.ID
				}
			}
			out[s.ID] = d
		}
		return out, nil
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return nil, lastErr
}

const maxExportAge = 30 * time.Second

// exportDetails accepts bounded display lag; enforcement never calls it.
func exportDetails(projectDir string, ids []string) map[string]Detail {
	dir := filepath.Join(projectDir, ".beads")
	f, err := os.Open(filepath.Join(dir, "issues.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || time.Since(fi.ModTime()) > maxExportAge {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make(map[string]Detail, len(ids))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var rec struct {
			ID           string `json:"id"`
			Dependencies []struct {
				DependsOnID string `json:"depends_on_id"`
				Type        string `json:"type"`
			} `json:"dependencies"`
			Comments []Comment `json:"comments"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil
		}
		if !want[rec.ID] {
			continue
		}
		d := Detail{Comments: rec.Comments}
		for _, e := range rec.Dependencies {
			d.Deps = append(d.Deps, Dep{IssueID: rec.ID, DependsOnID: e.DependsOnID, Type: e.Type})
			if e.Type == "parent-child" {
				d.Parent = e.DependsOnID
			}
		}
		out[rec.ID] = d
	}
	if sc.Err() != nil {
		return nil
	}
	return out
}

// ForActions adapts bulk loaders to use DB-backed details, including when they
// normally serve display data. Other reads retain the supplied client's policy.
func ForActions(c Client) Client { return actionClient{Client: c} }

type actionClient struct{ Client }

func (c actionClient) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return FreshDetails(ctx, c.Client, dir, ids)
}
func (c actionClient) freshDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return FreshDetails(ctx, c.Client, dir, ids)
}
