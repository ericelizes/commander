package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
)

func closureMock(t *testing.T) showByID {
	m := showByID{MockClient: &beads.MockClient{}, by: map[string]*beads.Bead{
		"a": {ID: "a", Title: "Prerequisite", Status: "open"},
		"b": {ID: "b", Title: "Waits on a", Status: "open"},
	}}
	m.DepsResult = map[string][]beads.Dep{"b": {{IssueID: "b", DependsOnID: "a", Type: "blocks"}}}
	m.DependentsResult = map[string][]beads.Dep{"a": {{IssueID: "b", DependsOnID: "a", Type: "blocks"}}}
	setupObjectiveProject(t, m)
	return m
}

func TestDepRmRunsTheClosureCheck(t *testing.T) {
	m := closureMock(t)
	t.Setenv("COMMANDER_ROLE", "manager-x")
	code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalDep([]string{"--project", "manifest", "rm", "b", "a"}, o, e)
	})
	if code == 0 || !strings.Contains(out, "no holder") || !strings.Contains(out, "b") || wrote(m, "RemoveDep") {
		t.Fatalf("dep rm of the only blocker must be refused: %d %s %v", code, out, m.Calls)
	}
	if !strings.Contains(out, "internal-set-objective") || strings.Contains(out, "Hold it in the same command: a running Operator (--owner") {
		t.Fatalf("dep rm's refusal must name verbs it can run first, not flags it does not take (m8): %s", out)
	}
	// The user (no role) is never refused: the receipt goes on b before the edge goes.
	t.Setenv("COMMANDER_ROLE", "")
	m2 := closureMock(t)
	code, out = runVerb(t, func(o, e *os.File) int {
		return runInternalDep([]string{"--project", "manifest", "rm", "b", "a"}, o, e)
	})
	receipt, removed := -1, -1
	for i, c := range m2.Calls {
		if strings.HasPrefix(c, "SetLabel:b|") && strings.Contains(c, "Manager_20to_20start") && strings.Contains(c, "on=true") && receipt < 0 {
			receipt = i
		}
		if strings.HasPrefix(c, "RemoveDep") && removed < 0 {
			removed = i
		}
	}
	if code != 0 || receipt < 0 || removed < 0 || receipt > removed {
		t.Fatalf("the user's dep rm: want the Manager's receipt on b before the edge is removed: %d %s %v", code, out, m2.Calls)
	}
}

// The user's explicit --leave-unheld lets the write through, recorded on the bead and in the log;
// an agent with the flag is refused, and a reason too short records nothing.
func TestLeaveUnheldIsTheUsersRecordedEscape(t *testing.T) {
	use := func(reason string) {
		orig, origRec := leaveUnheldReason, leaveUnheldRecorded
		leaveUnheldReason, leaveUnheldRecorded = reason, false
		t.Cleanup(func() { leaveUnheldReason, leaveUnheldRecorded = orig, origRec })
	}
	dep := func() (int, string) {
		return runVerb(t, func(o, e *os.File) int {
			return runInternalDep([]string{"--project", "manifest", "rm", "b", "a"}, o, e)
		})
	}
	t.Setenv("COMMANDER_ROLE", "manager-x")
	m := closureMock(t)
	use("the user wants it parked by hand later")
	if code, out := dep(); code == 0 || wrote(m, "RemoveDep") || !strings.Contains(out, "is the user's") {
		t.Fatalf("an agent's --leave-unheld: %d %s", code, out)
	}
	t.Setenv("COMMANDER_ROLE", "")
	m = closureMock(t)
	use("short")
	if code, _ := dep(); code == 0 || wrote(m, "RemoveDep") {
		t.Fatal("a short reason passed")
	}
	m = closureMock(t)
	use("the user wants it parked by hand later")
	if code, out := dep(); code != 0 || !wrote(m, "RemoveDep") || !wrote(m, "AddComment") {
		t.Fatalf("the user's --leave-unheld: %d %s %v", code, out, m.Calls)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("COMMANDER_HOME"), "logs", "leave-unheld.jsonl"))
	if err != nil || !strings.Contains(string(raw), "parked by hand") {
		t.Fatalf("log: %v %s", err, raw)
	}
	if got, reason := stripLeaveUnheld([]string{"internal-dep", "--leave-unheld", "why it is so", "rm", "b", "a"}); reason != "why it is so" || strings.Join(got, " ") != "internal-dep rm b a" {
		t.Fatalf("strip: %v %q", got, reason)
	}
}

