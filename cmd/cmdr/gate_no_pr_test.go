package main

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

const noPRWhy = "deploy on a VM, nothing to merge"

// seedNoPRShip is a stage that ships with no PR and its merged gate.
func seedNoPRShip(t *testing.T) *beads.MemClient {
	t.Helper()
	mem := setupGateProject(t)
	mem.Seed(beads.Bead{ID: "ms-1", Title: "Ship", Labels: []string{"kind:objective"}})
	_ = mem.SetParent(context.Background(), "/d", "ms-1.2", "ms-1")
	pinWorkflowRepo(t, "acme/widgets")
	pinWorkflowGH(t, workflowFakeGH)
	if code, _, errOut := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged", "--check", "the ship is live", "--verifier", "user"); code != 0 {
		t.Fatal(errOut)
	}
	return mem
}

func mergedGate(t *testing.T, mem *beads.MemClient) beads.Bead {
	t.Helper()
	for _, b := range mem.Beads {
		if hasLabel(b.Labels, "gate-name:merged") {
			return *b
		}
	}
	t.Fatal("no merged gate")
	return beads.Bead{}
}

func waiverNotes(t *testing.T, mem *beads.MemClient, id string) []string {
	t.Helper()
	cs, _ := mem.Comments(context.Background(), "/d", id)
	var out []string
	for _, c := range cs {
		if strings.HasPrefix(c.Text, noPRWaiverPrefix) {
			out = append(out, c.Text)
		}
	}
	return out
}

func TestGatePassNoPR_ApprovedShipPassesOnTheRecord(t *testing.T) {
	mem := seedNoPRShip(t)
	code, out, errOut := gateCmd(t, "pass", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged",
		"--evidence", "answer TEAM-12-a1: the user approved the deploy", "--no-pr", noPRWhy)
	if code != 0 {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	g := mergedGate(t, mem)
	if g.Status != "closed" || !strings.Contains(out, `"passed"`) {
		t.Fatalf("gate not passed: %s %s", g.Status, out)
	}
	notes := waiverNotes(t, mem, g.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], noPRWhy) || !strings.Contains(notes[0], "TEAM-12-a1") || !strings.Contains(notes[0], "the user") {
		t.Fatalf("waiver not recorded: %q", notes)
	}
}

