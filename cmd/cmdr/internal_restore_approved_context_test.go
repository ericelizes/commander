package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestRestoreApprovedContextCommandSchema20(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_ROLE", "manager-demo")
	ctx := context.Background()
	path := filepath.Join(home, "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.InsertProject(ctx, store.Project{Name: "sample", Path: "/fixture", Type: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordPitch(ctx, pid, "demo-gate", "Continue?", `{"question":"Continue?","context":"original context","input":true}`, nil); err != nil {
		t.Fatal(err)
	}
	r := store.NeedsResolution{ProjectID: pid, BeadID: "demo-gate", Generation: "1", AnswerID: "fixture-answer", Action: "approve"}
	if err = st.SaveNeedsResolution(ctx, r, "Approved"); err != nil {
		t.Fatal(err)
	}
	if err = st.AdvanceNeedsResolution(ctx, r, store.ResolutionMailed); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec("PRAGMA user_version=20"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	bc := &beads.MemClient{EdgeParentsOnly: true}
	bc.Seed(beads.Bead{ID: "demo", Labels: []string{"kind:objective"}})
	bc.Seed(beads.Bead{ID: "demo-gate", Status: "closed", Labels: []string{"gate", "approver:user"}})
	bc.Edges = append(bc.Edges, beads.Dep{IssueID: "demo-gate", DependsOnID: "demo", Type: "parent-child"})
	saved := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return bc }
	t.Cleanup(func() { newBeadsClientFn = saved })
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	args := []string{"--project", "sample", "--bead", "demo-gate", "--generation", "1", "--answer-id", "fixture-answer"}
	if code := runInternalRestoreApprovedContext(append(args, "--description", "caller text"), out, out); code == 0 {
		t.Fatal("accepted arbitrary scope")
	}
	if code := runInternalRestoreApprovedContext(append(args, "--dry-run"), out, out); code != 0 {
		t.Fatal("dry run failed")
	}
	b, _ := bc.Show(ctx, "/fixture", "demo-gate")
	if b.Description != "" {
		t.Fatal("dry run wrote")
	}
	if code := runInternalRestoreApprovedContext(args, out, out); code != 0 {
		t.Fatal("restore failed")
	}
	b, _ = bc.Show(ctx, "/fixture", "demo-gate")
	if b.Description != "original context" || b.Status != "closed" {
		t.Fatalf("%+v", b)
	}
	st, err = store.OpenProvenance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var version int
	if err = st.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 20 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}
