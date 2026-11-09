package gates

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func repairGraph() *beads.MemClient {
	m := publicationGraph()
	ctx := context.Background()
	m.Seed(beads.Bead{ID: "track", Labels: []string{"kind:objective", "kind:track"}})
	m.SetParent(ctx, "/p", "track", "sample")
	m.SetParent(ctx, "/p", "stage", "track")
	m.DependOn(ctx, "/p", "track", "card")
	m.DependOn(ctx, "/p", "stage", "card")
	m.AddComment(ctx, "/p", "card", "Original approval history")
	m.Seed(beads.Bead{ID: "prior-check", Status: "closed"})
	m.DependOn(ctx, "/p", "card", "prior-check")
	return m
}
func repairRun(c beads.Client, dry bool) error {
	return RepairPublication(context.Background(), c, "/p", "card", "track", []string{"track"}, "Redundant ancestor wait", "manager-sample", dry)
}
func TestPublicationRepairPreservesAuthority(t *testing.T) {
	ctx := context.Background()
	m := repairGraph()
	before, _ := m.Show(ctx, "/p", "card")
	beforeDetails, _ := m.Details(ctx, "/p", []string{"card"})
	edges := append([]beads.Dep(nil), m.Edges...)
	if err := repairRun(m, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(edges, m.Edges) {
		t.Fatal("dry run changed graph")
	}
	notes, _ := m.Comments(ctx, "/p", "card")
	if len(notes) != 1 {
		t.Fatal("dry run wrote audit")
	}
	for range 2 {
		if err := repairRun(m, false); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := m.Show(ctx, "/p", "card")
	after.ParentID = before.ParentID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed approval authority/content")
	}
	ds, _ := m.Details(ctx, "/p", []string{"card", "stage", "track"})
	if ds["card"].Parent != "track" || !repairWait(ds["stage"], "card") || repairWait(ds["track"], "card") {
		t.Fatal(ds)
	}
	if !reflect.DeepEqual(blocks(beforeDetails["card"]), blocks(ds["card"])) {
		t.Fatal("changed original gate prerequisites")
	}
	notes, _ = m.Comments(ctx, "/p", "card")
	if len(notes) != 3 || notes[0].Text != "Original approval history" {
		t.Fatal(notes)
	}
}
func TestPublicationRepairRefusals(t *testing.T) {
	for _, mode := range []string{"leaf", "root", "foreign", "operator", "no-leaf-wait", "no-ancestor-wait", "gate-chain", "unrelated-cycle", "related-pair", "held", "closed", "gate-ancestor", "wrong-parent", "missing-node", "ambiguous-gate"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m := repairGraph()
			waiters := []string{"track"}
			actor := "manager-sample"
			parent := "track"
			switch mode {
			case "leaf":
				waiters = []string{"stage"}
			case "root":
				waiters = []string{"sample"}
			case "foreign":
				actor = "manager-other"
			case "operator":
				actor = "operator-other"
			case "no-leaf-wait":
				m.RemoveDep(ctx, "/p", "stage", "card")
			case "no-ancestor-wait":
				m.RemoveDep(ctx, "/p", "track", "card")
			case "gate-chain":
				m.DependOn(ctx, "/p", "card", "stage")
			case "unrelated-cycle":
				m.Seed(beads.Bead{ID: "other"})
				m.DependOn(ctx, "/p", "other", "other")
			case "related-pair":
				m.Link(ctx, "/p", "track", "card", "related")
			case "held":
				m.SetLabel(ctx, "/p", "track", "hold", true)
			case "closed":
				m.Close(ctx, "/p", "card")
			case "gate-ancestor":
				m.SetLabel(ctx, "/p", "track", KindLabel, true)
			case "wrong-parent":
				parent = "sample"
			case "missing-node":
				m.DependOn(ctx, "/p", "card", "missing")
			case "ambiguous-gate":
				m.SetLabel(ctx, "/p", "card", "gate-of:track", true)
			}
			edges := append([]beads.Dep(nil), m.Edges...)
			notes, _ := m.Comments(ctx, "/p", "card")
			if err := RepairPublication(ctx, m, "/p", "card", parent, waiters, "Reason", actor, false); err == nil {
				t.Fatal("accepted unsafe repair")
			}
			after, _ := m.Comments(ctx, "/p", "card")
			if !reflect.DeepEqual(edges, m.Edges) || !reflect.DeepEqual(notes, after) {
				t.Fatal("wrote on refusal")
			}
		})
	}
}

