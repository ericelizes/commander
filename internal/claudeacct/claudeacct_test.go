package claudeacct

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUser gives the test its own HOME with a ~/.claude, a ~/.claude.json and
// a fake Keychain holding the given services.
func fakeUser(t *testing.T, keychain ...string) (home, user string) {
	t.Helper()
	user = t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	claude := filepath.Join(user, ".claude")
	for _, d := range []string{"skills", "plugins", "commands", "projects"} {
		os.MkdirAll(filepath.Join(claude, d), 0o755)
	}
	os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{"model":"opus"}`), 0o644)
	os.WriteFile(filepath.Join(claude, "CLAUDE.md"), []byte("# prefs"), 0o644)
	os.WriteFile(filepath.Join(claude, ".credentials.json"), []byte(`{"secret":1}`), 0o600)
	os.WriteFile(filepath.Join(user, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"me@example.com"},"mcpServers":{"linear":{"type":"http","url":"https://mcp.linear.app/mcp"}}}`), 0o600)
	held := map[string]bool{}
	for _, s := range keychain {
		held[s] = true
	}
	prev := KeychainHas
	KeychainHas = func(s string) bool { return held[s] }
	ForgetProbes()
	t.Cleanup(func() { KeychainHas = prev; ForgetProbes() })
	return t.TempDir(), user
}

func TestKeychainServiceMatchesClaudeCode(t *testing.T) {
	// Claude Code 2.1.308: "Claude Code-credentials" + "-" + sha256(CLAUDE_CONFIG_DIR)[:8].
	if got := KeychainService(""); got != "Claude Code-credentials" {
		t.Errorf("default = %q", got)
	}
	if got := KeychainService("/tmp/example/.commander/claude-accounts/work"); got != "Claude Code-credentials-467b6b41" {
		t.Errorf("named = %q", got)
	}
}

func TestCreateSharesConfigButNotCredentials(t *testing.T) {
	home, user := fakeUser(t)
	a, err := Create(home, "work")
	if err != nil {
		t.Fatal(err)
	}
	if a.LoggedIn || a.Dir != filepath.Join(home, "claude-accounts", "work") {
		t.Fatalf("new account = %+v", a)
	}
	for _, e := range []string{"settings.json", "CLAUDE.md", "skills", "plugins", "commands"} {
		target, err := os.Readlink(filepath.Join(a.Dir, e))
		if err != nil || target != filepath.Join(user, ".claude", e) {
			t.Errorf("%s -> %q (%v)", e, target, err)
		}
	}
	for _, e := range []string{".credentials.json", "projects"} {
		if _, err := os.Lstat(filepath.Join(a.Dir, e)); !os.IsNotExist(err) {
			t.Errorf("%s must not be shared: %v", e, err)
		}
	}
	var cfg map[string]json.RawMessage
	raw, _ := os.ReadFile(filepath.Join(a.Dir, ".claude.json"))
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["mcpServers"]; !ok {
		t.Errorf("mcp servers not copied: %s", raw)
	}
	if _, ok := cfg["oauthAccount"]; ok {
		t.Errorf("the default login leaked into the account: %s", raw)
	}
	if _, err := Create(home, "work"); err == nil {
		t.Error("duplicate account accepted")
	}
	for _, bad := range []string{"default", "codex", "Work", "../x", ""} {
		if _, err := Create(home, bad); err == nil {
			t.Errorf("Create(%q) accepted", bad)
		}
	}
}

func TestLoggedInFollowsTheAccountsOwnKeychainItem(t *testing.T) {
	home, _ := fakeUser(t)
	os.MkdirAll(filepath.Join(home, "claude-accounts", "work"), 0o700)
	os.MkdirAll(filepath.Join(home, "claude-accounts", "spare"), 0o700)
	work := KeychainService(filepath.Join(home, "claude-accounts", "work"))
	KeychainHas = func(s string) bool { return s == work || s == "Claude Code-credentials" }
	ForgetProbes()

	got := map[string]bool{}
	for _, a := range List(home) {
		got[a.Name] = a.LoggedIn
	}
	if len(got) != 3 || !got["default"] || !got["work"] || got["spare"] {
		t.Fatalf("logged in = %v", got)
	}
	if List(home)[0].Email != "me@example.com" {
		t.Errorf("default email = %+v", List(home)[0])
	}
	// The plaintext fallback counts too.
	os.WriteFile(filepath.Join(home, "claude-accounts", "spare", ".credentials.json"), []byte("{}"), 0o600)
	if !Get(home, "spare").LoggedIn {
		t.Error("credentials file not seen")
	}
}

func TestEnvAndLoginCommand(t *testing.T) {
	home := "/h"
	if Env(home, Default) != nil || LoginCommand(home, Default) != "env -u CLAUDE_CONFIG_DIR claude auth login" {
		t.Error("default must not override CLAUDE_CONFIG_DIR")
	}
	if got := Env(home, "work"); len(got) != 1 || got[0] != "CLAUDE_CONFIG_DIR=/h/claude-accounts/work" {
		t.Errorf("env = %v", got)
	}
	if got := LoginCommand("/my home", "work"); got != "CLAUDE_CONFIG_DIR='/my home/claude-accounts/work' claude auth login" {
		t.Errorf("login = %q", got)
	}
}