func TestDepRmKeepsAHolderPassing(t *testing.T) {
	m := closureMock(t)
	m.by["b"].Labels = []string{"waiting:1:user"} // another holder remains
	t.Setenv("COMMANDER_ROLE", "manager-x")
	if code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalDep([]string{"--project", "manifest", "rm", "b", "a"}, o, e)
	}); code != 0 || !wrote(m, "RemoveDep") {
		t.Fatalf("a wait still holds b: %d %s", code, out)
	}
}

func TestSetObjectiveOwnerNoneRunsTheClosureCheck(t *testing.T) {
	m := closureMock(t)
	m.by["b"] = &beads.Bead{ID: "b", Title: "Owned step", Status: "in_progress", Labels: []string{"owner:operator-a"}}
	m.DepsResult = nil
	registerRunningOperator(t, "operator-a")
	t.Setenv("COMMANDER_ROLE", "manager-x")
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "b", "--owner", "none"}, stdout, stderr)
	_, errOut := read()
	if code == 0 || !strings.Contains(errOut, "no holder") {
		t.Fatalf("dropping the only holder must be refused: %d %s", code, errOut)
	}
}

// Moving a parent's last open child away (track convert, a sidebar place) empties it: the parent
// is held by the Manager to plan wait, written before the move, never left unheld.
func TestTrackConvertHoldsTheParentItEmpties(t *testing.T) {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "p", Title: "Old home", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "k", Title: "Moving", Labels: []string{"kind:objective", "waiting:1:ci:7"}})
	m.Seed(beads.Bead{ID: "w", Title: "New home", Labels: []string{"kind:objective", "kind:workstream"}})
	m.Edges = []beads.Dep{{IssueID: "k", DependsOnID: "p", Type: "parent-child"}}
	setupObjectiveProject(t, m)
	t.Setenv("COMMANDER_ROLE", "manager-x")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalTrack([]string{"convert", "--project", "manifest", "--bead", "k", "--into", "w"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("move: %s", e)
	}
	ctx := context.Background()
	if d, _ := m.Details(ctx, "", []string{"k"}); d["k"].Parent != "w" {
		t.Fatal("not moved")
	}
	p, _ := m.Show(ctx, "", "p")
	if ws := beads.WaitsOf(p.Labels); len(ws) != 1 || ws[0].Ref != beads.ManagerPlanWait {
		t.Fatalf("the emptied parent is not held: %v", p.Labels)
	}
}

// Work a write gives back to its Manager by design (a failed spawn's task removed, an overrule)
// is held by a Manager dispatch wait before the write, never left unheld.
func TestHoldReleasedHandsTheStepToItsManager(t *testing.T) {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "s", Title: "Step"})
	m.Seed(beads.Bead{ID: "s.1", Title: "Task", Labels: []string{"kind:task"}})
	m.Edges = []beads.Dep{{IssueID: "s.1", DependsOnID: "s", Type: "parent-child"}}
	setupObjectiveProject(t, m)
	ctx := context.Background()
	held := holdReleased(ctx, m, "", "s.1", map[string]holding.Edit{"s.1": func(b *holding.Bead) { b.Status = holding.StatusClosed }}, "manager-s", beads.ManagerDispatchWait)
	if len(held) != 1 || held[0] != "s" {
		t.Fatalf("held %v, want [s]", held)
	}
	b, _ := m.Show(ctx, "", "s")
	if ws := beads.WaitsOf(b.Labels); len(ws) != 1 || ws[0].Ref != beads.ManagerDispatchWait || !slices.Contains(b.Labels, "owner:manager-s") {
		t.Fatalf("labels %v", b.Labels)
	}
}

