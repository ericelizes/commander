package evals

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

func TestSidebarDragUndoScenario(t *testing.T) {
	if os.Getenv("COMMANDER_APP_LAYOUT_EVAL") != "1" {
		t.Skip("set COMMANDER_APP_LAYOUT_EVAL=1 after building app/dist")
	}
	tmp := t.TempDir()
	testguard.NoLeakedProcesses(t, tmp)
	ctx, _, cancel := layoutContext(t, evalTimeout(t, 2*time.Minute))
	defer cancel()
	out, err := runProcessGroup(ctx, filepath.Join(repoRoot(), "app"), append(os.Environ(), "TMPDIR="+tmp), "node", "scripts/sidebar-drag-check.mjs")
	if err != nil {
		t.Fatalf("sidebar drag undo: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
