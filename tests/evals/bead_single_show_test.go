package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// Exercise the public fresh reader through an actual single-ID CLI process,
// using PostgreSQL's object and canonical dependency wire format.
func TestBeadSingleShowScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/bead-single-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		IDs             []string
		Parent, Failure string
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("COMMANDER_HOME", dir)
	script := filepath.Join(dir, "br")
	writeExecutable(t, script, `#!/bin/sh
if [ "$#" != 3 ] || [ "$1" != show ] || [ "$3" != --json ]; then
 echo 'show requires one bead' >&2; exit 1
fi
if [ "$2" = "$SINGLE_SHOW_FAIL" ]; then echo 'backend unavailable' >&2; exit 1; fi
printf '{"id":"%s","dependencies":[{"depends_on_id":"%s","type":"parent-child"}],"comments":[]}\n' "$2" "$SINGLE_SHOW_PARENT"
`)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	t.Setenv("SINGLE_SHOW_PARENT", sc.Parent)
	t.Setenv("SINGLE_SHOW_FAIL", "")
	c := beads.New()
	got, err := beads.FreshDetails(context.Background(), c, dir, sc.IDs)
	if err != nil || len(got) != len(sc.IDs) {
		t.Fatalf("details %v: %v", got, err)
	}
	for _, id := range sc.IDs {
		if got[id].Parent != sc.Parent {
			t.Fatalf("%s: %+v", id, got[id])
		}
	}
	t.Setenv("SINGLE_SHOW_FAIL", sc.Failure)
	got, err = beads.FreshDetails(context.Background(), c, dir, sc.IDs)
	if got != nil || err == nil || !strings.Contains(err.Error(), "backend unavailable") {
		t.Fatalf("fresh failure: %v, %v", got, err)
	}
}