func TestGateNoPR_RefusesWithoutWhyOrApproval(t *testing.T) {
	mem := seedNoPRShip(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--evidence", "answer a1", "--no-pr", "no PR"}, "15 characters"},
		{[]string{"--no-pr", noPRWhy}, "--evidence"},
	} {
		args := append([]string{"rm", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged"}, tc.args...)
		if code, _, errOut := gateCmd(t, args...); code == 0 || !strings.Contains(errOut, tc.want) {
			t.Fatalf("%v: %d %s", tc.args, code, errOut)
		}
	}
	if code, _, errOut := gateCmd(t, "check", "--project", "manifest", "--bead", "ms-1.2", "--no-pr", noPRWhy); code == 0 || !strings.Contains(errOut, "only supported") {
		t.Fatal(code, errOut)
	}
	if g := mergedGate(t, mem); g.Status == "closed" || len(waiverNotes(t, mem, g.ID)) != 0 {
		t.Fatal("a refused waiver changed the gate")
	}
}

// A real PR is never waived: a pr: label (here on the stage) still needs its block.
func TestGateNoPR_RefusesALivePR(t *testing.T) {
	mem := seedNoPRShip(t)
	_ = mem.SetLabel(context.Background(), "/d", "ms-1.2", "pr:7", true)
	code, _, errOut := gateCmd(t, "pass", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged",
		"--evidence", "answer a1", "--no-pr", noPRWhy)
	if code == 0 || !strings.Contains(errOut, "has a PR to review") {
		t.Fatal(code, errOut)
	}
	if g := mergedGate(t, mem); g.Status == "closed" || len(waiverNotes(t, mem, g.ID)) != 0 {
		t.Fatal("a live PR's gate was waived")
	}
}

func TestGateNoPR_WhoMayWaive(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		pass bool
	}{
		{map[string]string{"COMMANDER_ROLE": "operator-op-x-1"}, false},
		{map[string]string{"COMMANDER_ROLE": "", "COMMANDER_MANAGER_ROLE": "manager-ms-1"}, false},
		{map[string]string{"COMMANDER_ROLE": "manager-other"}, false},
		{map[string]string{"COMMANDER_ROLE": "manager-ms-1"}, true},
		{map[string]string{"COMMANDER_ROLE": "commander"}, true},
	} {
		t.Run(tc.env["COMMANDER_ROLE"]+tc.env["COMMANDER_MANAGER_ROLE"], func(t *testing.T) {
			mem := seedNoPRShip(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			code, _, errOut := gateCmd(t, "rm", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged",
				"--evidence", "answer a1", "--no-pr", noPRWhy)
			if (code == 0) != tc.pass {
				t.Fatalf("exit=%d: %s", code, errOut)
			}
			g := mergedGate(t, mem)
			if !tc.pass && (len(waiverNotes(t, mem, g.ID)) != 0 || hasLabel(g.Labels, "retired") || g.Status == "closed") {
				t.Fatal("a refused caller changed the gate")
			}
			if tc.pass && len(waiverNotes(t, mem, g.ID)) != 1 {
				t.Fatal("no record of the waiver")
			}
		})
	}
}

func TestGateApplyNoPR_ReplacesTheMergedGate(t *testing.T) {
	mem := seedNoPRShip(t)
	if code, _, errOut := gateCmd(t, "apply", "--project", "manifest", "--bead", "ms-1.2", "--template", "spec",
		"--evidence", "answer a1", "--no-pr", noPRWhy); code != 0 {
		t.Fatal(errOut)
	}
	if len(waiverNotes(t, mem, mergedGate(t, mem).ID)) != 1 {
		t.Fatal("no record of the waiver")
	}
	gs := gatesNow(t, "ms-1.2")
	if len(gs) != 1 || gs[0].Name != "spec" {
		t.Fatalf("gates: %+v", gs)
	}
}

// The standard merged gate names its PR only in its check (no pr: label): the
// rule reads it as having no PR, but the waiver must not take it for one.
func TestGateNoPR_RefusesAGateWhoseCheckNamesAPR(t *testing.T) {
	for _, verb := range []string{"pass", "rm", "apply"} {
		t.Run(verb, func(t *testing.T) {
			mem := setupGateProject(t)
			mem.Seed(beads.Bead{ID: "ms-1", Title: "Ship", Labels: []string{"kind:objective"}})
			_ = mem.SetParent(context.Background(), "/d", "ms-1.2", "ms-1")
			pinWorkflowRepo(t, "acme/widgets")
			pinWorkflowGH(t, workflowFakeGH)
			if code, _, errOut := gateCmd(t, "apply", "--project", "manifest", "--bead", "ms-1.2", "--template", "merged", "--pr", "7", "--repo", "acme/widgets"); code != 0 {
				t.Fatal(errOut)
			}
			g := mergedGate(t, mem)
			// merged is an auto gate: pass refuses it by verifier; make it a person's
			// check naming the PR so pass reaches the waiver.
			if verb == "pass" {
				if code, _, errOut := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged", "--check", "PR #7 merged on GitHub", "--verifier", "user"); code != 0 {
					t.Fatal(errOut)
				}
				g = mergedGate(t, mem)
			}
			args := map[string][]string{
				"pass":  {"pass", "--name", "merged"},
				"rm":    {"rm", "--name", "merged"},
				"apply": {"apply", "--template", "spec"},
			}[verb]
			args = append(args, "--project", "manifest", "--bead", "ms-1.2", "--evidence", "answer a1", "--no-pr", noPRWhy)
			code, _, errOut := gateCmd(t, args...)
			if code == 0 || !strings.Contains(errOut, "check names a PR") {
				t.Fatalf("exit=%d: %s", code, errOut)
			}
			g = mergedGate(t, mem)
			if g.Status == "closed" || hasLabel(g.Labels, "retired") || len(waiverNotes(t, mem, g.ID)) != 0 {
				t.Fatalf("a gate naming PR 7 was waived: %+v", g)
			}
		})
	}
}

func TestNamesPR(t *testing.T) {
	for text, want := range map[string]bool{
		`[ "$(gh pr view 7 --repo acme/widgets --json state --jq .state)" = MERGED ]`: true,
		"gh pr checks 12 green":                     true,
		"https://github.com/acme/widgets/pull/31":   true,
		"PR #4 merged":                              true,
		"pull request 9 is in":                      true,
		"the ship is live":                          false,
		"deployed to the VM, smoke #2 passed":       false,
		"no PR: deploy on a VM, health check green": false,
	} {
		if got := namesPR(beads.Bead{Description: text}); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}
