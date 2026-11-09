package spawn

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

func TestSuccessfulSpawnAcceptsApprovalHandoffOnActualAncestor(t *testing.T) {
	withTempHome(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := makeGitRepo(t)
	s, _, _, bc, _, _ := newSpawnerForTest(t, dir, "ses_test_handoff")
	wait := beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Minute), Ref: beads.ManagerDispatchWait}.Label()
	bc.ListResult = []beads.Bead{{ID: "repair-parent", Status: "open", Labels: []string{"kind:objective", wait}}, {ID: "unrelated", Status: "open", Labels: []string{"kind:objective", wait}}}
	bc.ShowResults = map[string]*beads.Bead{"repair-parent": &bc.ListResult[0], "unrelated": &bc.ListResult[1]}
	bc.ParentsResult = map[string]string{"bd-100": "repair-parent", "repair-parent": "", "unrelated": ""}
	result, err := s.Spawn(context.Background(), goodRequest(t, dir))
	if err != nil || result.RunID == 0 {
		t.Fatal(result, err)
	}
	if !slices.Contains(bc.Calls, "SetLabel:repair-parent|label="+wait+"|on=false|dir="+dir) {
		t.Fatal("actual spawn did not accept parent dispatch", bc.Calls)
	}
	if slices.Contains(bc.Calls, "SetLabel:unrelated|label="+wait+"|on=false|dir="+dir) {
		t.Fatal("cleared unrelated work")
	}
}

// The receipt a close or create leaves on ready work ("Manager to start") is
// accepted the same way: the spawn under it is its real successor.
func TestSuccessfulSpawnAcceptsStartReceiptOnActualAncestor(t *testing.T) {
	withTempHome(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := makeGitRepo(t)
	s, _, _, bc, _, _ := newSpawnerForTest(t, dir, "ses_test_start")
	wait := beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Hour), Ref: beads.ManagerStartWait}.Label()
	bc.ListResult = []beads.Bead{{ID: "next-step", Status: "open", Labels: []string{"kind:objective", wait}}}
	bc.ShowResults = map[string]*beads.Bead{"next-step": &bc.ListResult[0]}
	bc.ParentsResult = map[string]string{"bd-100": "next-step", "next-step": ""}
	if result, err := s.Spawn(context.Background(), goodRequest(t, dir)); err != nil || result.RunID == 0 {
		t.Fatal(result, err)
	}
	if !slices.Contains(bc.Calls, "SetLabel:next-step|label="+wait+"|on=false|dir="+dir) {
		t.Fatal("spawn did not accept the start receipt", bc.Calls)
	}
}

func TestFailedSpawnRetainsApprovalHandoff(t *testing.T) {
	s, _, _, bc, _, _ := newSpawnerForTest(t, t.TempDir(), "ses_test_handoff")
	if _, err := s.Spawn(context.Background(), SpawnRequest{ProjectType: "invalid"}); err == nil {
		t.Fatal("fixture must fail")
	}
	if len(bc.Calls) != 0 {
		t.Fatal("failed spawn touched custody", bc.Calls)
	}
}

func beadMutations(calls []string) []string {
	var writes []string
	for _, call := range calls {
		if !strings.HasPrefix(call, "Details:") && !strings.HasPrefix(call, "Show:") && !strings.HasPrefix(call, "List:") {
			writes = append(writes, call)
		}
	}
	return writes
}
