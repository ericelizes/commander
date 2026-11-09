package main

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// The diagram's door: writes that would leave the derived diagram unable to
// read right are refused with the fix; the right write passes.

func createRefused(t *testing.T, mock *beads.MockClient, args []string, want string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCreateObjective(args, stdout, stderr); code == 0 {
		t.Fatalf("%v: passed the door", args)
	}
	if _, errOut := read(); !strings.Contains(errOut, want) {
		t.Fatalf("stderr %q, want %q", errOut, want)
	}
	if len(mock.Calls) != 0 {
		t.Fatalf("a refused create wrote: %v", mock.Calls)
	}
}

func TestDiagramDoor_GateWithoutCriteriaIsRefused(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.4"}
	setupObjectiveProject(t, mock)
	createRefused(t, mock, []string{"--project", "manifest", "--parent", "obj-1", "--title", "PR approved", "--gate", "--approver", "reviewer-a"}, "a gate needs --criteria")
}

func TestDiagramDoor_GreenlightTheUserDoesNotApproveIsRefused(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.4"}
	setupObjectiveProject(t, mock)
	createRefused(t, mock, []string{"--project", "manifest", "--parent", "obj-1", "--title", "Greenlight merging", "--gate", "--approver", "reviewer-a", "--criteria", "x"}, "does not include user")
}

func TestDiagramDoor_GateWithCriteriaPasses(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.4"}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--parent", "obj-1", "--title", "Greenlight merging the export", "--gate", "--approver", "user", "--criteria", "The user says merge"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	if calls := strings.Join(mock.Calls, "\n"); !strings.Contains(calls, "acceptance=The user says merge") {
		t.Fatalf("criteria not written as acceptance criteria:\n%s", calls)
	}
}

func TestDiagramDoor_CriteriaWithoutGateIsRefused(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.4"}
	setupObjectiveProject(t, mock)
	createRefused(t, mock, []string{"--project", "manifest", "--parent", "obj-1", "--title", "Build it", "--criteria", "x"}, "go with --gate")
}

func TestDiagramDoor_LaneThatIsNotALaneIsRefused(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open"}}
	setupObjectiveProject(t, mock)
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--lane", "operators"}, "is not a lane")
	if len(mock.Calls) != 0 {
		t.Fatalf("a refused lane wrote: %v", mock.Calls)
	}
}

func TestDiagramDoor_LaneOnAGateIsRefused(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user"}}}
	setupObjectiveProject(t, mock)
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--lane", "elsewhere:legal"}, "it is a gate")
	if strings.Contains(strings.Join(mock.Calls, "\n"), "SetLabel") {
		t.Fatalf("a refused lane changed labels:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

func TestDiagramDoor_LaneOnAStepPasses(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open", Labels: []string{"kind:objective"}}}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--lane", "elsewhere:the finance team"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	if calls := strings.Join(mock.Calls, "\n"); !strings.Contains(calls, "label="+beads.LaneLabel("elsewhere", "the finance team")+"|on=true") {
		t.Fatalf("lane label not written:\n%s", calls)
	}
}

func TestDiagramDoor_CardThatDrawsItsOwnWorkflowIsRefused(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open"}}
	setupObjectiveProject(t, mock)
	card := `{"question": "Ship it?", "context": "**The plan**\n- ship\n\n**Execution workflow**\n` + "```cmdr-graph\\n{\\\"nodes\\\": []}\\n```" + `", "options": [{"label": "Go", "approves": true}]}`
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--decision", card}, "draws the workflow under every card from beads")
	if _, err := loadDecision(`{"question": "Ship it?", "context": "**The plan**\n- ship", "options": [{"label": "Go"}]}`); err != nil {
		t.Fatalf("a card without a drawn workflow: %v", err)
	}
}
