package main

import (
	"context"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyStateCLIIsCanonicalAndCleared(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "needs-user", "--no-notify"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	out, _ := read()
	if !strings.Contains(out, `"state":"needs-user"`) {
		t.Fatal(out)
	}
	calls := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"label=" + ownercompat.LegacyNeedsLabel + "|on=false", "label=needs-user|on=true"} {
		if !strings.Contains(calls, want) {
			t.Fatal(calls)
		}
	}
	mock.Calls = nil
	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "blocked", "--waiting", "until:2026-11-01T09:00:00Z:review window opens"}, stdout, stderr); code != 0 {
		t.Fatal(code)
	}
	calls = strings.Join(mock.Calls, "\n")
	for _, want := range []string{"label=" + ownercompat.LegacyNeedsLabel + "|on=false", "label=needs-user|on=false"} {
		if !strings.Contains(calls, want) {
			t.Fatal(calls)
		}
	}
}

func TestWorkflowGate_LegacyGreenlightApprover(t *testing.T) {
	gate := gateUnder("approval", "w1", "Greenlight adding search", "closed")
	gate.Labels = append(gate.Labels, "approver:user")
	closed, _ := firstGateOf("w1", []beads.Bead{gate}, map[string]string{gate.ID: "w1"})
	if !closed {
		t.Fatal("legacy user approver must count")
	}
}

// A retired owner name an installation lists in its legacy-owner-aliases file
// approves a prod gate; any other name still does not.
func TestSpawnOperator_ProdGateConfiguredLegacyApprover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(ownercompat.AliasesEnv, "")
	if err := os.WriteFile(filepath.Join(home, ownercompat.AliasesFile), []byte("quill-old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		approver string
		wantErr  bool
	}{"configured alias": {"quill-old", false}, "other name": {"quill-older", true}, "unlisted": {"mallory", true}} {
		t.Run(name, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			useMockBeads(t, prodGateMock("● P2 · CLOSED", []string{gates.KindLabel, "approver:" + tc.approver, "gate-of:" + beads.EscapeLabel("ms-1.2")}, "run migration 42"))
			stubMail(t)
			t.Setenv("COMMANDER_ROLE", "manager-ms-1")
			stdout, stderr, readOutput := withTempOutputFiles(t)
			code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--prod-gate", "ms-1.7"}, stdout, stderr)
			_, errOut := readOutput()
			if tc.wantErr != (code != 0) || (tc.wantErr && !strings.Contains(errOut, "not approved by the user")) {
				t.Fatalf("exit=%d stderr=%s", code, errOut)
			}
		})
	}
}

// An alias named only in the environment never counts in an agent's process:
// a Manager cannot make a closed approver:<anyone> gate pass --prod-gate by
// setting the variable on its own command line.
func TestSpawnOperator_ProdGateIgnoresEnvAliasInAgentRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(ownercompat.AliasesEnv, "mallory")
	for _, managerRole := range []string{"", "manager-ms-1"} {
		t.Run("manager role "+managerRole, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			useMockBeads(t, prodGateMock("● P2 · CLOSED", []string{gates.KindLabel, "approver:mallory", "gate-of:" + beads.EscapeLabel("ms-1.2")}, "run migration 42"))
			stubMail(t)
			t.Setenv("COMMANDER_ROLE", "manager-ms-1")
			t.Setenv("COMMANDER_MANAGER_ROLE", managerRole)
			stdout, stderr, readOutput := withTempOutputFiles(t)
			code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--prod-gate", "ms-1.7"}, stdout, stderr)
			_, errOut := readOutput()
			if code == 0 || !strings.Contains(errOut, "not approved by the user") {
				t.Fatalf("env alias widened an agent's authority: exit=%d stderr=%s", code, errOut)
			}
		})
	}
}

// A needs-<alias> label of a configured alias reads as needs-user, so every
// state edit and every answer must clear it too.
func TestConfiguredNeedsAliasIsCleared(t *testing.T) {
	writeAlias := func(t *testing.T) {
		t.Setenv(ownercompat.AliasesEnv, "")
		if err := os.WriteFile(filepath.Join(os.Getenv("COMMANDER_HOME"), ownercompat.AliasesFile), []byte("quill-old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("state edit", func(t *testing.T) {
		mock := &beads.MockClient{}
		setupObjectiveProject(t, mock)
		writeAlias(t)
		stdout, stderr, read := withTempOutputFiles(t)
		if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "blocked", "--waiting", "until:2026-11-01T09:00:00Z:review window opens"}, stdout, stderr); code != 0 {
			_, e := read()
			t.Fatal(e)
		}
		if calls := strings.Join(mock.Calls, "\n"); !strings.Contains(calls, "label=needs-quill-old|on=false") {
			t.Fatal(calls)
		}
	})
	t.Run("approve", func(t *testing.T) {
		stubMail(t)
		mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "ms-1.2", Status: "open", Labels: []string{"needs-quill-old"}}}
		setupObjectiveProject(t, mock)
		writeAlias(t)
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			t.Fatal(err)
		}
		p, _ := st.GetProjectByName(context.Background(), "manifest")
		if err := st.UpsertNeedsYou(context.Background(), store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.2", Ask: "Approve: import rollout", Note: "n"}); err != nil {
			t.Fatal(err)
		}
		cleanup()
		stdout, stderr, read := withTempOutputFiles(t)
		if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "ms-1.2", "--action", "approve"}, stdout, stderr); code != 0 {
			_, e := read()
			t.Fatal(e)
		}
		if calls := strings.Join(mock.Calls, "\n"); !strings.Contains(calls, "label=needs-quill-old|on=false") || !strings.Contains(calls, "Close:ms-1.2") {
			t.Fatal(calls)
		}
	})
	t.Run("reserved", func(t *testing.T) {
		setupObjectiveProject(t, &beads.MockClient{})
		writeAlias(t)
		if _, ok := reservedLabel("needs-quill-old"); !ok {
			t.Fatal("needs-<configured alias> must be reserved like needs-user")
		}
		if _, ok := reservedLabel("needs-mallory"); ok {
			t.Fatal("an unlisted needs- label is plain")
		}
	})
}
