package spawn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/tmuxctl"
)

const hostSessionID = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"

func TestHostOperatorCommandStripsTmuxEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMMANDER_HOME", dir)
	t.Setenv("TMUX", "poisoned-parent")
	t.Setenv("TMUX_PANE", "%999")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexec env\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", hostOperatorCommand(SpawnRequest{}, "operator-test", hostSessionID, false)).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "TMUX=") || strings.HasPrefix(line, "TMUX_PANE=") {
			t.Fatalf("agent inherited %s", line)
		}
	}
}

// Every Operator commits under its own committer identity, on both runtimes,
// overriding one it inherited: that header is how its commits are attributed.
func TestHostOperatorCommandSetsTheRunsGitCommitter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMMANDER_HOME", dir)
	t.Setenv("GIT_COMMITTER_EMAIL", "user@example.com")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, bin := range []string{"claude", "cmdr"} {
		if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!/bin/sh\nexec env\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, rt := range []string{"", "codex"} {
		out, err := exec.Command("sh", "-c", hostOperatorCommand(SpawnRequest{Runtime: rt}, "operator-op-X-t1.2", hostSessionID, false)).Output()
		if err != nil {
			t.Fatal(rt, err)
		}
		env := "\n" + string(out)
		for _, want := range []string{"\nGIT_COMMITTER_EMAIL=operator-op-x-t1.2@operator.commander.invalid\n", "\nGIT_COMMITTER_NAME=operator-op-X-t1.2\n"} {
			if !strings.Contains(env, want) {
				t.Errorf("runtime %q: env lacks %q", rt, strings.TrimSpace(want))
			}
		}
		if strings.Contains(env, "\nGIT_AUTHOR_") {
			t.Errorf("runtime %q: the author is the user's, not set", rt)
		}
	}
}

// newHostSpawner is a Spawner for the host runtime: a mock tmux, a stubbed
// fifo capture, and a COMMANDER_HOME so role prompts land in a temp dir.
func newHostSpawner(t *testing.T, dir string) (*Spawner, *fakeStore, *beads.MockClient, *agentmail.MockClient, *docker.MockClient, *tmuxctl.MockClient) {
	t.Helper()
	withTempHome(t)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	sp, fs, _, bd, am, dk := newSpawnerForTest(t, dir, "")
	tx := &tmuxctl.MockClient{}
	sp.tmux = tx
	sp.useHost = func() bool { return true }
	return sp, fs, bd, am, dk, tx
}

