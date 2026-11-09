package store

import (
	"context"
	"testing"
)

func TestWorkstreamManagers_RoundTripAndRoleLookup(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if err := st.UpsertWorkstreamManager(ctx, WorkstreamManager{ProjectID: 2, WorkstreamBeadID: "example-app-t11", SessionFile: "/p/.commander/workstreams/a.claude.session-id"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorkstreamManager(ctx, 2, "example-app-t11")
	if err != nil || got == nil || got.AmRole != "manager-example-app-t11" || got.CreatedAt == 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
	byRole, err := st.GetWorkstreamManagerByRole(ctx, "manager-example-app-t11")
	if err != nil || byRole == nil || byRole.ProjectID != 2 {
		t.Fatalf("by role: %+v %v", byRole, err)
	}
	if missing, _ := st.GetWorkstreamManagerByRole(ctx, "manager-nope"); missing != nil {
		t.Fatalf("unknown role should be nil, got %+v", missing)
	}

	// Upsert keeps the row and updates the session file (adoption re-points it).
	if err := st.UpsertWorkstreamManager(ctx, WorkstreamManager{ProjectID: 2, WorkstreamBeadID: "example-app-t11", SessionFile: "/other"}); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListWorkstreamManagers(ctx)
	if len(all) != 1 || all[0].SessionFile != "/other" {
		t.Fatalf("list = %+v", all)
	}
}

func TestRenameAmRoleKeepsAmName(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	if err := st.UpsertAmAgent(ctx, "/repo", "manager-example-app", "GoldTiger"); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameAmRole(ctx, "/repo", "manager-example-app", "manager-example-app-t01"); err != nil {
		t.Fatal(err)
	}
	if name, _ := st.GetAmAgent(ctx, "/repo", "manager-example-app-t01"); name != "GoldTiger" {
		t.Fatalf("new role should keep the am name, got %q", name)
	}
	if name, _ := st.GetAmAgent(ctx, "/repo", "manager-example-app"); name != "" {
		t.Fatalf("old role must stop resolving, got %q", name)
	}
	if err := st.RenameAmRole(ctx, "/repo", "manager-ghost", "manager-x"); err != nil {
		t.Fatalf("missing old role is a no-op: %v", err)
	}
}

func TestUnregisterNestedManagerIsPreciseAndIdempotent(t *testing.T) {
	st := openMem(t)
	ctx := context.Background()
	for _, id := range []string{"demo-root", "demo-task"} {
		if err := st.UpsertWorkstreamManager(ctx, WorkstreamManager{ProjectID: 1, WorkstreamBeadID: id, SessionFile: "/fixture/" + id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertAmAgent(ctx, "/fixture", "operator-demo", "FixtureFox"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		removed, err := st.UnregisterNestedManager(ctx, 1, "demo-task", "demo-root", "manager-demo-root")
		if err != nil || removed != (i == 0) {
			t.Fatalf("removed=%v err=%v", removed, err)
		}
	}
	rows, err := st.ListWorkstreamManagers(ctx)
	if err != nil || len(rows) != 1 || rows[0].WorkstreamBeadID != "demo-root" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if name, _ := st.GetAmAgent(ctx, "/fixture", "operator-demo"); name != "FixtureFox" {
		t.Fatal("operator mapping lost")
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM manager_unregistrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit=%d err=%v", n, err)
	}
	if _, err := st.UnregisterNestedManager(ctx, 1, "demo-root", "demo-root", "commander"); err == nil {
		t.Fatal("root deletion allowed")
	}
}
