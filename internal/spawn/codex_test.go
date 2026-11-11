package spawn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

func TestSpawnCodeHost_CodexRuntimeRunsTheBridge(t *testing.T) {
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, dir)
	req := goodRequest(t, dir)
	req.Runtime = chat.RuntimeCodex
	res, err := sp.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	home := os.Getenv("COMMANDER_HOME")
	cmd := launcherCmd(t, dir)
	// Same env, brief and session as a Claude Operator; Codex via the bridge.
	for _, want := range []string{
		"env -u TMUX -u TMUX_PANE -u ANTHROPIC_API_KEY -u CLAUDE_CONFIG_DIR GOFLAGS='-trimpath -p=2' COMMANDER_ROLE='operator-feat-auth' COMMANDER_RUN_SESSION='" + res.SessionID + "' 'GIT_COMMITTER_NAME=operator-feat-auth' 'GIT_COMMITTER_EMAIL=operator-feat-auth@operator.commander.invalid' COMMANDER_MANAGER_ROLE='manager-bd-100' 'cmdr' '__codex-bridge'",
		"'--session-id' '" + res.SessionID + "'",
		"'--thread-file' '" + chat.CodexThreadFile(home, "operator-feat-auth") + "'",
		"'--skip-permissions'",
		"`cmdr internal-mail --to manager-bd-100`",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("launcher lacks %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "'claude'") || !strings.Contains(cmd, "'--model' '"+chat.DefaultCodexModel+"'") {
		t.Errorf("a Codex Operator runs the bridge on Commander's Codex model, never claude or a Claude model:\n%s", cmd)
	}
	if got := chat.ReadRuntime(home, "operator-feat-auth"); got != chat.RuntimeCodex {
		t.Errorf("role runtime = %q: the app would open this Operator's chat on claude", got)
	}
}

