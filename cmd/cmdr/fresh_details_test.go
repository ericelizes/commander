package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestOpenGateAboveFreshDetailsBypassesDisplayExport(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte("{\"id\":\"p-1\",\"dependencies\":[]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "br")
	body := `#!/bin/sh
case "$*" in
"show p-1 --json") echo '[{"id":"p-1","dependencies":[{"id":"p-g","dependency_type":"blocks"}]}]' ;;
"show p-g --json") echo '[{"id":"p-g","labels":["gate"]}]' ;;
"show p-g") printf 'ID: p-g\nStatus: open\nLabels: gate\n' ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", script)
	bc := beads.NewCached(beads.New())
	ctx := context.Background()
	display, err := bc.Details(ctx, dir, []string{"p-1"})
	if err != nil || len(display["p-1"].Deps) != 0 {
		t.Fatalf("display=%v err=%v", display, err)
	}
	gate, err := openGateAbove(ctx, bc, dir, "p-1")
	if err != nil || gate != "p-g" {
		t.Fatalf("gate=%q err=%v", gate, err)
	}
}

// A gate that closes after a cached Show of it must not keep refusing: the
// blocker's status is read from br itself (il9.116.1).
func TestOpenGateAboveClosedGateIsNotServedStaleFromCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "status")
	if err := os.WriteFile(state, []byte("open"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "br")
	body := `#!/bin/sh
case "$*" in
"show p-1 --json") echo '[{"id":"p-1","dependencies":[{"id":"p-g","dependency_type":"blocks"}]}]' ;;
"show p-g --json") echo '[{"id":"p-g","labels":["gate"]}]' ;;
"show p-g") printf 'ID: p-g\nStatus: %s\nLabels: gate\n' "$(cat ` + state + `)" ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", script)
	bc := beads.NewCached(beads.New())
	ctx := context.Background()
	if _, err := bc.Show(ctx, dir, "p-g"); err != nil { // warms the cache with "open"
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("closed"), 0600); err != nil {
		t.Fatal(err)
	}
	gate, err := openGateAbove(ctx, bc, dir, "p-1")
	if err != nil || gate != "" {
		t.Fatalf("closed gate still refuses: gate=%q err=%v", gate, err)
	}
}

func TestOpenGateAbove_ClosedAndNonGateBlockers(t *testing.T) {
	ctx := context.Background()
	mk := func(status string, labels ...string) *beads.MemClient {
		m := &beads.MemClient{EdgeParentsOnly: true}
		m.Seed(beads.Bead{ID: "ws"})
		m.Seed(beads.Bead{ID: "ws.1"})
		m.Seed(beads.Bead{ID: "ws.1.1"})
		m.Seed(beads.Bead{ID: "b", Status: status, Labels: labels})
		m.Edges = []beads.Dep{
			{IssueID: "ws.1", DependsOnID: "ws", Type: "parent-child"},
			{IssueID: "ws.1.1", DependsOnID: "ws.1", Type: "parent-child"},
			{IssueID: "ws.1", DependsOnID: "b", Type: "blocks"},
		}
		return m
	}
	cases := []struct {
		name, want string
		m          *beads.MemClient
	}{
		{"open gate blocks (via an ancestor)", "b", mk("open", beads.GateLabel)},
		{"closed gate passes", "", mk("closed", beads.GateLabel)},
		{"closed step carrying the gate label with a passed gate passes", "", mk("closed", beads.GateLabel, "kind:objective")},
		{"open non-gate step is not a workflow gate", "", mk("open", "kind:objective")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := openGateAbove(ctx, tc.m, "/p", "ws.1.1")
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
