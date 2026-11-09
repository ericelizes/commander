package spawn

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/testguard"
	"github.com/example-user/commander/internal/tmuxctl"
)

// A host Operator on a real (private) tmux server leaves no process behind
// once its claude exits: not its launcher, not its pipe-pane writer. Before
// the pane output was a regular file, each spawn left a `sh -c cat >> <fifo>`
// blocked forever under launchd.
func TestSpawnHost_LeavesNoProcessWhenOperatorExits(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, projectType := range []string{"knowledge", "code"} {
		t.Run(projectType, func(t *testing.T) {
			dir := t.TempDir()
			if projectType == "code" {
				dir = makeGitRepo(t)
			}
			testguard.NoLeakedProcesses(t, dir)
			sp, _, _, _, _, _ := newHostSpawner(t, dir)
			socket := privateOpsServer(t, "#!/bin/sh\necho '{\"type\":\"system\"}'\nsleep 1\n")
			sp.tmux = tmuxctl.New()

			req := goodRequest(t, dir)
			req.ProjectType = projectType
			if projectType == "knowledge" {
				req.Branch = ""
			}
			if _, err := sp.Spawn(context.Background(), req); err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			session := hostSessionName(req.ProjectName, "100")
			if projectType == "code" {
				session = hostSessionName(req.ProjectName, req.slug())
			}
			waitFor(t, 10*time.Second, func() bool {
				return exec.Command("tmux", "-L", socket, "has-session", "-t", "="+session).Run() != nil
			})
			// Checked before the output is read: opening a fifo to read it would
			// itself release a blocked writer.
			waitFor(t, 3*time.Second, func() bool { return len(testguard.ProcessesMatching(dir)) == 0 })
			out := filepath.Join(dir, ".commander", "operators", strings.TrimPrefix(session, "cmdr-myproj-")+".fifo")
			if body, err := os.ReadFile(out); err != nil || !strings.Contains(string(body), `"type":"system"`) {
				t.Errorf("pane output %s = %q, %v", out, body, err)
			}
		})
	}
}