// A Manager on Codex, even one a person pinned there, passes nothing to its
// Operators: they run on Claude unless asked otherwise.
func TestOperatorRuntimeNeverInheritsAManagersCodex(t *testing.T) {
	useAgentOrder(t, chat.RuntimeClaude)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	home := os.Getenv("COMMANDER_HOME")
	req := SpawnRequest{TaskBeadID: "bd-1.1", managerRole: "manager-bd-1"}
	if got := pickRuntime(req, "operator-a"); got != chat.RuntimeClaude {
		t.Fatalf("no choice anywhere, share 0 = %q", got)
	}
	if err := chat.WriteRuntime(home, "manager-bd-1", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if !chat.RuntimePinned(home, "manager-bd-1") {
		t.Fatal("setup: Manager should be pinned")
	}
	if got := pickRuntime(req, "operator-b"); got != chat.RuntimeClaude {
		t.Errorf("Manager pinned on codex passed to its Operator: %q", got)
	}
	req.Runtime = chat.RuntimeClaude
	if got := pickRuntime(req, "operator-c"); got != chat.RuntimeClaude {
		t.Errorf("explicit claude: %q", got)
	}
}

// A drawn Codex respawn moves to Claude; an explicit choice still holds.
func TestOperatorRespawnMovesOffCodex(t *testing.T) {
	home := codexDraws(t) // every draw says Codex: a respawn must not draw either
	if err := chat.WriteAutoRuntime(home, "operator-a", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	req := SpawnRequest{TaskBeadID: "bd-1.1", Resume: true}
	if got := pickRuntime(req, "operator-a"); got != chat.RuntimeClaude || chat.ReadRuntime(home, "operator-a") != chat.RuntimeClaude {
		t.Errorf("respawn on codex = %q, recorded %q", got, chat.ReadRuntime(home, "operator-a"))
	}
	if got := pickRuntime(req, "operator-b"); got != chat.RuntimeClaude {
		t.Errorf("respawn with no runtime file drew %q", got)
	}
	req.Runtime = chat.RuntimeCodex
	if got := pickRuntime(req, "operator-a"); got != chat.RuntimeCodex {
		t.Errorf("explicit codex on a respawn = %q", got)
	}
}

// The move off Codex says so on the bead, once, as a cmdrd note (never read as
// the Operator's report); no other respawn writes a comment.
func TestOperatorRespawnOffCodexCommentsOnTheBead(t *testing.T) {
	home := codexDraws(t)
	if err := chat.WriteAutoRuntime(home, "operator-a", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	b := &beads.MockClient{}
	s := &Spawner{beads: b}
	req := SpawnRequest{TaskBeadID: "bd-1.1", ProjectPath: "/p", Resume: true}
	if rt := s.provideRuntime(t, req, "operator-a"); rt != chat.RuntimeClaude {
		t.Fatalf("runtime %q", rt)
	}
	if rt := s.provideRuntime(t, req, "operator-a"); rt != chat.RuntimeClaude {
		t.Fatalf("second respawn: runtime %q", rt)
	}
	comments := 0
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "AddComment:bd-1.1") {
			comments++
			for _, want := range []string{"cmdrd: ", "operator-a", "Claude", "Share-drawn"} {
				if !strings.Contains(c, want) {
					t.Errorf("comment lacks %q: %s", want, c)
				}
			}
			if strings.Contains(c, "no role") || strings.Contains(c, "cmdr runtime") {
				t.Errorf("comment points at clearing a role: %s", c)
			}
		}
	}
	if comments != 1 {
		t.Errorf("comments = %d: %v", comments, b.Calls)
	}
}

// Operators inherit only a runtime a person chose for their Manager, not one
// a share drew: else the Operator share would never apply under such Managers.
func TestOperatorRuntimeIgnoresAManagersDrawnRuntime(t *testing.T) {
	useAgentOrder(t, chat.RuntimeClaude)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	home := os.Getenv("COMMANDER_HOME")
	if err := chat.WriteAutoRuntime(home, "manager-bd-1", chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if got := pickRuntime(SpawnRequest{TaskBeadID: "bd-1.1", managerRole: "manager-bd-1"}, "operator-a"); got != chat.RuntimeClaude {
		t.Errorf("share 0 under a drawn-codex Manager: %s", got)
	}
}

func TestOperatorAccountDefaultsToItsManagers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(filepath.Join(home, "claude-accounts", "work"), 0o700)
	os.WriteFile(filepath.Join(home, "claude-accounts", "work", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600)
	os.MkdirAll(filepath.Join(home, "claude-accounts", "spare"), 0o700)
	req := SpawnRequest{managerRole: "manager-bd-1"}
	if got, err := resolveOperatorAccount(req, "operator-a"); err != nil || got != claudeacct.Default {
		t.Fatalf("no choice anywhere = %q %v", got, err)
	}
	if err := chat.WriteAccount(home, "manager-bd-1", "work"); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveOperatorAccount(req, "operator-b"); got != "work" || chat.ReadAccount(home, "operator-b") != "work" {
		t.Errorf("Manager on work: %q", got)
	}
	req.Account = "spare"
	if _, err := resolveOperatorAccount(req, "operator-c"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("logged-out account: %v", err)
	}
	req.Account = "nope"
	if _, err := resolveOperatorAccount(req, "operator-c"); err == nil {
		t.Error("unknown account accepted")
	}
	// A respawn keeps its own account.
	if err := chat.WriteAccount(home, "manager-bd-1", claudeacct.Default); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveOperatorAccount(SpawnRequest{managerRole: "manager-bd-1", Resume: true}, "operator-b"); got != "work" {
		t.Errorf("respawn: %q", got)
	}
}

// pickRuntime is resolveOperatorRuntime without the event reason.
func pickRuntime(req SpawnRequest, agentName string) string {
	rt, _ := resolveOperatorRuntime(req, agentName)
	return rt
}

// codexDraws puts new agents on Codex: the order Codex then Claude, Codex
// signed in, hooks ready.
func codexDraws(t *testing.T) string {
	t.Helper()
	t.Setenv("COMMANDER_HOME", t.TempDir())
	useAgentOrder(t, chat.RuntimeCodex, chat.RuntimeClaude)
	codexSignedIn(t)
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	return os.Getenv("COMMANDER_HOME")
}

// useAgentOrder sets the agents' account order for one test.
func useAgentOrder(t *testing.T, order ...string) {
	t.Helper()
	prev := defaultAgentOrder
	defaultAgentOrder = func() []string { return order }
	t.Cleanup(func() { defaultAgentOrder = prev })
}

// codexSignedIn gives the test a signed-in Codex account.
func codexSignedIn(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// New agents run on the first account in their order that can take a run.
func TestNewAgentsFollowTheAccountOrder(t *testing.T) {
	home := codexDraws(t)
	if got := pickRuntime(SpawnRequest{TaskBeadID: "bd-1"}, "operator-a"); got != chat.RuntimeCodex {
		t.Fatalf("Codex first: %s", got)
	}
	if got := pickRuntime(SpawnRequest{TaskBeadID: "bd-1", Runtime: chat.RuntimeClaude}, "operator-b"); got != chat.RuntimeClaude {
		t.Fatalf("explicit Claude lost to the order: %s", got)
	}
	useAgentOrder(t, chat.RuntimeClaude, chat.RuntimeCodex)
	if got := pickRuntime(SpawnRequest{TaskBeadID: "bd-1"}, "operator-d"); got != chat.RuntimeClaude {
		t.Fatalf("Claude first: %s", got)
	}
	useAgentOrder(t, chat.RuntimeCodex, chat.RuntimeClaude)
	codexHooksReady = func() bool { return false }
	if got := pickRuntime(SpawnRequest{TaskBeadID: "bd-1"}, "operator-e"); got != chat.RuntimeClaude {
		t.Fatalf("Codex without hooks: %s", got)
	}
	codexHooksReady = func() bool { return true }
	markOut(t, home, chat.RuntimeCodex, "")
	if got := pickRuntime(SpawnRequest{TaskBeadID: "bd-1"}, "operator-c"); got != chat.RuntimeClaude {
		t.Fatalf("Codex out, Claude is the fallback: %s", got)
	}
}

func TestServiceTaskForcesClaudeOnACodexDraw(t *testing.T) {
	codexDraws(t)
	req := SpawnRequest{TaskBeadID: "bd-1.1", TaskText: "Start the Podman machine and run the suite"}
	rt, reason := resolveOperatorRuntime(req, "operator-a")
	if rt != chat.RuntimeClaude || reason != ReasonClaudeServiceKeyword {
		t.Errorf("keyword on a codex draw = %q, %q", rt, reason)
	}
	req.TaskText = "Refactor the parser"
	if rt, reason := resolveOperatorRuntime(req, "operator-b"); rt != chat.RuntimeCodex || reason != ReasonAgentAccounts {
		t.Errorf("no keyword leaves the draw: %q, %q", rt, reason)
	}
}

func TestServiceTaskKeywordsAreCaseInsensitiveAndConservative(t *testing.T) {
	for _, text := range []string{"DOCKER build", "docker-compose up", "docker compose up", "run the Dev Server", "gvproxy", "vfkit", "colima start", "lima vm", "podman machine start"} {
		if !needsBackgroundService(text) {
			t.Errorf("%q should match", text)
		}
	}
	for _, text := range []string{"restart the daemon", "cmdrd daemon work", "Build VM over ssh", "the server service", "dockerfile lint", "climate"} {
		if needsBackgroundService(text) {
			t.Errorf("%q should not match", text)
		}
	}
}

func TestServiceTaskExplicitCodexWins(t *testing.T) {
	codexDraws(t)
	req := SpawnRequest{TaskBeadID: "bd-1.1", TaskText: "docker compose up", Runtime: chat.RuntimeCodex}
	if rt, reason := resolveOperatorRuntime(req, "operator-a"); rt != chat.RuntimeCodex || reason != chat.RuntimeCodex {
		t.Errorf("explicit codex = %q, %q", rt, reason)
	}
}

func TestOperatorRespawnPreservesExplicitPinWithReadyHooks(t *testing.T) {
	home := codexDraws(t)
	req := SpawnRequest{TaskBeadID: "bd-1.1", Runtime: chat.RuntimeCodex}
	if got := pickRuntime(req, "operator-explicit"); got != chat.RuntimeCodex || !chat.RuntimePinned(home, "operator-explicit") {
		t.Fatalf("explicit runtime %s is not pinned", got)
	}
	req.Runtime, req.Resume = "", true
	if got := pickRuntime(req, "operator-explicit"); got != chat.RuntimeCodex {
		t.Fatalf("pinned respawn %s", got)
	}
	codexHooksReady = func() bool { return false }
	if got := pickRuntime(req, "operator-explicit"); got != chat.RuntimeClaude {
		t.Fatalf("without hooks: %s", got)
	}
	if !chat.RuntimePinned(home, "operator-explicit") || chat.ReadRuntime(home, "operator-explicit") != chat.RuntimeClaude {
		t.Fatal("temporary hook failure lost the explicit choice")
	}
	codexHooksReady = func() bool { return true }
	if got := pickRuntime(req, "operator-explicit"); got != chat.RuntimeCodex {
		t.Fatalf("hooks restored: %s", got)
	}
}

func TestOperatorShareDrawRemainsAutomaticOnRespawn(t *testing.T) {
	home := codexDraws(t)
	req := SpawnRequest{TaskBeadID: "bd-1.1"}
	if got := pickRuntime(req, "operator-drawn"); got != chat.RuntimeCodex || chat.RuntimePinned(home, "operator-drawn") {
		t.Fatalf("draw %s was pinned", got)
	}
	req.Resume = true
	if got := pickRuntime(req, "operator-drawn"); got != chat.RuntimeClaude || chat.RuntimePinned(home, "operator-drawn") {
		t.Fatalf("drawn respawn %s was pinned", got)
	}
}

// Exercise the host launcher as well as the resolver: a pinned crash restart
// keeps the bridge and its role guard, while an automatic draw uses Claude.
func TestSpawnCodeHost_RespawnUsesPinnedRuntimeAndHooks(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned=%t", pinned), func(t *testing.T) {
			dir := makeGitRepo(t)
			sp, _, _, _, _, _ := newHostSpawner(t, dir)
			home := agentrt.CommanderHome()
			old := codexHooksReady
			codexHooksReady = func() bool { return true }
			t.Cleanup(func() { codexHooksReady = old })
			write := chat.WriteAutoRuntime
			if pinned {
				write = chat.WriteRuntime
			}
			if err := write(home, "operator-feat-auth", chat.RuntimeCodex); err != nil {
				t.Fatal(err)
			}
			req := goodRequest(t, dir)
			req.Resume = true
			if _, err := sp.Spawn(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			cmd := launcherCmd(t, dir)
			if pinned {
				for _, want := range []string{"'__codex-bridge'", "'--hooks-role' 'operator-feat-auth'", "'--guard-only'"} {
					if !strings.Contains(cmd, want) {
						t.Errorf("launcher lacks %s: %s", want, cmd)
					}
				}
			} else if !strings.Contains(cmd, "'claude'") || strings.Contains(cmd, "'__codex-bridge'") {
				t.Fatalf("automatic respawn launcher: %s", cmd)
			}
			if chat.RuntimePinned(home, "operator-feat-auth") != pinned {
				t.Fatal("respawn changed pin ownership")
			}
		})
	}
}

func TestOperatorLegacyRuntimeIsNotExplicit(t *testing.T) {
	home := codexDraws(t)
	role := "operator-legacy"
	if err := chat.WriteEffectiveRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if chat.RuntimePinned(home, role) {
		t.Fatal("legacy record became a pin")
	}
	req := SpawnRequest{Resume: true}
	if got := pickRuntime(req, role); got != chat.RuntimeClaude {
		t.Fatal(got)
	}
	// Reassert only a known explicit choice through the supported writer.
	if err := chat.WriteRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if got := pickRuntime(req, role); got != chat.RuntimeCodex {
		t.Fatal(got)
	}
}

func TestOperatorFallbackHandoffAndRecovery(t *testing.T) {
	home := codexDraws(t)
	role := "operator-recovery"
	if err := chat.WriteRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "chat", role+".jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	codexHooksReady = func() bool { return false }
	for i := 0; i < 2; i++ {
		rt, reason := resolveOperatorRuntime(SpawnRequest{Resume: true}, role)
		if rt != chat.RuntimeClaude || reason != "claude-hooks-unavailable" || chat.ReadRuntime(home, role) != rt || chat.ReadRuntimePin(home, role) != chat.RuntimeCodex {
			t.Fatalf("%s %s", rt, reason)
		}
		if _, err := os.Stat(chat.HandoffFile(home, role)); err != nil {
			t.Fatal(err)
		}
	}
	codexHooksReady = func() bool { return true }
	if got := pickRuntime(SpawnRequest{Resume: true}, role); got != chat.RuntimeCodex {
		t.Fatal(got)
	}
	if _, err := os.Stat(chat.HandoffFile(home, role)); !os.IsNotExist(err) {
		t.Fatal("stale handoff", err)
	}
}

func TestExplicitCodexRequiresHooksAndAccountSwitchKeepsIntent(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			home := codexDraws(t)
			role := "operator-hooks"
			codexHooksReady = func() bool { return false }
			rt, reason := resolveOperatorRuntime(SpawnRequest{Runtime: chat.RuntimeCodex, Resume: resume}, role)
			if rt != chat.RuntimeClaude || reason != ReasonClaudeHooksUnavailable || chat.ReadRuntime(home, role) != rt || chat.ReadRuntimePin(home, role) != chat.RuntimeCodex {
				t.Fatalf("unguarded explicit choice: %s %s pin=%s", rt, reason, chat.ReadRuntimePin(home, role))
			}
			// Switching an account must leave the Codex choice available for recovery.
			rt, _ = resolveOperatorRuntime(SpawnRequest{Resume: true, Account: "work"}, role)
			if rt != chat.RuntimeClaude || chat.ReadRuntimePin(home, role) != chat.RuntimeCodex {
				t.Fatal("account switch lost intent")
			}
			codexHooksReady = func() bool { return true }
			if got := pickRuntime(SpawnRequest{Resume: true}, role); got != chat.RuntimeCodex {
				t.Fatal("recovery:", got)
			}
			// Even with hooks ready an account-only request runs Claude without repinning.
			rt, _ = resolveOperatorRuntime(SpawnRequest{Resume: true, Account: "work"}, role)
			if rt != chat.RuntimeClaude || chat.ReadRuntimePin(home, role) != chat.RuntimeCodex {
				t.Fatal("ready account switch lost intent")
			}
		})
	}
}

func TestHooksFallbackNoteExplainsGuardReadiness(t *testing.T) {
	home := codexDraws(t)
	role := "operator-note"
	if err := chat.WriteRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	codexHooksReady = func() bool { return false }
	b := &beads.MockClient{}
	s := &Spawner{beads: b}
	s.provideRuntime(t, SpawnRequest{Resume: true, TaskBeadID: "sample-1", ProjectPath: "/sample"}, role)
	found := false
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "AddComment:") {
			found = true
			if !strings.Contains(c, "guard hooks are unavailable") || strings.Contains(c, "Share-drawn") {
				t.Fatal(c)
			}
		}
	}
	if !found {
		t.Fatal("missing fallback note")
	}
}

// provideRuntime resolves and applies a spawn's provider the way the host
// spawn does, returning the runtime it runs on.
func (s *Spawner) provideRuntime(t *testing.T, req SpawnRequest, agentName string) string {
	t.Helper()
	r, err := s.resolveProvider(context.Background(), req, agentName)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyProvider(context.Background(), r, agentName); err != nil {
		t.Fatal(err)
	}
	return r.Runtime
}
