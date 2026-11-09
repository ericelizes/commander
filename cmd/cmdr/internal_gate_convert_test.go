package main

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func runConvert(t *testing.T, mock beads.Client, args ...string) (int, string, string) {
	t.Helper()
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalGate(append([]string{"convert", "--project", "manifest"}, args...), stdout, stderr)
	out, errOut := read()
	return code, out, errOut
}

func convertGraph() *beads.MockClient {
	return &beads.MockClient{ShowResults: map[string]*beads.Bead{
		"ws-1":   {ID: "ws-1", Status: "open", Labels: []string{"kind:workstream"}},
		"ws-1.3": {ID: "ws-1.3", Title: "Platform lead signs off", Status: "open", Labels: []string{"kind:objective", "needs-user", "owner:manager-ws-1"}},
		"ws-1.4": {ID: "ws-1.4", Title: "Already one", Status: "open", Labels: []string{"gate"}},
		"ws-1.5": {ID: "ws-1.5", Title: "Done", Status: "closed"},
	}}
}

// A plain step becomes a gate in place: criteria and approvers first, the gate label last; its card,
// owner and waits are not touched, so no step is left flagged.
func TestGateConvertKeepsTheStepAndWritesTheGateLabelLast(t *testing.T) {
	mock := convertGraph()
	t.Setenv("COMMANDER_ROLE", "manager-ws-1")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	code, _, errOut := runConvert(t, mock, "--bead", "ws-1.3", "--approver", "lead-a", "--criteria", "The platform lead approves.")
	if code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	calls := strings.Join(mock.Calls, "\n")
	crit, appr, gate := strings.Index(calls, "UpdateFields:ws-1.3"), strings.Index(calls, "label=approver:lead-a|on=true"), strings.Index(calls, "label=gate|on=true")
	if crit < 0 || appr < 0 || gate < 0 || !(crit < appr && appr < gate) {
		t.Fatalf("order criteria %d approver %d gate %d:\n%s", crit, appr, gate, calls)
	}
	for _, c := range mock.Calls {
		if strings.Contains(c, "needs-user") || strings.Contains(c, "owner:") || strings.Contains(c, "on=false") || strings.HasPrefix(c, "SetStatus") || strings.HasPrefix(c, "Close") {
			t.Fatalf("the step's card, owner or status was touched: %s", c)
		}
	}
}

func TestGateConvertRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		role string
		args []string
		want string
	}{
		"an operator":       {"operator-x", []string{"--bead", "ws-1.3", "--approver", "user", "--criteria", "c"}, "Operators"},
		"another Manager":   {"manager-other", []string{"--bead", "ws-1.3", "--approver", "user", "--criteria", "c"}, "whose Manager is"},
		"no approver":       {"manager-ws-1", []string{"--bead", "ws-1.3", "--criteria", "c"}, "--approver"},
		"no criteria":       {"manager-ws-1", []string{"--bead", "ws-1.3", "--approver", "user"}, "--criteria"},
		"already a gate":    {"manager-ws-1", []string{"--bead", "ws-1.4", "--approver", "user", "--criteria", "c"}, "already a gate"},
		"closed":            {"manager-ws-1", []string{"--bead", "ws-1.5", "--approver", "user", "--criteria", "c"}, "only an open step"},
		"a workstream bead": {"manager-ws-1", []string{"--bead", "ws-1", "--approver", "user", "--criteria", "c"}, "not a plain step"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := convertGraph()
			t.Setenv("COMMANDER_ROLE", tc.role)
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			code, _, errOut := runConvert(t, mock, tc.args...)
			if code == 0 || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit=%d err=%s, want %q", code, errOut, tc.want)
			}
			for _, c := range mock.Calls {
				if strings.HasPrefix(c, "SetLabel") || strings.HasPrefix(c, "UpdateFields") {
					t.Fatalf("a refused convert wrote: %s", c)
				}
			}
		})
	}
}
