package spawn

import (
	"context"
	"errors"
	"github.com/example-user/commander/internal/beads"
	promptassets "github.com/example-user/commander/prompts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaimedExecutionBootstrapAndResumeUseCanonicalContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte("read-only prompt fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	req := SpawnRequest{ExecutionClaimToken: "private-reservation", ProjectID: 42, ProjectName: "p", ProjectPath: "/project", TaskBeadID: "task", Branch: "work/claim", managerRole: "commander-urgent", TaskText: "Exact bounded implementation and acceptance criteria", ResumeNote: "Saved answer: use the bounded implementation"}
	if req.promptRole(promptassets.RoleOperatorCode) != promptassets.RoleExecutor {
		t.Fatal("claim kept the legacy role")
	}
	for _, kind := range []string{"code", "knowledge"} {
		req.ProjectType = kind
		for name, render := range map[string]func(SpawnRequest) string{"host": hostBootstrapPrompt, "knowledge-host": knowledgeHostBootstrapPrompt, "container": bootstrapPrompt, "knowledge-adapter": knowledgeBootstrapPrompt, "resume": hostResumePrompt} {
			text := render(req)
			for _, want := range []string{"project ID 42", "cmdr bead --project-dir", "commander-urgent", req.TaskText, "never edit the shared tree"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s/%s lost %q: %s", kind, name, want, text)
				}
			}
			for _, bad := range []string{"mail your Manager", "never to commander", "commit directly to the current branch", "private-reservation"} {
				if strings.Contains(text, bad) {
					t.Errorf("%s/%s contains obsolete or private instruction %q", kind, name, bad)
				}
			}
			if name == "resume" && !strings.Contains(text, req.ResumeNote) {
				t.Fatal("resume lost the saved answer")
			}
		}
	}
	req.ExecutionClaimToken = ""
	if req.promptRole(promptassets.RoleOperatorCode) != promptassets.RoleOperatorCode {
		t.Fatal("legacy execution contract changed")
	}
}

func TestPostgresExecutionOwnerReadFailureCannotRouteToLegacyManager(t *testing.T) {
	for _, kind := range []string{"code", "knowledge"} {
		t.Run(kind, func(t *testing.T) {
			dir := makeGitRepo(t)
			sp, st, bc, _, _, tx := newHostSpawner(t, dir)
			if e := os.WriteFile(filepath.Join(os.Getenv("COMMANDER_HOME"), "state.postgres"), []byte("read-only owner fixture"), 0600); e != nil {
				t.Fatal(e)
			}
			bc.ShowErr = errors.New("canonical owner unavailable")
			req := goodRequest(t, dir)
			req.ProjectType = kind
			req.ExecutionClaimToken = "private-reservation"
			if _, e := sp.Spawn(context.Background(), req); e == nil || !strings.Contains(e.Error(), "planning owner") {
				t.Fatalf("owner failure was not returned: %v", e)
			}
			if len(st.runs) != 0 || len(tx.Calls) != 0 {
				t.Fatal("runtime launched without a current owner")
			}
			if got := ManagerRole(context.Background(), bc, dir, req.TaskBeadID); got != "" {
				t.Fatalf("read failure invented legacy role %q", got)
			}
			bc.ShowErr = nil
			bc.ShowResult = &beads.Bead{ID: req.TaskBeadID, PlanningCommander: "commander-urgent"}
			if got, e := executionOwner(context.Background(), bc, dir, req.TaskBeadID); e != nil || got != "commander-urgent" {
				t.Fatalf("current canonical owner unavailable: %s %v", got, e)
			}
		})
	}
}
