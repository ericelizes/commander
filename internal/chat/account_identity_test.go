package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat/chattest"
	"github.com/example-user/commander/internal/claudeacct"
)

func TestChildEnvDefaultDropsInheritedConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/invented/other")
	for _, kv := range childEnv() {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			t.Fatal(kv)
		}
	}
	if !strings.Contains(strings.Join(childEnv("CLAUDE_CONFIG_DIR=/invented/selected"), "\n"), "CLAUDE_CONFIG_DIR=/invented/selected") {
		t.Fatal("named profile lost")
	}
}

func TestNextAccountSkipsSameIdentity(t *testing.T) {
	user, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	for _, name := range []string{"default", "second"} {
		dir, cfg := filepath.Join(user, ".claude"), filepath.Join(user, ".claude.json")
		if name != "default" {
			dir = filepath.Join(home, "claude-accounts", name)
			cfg = filepath.Join(dir, ".claude.json")
		}
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0600)
		os.WriteFile(cfg, []byte(`{"oauthAccount":{"emailAddress":"one@example.test","accountUuid":"one"}}`), 0600)
	}
	e := New(Options{Home: home})
	for _, from := range []string{"default", "second"} {
		if next := e.NextAccount(from); next != "" {
			t.Fatalf("%s switched to same login %s", from, next)
		}
	}
	accounts := Accounts(home, nil, time.Now())
	if accounts[1].DuplicateOf != "default" {
		t.Fatalf("%+v", accounts)
	}
}

// A default chat's claude drops an inherited CLAUDE_CONFIG_DIR and writes under
// ~/.claude, so the Engine must look there too or a restart starts a new session.
func TestRestartDefaultChatResumesUnderInheritedConfigDir(t *testing.T) {
	h := newHarness(t)
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	projects := filepath.Join(user, ".claude", "projects")
	t.Setenv(chattest.ProjectsVar, projects)
	h.opts.ProjectsDir = "" // as cmdrd builds it
	h.e = New(h.opts)

	id := h.role("manager-a")
	h.start("manager-a")
	sub := h.subscribe("manager-a")
	if err := h.e.Send(context.Background(), "manager-a", "remember 42"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
	for deadline := time.Now().Add(5 * time.Second); FindTranscript(projects, id) == "" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if FindTranscript(projects, id) == "" {
		t.Fatal("the fake never wrote its transcript under ~/.claude")
	}

	if err := h.e.Stop(context.Background(), "manager-a"); err != nil {
		t.Fatal(err)
	}
	h.start("manager-a")
	argv := ""
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(argv, "--resume "+id) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		argv = strings.Join(toStrings(h.fakeRecord("argv-" + id)["argv"]), " ")
	}
	if !strings.Contains(argv, "--resume "+id) {
		t.Errorf("restart lost the conversation: %s", argv)
	}
	if dir := h.fakeRecord("argv-" + id)["config_dir"]; dir != "" {
		t.Errorf("default child ran with CLAUDE_CONFIG_DIR=%v", dir)
	}
}

// Every reader of a default transcript (spawn, carry, the foreign-session sync
// through opts.ProjectsDir) agrees with the child; a named account keeps its own.
func TestDefaultTranscriptReadersIgnoreInheritedConfigDir(t *testing.T) {
	user, home, inherited := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	want := filepath.Join(user, ".claude", "projects")
	if got := ClaudeProjectsDir(); got != want {
		t.Fatalf("ClaudeProjectsDir = %s, want %s", got, want)
	}
	const id = "11111111-2222-4333-8444-555555555555"
	transcript := filepath.Join(want, "-work", id+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte("{}\n"), 0o600)
	decoy := filepath.Join(inherited, "projects", "-work", id+".jsonl")
	os.MkdirAll(filepath.Dir(decoy), 0o700)
	os.WriteFile(decoy, []byte("{}\n"), 0o600)
	future := time.Now().Add(time.Hour)
	os.Chtimes(decoy, future, future)

	e := New(Options{Home: home})
	if e.opts.ProjectsDir != want {
		t.Errorf("Engine default = %s", e.opts.ProjectsDir)
	}
	if got := e.projectsDir(Spec{}); got != want {
		t.Errorf("projectsDir(default) = %s", got)
	}
	// The carry alone searches the inherited dir, and brings its newer copy here.
	if got, err := e.carryTranscript(Spec{SessionID: id}); err != nil || got != transcript {
		t.Errorf("carryTranscript = %q, %v, want %s", got, err, transcript)
	}
	if fi, err := os.Stat(transcript); err != nil || fi.ModTime().After(time.Now().Add(time.Minute)) {
		t.Errorf("the inherited copy was not carried: %v %v", fi, err)
	}
	if got := e.projectsDir(Spec{Account: "work"}); got != filepath.Join(home, "claude-accounts", "work", "projects") {
		t.Errorf("named account lost its own dir: %s", got)
	}
}

