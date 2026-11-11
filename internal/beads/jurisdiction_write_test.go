package beads

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func TestPostgresPlanningWritesRespectTransferredJurisdiction(t *testing.T) {
	c, dir, s := postgresBeads(t)
	ctx := context.Background()
	root, e := c.Create(ctx, dir, "Urgent", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	outside, e := c.Create(ctx, dir, "Other work", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e = s.DB().QueryRow(`SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if e = s.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: root, ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "focused response"}); e != nil {
		t.Fatal(e)
	}
	t.Setenv("COMMANDER_ROLE", "commander")
	if _, _, e = postgresRun(ctx, dir, []string{"update", root, "--title", "Global overwrite"}); e == nil {
		t.Fatal("default Commander overwrote urgent scope")
	}
	t.Setenv("COMMANDER_ROLE", "commander-unregistered")
	if _, _, e = postgresRun(ctx, dir, []string{"update", root, "--title", "Unregistered overwrite"}); e == nil {
		t.Fatal("unregistered planning identity bypassed jurisdiction")
	}
	t.Setenv("COMMANDER_ROLE", "commander-urgent")
	if _, _, e = postgresRun(ctx, dir, []string{"update", root, "--title", "Investigating"}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(ctx, dir, []string{"update", outside, "--title", "Unauthorized"}); e == nil {
		t.Fatal("focused Commander edited outside its scope")
	}
	if e = c.SetParent(ctx, dir, root, outside); e == nil {
		t.Fatal("cross-scope move bypassed coordination")
	}
	if _, _, e = postgresRun(ctx, dir, []string{"dep", "add", root, outside}); e == nil {
		t.Fatal("cross-scope dependency bypassed coordination")
	}
	b, e := c.Show(ctx, dir, root)
	if e != nil || b.PlanningCommander != "commander-urgent" || b.JurisdictionRevision != 2 || b.Title != "Investigating" {
		t.Fatalf("wrong live projection: %+v %v", b, e)
	}
	// A user operation remains allowed, even if cmdrd itself inherited an agent
	// environment: explicit empty door attribution must not borrow daemon role.
	userCtx := context.WithValue(ctx, doorEnvKey{}, []string{})
	if _, _, e = postgresRun(userCtx, dir, []string{"update", outside, "--title", "User edit"}); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresFinalReportRequiresMatchingRunActorAndSession(t *testing.T) {
	c, dir, s := postgresBeads(t)
	ctx := context.Background()
	bead, e := c.Create(ctx, dir, "Execution", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e = s.DB().QueryRow(`SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	rid, e := s.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: bead, AgentName: "operator-current", OpencodeSessionID: "session", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("COMMANDER_RUN_ID", strconv.FormatInt(rid, 10))
	t.Setenv("COMMANDER_RUN_SESSION", "session")
	t.Setenv("COMMANDER_ROLE", "operator-other")
	if e = c.AddComment(ctx, dir, bead, "Final report: forged"); e == nil {
		t.Fatal("foreign actor's report accepted")
	}
	t.Setenv("COMMANDER_ROLE", "operator-current")
	t.Setenv("COMMANDER_RUN_SESSION", "wrong")
	if e = c.AddComment(ctx, dir, bead, "Final report: stale session"); e == nil {
		t.Fatal("foreign session report accepted")
	}
	t.Setenv("COMMANDER_RUN_SESSION", "session")
	if e = c.AddComment(ctx, dir, bead, "Final report: validated result"); e != nil {
		t.Fatal(e)
	}
	var reports int
	if e = s.DB().QueryRow(`SELECT COUNT(*) FROM bead_comments WHERE project_id=? AND bead_id=?`, pid, bead).Scan(&reports); e != nil || reports != 1 {
		t.Fatalf("rejected reports leaked: %d %v", reports, e)
	}
}