func TestHostRuntimeSelected(t *testing.T) {
	emptyPath := t.TempDir()
	withDocker := t.TempDir()
	if err := os.WriteFile(filepath.Join(withDocker, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, runtime, path string
		want                bool
	}{
		{"docker on PATH is not enough", "", withDocker, true},
		{"no docker on PATH", "", emptyPath, true},
		{"forced host", "host", withDocker, true},
		{"explicit docker opt-in", "docker", withDocker, false},
		{"docker opt-in without docker installed", "docker", emptyPath, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvOperatorRuntime, c.runtime)
			t.Setenv("PATH", c.path)
			if got := hostRuntimeSelected(); got != c.want {
				t.Errorf("hostRuntimeSelected() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSpawnCodeHost_HappyPath(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, bd, am, dk, tx := newHostSpawner(t, dir)

	res, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worktree := filepath.Join(dir, ".worktrees", "feat-auth")
	if res.WorktreePath != worktree || res.AgentName != "operator-feat-auth" || res.ContainerID != "" || res.SessionID == "" {
		t.Errorf("result: %+v", res)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
	if len(dk.Calls) != 0 {
		t.Errorf("host runtime must not touch docker: %v", dk.Calls)
	}

	// tmux: session rooted in the worktree, piped to the fifo, launcher respawned.
	wantFifo := filepath.Join(dir, ".commander", "operators", "feat-auth.fifo")
	launcher := filepath.Join(dir, ".commander", "operators", "operator-feat-auth.sh")
	if len(tx.Calls) != 3 ||
		tx.Calls[0] != "NewSessionCommand(name=cmdr-myproj-feat-auth cwd="+worktree+" argv=cat)" ||
		tx.Calls[1] != "PipePane(target=cmdr-myproj-feat-auth fifo="+wantFifo+")" ||
		tx.Calls[2] != "RespawnPane(target=cmdr-myproj-feat-auth cwd="+worktree+" argv=sh "+launcher+")" {
		t.Fatalf("tmux calls: %v", tx.Calls)
	}
	cmd := launcherCmd(t, dir)
	if !strings.HasPrefix(cmd, "#!/bin/sh\nunset TMUX TMUX_PANE\nenv -u TMUX -u TMUX_PANE -u ANTHROPIC_API_KEY -u CLAUDE_CONFIG_DIR GOFLAGS='-trimpath -p=2' COMMANDER_ROLE='operator-feat-auth' 'GIT_COMMITTER_NAME=operator-feat-auth' 'GIT_COMMITTER_EMAIL=operator-feat-auth@operator.commander.invalid' COMMANDER_MANAGER_ROLE='manager-bd-100' 'claude' '-p'") ||
		!strings.Contains(cmd, "'--session-id' '"+res.SessionID+"'") { // minted up front, not captured
		t.Fatalf("launcher: %s", cmd)
	}
	if info, err := os.Stat(launcher); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("launcher should be 0700: %v", err)
	}
	// A regular file, never a named pipe: a pipe-pane writer blocks forever
	// opening a fifo nobody reads.
	if info, err := os.Stat(wantFifo); err != nil || !info.Mode().IsRegular() {
		t.Errorf("pane output should be a regular file that persists for the watchdog: %v %v", info, err)
	}

	// Session id + role prompt: Claude flavored.
	if body, err := os.ReadFile(filepath.Join(dir, ".commander", "operators", "feat-auth.claude.session-id")); err != nil || strings.TrimSpace(string(body)) != res.SessionID {
		t.Errorf("session file: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("COMMANDER_HOME"), "claude-agents", "operator-feat-auth.md")); err != nil {
		t.Errorf("claude operator prompt missing: %v", err)
	}

	if !containsCall(am.Calls, "Register:myproj/claude-code/sonnet/operator-feat-auth") {
		t.Errorf("am register: %v", am.Calls)
	}
	// Manager resolution and post-start custody reads must add no extra writes.
	if writes := beadMutations(bd.Calls); len(writes) != 1 || !strings.HasPrefix(writes[0], "SetState:bd-100:cmdr=brainstorming") {
		t.Errorf("bd calls: %v", bd.Calls)
	}

	// The agent_runs row carries the tmux session, not a container.
	if len(fs.runs) != 1 {
		t.Fatalf("runs: %v", fs.runs)
	}
	r := fs.runs[0]
	if r.ContainerID.Valid || !r.TmuxPaneID.Valid || r.TmuxPaneID.String != "cmdr-myproj-feat-auth" || r.Status != "running" {
		t.Errorf("agent_runs row: %+v", r)
	}
}

func TestSpawnCodeHost_NeverForwardsCredentials(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, dir)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret-token")

	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cmd := launcherCmd(t, dir)
	for _, secret := range []string{"sk-ant-secret-key", "oauth-secret-token", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if strings.Contains(cmd, secret) {
			t.Errorf("command must not carry %q: %s", secret, cmd)
		}
	}
	if !strings.Contains(cmd, "env -u TMUX -u TMUX_PANE -u ANTHROPIC_API_KEY ") {
		t.Errorf("ANTHROPIC_API_KEY must be unset for the child: %s", cmd)
	}
	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Errorf("host Operators run like Managers (no approval prompts; a headless run cannot answer them): %s", cmd)
	}
}

func TestSpawnCodeHost_MountReadsBecomeEnv(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, dir)
	reads := t.TempDir()
	req := goodRequest(t, dir)
	req.MountReads = []string{reads}

	if _, err := sp.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if cmd := launcherCmd(t, dir); !strings.Contains(cmd, "COMMANDER_READS='"+reads+"' 'claude'") {
		t.Errorf("COMMANDER_READS missing: %s", cmd)
	}
}

func TestSpawnCodeHost_RollbackKillsSessionAndRemovesWorktree(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, bd, am, _, tx := newHostSpawner(t, dir)
	tx.RespawnPaneErr = errors.New("tmux server gone")

	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err == nil || !strings.Contains(err.Error(), "respawn-pane") {
		t.Fatalf("expected send-keys error, got %v", err)
	}
	if !containsCall(tx.Calls, "KillPane(target=cmdr-myproj-feat-auth)") {
		t.Errorf("session must be killed on rollback: %v", tx.Calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees", "feat-auth")); err == nil {
		t.Error("worktree should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".commander", "operators", "feat-auth.fifo")); err == nil {
		t.Error("fifo should be removed")
	}
	// The only bead call before launch is the read that names the Manager.
	if len(fs.runs) != 0 || len(bd.Calls) != 1 || !strings.HasPrefix(bd.Calls[0], "Details:") || len(am.Calls) != 0 {
		t.Errorf("no bookkeeping before launch: %v %v %v", fs.runs, bd.Calls, am.Calls)
	}
}

func TestSpawnCodeHost_RollbackWhenBookkeepingFailsAfterLaunch(t *testing.T) {
	dir := makeGitRepo(t)
	sp, fs, _, _, _, tx := newHostSpawner(t, dir)
	fs.insertAgentRunErr = errors.New("db locked")

	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err == nil {
		t.Fatal("expected insert error")
	}
	if !containsCall(tx.Calls, "KillPane(target=cmdr-myproj-feat-auth)") {
		t.Errorf("a launched process must be killed on rollback: %v", tx.Calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".commander", "operators", "feat-auth.claude.session-id")); err == nil {
		t.Error("session file should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees", "feat-auth")); err == nil {
		t.Error("worktree should be removed")
	}
}

func TestSpawnCodeHost_RespawnFailureRollsBack(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, _, _, _, tx := newHostSpawner(t, dir)
	tx.RespawnPaneErr = errors.New("tmux gone")
	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err == nil || !strings.Contains(err.Error(), "respawn-pane") {
		t.Fatalf("err = %v", err)
	}
	if !containsCall(tx.Calls, "KillPane(target=cmdr-myproj-feat-auth)") {
		t.Errorf("tmux calls: %v", tx.Calls)
	}
}

func TestHostSessionName_AvoidsTmuxRewrittenCharacters(t *testing.T) {
	if got := hostSessionName("p", "op-fix-ms-1.2.1"); got != "cmdr-p-op-fix-ms-1-2-1" {
		t.Errorf("got %q", got)
	}
}

func TestSpawnDispatch_DockerStillUsedWhenAvailable(t *testing.T) {
	withTempHome(t)
	dir := makeGitRepo(t)
	sp, _, _, _, _, dk := newSpawnerForTest(t, dir, "ses_x") // useHost=false
	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(dk.Calls) != 1 {
		t.Errorf("docker should have been used: %v", dk.Calls)
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// What the Operator stop ban in internal-mail relies on: a host Operator has
// its own COMMANDER_ROLE beside COMMANDER_MANAGER_ROLE, on both runtimes.
func TestHostOperatorEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-parent")
	os.Unsetenv("COMMANDER_MANAGER_ROLE")
	t.Setenv("COMMANDER_ROLE", "")
	os.Unsetenv("COMMANDER_ROLE")
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexec env\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := SpawnRequest{ProjectPath: dir, TaskBeadID: "p-1", managerRole: "manager-p-1"}
	out, err := exec.Command("sh", "-c", hostOperatorCommand(req, "operator-test", hostSessionID, false)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "COMMANDER_MANAGER_ROLE=") || strings.HasPrefix(line, "COMMANDER_ROLE=") {
			env = append(env, line)
		}
	}
	sort.Strings(env)
	if got := strings.Join(env, " "); got != "COMMANDER_MANAGER_ROLE=manager-p-1 COMMANDER_ROLE=operator-test" {
		t.Fatalf("role environment: %q", got)
	}

	req.Runtime = chat.RuntimeCodex
	codex := hostOperatorCommand(req, "operator-test", hostSessionID, false)
	if !strings.Contains(codex, "COMMANDER_MANAGER_ROLE='manager-p-1'") || !strings.Contains(codex, "COMMANDER_ROLE='operator-test'") {
		t.Fatalf("codex command: %s", codex)
	}
}

func TestHostOperatorCommandResumesTheSession(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	req := SpawnRequest{ProjectName: "p", ProjectPath: "/p", TaskBeadID: "p-1", Branch: "op/x", Model: "sonnet"}
	fresh := hostOperatorCommand(req, "operator-op-x", "sess-1", false)
	if !strings.Contains(fresh, "--session-id") || strings.Contains(fresh, "--resume") {
		t.Fatalf("fresh: %s", fresh)
	}
	// No transcript anywhere (it only ran on Codex): same id, fresh session, resume prompt.
	noTranscript := hostOperatorCommand(req, "operator-op-x", "sess-1", true)
	if !strings.Contains(noTranscript, "--session-id") || !strings.Contains(noTranscript, "[resume]") {
		t.Fatalf("resumed without a transcript: %s", noTranscript)
	}
	os.MkdirAll(filepath.Join(user, ".claude", "projects", "-p"), 0o755)
	os.WriteFile(filepath.Join(user, ".claude", "projects", "-p", "sess-1.jsonl"), []byte("{}\n"), 0o600)
	resumed := hostOperatorCommand(req, "operator-op-x", "sess-1", true)
	if !strings.Contains(resumed, "--resume") || !strings.Contains(resumed, "sess-1") || strings.Contains(resumed, "--session-id") || !strings.Contains(resumed, "[resume]") {
		t.Fatalf("resumed: %s", resumed)
	}
	if strings.Contains(resumed, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("default account must not set CLAUDE_CONFIG_DIR: %s", resumed)
	}
}

// A respawn on another account runs claude with that CLAUDE_CONFIG_DIR and
// resumes the session from a copy of its transcript there.
func TestHostOperatorCommandOnAnotherAccount(t *testing.T) {
	user, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("COMMANDER_HOME", home)
	if _, err := claudeacct.Create(home, "work"); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(user, ".claude", "projects", "-p"), 0o755)
	os.WriteFile(filepath.Join(user, ".claude", "projects", "-p", "sess-2.jsonl"), []byte("{}\n"), 0o600)
	req := SpawnRequest{ProjectName: "p", ProjectPath: "/p", TaskBeadID: "p-1", Branch: "op/x", Model: "sonnet", Account: "work"}
	cmd := hostOperatorCommand(req, "operator-op-x", "sess-2", true)
	dir := claudeacct.Dir(home, "work")
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR="+dir) || !strings.Contains(cmd, "--resume") {
		t.Fatalf("command: %s", cmd)
	}
	if _, err := os.Stat(filepath.Join(dir, "projects", "-p", "sess-2.jsonl")); err != nil {
		t.Errorf("transcript not carried: %v", err)
	}
	codex := hostOperatorCommand(SpawnRequest{ProjectName: "p", ProjectPath: "/p", TaskBeadID: "p-1", Account: "work", Runtime: "codex"}, "operator-op-x", "sess-2", true)
	if strings.Contains(codex, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("a Codex Operator gets no Claude account: %s", codex)
	}
}

func TestManagerRoleIsTheWorkstreamRoot(t *testing.T) {
	// example-app-t02.8 was promoted to a workstream: no parent edge any more.
	bc := &beads.MockClient{ParentsResult: map[string]string{"example-app-t02.8": ""}}
	cases := map[string]string{
		"example-app-t01.17.5":   "manager-example-app-t01",
		"example-app-t01.17.5.1": "manager-example-app-t01",
		"commander-il9.15.38":    "manager-commander-il9",
		"commander-il9":          "manager-commander-il9",
		"example-app-t02.8.4.5":  "manager-example-app-t02.8",
		"example-app-t02.2":      "manager-example-app-t02",
		"":                       "",
	}
	for task, want := range cases {
		if got := ManagerRole(context.Background(), bc, "/p", task); got != want {
			t.Errorf("ManagerRole(%q) = %q, want %q", task, got, want)
		}
	}
}

func TestHostOperatorNamesItsManager(t *testing.T) {
	req := SpawnRequest{ProjectName: "example-app", ProjectPath: "/p", TaskBeadID: "example-app-t01.17.5", Branch: "op/x", Model: "sonnet", managerRole: "manager-example-app-t01"}
	if p := hostBootstrapPrompt(req); !strings.Contains(p, "`manager-example-app-t01`") {
		t.Fatalf("prompt lacks the Manager role: %s", p)
	}
	if cmd := hostOperatorCommand(req, "operator-op-x", "sess-1", false); !strings.Contains(cmd, "COMMANDER_MANAGER_ROLE='manager-example-app-t01'") {
		t.Fatalf("command lacks the env: %s", cmd)
	}
	if cmd := hostOperatorCommand(req, "operator-op-x", "sess-1", true); !strings.Contains(cmd, "COMMANDER_MANAGER_ROLE='manager-example-app-t01'") {
		t.Fatalf("resumed command lacks the env: %s", cmd)
	}
}

// The live case: an Operator under a promoted workstream reports to that
// workstream's Manager, not to the one its dotted id names.
func TestSpawnCodeHost_PromotedWorkstreamManager(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, bd, _, _, _ := newHostSpawner(t, dir)
	bd.ParentsResult = map[string]string{"example-app-t02.8": ""}
	req := goodRequest(t, dir)
	req.TaskBeadID = "example-app-t02.8.4.5"
	if _, err := sp.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cmd := launcherCmd(t, dir)
	if !strings.Contains(cmd, "COMMANDER_MANAGER_ROLE='manager-example-app-t02.8'") || !strings.Contains(cmd, "`manager-example-app-t02.8`") {
		t.Fatalf("launcher names the wrong Manager: %s", cmd)
	}
}

// launcherCmd is the launcher script the host spawner wrote for operator-feat-auth.
func launcherCmd(t *testing.T, dir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, ".commander", "operators", "operator-feat-auth.sh"))
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
	return string(body)
}

