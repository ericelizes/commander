package spawn

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

// usageHome is a private home with Claude accounts default (HOME's ~/.claude)
// and the named others, all logged in, each on its own login.
func usageHome(t *testing.T, accounts ...string) string {
	t.Helper()
	home, user := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("HOME", user)
	dirs := []string{filepath.Join(user, ".claude")}
	for _, a := range accounts {
		dirs = append(dirs, filepath.Join(home, "claude-accounts", a))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// markOut records runtime/account as out for an hour, as cmdrd does at a limit.
func markOut(t *testing.T, home, runtime, account string) {
	t.Helper()
	now := time.Now()
	chat.OpenUsage(home).MarkOut(runtime, account, "manager-x", now.Add(time.Hour).Unix(), now)
}

func TestSteerByUsage(t *testing.T) {
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	now := time.Now()
	out := chat.Usage{Status: chat.UsageOut, OutUntil: now.Add(time.Hour).Unix()}
	reset := chat.Usage{Status: chat.UsageOut, OutUntil: now.Add(-time.Minute).Unix()}
	home := usageHome(t, "work")
	cases := []struct {
		name               string
		usage              map[string]chat.Usage
		rt, acct           string
		askedRt, askedAcct string
		wantRt, wantAcct   string
		moved              bool
		err                []string
	}{
		{name: "nothing out", rt: chat.RuntimeCodex, acct: "default", wantRt: chat.RuntimeCodex, wantAcct: "default"},
		{name: "codex out, drawn: moves to claude", usage: map[string]chat.Usage{"codex": out},
			rt: chat.RuntimeCodex, acct: "default", wantRt: chat.RuntimeClaude, wantAcct: "default", moved: true},
		{name: "codex out, asked: refused naming claude", usage: map[string]chat.Usage{"codex": out},
			rt: chat.RuntimeCodex, acct: "default", askedRt: chat.RuntimeCodex,
			err: []string{"Codex is out of usage until", "--runtime claude"}},
		{name: "codex out and default out, drawn: moves to work", usage: map[string]chat.Usage{"codex": out, "claude:default": out},
			rt: chat.RuntimeCodex, acct: "default", wantRt: chat.RuntimeClaude, wantAcct: "work", moved: true},
		{name: "claude account out: next with usage", usage: map[string]chat.Usage{"claude:default": out},
			rt: chat.RuntimeClaude, acct: "default", wantRt: chat.RuntimeClaude, wantAcct: "work", moved: true},
		{name: "claude account out, runtime asked: account still moves", usage: map[string]chat.Usage{"claude:default": out},
			rt: chat.RuntimeClaude, acct: "default", askedRt: chat.RuntimeClaude, wantRt: chat.RuntimeClaude, wantAcct: "work", moved: true},
		{name: "claude account out, asked: refused naming the next", usage: map[string]chat.Usage{"claude:work": out},
			rt: chat.RuntimeClaude, acct: "work", askedAcct: "work", err: []string{"Claude account work is out of usage", "--account default"}},
		{name: "every claude account out: refused naming codex", usage: map[string]chat.Usage{"claude:default": out, "claude:work": out},
			rt: chat.RuntimeClaude, acct: "default", err: []string{"no other logged-in Claude account has usage left", "--runtime codex"}},
		{name: "everything out: refused", usage: map[string]chat.Usage{"codex": out, "claude:default": out, "claude:work": out},
			rt: chat.RuntimeCodex, acct: "default", err: []string{"no other logged-in Claude account has usage left", "nothing was started"}},
		{name: "a reset passed counts as usage", usage: map[string]chat.Usage{"codex": reset},
			rt: chat.RuntimeCodex, acct: "default", askedRt: chat.RuntimeCodex, wantRt: chat.RuntimeCodex, wantAcct: "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mv, err := steerByUsage(home, c.usage, now, c.rt, c.acct, c.askedRt, c.askedAcct)
			if len(c.err) > 0 {
				if err == nil {
					t.Fatalf("not refused: %+v", mv)
				}
				for _, w := range c.err {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error lacks %q: %v", w, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mv.runtime != c.wantRt || mv.account != c.wantAcct || (mv.note != "") != c.moved {
				t.Fatalf("got %+v, want %s/%s moved=%t", mv, c.wantRt, c.wantAcct, c.moved)
			}
		})
	}
	// Every refusal on an everything-out machine still names no account with usage.
	_, err := steerByUsage(home, map[string]chat.Usage{"codex": out, "claude:default": out, "claude:work": out}, now,
		chat.RuntimeClaude, "work", "", "work")
	if err == nil || strings.Contains(err.Error(), "--account") || strings.Contains(err.Error(), "--runtime codex") {
		t.Fatalf("named an out account as the way out: %v", err)
	}
}

// Profiles on one login run out together: an out default never moves to a
// second profile of the same login.
func TestSteerByUsageSkipsTheSameLogin(t *testing.T) {
	home := usageHome(t, "twin")
	for _, d := range []string{filepath.Join(os.Getenv("HOME"), ".claude.json"), filepath.Join(home, "claude-accounts", "twin", ".claude.json")} {
		if err := os.WriteFile(d, []byte(`{"oauthAccount":{"emailAddress":"one@example.test","accountUuid":"one"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	_, err := steerByUsage(home, map[string]chat.Usage{"claude:default": {Status: chat.UsageOut}}, now, chat.RuntimeClaude, "default", "", "")
	if err == nil || !strings.Contains(err.Error(), "no other logged-in Claude account") {
		t.Fatalf("moved to the same login: %v", err)
	}
}

// A pinned Codex Operator whose crash restart meets Codex out runs on Claude,
// keeps its pin for later, records the reason and says so on its bead.
func TestResolveProviderMovesAPinnedCodexRestartOffAnOutCodex(t *testing.T) {
	home := usageHome(t, "work")
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	role := "operator-pinned"
	if err := chat.WriteRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	markOut(t, home, chat.RuntimeCodex, "")
	b := &beads.MockClient{}
	s := &Spawner{beads: b}
	req, err := s.resolveProvider(context.Background(), SpawnRequest{Resume: true, TaskBeadID: "sample-1", ProjectPath: "/sample"}, role)
	if err != nil {
		t.Fatal(err)
	}
	if req.Runtime != chat.RuntimeClaude || req.runtimeReason != ReasonClaudeCodexOut || req.Account != claudeacct.Default {
		t.Fatalf("req = %s %s %s", req.Runtime, req.runtimeReason, req.Account)
	}
	// Decided, not recorded: a later refusal in the spawn leaves no move behind.
	if chat.ReadRuntime(home, role) != chat.RuntimeCodex || len(b.Calls) != 0 {
		t.Fatalf("recorded before the spawn could refuse: runtime %s, calls %v", chat.ReadRuntime(home, role), b.Calls)
	}
	if err := s.applyProvider(context.Background(), req, role); err != nil {
		t.Fatal(err)
	}
	if chat.ReadRuntime(home, role) != chat.RuntimeClaude || chat.ReadRuntimePin(home, role) != chat.RuntimeCodex {
		t.Fatalf("runtime %s pin %s", chat.ReadRuntime(home, role), chat.ReadRuntimePin(home, role))
	}
	if !strings.Contains(req.moved, "Codex is out of usage") {
		t.Fatalf("moved = %q", req.moved)
	}
	notes := 0
	for _, c := range b.Calls {
		if strings.HasPrefix(c, "AddComment:sample-1") {
			notes++
			if !strings.Contains(c, "cmdrd: operator-pinned was headed for Codex") {
				t.Errorf("note = %s", c)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("notes = %d: %v", notes, b.Calls)
	}
}

// An account switch on a respawn that meets the account out is refused before
// anything moves; an inherited (Manager's) account that is out moves.
func TestResolveProviderAccount(t *testing.T) {
	home := usageHome(t, "work")
	markOut(t, home, chat.RuntimeClaude, "work")
	b := &beads.MockClient{}
	s := &Spawner{beads: b}
	if _, err := s.resolveProvider(context.Background(), SpawnRequest{Account: "work", TaskBeadID: "sample-1"}, "operator-a"); err == nil ||
		!strings.Contains(err.Error(), "--account default") {
		t.Fatalf("explicit out account: %v", err)
	}
	if err := chat.WriteAccount(home, "manager-sample-1", "work"); err != nil {
		t.Fatal(err)
	}
	req, err := s.resolveProvider(context.Background(), SpawnRequest{managerRole: "manager-sample-1", TaskBeadID: "sample-1"}, "operator-b")
	if err != nil {
		t.Fatal(err)
	}
	if req.Account != claudeacct.Default || req.moved == "" {
		t.Fatalf("inherited out account: %s %q", req.Account, req.moved)
	}
	if _, err := os.Stat(chat.AccountFile(home, "operator-b")); !os.IsNotExist(err) {
		t.Fatalf("resolveProvider wrote the account before the spawn could refuse: %v", err)
	}
	if err := s.applyProvider(context.Background(), req, "operator-b"); err != nil {
		t.Fatal(err)
	}
	if chat.ReadAccount(home, "operator-b") != claudeacct.Default || chat.ReadAccountIntent(home, "operator-b") != "work" {
		t.Fatalf("after the move: account %s intent %s", chat.ReadAccount(home, "operator-b"), chat.ReadAccountIntent(home, "operator-b"))
	}
}

// A move keeps the account the role was headed for: a respawn while it is
// still out moves again and keeps it; once it has usage the respawn goes back
// to it and the intent is spent. A person's later account choice replaces it.
func TestUsageMoveKeepsTheChosenAccount(t *testing.T) {
	home := usageHome(t, "work")
	role := "operator-c"
	if err := chat.WriteAccount(home, role, "work"); err != nil {
		t.Fatal(err)
	}
	markOut(t, home, chat.RuntimeClaude, "work")
	s := &Spawner{beads: &beads.MockClient{}}
	respawn := func() SpawnRequest {
		t.Helper()
		req, err := s.resolveProvider(context.Background(), SpawnRequest{Resume: true, TaskBeadID: "sample-1"}, role)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.applyProvider(context.Background(), req, role); err != nil {
			t.Fatal(err)
		}
		return req
	}
	for i := 0; i < 2; i++ {
		if req := respawn(); req.Account != claudeacct.Default || chat.ReadAccountIntent(home, role) != "work" {
			t.Fatalf("respawn %d while out: %s intent %q", i, req.Account, chat.ReadAccountIntent(home, role))
		}
	}
	if err := os.Remove(filepath.Join(home, "chat", "usage.json")); err != nil {
		t.Fatal(err)
	}
	if req := respawn(); req.Account != "work" || req.moved != "" || chat.ReadAccountIntent(home, role) != "" {
		t.Fatalf("back with usage: %s %q intent %q", req.Account, req.moved, chat.ReadAccountIntent(home, role))
	}
	if err := chat.WriteRunAccount(home, role, claudeacct.Default, "work"); err != nil {
		t.Fatal(err)
	}
	if err := chat.WriteAccount(home, role, claudeacct.Default); err != nil {
		t.Fatal(err)
	}
	if chat.ReadAccountIntent(home, role) != "" {
		t.Fatal("a person's account choice kept the old intent")
	}
}

// CheckUsage refuses exactly what the spawner would, with nothing written: a
// respawn on the role's or Manager's out account with no other account left;
// a new run whose share draw could still land on Codex with usage passes.
func TestCheckUsage(t *testing.T) {
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	home := usageHome(t, "work")
	markOut(t, home, chat.RuntimeClaude, "work")
	if err := CheckUsage(SpawnRequest{Resume: true, Runtime: chat.RuntimeClaude}, "operator-d", ""); err != nil {
		t.Fatalf("default has usage: %v", err)
	}
	markOut(t, home, chat.RuntimeClaude, "default")
	if err := chat.WriteAccount(home, "manager-sample-1", "work"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(filepath.Join(home, "chat"))
	for _, req := range []SpawnRequest{
		{Resume: true, Runtime: chat.RuntimeClaude, TaskBeadID: "sample-1"},
		{Resume: true, TaskBeadID: "sample-1"},
		{TaskText: "fix it"},
	} {
		err := CheckUsage(req, "operator-d", "manager-sample-1")
		if err == nil || !strings.Contains(err.Error(), "Claude account work is out of usage") {
			t.Errorf("%+v: %v", req, err)
		}
	}
	after, _ := os.ReadDir(filepath.Join(home, "chat"))
	if len(after) != len(before) {
		t.Fatalf("CheckUsage wrote: %d -> %d files", len(before), len(after))
	}
	// Codex has usage and the order may pick it: not refused.
	useAgentOrder(t, chat.RuntimeCodex, chat.RuntimeClaude)
	codexSignedIn(t)
	if err := CheckUsage(SpawnRequest{TaskText: "fix it"}, "", "manager-sample-1"); err != nil {
		t.Fatalf("draw could land on Codex: %v", err)
	}
	// A named Codex without its hooks runs Claude: checked as Claude, no loop offered.
	codexHooksReady = func() bool { return false }
	err := CheckUsage(SpawnRequest{Resume: true, Runtime: chat.RuntimeCodex, TaskBeadID: "sample-1"}, "operator-d", "manager-sample-1")
	if err == nil || strings.Contains(err.Error(), "--runtime codex") {
		t.Fatalf("codex without hooks: %v", err)
	}
}

// With no account named, a refused Codex request's way out is --runtime claude
// alone: the spawner moves the run to whichever account has usage.
func TestClaudeWayOutWithNoAccountNamed(t *testing.T) {
	home := usageHome(t, "work")
	now := time.Now()
	out := chat.Usage{Status: chat.UsageOut, OutUntil: now.Add(time.Hour).Unix()}
	usage := map[string]chat.Usage{"codex": out, "claude:default": out}
	_, err := steerByUsage(home, usage, now, chat.RuntimeCodex, claudeacct.Default, chat.RuntimeCodex, "")
	if err == nil || !strings.Contains(err.Error(), "spawn again with --runtime claude (a Claude account has usage left)") {
		t.Fatalf("%v", err)
	}
}

// The default fallback never lands on a login another out profile shares.
func TestNextWithUsageDefaultSharingAnOutLogin(t *testing.T) {
	home := usageHome(t, "twin", "other")
	for _, d := range []string{filepath.Join(os.Getenv("HOME"), ".claude.json"), filepath.Join(home, "claude-accounts", "twin", ".claude.json")} {
		if err := os.WriteFile(d, []byte(`{"oauthAccount":{"emailAddress":"one@example.test","accountUuid":"one"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "claude-accounts", "other", ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"two@example.test","accountUuid":"two"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	out := chat.Usage{Status: chat.UsageOut, OutUntil: now.Add(time.Hour).Unix()}
	usage := map[string]chat.Usage{"claude:twin": out, "claude:other": out}
	if next := nextWithUsage(home, usage, now, "other"); next != "" {
		t.Fatalf("fell back to default, which shares twin's out login: %q", next)
	}
}

func TestRequestedUsage(t *testing.T) {
	home := usageHome(t, "work")
	if err := RequestedUsage("", ""); err != nil {
		t.Fatal(err)
	}
	markOut(t, home, chat.RuntimeCodex, "")
	markOut(t, home, chat.RuntimeClaude, "work")
	if err := RequestedUsage(chat.RuntimeCodex, ""); err == nil || !strings.Contains(err.Error(), "Codex is out of usage") {
		t.Fatalf("codex: %v", err)
	}
	if err := RequestedUsage("", "work"); err == nil || !strings.Contains(err.Error(), "Claude account work is out of usage") {
		t.Fatalf("work: %v", err)
	}
	for _, ok := range [][2]string{{chat.RuntimeClaude, ""}, {"", "default"}, {chat.RuntimeClaude, "default"}} {
		if err := RequestedUsage(ok[0], ok[1]); err != nil {
			t.Errorf("%v refused: %v", ok, err)
		}
	}
}

// The whole host spawn: a share draw to Codex while Codex is out launches
// claude and reports the move; a named Codex while it is out starts nothing.
func TestSpawnCodeHost_CodexOutRunsClaude(t *testing.T) {
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	dir := makeGitRepo(t)
	sp, _, _, _, _, _ := newHostSpawner(t, dir)
	home := os.Getenv("COMMANDER_HOME")
	markOut(t, home, chat.RuntimeCodex, "")

	req := goodRequest(t, dir)
	req.Runtime = chat.RuntimeCodex
	if _, err := sp.Spawn(context.Background(), req); err == nil || !strings.Contains(err.Error(), "Codex is out of usage") {
		t.Fatalf("named Codex while out: %v", err)
	}

	// Unnamed: the order skips the out Codex and runs on its Claude fallback.
	useAgentOrder(t, chat.RuntimeCodex, chat.RuntimeClaude)
	codexSignedIn(t)
	req = goodRequest(t, dir)
	res, err := sp.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if res.Runtime != chat.RuntimeClaude {
		t.Fatalf("result %s %q", res.Runtime, res.Moved)
	}
	if cmd := launcherCmd(t, dir); strings.Contains(cmd, "__codex-bridge") {
		t.Fatalf("launched Codex while it is out:\n%s", cmd)
	}
}

// A spawn the spawner refuses after deciding a usage move (here: its branch is
// the main checkout's) records no move: no cmdrd: note, no moved runtime.
func TestRefusedSpawnRecordsNoUsageMove(t *testing.T) {
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	dir := makeGitRepo(t)
	sp, _, bd, _, _, _ := newHostSpawner(t, dir)
	home := os.Getenv("COMMANDER_HOME")
	markOut(t, home, chat.RuntimeCodex, "")
	useAgentOrder(t, chat.RuntimeCodex, chat.RuntimeClaude)
	codexSignedIn(t)
	current, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	req := goodRequest(t, dir)
	req.Branch = strings.TrimSpace(string(current))
	if _, err := sp.Spawn(context.Background(), req); err == nil || !strings.Contains(err.Error(), "outside the project's worktree folders") {
		t.Fatalf("spawn: %v", err)
	}
	for _, c := range bd.Calls {
		if strings.HasPrefix(c, "AddComment") && strings.Contains(c, "cmdrd:") {
			t.Fatalf("a refused spawn noted a move: %v", bd.Calls)
		}
	}
	role := "operator-" + req.slug()
	for _, f := range []string{chat.RuntimeFile(home, role), chat.AccountFile(home, role), chat.AccountIntentFile(home, role)} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("a refused spawn wrote %s: %v", f, err)
		}
	}
}

// usageNotes are the cmdrd: notes a spawn wrote on its task bead.
func usageNotes(bd *beads.MockClient) []string {
	var out []string
	for _, c := range bd.Calls {
		if strings.HasPrefix(c, "AddComment") && strings.Contains(c, "cmdrd:") {
			out = append(out, c)
		}
	}
	return out
}

// Through the production entry point, for both host spawns: a run moved off an
// out Codex (share draw) or an out account records where it really runs (the
// role's runtime and account, as chat.ProviderOf reads them, equal the
// result), keeps the account it was headed for as its intent, and notes the
// move once on the task bead.
func TestSpawnRecordsTheUsageMove(t *testing.T) {
	old := codexHooksReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady = old })
	for _, projectType := range []string{"code", "knowledge"} {
		for _, move := range []string{"account-out"} {
			t.Run(projectType+"/"+move, func(t *testing.T) {
				dir := makeGitRepo(t)
				sp, _, bd, _, _, _ := newHostSpawner(t, dir)
				home := usageHome(t, "work")
				req := goodRequest(t, dir)
				req.ProjectType = projectType
				role := "operator-feat-auth"
				if projectType == "knowledge" {
					req.Branch = ""
					role = "operator-" + flatTask("bd-100")
				}
				// A crash restart on the role's out account.
				if err := chat.WriteAccount(home, role, "work"); err != nil {
					t.Fatal(err)
				}
				markOut(t, home, chat.RuntimeClaude, "work")
				req.Resume = true
				wantIntent, wantNote := "work", "was headed for Claude account work"
				res, err := sp.Spawn(context.Background(), req)
				if err != nil {
					t.Fatalf("Spawn: %v", err)
				}
				got := chat.ProviderOf(home, role)
				if res.Runtime != chat.RuntimeClaude || res.Account != claudeacct.Default || got != (chat.Provider{Runtime: res.Runtime, Account: res.Account}) {
					t.Fatalf("result %s/%s, recorded %+v", res.Runtime, res.Account, got)
				}
				if chat.ReadRuntimePin(home, role) != "" {
					t.Fatal("a move left a pin nobody chose")
				}
				if i := chat.ReadAccountIntent(home, role); i != wantIntent {
					t.Fatalf("intent %q, want %q", i, wantIntent)
				}
				notes := usageNotes(bd)
				if len(notes) != 1 || !strings.Contains(notes[0], wantNote) || !strings.Contains(res.Moved, wantNote) {
					t.Fatalf("notes %v, moved %q, want one with %q", notes, res.Moved, wantNote)
				}
			})
		}
	}
}

// A crash restart refused for usage (cmdrd calls Spawn with no CLI pre-check)
// writes nothing: the role's runtime, account and kept intent stay as they
// were, its Codex thread stays, and the bead gets no "was on Codex" note.
func TestRefusedRestartWritesNothing(t *testing.T) {
	dir := makeGitRepo(t)
	sp, _, bd, _, _, _ := newHostSpawner(t, dir)
	home := usageHome(t, "work")
	role := "operator-feat-auth"
	if err := chat.WriteAutoRuntime(home, role, chat.RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := chat.WriteRunAccount(home, role, claudeacct.Default, "work"); err != nil {
		t.Fatal(err)
	}
	thread := chat.CodexThreadFile(home, role)
	if err := os.WriteFile(thread, []byte("thread-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		out := map[string]string{}
		entries, _ := os.ReadDir(filepath.Join(home, "chat"))
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(home, "chat", e.Name()))
			out[e.Name()] = string(b)
		}
		return out
	}
	markOut(t, home, chat.RuntimeCodex, "")
	markOut(t, home, chat.RuntimeClaude, "work")
	markOut(t, home, chat.RuntimeClaude, "default")
	before := snapshot()
	req := goodRequest(t, dir)
	req.Resume = true
	if _, err := sp.Spawn(context.Background(), req); err == nil || !strings.Contains(err.Error(), "has usage left") {
		t.Fatalf("spawn: %v", err)
	}
	after := snapshot()
	for name, v := range before {
		if name != "usage.json" && after[name] != v {
			t.Errorf("%s: %q -> %q", name, v, after[name])
		}
	}
	if len(after) != len(before) {
		t.Errorf("files %d -> %d", len(before), len(after))
	}
	for _, c := range bd.Calls {
		if strings.HasPrefix(c, "AddComment") {
			t.Fatalf("a refused restart wrote on the bead: %v", bd.Calls)
		}
	}
}
