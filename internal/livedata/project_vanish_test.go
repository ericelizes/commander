package livedata

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestColdProjectDegradedWhenReadOverlapsWrites(t *testing.T) {
	old := beads.ReadRetryBackoff
	beads.ReadRetryBackoff = func() time.Duration { return time.Millisecond }
	defer func() { beads.ReadRetryBackoff = old }()
	ctx := context.Background()
	mem := &beads.MemClient{Prefix: "p"}
	busy := coldProject(t, mem, 1, "busy")
	healthyMem := &beads.MemClient{Prefix: "q"}
	healthy := coldProject(t, healthyMem, 2, "healthy")
	bc := &touchingClient{MemClient: mem}
	bc.touch.Store(true)
	cache := &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}
	rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{busy}, cache)
	if err == nil || !OnlyProjectReadErrors(err) || !strings.Contains(err.Error(), "warming") || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v, want a degraded 'warming' project", len(rows), err)
	}
	// A healthy project next to it is unaffected.
	both, err := objectiveRowsFromBeadsCached(ctx, &mixedClient{MemClient: healthyMem, busy: bc, busyDir: busy.Path}, []store.Project{busy, healthy}, cache)
	if err == nil || len(both) != 2 {
		t.Fatalf("rows=%d err=%v, want the healthy project's 2 rows plus an error", len(both), err)
	}
	bc.touch.Store(false)
	rows, err = objectiveRowsFromBeadsCached(ctx, bc, []store.Project{busy}, cache)
	if err != nil || len(rows) != 2 {
		t.Fatalf("recovery rows=%d err=%v", len(rows), err)
	}
}

type mixedClient struct {
	*beads.MemClient
	busy    *touchingClient
	busyDir string
}

func (c *mixedClient) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	if dir == c.busyDir {
		return c.busy.List(ctx, dir, o)
	}
	return c.MemClient.List(ctx, dir, o)
}

func TestProjectVanishClosedWorkstreamWithOpenChildStaysHealthy(t *testing.T) {
	ctx := context.Background()
	project := store.Project{ID: 1, Name: "alpha", Path: t.TempDir()}
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "p-1", Title: "Done", Status: "closed", Labels: []string{"kind:objective"}},
			{ID: "p-1.1", Title: "Leftover", Status: "open", Labels: []string{"kind:task"}},
		},
		ParentsResult: map[string]string{"p-1": "", "p-1.1": "p-1"},
	}
	if rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{project}, nil); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%d err=%v, want healthy and empty", len(rows), err)
	}
}

func TestProjectVanishExpectsRows(t *testing.T) {
	open := func(id string) beads.Bead { return beads.Bead{ID: id, Status: "open"} }
	closedB := beads.Bead{ID: "c", Status: "closed"}
	parents := map[string]string{"c.1": "c", "c.1.1": "c.1", "d.1": "d", "o.1": "o"}
	po := func(id string) string { return parents[id] }
	closedSet := map[string]bool{"c": true}
	cases := []struct {
		name string
		in   []beads.Bead
		want bool
	}{
		{"open workstream whose rows failed to build", []beads.Bead{open("o"), open("o.1")}, true},
		{"closed root with open child", []beads.Bead{closedB, open("c.1")}, false},
		{"open grandchild under closed ancestor", []beads.Bead{closedB, open("c.1"), open("c.1.1")}, false},
		{"deferred root and child", []beads.Bead{{ID: "d", Status: beads.StatusDeferred}, {ID: "d.1", Status: beads.StatusDeferred}}, false},
		{"superseded root", []beads.Bead{{ID: "s", Status: "open", Labels: []string{"superseded"}}}, false},
		{"all closed", []beads.Bead{closedB}, false},
	}
	for _, c := range cases {
		if got := expectsRows(c.in, closedSet, po); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestProjectVanishRecoveryClears(t *testing.T) {
	ctx := context.Background()
	project := store.Project{ID: 1, Name: "alpha", Path: t.TempDir()}
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "p-1", Title: "Done", Status: "closed", Labels: []string{"kind:objective"}},
			{ID: "p-1.1", Title: "Leftover", Status: "open", Labels: []string{"kind:task"}},
			{ID: "p-2", Title: "Live", Status: "open", Labels: []string{"kind:objective"}},
		},
		ParentsResult: map[string]string{"p-1": "", "p-1.1": "p-1", "p-2": ""},
	}
	if rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{project}, nil); err != nil || len(rows) == 0 {
		t.Fatalf("rows=%d err=%v, want the live workstream shown", len(rows), err)
	}
}