func TestHostOperatorCommandSharesTheMachine(t *testing.T) {
	req := SpawnRequest{ProjectName: "p", ProjectPath: "/p", TaskBeadID: "p-1", Branch: "op/x", Model: "sonnet"}
	for _, resuming := range []bool{false, true} {
		cmd := hostOperatorCommand(req, "operator-op-x", "sess-1", resuming)
		for _, want := range []string{"GOFLAGS='-trimpath -p=2'", "--settings", "cmdr internal-operator-guard --role operator-op-x"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("resuming=%v: command lacks %q: %s", resuming, want, cmd)
			}
		}
	}
}

func TestHostBootstrapPrompt_ProdGate(t *testing.T) {
	req := SpawnRequest{ProjectName: "example-app", ProjectPath: "/p", TaskBeadID: "example-app-t01.1", Branch: "op/x", managerRole: "manager-example-app-t01"}
	if p := hostBootstrapPrompt(req); strings.Contains(p, "PRODUCTION GATE") {
		t.Fatalf("no gate, no exception: %s", p)
	}
	req.ProdGate, req.ProdGateScope = "example-app-t01.9", "run migration 42 on prod"
	p := hostBootstrapPrompt(req)
	for _, want := range []string{"PRODUCTION GATE example-app-t01.9", "run migration 42 on prod", "never exceed it"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q: %s", want, p)
		}
	}
	if cmd := hostOperatorCommand(req, "operator-x", "sid", false); !strings.Contains(cmd, "COMMANDER_PROD_GATE='example-app-t01.9'") {
		t.Errorf("env missing: %s", cmd)
	}
}

