package evals

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// The production Spawn (spawn.New, as cmdrd and the CLI verbs build it) on a
// private home with a real store and git repo, tmux, beads and agent mail
// mocked, a scripted Codex whose hooks are on, and a usage file saying which
// providers are out: a run moved off an out provider records where it really
// runs (chat.ProviderOf equals the result), keeps the account it was headed
// for as its intent and notes the move once on the task bead; a crash restart
// with nothing left is refused and writes nothing. Code and knowledge hosts.
func TestSpawnUsageMoveScenario(t *testing.T) {
	var scenario struct {
		Accounts []string
		Moves    []struct {
			Name        string
			ProjectType string `json:"project_type"`
			Share       string
			Resume      bool
			RoleRuntime string `json:"role_runtime"`
			RoleAccount string `json:"role_account"`
			Out         []string
			Runtime     string
			Account     string
			Intent      string
			Note        string
			Refused     string
		}
	}
	raw, err := os.ReadFile("scenarios/spawn-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Moves) == 0 {
		t.Fatal("scenario has no moves")
	}
	for _, c := range scenario.Moves {
		t.Run(c.Name, func(t *testing.T) {
			user, home, repo := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", user)
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv(spawn.EnvOperatorRuntime, "host")
			t.Setenv("COMMANDER_CODEX_OPERATOR_SHARE", c.Share)
			codex := filepath.Join(t.TempDir(), "codex")
			writeExecutable(t, codex, "#!/bin/sh\necho 'hooks stable true'\n")
			t.Setenv("COMMANDER_CODEX_BIN", codex)
			for _, d := range []string{filepath.Join(user, ".claude")} {
				os.MkdirAll(d, 0o700)
				os.WriteFile(filepath.Join(d, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600)
			}
			for _, a := range scenario.Accounts {
				d := filepath.Join(home, "claude-accounts", a)
				os.MkdirAll(d, 0o700)
				os.WriteFile(filepath.Join(d, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600)
			}
			for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "t@example.com"},
				{"config", "user.name", "t"}, {"commit", "--allow-empty", "-m", "init"}} {
				if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
			}
			os.MkdirAll(filepath.Join(repo, ".beads"), 0o755)

			req := spawn.SpawnRequest{ProjectID: 1, ProjectName: "demo", ProjectPath: repo, ProjectType: c.ProjectType,
				TaskBeadID: "demo-1.2.1", Branch: "op/fix", Model: "anthropic/claude-sonnet-4-5", Resume: c.Resume}
			role := "operator-op-fix"
			if c.ProjectType == "knowledge" {
				req.Branch, req.TaskBeadID, role = "", "demo-4fc", "operator-4fc"
			}
			if c.RoleRuntime != "" {
				if err := chat.WriteAutoRuntime(home, role, c.RoleRuntime); err != nil {
					t.Fatal(err)
				}
			}
			if c.RoleAccount != "" {
				if err := chat.WriteAccount(home, role, c.RoleAccount); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			for _, key := range c.Out {
				rt, acct, _ := strings.Cut(key, ":")
				chat.OpenUsage(home).MarkOut(rt, acct, "manager-demo-1", now.Add(time.Hour).Unix(), now)
			}

			st, err := store.Open(filepath.Join(home, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			pid, err := st.InsertProject(context.Background(), store.Project{Name: "demo", Type: c.ProjectType, Path: repo, Status: "active", ActivatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			req.ProjectID = pid
			bd := &beads.MockClient{}
			sp := spawn.New(st, &opencode.MockClient{}, bd, &agentmail.MockClient{}, &docker.MockClient{}, &tmuxctl.MockClient{}, t.TempDir())
			before := treeOf(t, filepath.Join(home, "chat"))
			res, err := sp.Spawn(context.Background(), req)
			var notes []string
			for _, call := range bd.Calls {
				if strings.HasPrefix(call, "AddComment") && strings.Contains(call, "cmdrd:") {
					notes = append(notes, call)
				}
			}
			if c.Refused != "" {
				if err == nil || !strings.Contains(err.Error(), c.Refused) {
					t.Fatalf("spawn: %v", err)
				}
				if after := treeOf(t, filepath.Join(home, "chat")); after != before || len(notes) != 0 {
					t.Fatalf("a refused restart wrote: notes %v\n%s\n->\n%s", notes, before, after)
				}
				return
			}
			if err != nil {
				t.Fatalf("spawn: %v", err)
			}
			if res.AgentName != role {
				t.Fatalf("agent %s, want %s", res.AgentName, role)
			}
			got := chat.ProviderOf(home, role)
			if res.Runtime != c.Runtime || res.Account != c.Account || got != (chat.Provider{Runtime: c.Runtime, Account: c.Account}) {
				t.Fatalf("result %s/%s, recorded %+v, want %s/%s", res.Runtime, res.Account, got, c.Runtime, c.Account)
			}
			if i := chat.ReadAccountIntent(home, role); i != c.Intent {
				t.Fatalf("intent %q, want %q", i, c.Intent)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], c.Note) || !strings.Contains(res.Moved, c.Note) {
				t.Fatalf("notes %v, moved %q, want one with %q", notes, res.Moved, c.Note)
			}
		})
	}
}