// profile writes a logged-in profile with the given identity, default under HOME.
func profile(t *testing.T, user, home, name, email, uuid string) {
	t.Helper()
	dir, cfg := filepath.Join(user, ".claude"), filepath.Join(user, ".claude.json")
	if name != claudeacct.Default {
		dir = filepath.Join(home, "claude-accounts", name)
		cfg = filepath.Join(dir, ".claude.json")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600)
	os.WriteFile(cfg, []byte(`{"oauthAccount":{"emailAddress":"`+email+`","accountUuid":"`+uuid+`"}}`), 0o600)
}

// Usage is per profile: a profile with no report that shares a login with one
// that is out is out too, whichever profile the switch starts from.
func TestNextAccountSkipsEveryProfileSharingALoginThatIsOut(t *testing.T) {
	user, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	profile(t, user, home, claudeacct.Default, "one@example.test", "one")
	profile(t, user, home, "second", "one@example.test", "one")
	profile(t, user, home, "spare", "two@example.test", "two")
	e := New(Options{Home: home})
	now := e.opts.Now()

	if next := e.NextAccount("spare"); next != claudeacct.Default {
		t.Fatalf("nothing out yet: next = %q, want default", next)
	}
	e.opts.Usage.MarkOut(RuntimeClaude, claudeacct.Default, "manager-a", now.Add(time.Hour).Unix(), now)
	if next := e.NextAccount("spare"); next != "" {
		t.Fatalf("spare moved to %q, the login that is out", next)
	}
	if next := e.NextAccount(claudeacct.Default); next != "spare" {
		t.Fatalf("default: next = %q, want spare", next)
	}
	if next := e.NextAccount("second"); next != "spare" {
		t.Fatalf("second: next = %q, want spare", next)
	}
}

// A pop-out runs on default, whatever the role's account, and takes the newest
// transcript with it: from the account's dir, and from the config dir the tmux
// server would pass on.
func TestPopOutOfANamedAccountRunsOnDefaultAndCarries(t *testing.T) {
	for _, tc := range []struct{ name, from string }{{"account", "account"}, {"tmux server", "tmux"}} {
		t.Run(tc.name, func(t *testing.T) {
			h, tm, id := handoverHarness(t)
			h.procs.set(id, nil)
			ctx := context.Background()
			t.Setenv("COMMANDER_HOME", h.home)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			dir := claudeacct.Dir(h.home, "second")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := WriteAccount(h.home, "manager-ws1", "second"); err != nil {
				t.Fatal(err)
			}
			source := dir
			if tc.from == "tmux" {
				source = t.TempDir()
				tm.env = "CLAUDE_CONFIG_DIR=" + source + "\n"
			}
			from := filepath.Join(source, "projects", "-work", id+".jsonl")
			if err := os.MkdirAll(filepath.Dir(from), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(from, []byte("carried\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := h.e.PopOut(ctx, "manager-ws1", HandoverOptions{Confirm: true, Wait: 2 * time.Second}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(h.e.opts.ProjectsDir, "-work", id+".jsonl"))
			if err != nil || string(got) != "carried\n" {
				t.Fatalf("transcript in the default dir = %q, %v", got, err)
			}
			resp := tm.called("respawn-pane")
			if len(resp) != 1 || !strings.Contains(resp[0], "env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 ") {
				t.Fatalf("pop-out respawn = %v", resp)
			}
		})
	}
}

// A switch of a role a terminal pane holds records the choice and starts nothing:
// the status says who holds it, which is what cmdr account use reports.
func TestSetAccountOnATerminalHeldRoleKeepsThePane(t *testing.T) {
	h, tm, _ := handoverHarness(t)
	ctx := context.Background()
	if err := os.MkdirAll(claudeacct.Dir(h.home, "second"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeacct.Dir(h.home, "second"), ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeacct.ForgetProbes()
	st, err := h.e.SetAccount(ctx, "manager-ws1", "second", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.OwnedBy != "terminal" {
		t.Errorf("status = running %v, owned by %q", st.Running, st.OwnedBy)
	}
	if got := tm.called("respawn-pane"); len(got) != 0 {
		t.Errorf("the pane was restarted: %v", got)
	}
}
