package beads

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/store"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresPlanBatchConflictsAreAtomic(t *testing.T) {
	var scenario struct {
		Titles   []string `json:"titles"`
		Evidence string   `json:"evidence"`
		Winners  int      `json:"expected_winners"`
		Comments int      `json:"expected_comments"`
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-atomic-plan.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, "Workstream", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := c.Create(ctx, dir, "Task", CreateOptions{Parent: root, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Show(ctx, dir, leaf)
	if e != nil {
		t.Fatal(e)
	}
	parent, e := c.Show(ctx, dir, root)
	if e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{root: parent.PlanRevision, leaf: b.PlanRevision}}
	batchCtx := WithPlanBasis(ctx, &basis)
	start := make(chan struct{})
	results := make(chan DoorResponse, 2)
	var wg sync.WaitGroup
	for _, title := range scenario.Titles {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			<-start
			results <- postgresApplyOps(batchCtx, dir, [][]string{{"update", leaf, "--title", title}, {"comments", "add", leaf, scenario.Evidence}})
		}(title)
	}
	close(start)
	wg.Wait()
	close(results)
	won := 0
	for result := range results {
		if result.Exit == 0 {
			won++
		} else if result.Applied != 0 {
			t.Fatalf("partial conflicting batch: %+v", result)
		}
	}
	if won != scenario.Winners {
		t.Fatalf("concurrent planners both won or both lost: %d", won)
	}
	var n int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_comments WHERE bead_id=?`, leaf).Scan(&n); e != nil || n != scenario.Comments {
		t.Fatalf("rejected plan leaked comment: %d %v", n, e)
	}
	b, e = c.Show(ctx, dir, leaf)
	if e != nil {
		t.Fatal(e)
	}
	missing := WithPlanBasis(ctx, &PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{leaf: b.PlanRevision}})
	r := postgresApplyOps(missing, dir, [][]string{{"update", leaf, "--title", "Must roll back"}, {"dep", "add", leaf, root}})
	if r.Exit == 0 {
		t.Fatal("missing endpoint basis was accepted")
	}
	after, e := c.Show(ctx, dir, leaf)
	if e != nil || after.Title != b.Title {
		t.Fatalf("missing basis changed plan: %+v %v", after, e)
	}
}

func TestPostgresPlanDependencyEditsInvalidateReadBasis(t *testing.T) {
	c, dir, _ := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	a, e := c.Create(ctx, dir, "A", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Create(ctx, dir, "B", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	first, e := c.Show(ctx, dir, a)
	if e != nil {
		t.Fatal(e)
	}
	second, e := c.Show(ctx, dir, b)
	if e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{a: first.PlanRevision, b: second.PlanRevision}}
	if r := postgresApplyOps(WithPlanBasis(ctx, &basis), dir, [][]string{{"dep", "add", a, b}}); r.Exit != 0 {
		t.Fatal(r.Error)
	}
	if r := postgresApplyOps(WithPlanBasis(ctx, &basis), dir, [][]string{{"update", a, "--title", "Stale plan"}}); r.Exit == 0 {
		t.Fatal("dependency edit did not invalidate stale planning basis")
	}
}

func TestPostgresPlanBasisChecksOwnershipAndWholeBatch(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, "Original", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e = st.DB().QueryRowContext(ctx, `SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	read := func(jrev int64) context.Context {
		t.Helper()
		b, err := c.Show(ctx, dir, root)
		if err != nil {
			t.Fatal(err)
		}
		return WithPlanBasis(ctx, &PlanBasis{JurisdictionRevision: jrev, Beads: map[string]int64{root: b.PlanRevision}})
	}
	// Even a valid first edit must roll back if a later edit is invalid.
	r := postgresApplyOps(read(1), dir, [][]string{{"update", root, "--title", "Partial"}, {"update", root, "--priority", "999"}})
	if r.Exit == 0 {
		t.Fatal("invalid batch committed")
	}
	b, e := c.Show(ctx, dir, root)
	if e != nil || b.Title != "Original" {
		t.Fatal("partial plan leaked", e)
	}
	if e = st.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	stale := read(1)
	if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: root, ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent focus"}); e != nil {
		t.Fatal(e)
	}
	if r = postgresApplyOps(stale, dir, [][]string{{"update", root, "--title", "Stale owner"}}); r.Exit == 0 {
		t.Fatal("stale jurisdiction basis committed")
	}
	if r = postgresApplyOps(WithDoorEnv(read(2), []string{"COMMANDER_ROLE=commander"}), dir, [][]string{{"update", root, "--title", "Outside scope"}}); r.Exit == 0 {
		t.Fatal("foreign Commander edited transferred scope")
	}
	if r = postgresApplyOps(WithDoorEnv(read(2), []string{"COMMANDER_ROLE=operator-generic"}), dir, [][]string{{"update", root, "--title", "Executor changed plan"}}); r.Exit == 0 {
		t.Fatal("executor submitted planning batch")
	}
	if r = postgresApplyOps(WithDoorEnv(read(2), []string{"COMMANDER_ROLE=commander-urgent"}), dir, [][]string{{"update", root, "--title", "Owned urgent plan"}}); r.Exit != 0 {
		t.Fatal(r.Error)
	}
}

func TestPostgresPlanParentBasisChangesWhenChildrenChange(t *testing.T) {
	c, dir, _ := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, "Workstream", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	parent, e := c.Show(ctx, dir, root)
	if e != nil {
		t.Fatal(e)
	}
	stale := WithPlanBasis(ctx, &PlanBasis{JurisdictionRevision: 1, Beads: map[string]int64{root: parent.PlanRevision}})
	if _, e = c.Create(ctx, dir, "New work", CreateOptions{Parent: root, Silent: true}); e != nil {
		t.Fatal(e)
	}
	r := postgresApplyOps(stale, dir, [][]string{{"update", root, "--description", "Plan based on an empty workstream"}})
	if r.Exit == 0 {
		t.Fatal("child insertion left parent planning basis current")
	}
}