// A respawn that fails leaves the bead to its Manager with Manager to recover, written before the
// status drops the running claim (never "open, no owner").
func TestOperatorProviderFailedRespawnHoldsTheBead(t *testing.T) {
	accountHome(t, []string{"work"})
	_, sp, _ := stubProviderDeps(t)
	sp.err = errors.New("no capacity")
	deps, _ := operatorProviderDepsFn()
	mock := deps.Beads.(*beads.MockClient)
	var out, errOut bytes.Buffer
	if code := runInternalOperatorProvider([]string{"operator-op-a-ws1-1", "--account", "work"}, &out, &errOut); code == 0 {
		t.Fatal("a failed respawn passed")
	}
	wait, open := -1, -1
	for i, c := range mock.Calls {
		if strings.HasPrefix(c, "SetLabel:ws1.1.1|") && strings.Contains(c, "Manager_20to_20recover") {
			wait = i
		}
		if c == "SetStatus:ws1.1.1|open" || strings.HasPrefix(c, "SetStatus:ws1.1.1") && strings.Contains(c, "open") {
			open = i
		}
	}
	if wait < 0 || open < 0 || wait > open {
		t.Fatalf("want the recover wait before the status, calls:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

// Every verb that can drop a holder asks the closure check (a parse registry, like the launcher one):
// a new write path that releases work must be added here and call requireHeldAfter.
func TestEveryHolderDroppingVerbRunsTheClosureCheck(t *testing.T) {
	for file, want := range map[string]int{
		"internal_bead_write.go":        1, // dep rm
		"internal_gate.go":              1, // requireGateClosesHeld: pass, rm, auto check
		"internal_gate_templates.go":    1, // apply
		"internal_objective_edit.go":    3, // not-started / owner none / waiting none, unpark, close
		"internal_track.go":             1, // track convert (and place under a parent): requireMoveHeld
		"internal_place.go":             2, // place at the top level, unwrap
		"proposals.go":                  1, // overrule: what it releases goes to the Manager
		"internal_spawn_operator.go":    2, // failure: the removed task's step, a task that stays
		"internal_operator_provider.go": 1, // failed respawn: Manager to recover
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, call := range []string{"requireHeldAfter(", "requireHeldAfterSkipping(", "requireGateClosesHeld(", "requireMoveHeld(", "holdReleased(", "holdReleasedLive(", "holdForManager(", "holdWithManager("} {
			n += strings.Count(string(raw), call)
		}
		switch file {
		case "internal_gate.go", "internal_track.go":
			n -= 1 // its own definition
		}
		if n < want {
			t.Errorf("%s: %d closure checks, want at least %d", file, n, want)
		}
	}
}

// Every holder refusal is one template ("refused: <verb> would leave <bead> ... Nothing was ...")
// and its fix names only verbs main dispatches and flags the objective verbs define, so the
// command it teaches runs.
func TestHolderRefusalsNameExistingVerbsAndFlags(t *testing.T) {
	verbs := cmdrVerbsInMain(t)
	src, err := os.ReadFile("internal_objective_edit.go")
	if err != nil {
		t.Fatal(err)
	}
	for name, fix := range map[string]string{"holdFlags": holdFlags, "thenFlags": thenFlags} {
		for _, m := range regexp.MustCompile(`cmdr (internal-[a-z-]+)`).FindAllStringSubmatch(fix, -1) {
			if !verbs[m[1]] {
				t.Errorf("%s names cmdr %s, which main.go does not dispatch", name, m[1])
			}
		}
		for _, m := range regexp.MustCompile(`--([a-z][a-z-]+)`).FindAllStringSubmatch(fix, -1) {
			if !strings.Contains(string(src), `"`+m[1]+`",`) {
				t.Errorf("%s names --%s, which no objective verb defines", name, m[1])
			}
		}
	}
	m := closureMock(t)
	_ = m
	t.Setenv("COMMANDER_ROLE", "manager-x")
	code, out := runVerb(t, func(o, e *os.File) int {
		return runInternalDep([]string{"--project", "manifest", "rm", "b", "a"}, o, e)
	})
	if code == 0 || !regexp.MustCompile(`refused: .* would leave b .*Hold it in the same command: .*Nothing was written`).MatchString(out) {
		t.Fatalf("refusal text: %s", out)
	}
	gateMem(t, "commander")
	code, out = runVerb(t, func(o, e *os.File) int {
		return runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, o, e)
	})
	if code == 0 || !regexp.MustCompile(`refused: closing w\.9 would leave w\.2 .*Nothing was closed`).MatchString(out) {
		t.Fatalf("close refusal text: %s", out)
	}
}

// allowUnheld runs a test as the user passing --leave-unheld reason: for fixtures whose holders
// are not what the test is about.
func allowUnheld(t *testing.T, reason string) {
	t.Helper()
	orig, origRec := leaveUnheldReason, leaveUnheldRecorded
	leaveUnheldReason, leaveUnheldRecorded = reason, false
	t.Cleanup(func() { leaveUnheldReason, leaveUnheldRecorded = orig, origRec })
}

func hasStartReceipt(t *testing.T, m *beads.MemClient, id string) bool {
	t.Helper()
	b, _ := m.Show(context.Background(), "", id)
	for _, w := range beads.WaitsOf(b.Labels) {
		if w.Ref == beads.ManagerStartWait {
			return true
		}
	}
	return false
}

// M5: --state not-started on a needs-user bead takes the card away too; the closure check
// simulates all of it, so an agent is refused, and the user's write leaves the Manager's receipt.
func TestNotStartedOnACardKeepsAHolder(t *testing.T) {
	seed := func() *beads.MemClient {
		m := &beads.MemClient{}
		m.Seed(beads.Bead{ID: "w", Title: "Area", Status: "open", Labels: []string{"kind:objective"}})
		m.Seed(beads.Bead{ID: "w.1", Title: "Pick a name", Status: "open", Labels: []string{"kind:objective", beads.NeedsUserLabel}, ParentID: "w"})
		setupObjectiveProject(t, m)
		return m
	}
	m := seed()
	t.Setenv("COMMANDER_ROLE", "manager-x")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "w.1", "--state", "not-started"}, stdout, stderr); code == 0 {
		t.Fatal("an agent took the only holder (the card) off w.1")
	}
	if _, e := read(); !strings.Contains(e, "no holder") {
		t.Fatalf("stderr %s", e)
	}
	if b, _ := m.Show(context.Background(), "", "w.1"); !slices.Contains(b.Labels, beads.NeedsUserLabel) {
		t.Fatalf("a refused write changed w.1: %v", b.Labels)
	}
	m = seed()
	t.Setenv("COMMANDER_ROLE", "")
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "w.1", "--state", "not-started"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("the user's write was refused: %s", e)
	}
	if !hasStartReceipt(t, m, "w.1") {
		b, _ := m.Show(context.Background(), "", "w.1")
		t.Fatalf("the user's write left w.1 with no holder: %v", b.Labels)
	}
}

