package gates

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestGateFreshEvidenceBypassesDisplayCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	export := `{"id":"p-g","comments":[{"text":"gate passed by auto at 1: exported"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(export), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "br")
	body := `#!/bin/sh
case "$1" in
list) echo '[{"id":"p-g","status":"closed","labels":["kind:gate","gate-of:p-1","gate-name:tests","verifier:auto"]}]' ;;
show)
  echo show >> calls
  echo '[{"id":"p-g","comments":[{"text":"gate passed by auto at 2: database"}]}]' ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", script)
	bc := beads.NewCached(beads.New())
	ctx := context.Background()
	assertEvidence := func(ns []Node, err error, want string) {
		t.Helper()
		if err != nil || len(ns) != 1 || ns[0].Gate.Evidence != want {
			t.Fatalf("gates=%+v err=%v, want evidence %q", ns, err, want)
		}
	}
	ns, err := All(ctx, bc, dir, 1)
	assertEvidence(ns, err, "exported")
	ns, err = AllFresh(ctx, bc, dir, 1)
	assertEvidence(ns, err, "database")
	ns, err = ForStageFresh(ctx, bc, dir, 1, "p-1")
	assertEvidence(ns, err, "database")
	for range 3 {
		ns, err = All(ctx, bc, dir, 1)
		assertEvidence(ns, err, "exported")
		ns, err = ForStage(ctx, bc, dir, 1, "p-1")
		assertEvidence(ns, err, "exported")
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || string(calls) != "show\nshow\n" {
		t.Fatalf("show calls=%q err=%v, want only the two enforcement reads", calls, err)
	}
}