// The launcher a real pane runs gives the agent COMMANDER_ROLE = its AgentName
// whatever the spawning process carried, on every path: code and knowledge,
// Claude and Codex, and the crash restart (Resume).
func TestSpawnHost_OperatorEnvironmentCarriesItsRole(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	cases := []struct {
		name, projectType, runtime string
		resume                     bool
	}{
		{"code claude", "code", "", false},
		{"code claude crash restart", "code", "", true},
		{"code codex", "code", chat.RuntimeCodex, false},
		{"knowledge claude", "knowledge", "", false},
		{"knowledge codex", "knowledge", chat.RuntimeCodex, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.projectType == "code" {
				dir = makeGitRepo(t)
				// The layout the commander repo has: git records the real path.
				if err := os.Mkdir(filepath.Join(dir, ".worktrees.noindex"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); err != nil {
					t.Fatal(err)
				}
			}
			testguard.NoLeakedProcesses(t, dir)
			sp, _, _, _, _, _ := newHostSpawner(t, dir)
			envFile := filepath.Join(dir, "agent.env")
			agent := "#!/bin/sh\nenv > " + shellQuote(envFile) + ".tmp\nmv " + shellQuote(envFile) + ".tmp " + shellQuote(envFile) + "\n"
			socket := privateOpsServer(t, agent)
			// A Codex Operator's launcher runs `cmdr __codex-bridge`.
			claude, err := exec.LookPath("claude")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(claude), "cmdr"), []byte(agent), 0o755); err != nil {
				t.Fatal(err)
			}
			sp.tmux = tmuxctl.New()
			t.Setenv("COMMANDER_ROLE", "manager-the-spawner")

			req := goodRequest(t, dir)
			req.ProjectType, req.Runtime = c.projectType, c.runtime
			if c.projectType == "knowledge" {
				req.Branch = ""
			}
			session := hostSessionName(req.ProjectName, req.slug())
			if c.projectType == "knowledge" {
				session = hostSessionName(req.ProjectName, "100")
			}
			spawnAndWait := func(r SpawnRequest) {
				t.Helper()
				os.Remove(envFile)
				if _, err := sp.Spawn(context.Background(), r); err != nil {
					t.Fatalf("Spawn: %v", err)
				}
				waitFor(t, 10*time.Second, func() bool {
					return exec.Command("tmux", "-L", socket, "has-session", "-t", "="+session).Run() != nil
				})
			}
			spawnAndWait(req)
			if c.resume {
				// A new Spawner, as cmdrd's restart path has: the first run is over.
				sp, _, _, _, _, _ = newHostSpawner(t, dir)
				sp.tmux = tmuxctl.New()
				req.Resume = true
				spawnAndWait(req)
			}
			raw, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatalf("the agent's environment: %v", err)
			}
			want := map[string]string{"COMMANDER_ROLE": "operator-" + strings.TrimPrefix(session, "cmdr-myproj-"), "COMMANDER_MANAGER_ROLE": "manager-" + req.TaskBeadID}
			got := map[string]string{}
			for _, line := range strings.Split(string(raw), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok && want[k] != "" {
					got[k] = v
				}
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// The settings a real pane's launcher hands the agent: a Claude host Operator,
// fresh or crash-restarted, has the mail hook beside its guard; a Codex one has
// only the guard, passed to the bridge. Real launcher, private tmux, fake agent.
func TestSpawnHost_LauncherCarriesTheMailHook(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	cases := []struct {
		name, projectType, runtime string
		resume, mail               bool
	}{
		{"code claude", "code", "", false, true},
		{"code claude crash restart", "code", "", true, true},
		{"knowledge claude", "knowledge", "", false, true},
		{"code codex", "code", chat.RuntimeCodex, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.projectType == "code" {
				dir = makeGitRepo(t)
				if err := os.Mkdir(filepath.Join(dir, ".worktrees.noindex"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); err != nil {
					t.Fatal(err)
				}
			}
			testguard.NoLeakedProcesses(t, dir)
			sp, _, _, _, _, _ := newHostSpawner(t, dir)
			argsFile := filepath.Join(dir, "agent.args")
			agent := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsFile) + ".tmp\nmv " + shellQuote(argsFile) + ".tmp " + shellQuote(argsFile) + "\n"
			socket := privateOpsServer(t, agent)
			claude, err := exec.LookPath("claude")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(claude), "cmdr"), []byte(agent), 0o755); err != nil {
				t.Fatal(err)
			}
			sp.tmux = tmuxctl.New()
			req := goodRequest(t, dir)
			req.ProjectType, req.Runtime = c.projectType, c.runtime
			session := hostSessionName(req.ProjectName, req.slug())
			if c.projectType == "knowledge" {
				req.Branch = ""
				session = hostSessionName(req.ProjectName, "100")
			}
			spawnAndWait := func(r SpawnRequest) {
				t.Helper()
				os.Remove(argsFile)
				if _, err := sp.Spawn(context.Background(), r); err != nil {
					t.Fatalf("Spawn: %v", err)
				}
				waitFor(t, 10*time.Second, func() bool {
					return exec.Command("tmux", "-L", socket, "has-session", "-t", "="+session).Run() != nil
				})
			}
			spawnAndWait(req)
			if c.resume {
				sp, _, _, _, _, _ = newHostSpawner(t, dir)
				sp.tmux = tmuxctl.New()
				req.Resume = true
				spawnAndWait(req)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			guard := "cmdr internal-operator-guard --role "
			if c.runtime == chat.RuntimeCodex {
				guard = "--guard-only"
			}
			if !strings.Contains(got, guard) {
				t.Fatalf("no guard: %s", got)
			}
			for _, event := range []string{"PostToolUse", "Stop"} {
				if has := strings.Contains(got, "--event "+event+" --mail"); has != c.mail {
					t.Errorf("mail hook on %s = %v, want %v: %s", event, has, c.mail, got)
				}
			}
		})
	}
}

// privateOpsServer points tmuxctl at a throwaway server in a short temp dir
// and puts a fake claude first on PATH. The server is killed at cleanup.
func privateOpsServer(t *testing.T, fakeClaude string) (socket string) {
	t.Helper()
	tmp, err := os.MkdirTemp("/tmp", "tk.")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	socket = "leak-test"
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", tmp)
	t.Setenv("COMMANDER_TMUX_SOCKET", socket)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	// A keepalive session: the Operator's session ending must not take the
	// server (and every pane writer with it) down, as the keeper ensures.
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "keep", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("tmux keepalive: %v %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		os.RemoveAll(tmp)
	})
	return socket
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met")
}