// M2: the user's --owner none / --waiting none (the app sends no role) are never refused; the step
// they leave unheld carries the Manager's receipt once the write landed.
func TestUsersHolderDropGetsTheManagersReceipt(t *testing.T) {
	for name, args := range map[string][]string{
		"owner none":   {"--owner", "none"},
		"waiting none": {"--waiting", "none"},
	} {
		t.Run(name, func(t *testing.T) {
			m := &beads.MemClient{}
			m.Seed(beads.Bead{ID: "w", Title: "Area", Status: "open", Labels: []string{"kind:objective"}})
			m.Seed(beads.Bead{ID: "w.1", Title: "Build", Status: "in_progress", Labels: []string{"kind:objective", "owner:operator-a", "waiting:1:review:rev"}, ParentID: "w"})
			setupObjectiveProject(t, m)
			if name == "waiting none" {
				_ = m.SetLabel(context.Background(), "", "w.1", "owner:operator-a", false)
			}
			t.Setenv("COMMANDER_ROLE", "")
			if name == "owner none" {
				_ = m.SetLabel(context.Background(), "", "w.1", "waiting:1:review:rev", false)
				registerRunningOperator(t, "operator-a")
			}
			stdout, stderr, read := withTempOutputFiles(t)
			if code := runInternalSetObjective(append([]string{"--project", "manifest", "--bead", "w.1"}, args...), stdout, stderr); code != 0 {
				_, e := read()
				t.Fatalf("refused: %s", e)
			}
			if out, _ := read(); !hasStartReceipt(t, m, "w.1") || !strings.Contains(out, `"custody":["w.1"]`) {
				b, _ := m.Show(context.Background(), "", "w.1")
				t.Fatalf("no receipt: %v %s", b.Labels, out)
			}
		})
	}
}
