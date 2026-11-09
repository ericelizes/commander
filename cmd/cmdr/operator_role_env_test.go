package main

import (
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// operatorRole has the shape a launcher sets (spawn.hostOperatorCommand): the
// stored AgentName, dots of the bead id included.
const operatorRole = "operator-op-x-w.2"

// What a host Operator is refused now that its launcher sets COMMANDER_ROLE.
func TestOperatorRole_Refusals(t *testing.T) {
	cases := []struct {
		name string
		run  func(o, e *os.File) int
		want string
	}{
		{"worktrees gc", func(o, e *os.File) int { return runWorktrees([]string{"gc", "--project", "manifest"}, o, e) }, "Operators must not run it"},
		{"push reset", func(o, e *os.File) int { return runPushReset("manifest", "w.2", o, e) }, "Commander and the user"},
		{"proposals accept", func(o, e *os.File) int {
			return runProposals([]string{"accept", "--project", "manifest", "--bead", "w.2"}, o, e)
		}, "only Commander"},
		{"decisions add", func(o, e *os.File) int {
			return runDecisions([]string{"add", "--words", "no friday deploys"}, o, e)
		}, "does not record"},
		{"decisions retire", func(o, e *os.File) int { return runDecisions([]string{"retire", "d1", "--reason", "changed"}, o, e) }, "Commander"},
		{"park", func(o, e *os.File) int {
			return runInternalSetObjective([]string{"--project", "manifest", "--bead", "w.2", "--park"}, o, e)
		}, "cannot park"},
		{"hold", func(o, e *os.File) int {
			return runInternalSetObjective([]string{"--project", "manifest", "--bead", "w.2", "--hold", "stop"}, o, e)
		}, "cannot set or clear a hold"},
		{"card check", func(o, e *os.File) int {
			return runInternalSetObjective([]string{"--project", "manifest", "--bead", "w.2", "--card-check", "still-right", "--reason", "looked at the spec again"}, o, e)
		}, "cannot confirm a card"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}}
			setupObjectiveProject(t, mock)
			t.Setenv("COMMANDER_ROLE", operatorRole)
			stdout, stderr, read := withTempOutputFiles(t)
			if code := c.run(stdout, stderr); code == 0 {
				t.Fatal("an Operator passed")
			}
			if _, errOut := read(); !strings.Contains(errOut, c.want) {
				t.Fatalf("stderr %q, want %q", errOut, c.want)
			}
			for _, call := range mock.Calls {
				if !strings.HasPrefix(call, "Show") && !strings.HasPrefix(call, "List") {
					t.Fatalf("a refused verb wrote: %s", call)
				}
			}
		})
	}
}

// What a host Operator does every run keeps working with the variable set.
func TestOperatorRole_OwnWritesStillPass(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "x-1", Status: "in_progress"}}
	setupObjectiveProject(t, mock)
	t.Setenv("COMMANDER_ROLE", operatorRole)
	stdout, stderr, read := withTempOutputFiles(t)
	for name, code := range map[string]int{
		"internal-comment": runInternalComment([]string{"--project", "manifest", "--bead", "x-1", "--text", "final report"}, strings.NewReader(""), stdout, stderr),
		"internal-create":  runInternalCreateObjective([]string{"--project", "manifest", "--title", "sub task", "--parent", "x-1", "--operational"}, stdout, stderr),
		"internal-dep":     runInternalDep([]string{"--project", "manifest", "add", "x-2", "x-1"}, stdout, stderr),
		"internal-label":   runInternalLabel([]string{"--project", "manifest", "--bead", "x-1", "add", "note"}, stdout, stderr),
		"internal-update":  runInternalUpdate([]string{"--project", "manifest", "--bead", "x-1", "--notes", "n"}, strings.NewReader(""), stdout, stderr),
	} {
		if code != 0 {
			_, e := read()
			t.Errorf("%s refused an Operator: %s", name, e)
		}
	}
}

// An Operator finishing its own task, or a step under it, passes the close door
// as it did with no role: knowledge Operators close their beads by design.
func TestCloseDoor_OperatorClosingItsOwnWorkPasses(t *testing.T) {
	own := []string{"owner:operator-op-x-w-2"} // the label's dashed spelling of operatorRole
	for name, seed := range map[string][]beads.Bead{
		"its task":        {{ID: "w", Status: "open"}, {ID: "w.9", Title: "Report", Status: "in_progress", Labels: own}},
		"a step under it": {{ID: "w", Status: "open", Labels: own}, {ID: "w.9", Title: "Report", Status: "open"}},
	} {
		t.Run(name, func(t *testing.T) {
			m := &beads.MemClient{}
			for _, b := range seed {
				m.Seed(b)
			}
			setupObjectiveProject(t, m)
			withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), operatorRole)
			stdout, stderr, read := withTempOutputFiles(t)
			if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
				_, e := read()
				t.Fatalf("own close refused: %s", e)
			}
		})
	}
}

func TestCloseDoor_OperatorClosingAnotherBeadIsJudged(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "w", Status: "open"})
	m.Seed(beads.Bead{ID: "w.9", Title: "Report", Status: "in_progress", Labels: []string{"owner:operator-someone-else"}})
	setupObjectiveProject(t, m)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), operatorRole)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code == 0 {
		t.Fatal("a close of someone else's bead that leaves w.2 unheld passed")
	}
	if _, e := read(); !strings.Contains(e, "State missing") {
		t.Fatalf("stderr %q", e)
	}
}