type repairFailure struct {
	beads.Client
	fail    string
	applied bool
}

func (c *repairFailure) AddComment(ctx context.Context, dir, id, text string) error {
	if c.fail == "intent" || (c.fail == "completion" && len(text) > 10 && text[len(text)-10:] == " completed") {
		return errors.New("audit failed")
	}
	return c.Client.AddComment(ctx, dir, id, text)
}
func (c *repairFailure) RemoveDep(ctx context.Context, dir, id, on string) error {
	if c.fail == "remove" {
		if c.applied {
			c.Client.RemoveDep(ctx, dir, id, on)
		}
		return errors.New("response lost")
	}
	return c.Client.RemoveDep(ctx, dir, id, on)
}
func (c *repairFailure) SetParent(ctx context.Context, dir, id, parent string) error {
	if c.fail == "parent" {
		if c.applied {
			c.Client.SetParent(ctx, dir, id, parent)
		}
		return errors.New("response lost")
	}
	return c.Client.SetParent(ctx, dir, id, parent)
}
func TestPublicationRepairRetry(t *testing.T) {
	for _, failure := range []string{"intent", "remove", "parent", "completion"} {
		for _, applied := range []bool{false, true} {
			t.Run(failure+map[bool]string{false: "-before", true: "-after"}[applied], func(t *testing.T) {
				m := repairGraph()
				c := &repairFailure{Client: m, fail: failure, applied: applied}
				if repairRun(c, false) == nil {
					t.Fatal("ignored failure")
				}
				ds, _ := m.Details(context.Background(), "/p", []string{"stage", "track"})
				if !repairWait(ds["stage"], "card") {
					t.Fatal("lost protected wait")
				}
				if failure == "intent" && !repairWait(ds["track"], "card") {
					t.Fatal("removed without intent")
				}
				c.fail = ""
				if err := repairRun(c, false); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Interrupt after one successful removal: the durable intent lets a repeat
// reconcile the absent first wait while leaving every protected edge in place.
type partialRepair struct {
	beads.Client
	removed int
	fail    bool
}

func (c *partialRepair) RemoveDep(ctx context.Context, dir, id, on string) error {
	if c.fail && c.removed == 1 {
		return errors.New("second removal failed")
	}
	if err := c.Client.RemoveDep(ctx, dir, id, on); err != nil {
		return err
	}
	c.removed++
	return nil
}
func TestPublicationRepairPartialAncestorSet(t *testing.T) {
	ctx := context.Background()
	m := repairGraph()
	m.Seed(beads.Bead{ID: "wrapper", Labels: []string{"kind:objective"}})
	m.SetParent(ctx, "/p", "wrapper", "track")
	m.SetParent(ctx, "/p", "stage", "wrapper")
	m.DependOn(ctx, "/p", "wrapper", "card")
	c := &partialRepair{Client: m, fail: true}
	run := func() error {
		return RepairPublication(ctx, c, "/p", "card", "wrapper", []string{"track", "wrapper"}, "Redundant ancestor waits", "manager-sample", false)
	}
	if run() == nil {
		t.Fatal("ignored partial failure")
	}
	ds, _ := m.Details(ctx, "/p", []string{"track", "wrapper", "stage", "card"})
	if repairWait(ds["track"], "card") || !repairWait(ds["wrapper"], "card") || !repairWait(ds["stage"], "card") || ds["card"].Parent != "" {
		t.Fatal(ds)
	}
	c.fail = false
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

type changedRepairRead struct {
	beads.Client
	fail bool
}

func (c *changedRepairRead) AddComment(ctx context.Context, dir, id, text string) error {
	if err := c.Client.AddComment(ctx, dir, id, text); err != nil {
		return err
	}
	c.fail = true
	return nil
}
func (c *changedRepairRead) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if c.fail {
		return nil, errors.New("fresh evidence unavailable")
	}
	return c.Client.Details(ctx, dir, ids)
}
func TestPublicationRepairFreshFailureAfterIntent(t *testing.T) {
	m := repairGraph()
	edges := append([]beads.Dep(nil), m.Edges...)
	c := &changedRepairRead{Client: m}
	if repairRun(c, false) == nil {
		t.Fatal("ignored lost fresh evidence")
	}
	if !reflect.DeepEqual(edges, m.Edges) {
		t.Fatal("removed edges without fresh evidence")
	}
}

type movedLeafDuringPreflight struct {
	beads.Client
	changed bool
}

func (c *movedLeafDuringPreflight) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if len(ids) > 2 && !c.changed {
		c.changed = true
		c.Client.SetParent(ctx, dir, "stage", "sample")
	}
	return c.Client.Details(ctx, dir, ids)
}
func TestPublicationRepairRejectsMovedLeafDuringPreflight(t *testing.T) {
	m := repairGraph()
	c := &movedLeafDuringPreflight{Client: m}
	if repairRun(c, false) == nil {
		t.Fatal("accepted leaf moved during fresh graph read")
	}
	ds, _ := m.Details(context.Background(), "/p", []string{"card", "track"})
	if ds["card"].Parent != "" || !repairWait(ds["track"], "card") || len(ds["card"].Comments) != 1 {
		t.Fatal("repair wrote after ancestry changed")
	}
}

// A wait is redundant only when nothing else under its waiter depends on it.
func TestPublicationRepairOtherLiveWorkUnderAWaiter(t *testing.T) {
	work := func(m *beads.MemClient, id, parent string, labels ...string) {
		m.Seed(beads.Bead{ID: id, Labels: append(labels, "kind:objective")})
		m.SetParent(context.Background(), "/p", id, parent)
	}
	for _, tc := range []struct {
		name    string
		shape   func(*beads.MemClient)
		refused string
	}{
		{"sibling with no wait", func(m *beads.MemClient) { work(m, "sibling", "track") }, "sibling"},
		{"parked sibling", func(m *beads.MemClient) {
			work(m, "sibling", "track")
			m.SetStatus(context.Background(), "/p", "sibling", beads.StatusDeferred)
		}, "sibling"},
		{"superseded but open", func(m *beads.MemClient) { work(m, "sibling", "track", "superseded") }, "sibling"},
		{"open work under a closed bead", func(m *beads.MemClient) {
			work(m, "mid", "track")
			work(m, "deep", "mid")
			m.Close(context.Background(), "/p", "mid")
		}, "deep"},
		{"a closed bead keeps no wait", func(m *beads.MemClient) {
			work(m, "mid", "track")
			work(m, "deep", "mid")
			m.DependOn(context.Background(), "/p", "mid", "card")
			m.Close(context.Background(), "/p", "mid")
		}, "deep"},
		{"a related edge is no wait", func(m *beads.MemClient) {
			work(m, "sibling", "track")
			m.Link(context.Background(), "/p", "sibling", "card", "related")
		}, "sibling"},
		{"sibling's own wait", func(m *beads.MemClient) {
			work(m, "sibling", "track")
			m.DependOn(context.Background(), "/p", "sibling", "card")
		}, ""},
		{"under a bead keeping the wait", func(m *beads.MemClient) {
			work(m, "mid", "track")
			work(m, "deep", "mid")
			m.DependOn(context.Background(), "/p", "mid", "card")
		}, ""},
		{"closed sibling", func(m *beads.MemClient) {
			work(m, "sibling", "track")
			m.Close(context.Background(), "/p", "sibling")
		}, ""},
		{"under the leaf", func(m *beads.MemClient) { work(m, "step", "stage") }, ""},
		{"under the gate", func(m *beads.MemClient) { work(m, "input", "card") }, ""},
		{"outside the waiter", func(m *beads.MemClient) { work(m, "elsewhere", "sample") }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := repairGraph()
			tc.shape(m)
			edges := append([]beads.Dep(nil), m.Edges...)
			err := repairRun(m, false)
			if tc.refused == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "would release live "+tc.refused) {
				t.Fatalf("got %v, want a refusal naming %s", err, tc.refused)
			}
			notes, _ := m.Comments(context.Background(), "/p", "card")
			if !reflect.DeepEqual(edges, m.Edges) || len(notes) != 1 {
				t.Fatal("wrote on refusal")
			}
		})
	}
}

// The wait of a listed waiter does not hold what sits under it, wherever on the
// chain the listed waiter is.
func TestPublicationRepairListedWaitersKeepNothing(t *testing.T) {
	ctx := context.Background()
	shape := func() *beads.MemClient {
		m := repairGraph()
		m.Seed(beads.Bead{ID: "upper", Labels: []string{"kind:objective"}})
		m.Seed(beads.Bead{ID: "lower", Labels: []string{"kind:objective"}})
		m.Seed(beads.Bead{ID: "sibling", Labels: []string{"kind:objective"}})
		m.SetParent(ctx, "/p", "upper", "track")
		m.SetParent(ctx, "/p", "lower", "upper")
		m.SetParent(ctx, "/p", "stage", "lower")
		m.SetParent(ctx, "/p", "sibling", "lower")
		m.DependOn(ctx, "/p", "upper", "card")
		m.DependOn(ctx, "/p", "lower", "card")
		return m
	}
	run := func(m *beads.MemClient, waiters ...string) error {
		return RepairPublication(ctx, m, "/p", "card", "lower", waiters, "Redundant ancestor waits", "manager-sample", true)
	}
	if err := run(shape(), "track", "upper", "lower"); err == nil || !strings.Contains(err.Error(), "removing lower's wait would release live sibling") {
		t.Fatalf("all listed: %v", err)
	}
	// An unlisted chain bead cannot hold it instead: its wait makes the cycle.
	if err := run(shape(), "track", "lower"); err == nil || !strings.Contains(err.Error(), "resulting dependency cycle") {
		t.Fatalf("upper left waiting: %v", err)
	}
}

// The repeat of an interrupted repair judges the same rule on the changed graph:
// removed edges and a published gate do not make new refusals.
func TestPublicationRepairRepeatAfterPublish(t *testing.T) {
	ctx := context.Background()
	m := repairGraph()
	m.Seed(beads.Bead{ID: "input", Labels: []string{"kind:objective"}})
	m.SetParent(ctx, "/p", "input", "card")
	for range 2 {
		if err := repairRun(m, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublicationRepairInputs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		waiters []string
		reason  string
		want    string
	}{
		{"blank reason", []string{"track"}, " \t", "--reason"},
		{"no waiters", nil, "Reason", "--repair-ancestor-waits"},
		{"duplicate waiter", []string{"track", "track"}, "Reason", "duplicate waiter"},
		{"empty waiter", []string{"", "track"}, "Reason", "duplicate waiter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := repairGraph()
			edges := append([]beads.Dep(nil), m.Edges...)
			err := RepairPublication(context.Background(), m, "/p", "card", "track", tc.waiters, tc.reason, "manager-sample", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if !reflect.DeepEqual(edges, m.Edges) {
				t.Fatal("wrote on refusal")
			}
		})
	}
}

// editedDetails changes what a fresh read says about one bead.
type editedDetails struct {
	beads.Client
	id   string
	edit func(*beads.Detail)
}

func (c *editedDetails) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	ds, err := c.Client.Details(ctx, dir, ids)
	if d, ok := ds[c.id]; ok && err == nil {
		c.edit(&d)
		ds[c.id] = d
	}
	return ds, err
}

func TestPublicationRepairEdgeShapes(t *testing.T) {
	addDep := func(e beads.Dep) func(*beads.Detail) {
		return func(d *beads.Detail) { d.Deps = append(d.Deps, e) }
	}
	for _, tc := range []struct {
		name string
		id   string
		edit func(*beads.Detail)
		want string
	}{
		{"duplicate wait edge", "track", addDep(beads.Dep{IssueID: "track", DependsOnID: "card", Type: "blocks"}), "ambiguous"},
		{"edge of another bead", "other", addDep(beads.Dep{IssueID: "else", DependsOnID: "sample", Type: "blocks"}), "malformed"},
		{"edge with no endpoint", "other", addDep(beads.Dep{IssueID: "other", Type: "blocks"}), "malformed"},
		{"parent edge disagrees with the parent", "other", addDep(beads.Dep{IssueID: "other", DependsOnID: "track", Type: "parent-child"}), "conflicting parent"},
		{"two parent edges", "other", func(d *beads.Detail) {
			d.Parent = "track"
			d.Deps = append(d.Deps,
				beads.Dep{IssueID: "other", DependsOnID: "track", Type: "parent-child"},
				beads.Dep{IssueID: "other", DependsOnID: "track", Type: "parent-child"})
		}, "multiple parents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := repairGraph()
			m.Seed(beads.Bead{ID: "other", Labels: []string{"kind:objective"}})
			edges := append([]beads.Dep(nil), m.Edges...)
			err := repairRun(&editedDetails{Client: m, id: tc.id, edit: tc.edit}, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			notes, _ := m.Comments(context.Background(), "/p", "card")
			if !reflect.DeepEqual(edges, m.Edges) || len(notes) != 1 {
				t.Fatal("wrote on refusal")
			}
		})
	}
}

// afterIntent runs a change once the intent comment has been accepted.
type afterIntent struct {
	beads.Client
	change func()
}

func (c *afterIntent) AddComment(ctx context.Context, dir, id, text string) error {
	if strings.HasPrefix(text, PublicationRepairPrefix) && c.change != nil {
		change := c.change
		c.change = nil
		if err := c.Client.AddComment(ctx, dir, id, text); err != nil {
			return err
		}
		change()
		return nil
	}
	return c.Client.AddComment(ctx, dir, id, text)
}

// The graph moves between the intent and the first removal: every case stops
// with the wait still there, on the narrow re-read alone.
func TestPublicationRepairStopsWhenTheRereadDisagrees(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		change func(*beads.MemClient)
		want   string
	}{
		{"root moved, so the intent no longer matches", func(m *beads.MemClient) {
			m.Seed(beads.Bead{ID: "newroot", Labels: []string{"kind:objective"}})
			m.SetParent(ctx, "/p", "sample", "newroot")
		}, "ancestry changed since intent"},
		{"track held", func(m *beads.MemClient) { m.SetLabel(ctx, "/p", "track", "hold", true) }, "held"},
		{"leaf lost its wait", func(m *beads.MemClient) { m.RemoveDep(ctx, "/p", "stage", "card") }, "must wait on gate"},
		{"gate closed", func(m *beads.MemClient) { m.Close(ctx, "/p", "card") }, "open user approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := repairGraph()
			c := &afterIntent{Client: m, change: func() { tc.change(m) }}
			err := RepairPublication(ctx, c, "/p", "card", "track", []string{"track"}, "Reason", "user", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			ds, _ := m.Details(ctx, "/p", []string{"track", "card"})
			if !repairWait(ds["track"], "card") || ds["card"].Parent != "" {
				t.Fatal("wrote after the graph moved")
			}
		})
	}
}

// droppedIntent accepts the intent comment and never stores it.
type droppedIntent struct{ beads.Client }

func (c *droppedIntent) AddComment(ctx context.Context, dir, id, text string) error {
	if strings.HasPrefix(text, PublicationRepairPrefix) {
		return nil
	}
	return c.Client.AddComment(ctx, dir, id, text)
}

func TestPublicationRepairNeedsADurableIntent(t *testing.T) {
	m := repairGraph()
	err := repairRun(&droppedIntent{m}, false)
	if err == nil || !strings.Contains(err.Error(), "durable intent missing") {
		t.Fatalf("got %v", err)
	}
	ds, _ := m.Details(context.Background(), "/p", []string{"track"})
	if !repairWait(ds["track"], "card") {
		t.Fatal("removed a wait with no durable intent")
	}
}

// countedReads counts reads, split at the first write.
type countedReads struct {
	beads.Client
	wrote     bool
	lists     [2]int
	detailIDs [2]int
	shows     [2]int
}

func (c *countedReads) phase() int {
	if c.wrote {
		return 1
	}
	return 0
}
func (c *countedReads) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	c.lists[c.phase()]++
	return c.Client.List(ctx, dir, o)
}
func (c *countedReads) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c.detailIDs[c.phase()] += len(ids)
	return c.Client.Details(ctx, dir, ids)
}
func (c *countedReads) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	c.shows[c.phase()]++
	return c.Client.Show(ctx, dir, id)
}
func (c *countedReads) AddComment(ctx context.Context, dir, id, text string) error {
	c.wrote = true
	return c.Client.AddComment(ctx, dir, id, text)
}