func TestProjectVanishCheckinReportSaysDegraded(t *testing.T) {
	s := Snapshot{Degraded: &SnapDegraded{Reason: "project alpha: not read yet (warming)"}}
	if out := CheckinReport(s, 0, AuditOptions{}); !strings.Contains(out, "DEGRADED") || !strings.Contains(out, "alpha") {
		t.Fatalf("report hides degraded state:\n%s", out)
	}
}

// The whole subtree of a closed workstream, at any depth (steps, gates, tracks,
// their children), yields no rows: none is promoted to a workstream.
func TestClosedWorkstreamSubtreeYieldsNoRows(t *testing.T) {
	ctx := context.Background()
	project := store.Project{ID: 1, Name: "alpha", Path: t.TempDir()}
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "p-1", Title: "Audits", Status: "closed", Labels: []string{"kind:objective"}},
			{ID: "p-1.1", Title: "step", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-1.1.1", Title: "sub step", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-1.2", Title: "track", Status: "open", Labels: []string{"kind:objective", "kind:track"}},
			{ID: "p-1.2.1", Title: "track gate", Status: "open", Labels: []string{"kind:objective", "gate"}},
			{ID: "p-1.2.2", Title: "track step", Status: "closed", Labels: []string{"kind:objective"}},
			// a promoted step: dotted id elsewhere, real parent edge under p-1.1
			{ID: "p-9", Title: "promoted", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-2", Title: "Live", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-2.1", Title: "live step", Status: "open", Labels: []string{"kind:objective"}},
		},
		ParentsResult: map[string]string{
			"p-1": "", "p-1.1": "p-1", "p-1.1.1": "p-1.1", "p-1.2": "p-1", "p-1.2.1": "p-1.2", "p-1.2.2": "p-1.2",
			"p-9": "p-1.1", "p-2": "", "p-2.1": "p-2",
		},
	}
	rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{project}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range NestObjectives(rows) {
		ids = append(ids, n.Row.BeadID)
	}
	if len(ids) != 2 || ids[0] != "p-2" || ids[1] != "p-2.1" {
		t.Fatalf("got %v, want only the open workstream [p-2 p-2.1]", ids)
	}
}

// A promoted step whose parent edge is under an OPEN workstream still nests; an
// open bead whose parent is a plain container (no row, not closed) or is not in
// the read at all (partial read) stays a workstream root, never lost.
func TestOpenOrphansStayRootsAndOpenPromotedStepNests(t *testing.T) {
	ctx := context.Background()
	project := store.Project{ID: 1, Name: "alpha", Path: t.TempDir()}
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "p-2", Title: "Live", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-9", Title: "promoted", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-5", Title: "under plain container", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-7", Title: "parent outside read", Status: "open", Labels: []string{"kind:objective"}},
		},
		ParentsResult: map[string]string{"p-2": "", "p-9": "p-2", "p-5": "p-epic", "p-7": "p-gone"},
	}
	rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{project}, nil)
	if err != nil {
		t.Fatal(err)
	}
	depth := map[string]int{}
	for _, n := range NestObjectives(rows) {
		depth[n.Row.BeadID] = n.Depth
	}
	want := map[string]int{"p-2": 0, "p-9": 1, "p-5": 0, "p-7": 0}
	for id, d := range want {
		if got, ok := depth[id]; !ok || got != d {
			t.Errorf("%s depth = %d (present %v), want %d; all=%v", id, got, ok, d, depth)
		}
	}
}