// The bd command shape in the Operator prompts must work against a real bd
// from a cwd that is not the project root.
func TestBootstrapPromptBrCommandRuns(t *testing.T) {
	bd, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not installed")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}
	init := exec.Command(bd, "init", "-q", "--prefix", "tst")
	init.Dir = root
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed: %v: %s", err, out)
	}
	create := exec.Command(bd, "create", "hello bead", "--json")
	create.Dir = root
	out, err := create.Output()
	if err != nil {
		t.Fatalf("bd create failed: %v", err)
	}
	if i := strings.Index(string(out), "{"); i > 0 {
		out = out[i:]
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &made); err != nil || made.ID == "" {
		t.Fatalf("bd create output unparsable: %s", out)
	}

	req := SpawnRequest{ProjectName: "tst", ProjectPath: root, TaskBeadID: made.ID, Branch: "op/x"}
	for name, p := range map[string]string{"code": hostBootstrapPrompt(req), "knowledge": knowledgeHostBootstrapPrompt(req), "resume": hostResumePrompt(req)} {
		if strings.Contains(p, "bd -C") {
			t.Fatalf("%s prompt still uses bd -C: %s", name, p)
		}
		want := BrCommand(root) + " show " + made.ID
		if !strings.Contains(p, "`"+want+"`") {
			t.Fatalf("%s prompt lacks %q: %s", name, want, p)
		}
		cmd := exec.Command("sh", "-c", want)
		cmd.Dir = t.TempDir()
		got, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(got), "hello bead") {
			t.Fatalf("%s: %q failed: %v: %s", name, want, err, got)
		}
	}
}