func TestSyncRefreshesMCPAndNewLinks(t *testing.T) {
	home, user := fakeUser(t)
	a, _ := Create(home, "work")
	os.MkdirAll(filepath.Join(user, ".claude", "agents"), 0o755)
	os.WriteFile(filepath.Join(user, ".claude.json"), []byte(`{"mcpServers":{"notion":{}}}`), 0o600)
	if err := Sync(home, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(a.Dir, "agents")); err != nil {
		t.Error("new shared entry not linked")
	}
	raw, _ := os.ReadFile(filepath.Join(a.Dir, ".claude.json"))
	if !json.Valid(raw) || !strings.Contains(string(raw), "notion") {
		t.Errorf("mcp not refreshed: %s", raw)
	}
}

func TestDefaultIgnoresCallerConfigDirAndMarksDuplicates(t *testing.T) {
	home, user := fakeUser(t)
	a, err := Create(home, "second")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(user, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@example.test","accountUuid":"account-one","organizationUuid":"org-one"}}`), 0600)
	os.WriteFile(filepath.Join(a.Dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@example.test","accountUuid":"account-one"}}`), 0600)
	os.WriteFile(filepath.Join(a.Dir, ".credentials.json"), []byte("{}"), 0600)
	t.Setenv("CLAUDE_CONFIG_DIR", a.Dir)
	got := List(home)
	if got[0].Dir != filepath.Join(user, ".claude") || got[0].AccountUUID != "account-one" || got[0].OrgUUID != "org-one" || got[1].DuplicateOf != Default {
		t.Fatalf("identity: %+v", got)
	}
	if SameIdentity(Account{LoggedIn: true, AccountUUID: "a", Email: "same@example.test"}, Account{LoggedIn: true, AccountUUID: "b", Email: "same@example.test"}) {
		t.Fatal("different UUIDs merged")
	}
	if !SameIdentity(Account{LoggedIn: true, Email: "ONE@example.test"}, Account{LoggedIn: true, Email: "one@example.test"}) {
		t.Fatal("email fallback")
	}
	if SameIdentity(Account{Email: "one@example.test"}, got[0]) {
		t.Fatal("logged-out identity counts")
	}
}

func TestVerifyUsesProfileAndDoesNotExposeRawOutput(t *testing.T) {
	home, _ := fakeUser(t)
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$*\" = 'auth status --json' ] || exit 8\n[ -z \"$CLAUDE_CONFIG_DIR\" ] || exit 9\nprintf '%s' '{\"loggedIn\":true,\"email\":\"me@example.com\",\"private\":\"never-return\"}'\n"
	os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", "/invented/inherited")
	v, err := Verify(context.Background(), home, Default)
	if err != nil || !v.Matches || v.Email != "me@example.com" {
		t.Fatalf("%+v %v", v, err)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "never-return") {
		t.Fatal("raw output leaked")
	}
	if got := LoginCommand(home, Default, "x'; echo bad"); !strings.Contains(got, "--email '") {
		t.Fatal(got)
	}
}

func TestVerifyComparesTheFieldsBothSidesKnow(t *testing.T) {
	const stored = `{"oauthAccount":{"emailAddress":"me@example.com","accountUuid":"uuid-one"}}`
	for _, c := range []struct {
		name, profile, status string
		want                  bool
	}{
		{"uuid and email agree", stored, `{"loggedIn":true,"email":"ME@example.com","accountUuid":"uuid-one"}`, true},
		{"matching uuids, CLI email empty", stored, `{"loggedIn":true,"email":"","accountUuid":"uuid-one"}`, true},
		{"matching uuids, profile email empty", `{"oauthAccount":{"accountUuid":"uuid-one"}}`, `{"loggedIn":true,"email":"me@example.com","accountUuid":"uuid-one"}`, true},
		{"email only agrees", stored, `{"loggedIn":true,"email":"me@example.com"}`, true},
		{"different uuid", stored, `{"loggedIn":true,"email":"me@example.com","accountUuid":"uuid-two"}`, false},
		{"different email", stored, `{"loggedIn":true,"email":"other@example.com","accountUuid":"uuid-one"}`, false},
		{"different email, no uuid", stored, `{"loggedIn":true,"email":"other@example.com"}`, false},
		{"nothing to compare", `{}`, `{"loggedIn":true,"email":"me@example.com","accountUuid":"uuid-one"}`, false},
		{"CLI logged out", stored, `{"loggedIn":false,"email":"me@example.com","accountUuid":"uuid-one"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, user := fakeUser(t)
			os.WriteFile(filepath.Join(user, ".claude.json"), []byte(c.profile), 0o600)
			bin := t.TempDir()
			os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nprintf '%s' '"+c.status+"'\n"), 0o700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			v, err := Verify(context.Background(), home, Default)
			if err != nil || v.Matches != c.want {
				t.Fatalf("Matches = %v, want %v (%+v, %v)", v.Matches, c.want, v, err)
			}
		})
	}
}

func TestVerifyLoggedOutProfileNeverMatches(t *testing.T) {
	home, user := fakeUser(t)
	dir := filepath.Join(home, "claude-accounts", "second")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"me@example.com","accountUuid":"uuid-one"}}`), 0o600)
	_ = user
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nprintf '%s' '{\"loggedIn\":true,\"email\":\"me@example.com\",\"accountUuid\":\"uuid-one\"}'\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if Get(home, "second").LoggedIn {
		t.Fatal("fixture profile should be logged out")
	}
	v, err := Verify(context.Background(), home, "second")
	if err != nil || v.Matches {
		t.Fatalf("logged-out profile matched: %+v %v", v, err)
	}
}

func TestValidLoginEmail(t *testing.T) {
	for email, want := range map[string]bool{
		"":                      true,
		"two@example.test":      true,
		" two@example.test ":    true,
		"o'neil@example.test":   true,
		"--x":                   false,
		" -x@example.test":      false,
		"a\rb@example.test":     false,
		"a\nb@example.test":     false,
		"a\tb@example.test":     false,
		"a\x1b[0m@example.test": false,
		"a\u0085b@example.test": false,
	} {
		if got := ValidLoginEmail(email); got != want {
			t.Errorf("ValidLoginEmail(%q) = %v, want %v", email, got, want)
		}
	}
}

func TestLoginCommandQuotesByAllowList(t *testing.T) {
	home, _ := fakeUser(t)
	for email, want := range map[string]string{
		"two+x@example.test": " --email two+x@example.test",
		"a^b@example.test":   " --email 'a^b@example.test'",
		"a=é@example.test":   " --email 'a=é@example.test'",
		"x'; echo bad":       ` --email 'x'\''; echo bad'`,
		"~root@example.test": " --email '~root@example.test'",
	} {
		if got := LoginCommand(home, Default, email); !strings.HasSuffix(got, want) {
			t.Errorf("LoginCommand(%q) = %s, want suffix %s", email, got, want)
		}
	}
}

func writeCarried(t *testing.T, projects, id, text string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(projects, "-work", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCarryTranscriptSearchesEveryDirAndTheNewestWins(t *testing.T) {
	const id = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"
	type copyAt struct {
		dir string // default, inherited, passed, second
		age time.Duration
	}
	for _, tc := range []struct {
		name   string
		copies []copyAt
		want   string
	}{
		{"only in the inherited dir", []copyAt{{"inherited", time.Hour}}, "inherited"},
		{"only in the tmux server's dir", []copyAt{{"passed", time.Hour}}, "passed"},
		{"only in a named account", []copyAt{{"second", time.Hour}}, "second"},
		{"newer inherited replaces an older copy", []copyAt{{"default", 2 * time.Hour}, {"inherited", time.Hour}}, "inherited"},
		{"an older inherited copy never rewinds", []copyAt{{"default", time.Hour}, {"inherited", 2 * time.Hour}}, "default"},
		{"newest of all wins", []copyAt{{"default", 3 * time.Hour}, {"second", 2 * time.Hour}, {"inherited", 4 * time.Hour}, {"passed", time.Hour}}, "passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := fakeUser(t)
			inherited, passed := t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", inherited)
			if _, err := Create(home, "second"); err != nil {
				t.Fatal(err)
			}
			def := ProjectsDir(home, Default)
			roots := map[string]string{"default": def, "inherited": filepath.Join(inherited, "projects"), "passed": filepath.Join(passed, "projects"), "second": ProjectsDir(home, "second")}
			for _, c := range tc.copies {
				writeCarried(t, roots[c.dir], id, c.dir+"\n", c.age)
			}
			got, err := CarryTranscript(home, def, id, Default, passed)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(def, "-work", id+".jsonl")
			if got != want {
				t.Fatalf("resume from %s, want %s", got, want)
			}
			raw, err := os.ReadFile(want)
			if err != nil || string(raw) != tc.want+"\n" {
				t.Fatalf("default holds %q (%v), want %s", raw, err, tc.want)
			}
		})
	}
}

func TestCarryTranscriptWithNoSessionCopiesNothing(t *testing.T) {
	home, _ := fakeUser(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if got, err := CarryTranscript(home, ProjectsDir(home, Default), "no-such-session", Default); err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTmuxConfigDirReadsOnlyAnAbsoluteValue(t *testing.T) {
	for _, tc := range []struct {
		out  string
		err  error
		want string
	}{
		{"CLAUDE_CONFIG_DIR=/invented/dir\n", nil, "/invented/dir"},
		{"-CLAUDE_CONFIG_DIR\n", nil, ""},
		{"", errors.New("unknown variable"), ""},
		{"CLAUDE_CONFIG_DIR=relative\n", nil, ""},
		{"%99\n", nil, ""},
	} {
		run := func(context.Context, ...string) ([]byte, []byte, error) { return []byte(tc.out), nil, tc.err }
		if got := TmuxConfigDir(context.Background(), run); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.out, got, tc.want)
		}
	}
}
