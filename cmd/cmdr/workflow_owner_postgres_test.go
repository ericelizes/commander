package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

// Read the same inherited owner projection as jurisdiction status, including a
// transfer after the first authorization. No legacy Manager fallback is allowed.
func TestWorkflowOwnerPostgresHandoff(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	st, dsn := pgtest.Open(t)
	if err := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid, err := st.InsertProject(ctx, store.Project{Name: "owner-test", Path: dir, Type: "code", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"root", "block"} {
		if _, err = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,status,labels,created_at,updated_at) VALUES(?,?,?,'open','[]','now','now')`, pid, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET parent_id='root' WHERE project_id=? AND id='block'`, pid); err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterCommander(ctx, "commander-peer", "Peer"); err != nil {
		t.Fatal(err)
	}
	bc := beads.New()
	check := func(owner string) {
		t.Helper()
		b, e := beads.FreshShow(ctx, bc, dir, "block")
		if e != nil {
			t.Fatal(e)
		}
		if b.PlanningCommander != owner {
			t.Fatalf("owner: %q", b.PlanningCommander)
		}
		for _, role := range []string{"commander", "commander-peer", "manager-root", "operator-x"} {
			t.Setenv("COMMANDER_ROLE", role)
			e = authorizeWorkflowOwner(ctx, bc, dir, *b, "root", "writer selection", false)
			if (e == nil) != (role == owner) {
				t.Fatalf("owner %s role %s: %v", owner, role, e)
			}
		}
	}
	check("commander")
	basis, err := st.Jurisdiction(ctx, pid, "root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "root", ExpectedOwner: "commander", ExpectedRevision: basis.Revision, NextCommander: "commander-peer", Reason: "review owner handoff"}); err != nil {
		t.Fatal(err)
	}
	check("commander-peer")
}
