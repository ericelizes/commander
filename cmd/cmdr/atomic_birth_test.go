package main

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func birthWaits(call string) []beads.Wait {
	var out []beads.Wait
	for _, f := range strings.FieldsFunc(call, func(r rune) bool { return r == ',' || r == ' ' || r == '=' || r == '|' }) {
		if w, ok := beads.ParseWaitLabel(f); ok {
			out = append(out, w)
		}
	}
	return out
}

// A plain step that names no holder is born carrying its Manager's "Manager to start"
// receipt in the same create, never refused and never unheld; a Manager owner is still refused.
func TestCreateStepNamingNoHolderIsBornWithTheManagersReceipt(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.1"}
	setupObjectiveProject(t, mock)
	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "plain step", "--parent", "obj-1"}, stdout, stderr)
	if _, errOut := readOutput(); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	waits := birthWaits(createCall(mock))
	if len(waits) != 1 || waits[0].Ref != beads.ManagerStartWait {
		t.Fatalf("born without the receipt in the create: %v %v", waits, mock.Calls)
	}
	stdout, stderr, readOutput = withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "plain step", "--parent", "obj-1", "--owner", "manager-x"}, stdout, stderr); code == 0 {
		t.Fatalf("a Manager owner must be refused: %s", func() string { _, e := readOutput(); return e }())
	}
}

// m1: a named blocker that is closed holds nothing, so the step gets the receipt after the links.
func TestCreateBlockedByAClosedBeadGetsTheReceipt(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "obj-1", Title: "Area", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "obj-1.0", Title: "Done", Status: "closed", Labels: []string{"kind:objective"}, ParentID: "obj-1"})
	setupObjectiveProject(t, m)
	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "plain step", "--parent", "obj-1", "--deps", "blocked-by:obj-1.0"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if !strings.Contains(out, `"custody"`) {
		t.Fatalf("a step behind a closed blocker got no receipt: %s", out)
	}
}

func TestCreateStepBornHeld(t *testing.T) {
	for name, args := range map[string][]string{
		"wait":    {"--waiting", "review:rev"},
		"blocker": {"--deps", "blocked-by:obj-1.0"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := &beads.MockClient{CreateID: "obj-1.1"}
			setupObjectiveProject(t, mock)
			stdout, stderr, readOutput := withTempOutputFiles(t)
			code := runInternalCreateObjective(append([]string{"--project", "manifest", "--title", "plain step", "--parent", "obj-1"}, args...), stdout, stderr)
			if _, errOut := readOutput(); code != 0 {
				t.Fatalf("exit=%d %s", code, errOut)
			}
		})
	}
}

func TestWorkstreamBornPlanning(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-9"}
	setupObjectiveProject(t, mock)
	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "new area"}, stdout, stderr)
	if _, errOut := readOutput(); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	waits := birthWaits(createCall(mock))
	if len(waits) != 1 || waits[0].Ref != beads.ManagerPlanWait {
		t.Fatalf("a workstream is born with the Manager to plan wait in the same create: %v %v", waits, mock.Calls)
	}
}

func dispatchLabelWrites(m showByID) int {
	n := 0
	for _, c := range m.Calls {
		if strings.HasPrefix(c, "SetLabel:") && strings.Contains(c, "on=true") && strings.Contains(c, "Manager_20dispatch") {
			n++
		}
	}
	return n
}

// A Manager's name is accountability, not a holder: the same write adds its dispatch wait.
func TestManagerOwnerWritesDispatch(t *testing.T) {
	m := reviewMock(t)
	runOK(t, runInternalSetObjective, "--project", "manifest", "--bead", "x-1", "--state", "in-progress", "--owner", "manager-x", "--writes-workflow")
	if dispatchLabelWrites(m) != 1 {
		t.Fatalf("a Manager-owned step gets exactly one dispatch wait: %v", m.Calls)
	}
}

func TestManagerOwnerKeepsAnExistingHolder(t *testing.T) {
	m := reviewMock(t)
	m.by["x-1"].Labels = []string{"waiting:1:user"}
	m.by["x-2"].Labels = []string{"w:to8tqk:un:to8ttw:Manager_20dispatch"}
	for _, id := range []string{"x-1", "x-2"} {
		runOK(t, runInternalSetObjective, "--project", "manifest", "--bead", id, "--owner", "manager-x")
	}
	if dispatchLabelWrites(m) != 0 {
		t.Fatalf("a step with a wait already is held by it: %v", m.Calls)
	}
}