// A Resume continues its run, so it keeps that run's stamp: mail sent to it more
// than FirstSightWindow before the restart is still its own, while a fresh spawn
// of the same name starts a new bound.
func TestSpawnHost_ResumeKeepsTheRunStartOfTheRunItContinues(t *testing.T) {
	for _, projectType := range []string{"code", "knowledge"} {
		t.Run(projectType, func(t *testing.T) {
			role := "operator-feat-auth"
			if projectType == "knowledge" {
				role = "operator-" + flatTask("bd-100")
			}
			dir := makeGitRepo(t)
			sp, _, _, _, _, _ := newHostSpawner(t, dir)
			t.Setenv("COMMANDER_HOME", t.TempDir())
			start := time.Now().Add(-2 * time.Hour)
			if err := mailstate.MarkRunStart(role, start); err != nil {
				t.Fatal(err)
			}
			sp.now = func() time.Time { return start.Add(time.Hour) }
			unseen := agentmail.Mail{ID: "1", Subject: "direction to the run that died", SentAt: start.Add(30 * time.Minute)}

			req := goodRequest(t, dir)
			req.ProjectType, req.Resume = projectType, true
			if projectType == "knowledge" {
				req.Branch = ""
			}
			if _, err := sp.Spawn(context.Background(), req); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if n := mailstate.HiddenByAge(role, []agentmail.Mail{unseen}); n != 0 {
				t.Fatalf("hidden = %d: a Resume moved the stamp past its own run's mail", n)
			}
			if !mailstate.IsUnread(role, unseen) {
				t.Fatal("a Resume's own unseen mail no longer reads as unread")
			}
		})
	}
}

