package beads

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresPlanReceiptMakesConcurrentCreateRetriesSafe(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, "Root", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Show(ctx, dir, root)
	if e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{RequestID: "stable-client-request", JurisdictionRevision: 1, Beads: map[string]int64{root: b.PlanRevision}}
	ops := [][]string{{"create", "New task", "--parent", root, "--silent"}}
	req := WithPlanBasis(ctx, &basis)
	start := make(chan struct{})
	results := make(chan DoorResponse, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- postgresApplyOps(req, dir, ops) }()
	}
	close(start)
	wg.Wait()
	close(results)
	commits, replays := 0, 0
	var created string
	for result := range results {
		if result.Exit != 0 || result.PlanReceipt != basis.RequestID {
			t.Fatalf("retry failed: %+v", result)
		}
		if result.Replayed {
			replays++
		} else {
			commits++
		}
		if created != "" && result.Stdout != created {
			t.Fatal("retry returned a different created bead")
		}
		created = result.Stdout
	}
	if commits != 1 || replays != 1 {
		t.Fatalf("commits=%d replays=%d", commits, replays)
	}
	var count int
	if e = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks WHERE parent_id=?`, root).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate task after retry", count, e)
	}
	// Changing the payload under an explicit key must not reuse the receipt.
	changed := postgresApplyOps(req, dir, [][]string{{"create", "Different task", "--parent", root}})
	if changed.Exit == 0 {
		t.Fatal("request key reused for different intent")
	}
	// A process restart can retrieve the immutable receipt without rerunning writes.
	prior, e := LookupPlanReceipt(req, dir, ops)
	if e != nil || prior == nil || !prior.Replayed || prior.Stdout != created {
		t.Fatalf("durable receipt lost: %+v %v", prior, e)
	}
}

func TestPostgresFailedPlanDoesNotReserveReceipt(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	basis := PlanBasis{RequestID: "failed-attempt", JurisdictionRevision: 1, Beads: map[string]int64{}}
	req := WithPlanBasis(ctx, &basis)
	result := postgresApplyOps(req, dir, [][]string{{"create", "Would have leaked", "--ref", "new"}, {"update", "$new", "--priority", "999"}})
	if result.Exit == 0 {
		t.Fatal("invalid plan committed")
	}
	var count int
	if e := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM commander_plan_receipts`).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed plan retained receipt", count, e)
	}
	if e := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_tasks`).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed plan leaked a created bead", count, e)
	}
}

func TestPlanClientNeverFallsBackToLegacyWriteEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == DoorPath {
			writes++
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	for name, value := range map[string]string{"state.postgres": "configured-marker", "api-url": server.URL, "api-token": "test-token"} {
		if e := os.WriteFile(filepath.Join(home, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	_, e := ApplyPlan(context.Background(), t.TempDir(), [][]string{{"create", "Must not reach legacy daemon"}}, PlanBasis{JurisdictionRevision: 1}, nil)
	if e == nil || writes != 0 {
		t.Fatalf("legacy daemon executed an unversioned plan: writes=%d error=%v", writes, e)
	}
}