// Five wrappers in a 5,000-bead project: one full read before the first write,
// and the writes' re-reads do not grow with the project.
func TestPublicationRepairReadsTheProjectOnce(t *testing.T) {
	ctx := context.Background()
	shape := func(unrelated int) *countedReads {
		m := repairGraph()
		prev := "track"
		waiters := []string{"track"}
		for _, w := range []string{"wrap-a", "wrap-b", "wrap-c", "wrap-d"} {
			m.Seed(beads.Bead{ID: w, Labels: []string{"kind:objective"}})
			m.SetParent(ctx, "/p", w, prev)
			m.DependOn(ctx, "/p", w, "card")
			waiters = append(waiters, w)
			prev = w
		}
		m.SetParent(ctx, "/p", "stage", prev)
		for i := range unrelated {
			m.Seed(beads.Bead{ID: fmt.Sprintf("work-%d", i), Status: "closed"})
		}
		c := &countedReads{Client: m}
		if err := RepairPublication(ctx, c, "/p", "card", prev, waiters, "Redundant ancestor waits", "manager-sample", false); err != nil {
			t.Fatal(err)
		}
		return c
	}
	small, big := shape(0), shape(5000)
	t.Logf("5,000 unrelated beads: before the first write %d lists, %d detail ids, %d shows; after it %d lists, %d detail ids, %d shows",
		big.lists[0], big.detailIDs[0], big.shows[0], big.lists[1], big.detailIDs[1], big.shows[1])
	if big.lists[0] != 1 || big.lists[1] != 0 {
		t.Fatalf("lists %v, want one before the first write and none after", big.lists)
	}
	if big.detailIDs[0] < 5000 {
		t.Fatalf("the preflight read only %d ids", big.detailIDs[0])
	}
	if big.detailIDs[1] != small.detailIDs[1] || big.shows[1] != small.shows[1] {
		t.Fatalf("re-reads grew with the project: %d/%d ids, %d/%d shows", small.detailIDs[1], big.detailIDs[1], small.shows[1], big.shows[1])
	}
	if big.detailIDs[1] > 200 {
		t.Fatalf("re-reads asked for %d detail ids", big.detailIDs[1])
	}
}