// A fresh same-name spawn still moves the stamp: older mail is an earlier run's.
func TestSpawnHost_FreshSpawnStillBoundsOlderMail(t *testing.T) {
	const role = "operator-feat-auth"
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, dir)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	start := time.Now().Add(-2 * time.Hour)
	if err := mailstate.MarkRunStart(role, start); err != nil {
		t.Fatal(err)
	}
	sp.now = func() time.Time { return start.Add(time.Hour) }
	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	older := agentmail.Mail{ID: "1", Subject: "an earlier run's", SentAt: start.Add(30 * time.Minute)}
	if n := mailstate.HiddenByAge(role, []agentmail.Mail{older}); n != 1 {
		t.Fatalf("hidden = %d: a fresh spawn lost the bound", n)
	}
}

func TestHostOperatorCommandDefaultUnsetsConfigDir(t *testing.T) {
	cmd := hostOperatorCommand(SpawnRequest{}, "operator-example", "example-session", false)
	if !strings.Contains(cmd, "-u CLAUDE_CONFIG_DIR") {
		t.Fatal("default inherits caller profile")
	}
}

// A spawn stamps the run's start before the Operator can run a hook, so a
// same-name respawn with no cursor does not read its whole inbox as new.
func TestSpawnCodeHost_MarksTheRunStartForItsMailHook(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, dir)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if _, err := sp.Spawn(context.Background(), goodRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	old := agentmail.Mail{ID: "1", Subject: "earlier", SentAt: sp.now().Add(-time.Hour)}
	if n := mailstate.HiddenByAge("operator-feat-auth", []agentmail.Mail{old}); n != 1 {
		t.Fatalf("hidden = %d: the spawn left no run start", n)
	}
}
