package beads

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"
)

// TestBDClientEndToEnd runs Commander's beads client against a real bd server:
// COMMANDER_BD_INTEGRATION=1 go test ./internal/beads -run BDClient -v
func TestBDClientEndToEnd(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	c := New()
	if err := c.Init(ctx, dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "-C", dir, "dolt", "stop").Run() })
	for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(dir + "/" + f); err == nil {
			t.Errorf("bd init wrote %s into the project", f)
		}
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	parent, err := c.Create(ctx, dir, "Parent", CreateOptions{Labels: []string{"kind:objective"}, Silent: true})
	must("create", err)
	child, err := c.Create(ctx, dir, "Child", CreateOptions{Parent: parent, Labels: []string{"kind:objective", "gate"}, Silent: true})
	must("create child", err)
	blocker, err := c.Create(ctx, dir, "Blocker", CreateOptions{Labels: []string{"kind:objective"}, Silent: true})
	must("create blocker", err)
	must("label add", c.SetLabel(ctx, dir, child, "approver:user", true))
	must("label rm", c.SetLabel(ctx, dir, child, "gate", false))
	must("status", c.SetStatus(ctx, dir, child, "in_progress"))
	must("set-state", c.SetState(ctx, dir, child, "phase", "build", "test"))
	must("comment", c.AddComment(ctx, dir, child, "hello"))
	must("dep", c.DependOn(ctx, dir, child, blocker))
	must("rename", c.Rename(ctx, dir, child, "Child renamed"))

	cs, err := c.Comments(ctx, dir, child)
	must("comments", err)
	if len(cs) != 1 || cs[0].Text != "hello" {
		t.Errorf("comments = %+v", cs)
	}
	d, err := FreshDetails(ctx, c, dir, []string{child})
	must("fresh details", err)
	if d[child].Parent != parent || len(d[child].Comments) != 1 || len(d[child].Deps) != 2 {
		t.Errorf("fresh details = %+v", d[child])
	}
	all, err := c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	must("list", err)
	var got *Bead
	for i := range all {
		if all[i].ID == child {
			got = &all[i]
		}
	}
	if got == nil || got.Status != "in_progress" || got.Title != "Child renamed" || !slices.Contains(got.Labels, "approver:user") || slices.Contains(got.Labels, "gate") || !slices.Contains(got.Labels, "phase:build") {
		t.Errorf("listed child = %+v", got)
	}

	// The board's read path: the export is refreshed from bd and matches.
	ex := ExportReads(c)
	viaExport, err := ex.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	must("export list", err)
	if len(viaExport) != len(all) {
		t.Errorf("export list has %d beads, bd list %d", len(viaExport), len(all))
	}
	ed, err := ex.Details(ctx, dir, []string{child})
	must("export details", err)
	if ed[child].Parent != parent || len(ed[child].Comments) != 1 {
		t.Errorf("export details = %+v", ed[child])
	}

	must("close while blocked", c.Close(ctx, dir, child))
	must("delete", c.Delete(ctx, dir, blocker, "never was work"))
}
