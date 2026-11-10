package holdread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
)

func world(t *testing.T) *beads.MemClient {
	t.Helper()
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "w", Title: "Area", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "a", Title: "First", Status: "in_progress", Labels: []string{"waiting:1:ci:7"}})
	m.Seed(beads.Bead{ID: "b", Title: "Then", Labels: nil})
	m.Edges = []beads.Dep{
		{IssueID: "a", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "b", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "b", DependsOnID: "a", Type: "blocks"},
	}
	return m
}

func judge(t *testing.T, id string, ops ...[]string) []string {
	t.Helper()
	g, err := Load(context.Background(), world(t), "", id, nil, 60)
	if err != nil {
		t.Fatal(err)
	}
	edits, ok := EditsFromOps(ops)
	if !ok {
		t.Fatalf("ops not simulated: %v", ops)
	}
	var out []string
	for _, v := range holding.NewlyUnheld(g, id, edits) {
		out = append(out, v.ID)
	}
	return out
}

// The door simulates the argv it is about to run, on fresh reads, with the one predicate.
func TestEditsFromOpsJudgeTheWrite(t *testing.T) {
	if got := judge(t, "a", []string{"close", "a"}); strings.Join(got, ",") != "b" {
		t.Fatalf("closing a releases b with no holder: %v", got)
	}
	if got := judge(t, "a", []string{"update", "b", "--add-label", "waiting:2:review:reviewer-z"}, []string{"close", "a"}); len(got) != 0 {
		t.Fatalf("a wait on b in the same request holds it: %v", got)
	}
	if got := judge(t, "a", []string{"update", "a", "--remove-label", "waiting:1:ci:7"}); strings.Join(got, ",") != "a" {
		t.Fatalf("dropping a's only wait: %v", got)
	}
	if got := judge(t, "b", []string{"dep", "remove", "b", "a"}); strings.Join(got, ",") != "b" {
		t.Fatalf("removing b's only blocker: %v", got)
	}
	if got := judge(t, "a", []string{"--db", "/x/.beads/beads.db", "update", "a", "--status=open"}); len(got) != 0 {
		t.Fatalf("a status change with its wait kept: %v", got)
	}
	if _, ok := EditsFromOps([][]string{{"sql", "delete from issues"}}); ok {
		t.Fatal("an op it cannot read is simulated")
	}
}

func TestHolderFirstRunsHolderAddsFirst(t *testing.T) {
	ops := [][]string{
		{"update", "a", "--remove-label", "owner:operator-x"},
		{"update", "a", "--add-label", "waiting:2:until:9:Manager_20to_20recover", "--add-label", "owner:manager-w"},
		{"close", "z"},
		{"dep", "add", "b", "c"},
	}
	got := HolderFirst(ops)
	if got[0][1] != "a" || got[0][2] != "--add-label" || got[1][0] != "dep" || got[2][2] != "--remove-label" || got[3][0] != "close" {
		t.Fatalf("order %v", got)
	}
}

// countingClient counts every read Load makes.
type countingClient struct {
	*beads.MemClient
	reads int
}

func (c *countingClient) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	c.reads++
	return c.MemClient.List(ctx, dir, o)
}
func (c *countingClient) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	c.reads++
	return c.MemClient.Show(ctx, dir, id)
}
func (c *countingClient) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c.reads++
	return c.MemClient.Details(ctx, dir, ids)
}
func (c *countingClient) Dependents(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	c.reads++
	return c.MemClient.Dependents(ctx, dir, id)
}

// bigParent is a workstream w with n plain children besides a (in progress, waiting on CI) and b
// (waits on a, nothing else holds it): the real Commander workstream has about 200.
func bigParent(n int) *countingClient {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "w", Title: "Area", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "a", Title: "First", Status: "in_progress", Labels: []string{"waiting:1:ci:7"}})
	m.Seed(beads.Bead{ID: "b", Title: "Then"})
	m.Edges = []beads.Dep{
		{IssueID: "a", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "b", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "b", DependsOnID: "a", Type: "blocks"},
	}
	for i := range n {
		id := fmt.Sprintf("s%03d", i)
		m.Seed(beads.Bead{ID: id, Title: "Sibling", Status: "closed"})
		m.Edges = append(m.Edges, beads.Dep{IssueID: id, DependsOnID: "w", Type: "parent-child"})
	}
	return &countingClient{MemClient: m}
}

// A close under a parent with hundreds of children is judged (R1): the siblings cost one status
// list, not two reads each, and never push the closure over its bound (which would fail open).
func TestLoadUnderALargeParentIsJudgedInBoundedReads(t *testing.T) {
	c := bigParent(250)
	g, err := Load(context.Background(), c, "", "a", nil, 60)
	if err != nil {
		t.Fatalf("a close under 252 siblings was not judged: %v", err)
	}
	var got []string
	for _, v := range holding.NewlyUnheld(g, "a", map[string]holding.Edit{"a": func(b *holding.Bead) { b.Status = holding.StatusClosed }}) {
		got = append(got, v.ID)
	}
	if strings.Join(got, ",") != "b" {
		t.Fatalf("closing a releases b with no holder: %v", got)
	}
	if c.reads > 15 {
		t.Fatalf("%d reads for one close: want a handful, not two per sibling", c.reads)
	}
	t.Logf("reads: %d", c.reads)
}

// A closure past its bound is an error the caller must act on, never a graph that reads "held".
func TestLoadPastItsBoundSaysSo(t *testing.T) {
	c := bigParent(0)
	for i := range 70 {
		id := fmt.Sprintf("d%03d", i)
		c.Seed(beads.Bead{ID: id, Title: "Waits on a"})
		c.Edges = append(c.Edges, beads.Dep{IssueID: id, DependsOnID: "a", Type: "blocks"})
	}
	if _, err := Load(context.Background(), c, "", "a", nil, 60); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v, want ErrTooLarge", err)
	}
}
