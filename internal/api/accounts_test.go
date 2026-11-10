package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

func TestAddAccountOpensItsLoginAndListsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	prev := claudeacct.KeychainHas
	claudeacct.KeychainHas = func(string) bool { return false }
	claudeacct.ForgetProbes()
	t.Cleanup(func() { claudeacct.KeychainHas = prev; claudeacct.ForgetProbes() })

	home := t.TempDir()
	e := chat.New(chat.Options{Home: home})
	var opened []string
	src := &fakeSource{}
	src.set("x")
	s := New(Options{Token: testToken, Source: src.load, Chat: e,
		OpenTerminal: func(_ context.Context, cmd string) error { opened = append(opened, cmd); return nil }})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	code, _ := postChat(t, ts, "/api/chat/accounts", map[string]any{"name": "work"})
	want := "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude-accounts", "work") + " claude auth login"
	if code != http.StatusOK || len(opened) != 1 || opened[0] != want {
		t.Fatalf("add: %d, opened %q", code, opened)
	}
	if code, _ := postChat(t, ts, "/api/chat/accounts", map[string]any{"name": "work"}); code != http.StatusBadRequest {
		t.Errorf("duplicate add: %d", code)
	}
	if code, _ := postChat(t, ts, "/api/chat/accounts/work/login", nil); code != http.StatusOK || len(opened) != 2 {
		t.Errorf("login again: %d %v", code, opened)
	}
	// A logged-out account cannot take a chat.
	if code, r := postChat(t, ts, "/api/chat/manager-x/account", map[string]any{"account": "work"}); code != http.StatusBadRequest || !strings.Contains(r.Error, "not logged in") {
		t.Errorf("switch to a logged-out account: %d %+v", code, r)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/chat", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Accounts []chat.AccountView `json:"accounts"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	var names []string
	for _, a := range body.Accounts {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "default,work,codex" {
		t.Errorf("accounts = %v", names)
	}
}

func TestSwitchingAHostOperatorRespawnsIt(t *testing.T) {
	var ran [][]string
	e := chat.New(chat.Options{Home: t.TempDir()})
	src := &fakeSource{}
	src.set("x")
	s := New(Options{Token: testToken, Source: src.load, Chat: e,
		Run: func(_ context.Context, argv []string) CmdResult { ran = append(ran, argv); return CmdResult{OK: true} }})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	if code, r := postChat(t, ts, "/api/chat/operator-op-x/account", map[string]any{"account": "work"}); code != http.StatusOK {
		t.Fatalf("%d %+v", code, r)
	}
	if code, _ := postChat(t, ts, "/api/chat/operator-op-x/runtime", map[string]any{"runtime": "codex"}); code != http.StatusOK {
		t.Fatalf("runtime: %d", code)
	}
	if len(ran) != 2 || strings.Join(ran[0], " ") != "internal-operator-provider operator-op-x --account work" || strings.Join(ran[1], " ") != "internal-operator-provider operator-op-x --runtime codex" {
		t.Fatalf("ran = %v", ran)
	}
}

func TestReconnectDefaultReturnsIdentityAndCopyableCommand(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "/invented/wrong-profile")
	os.MkdirAll(filepath.Join(user, ".claude"), 0700)
	os.WriteFile(filepath.Join(user, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0600)
	os.WriteFile(filepath.Join(user, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@example.test","accountUuid":"one"}}`), 0600)
	e := chat.New(chat.Options{Home: t.TempDir()})
	var command string
	s := New(Options{Token: testToken, Chat: e, OpenTerminal: func(_ context.Context, cmd string) error { command = cmd; return errors.New("fixture has no terminal") }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/chat/accounts/default/login", strings.NewReader(`{"email":"two@example.test"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Opened       bool
		Before       claudeacct.Account
		LoginCommand string `json:"login_command"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || result.Opened || result.Before.AccountUUID != "one" || result.Before.Email != "one@example.test" || command != "env -u CLAUDE_CONFIG_DIR claude auth login --email two@example.test" || result.LoginCommand != command {
		t.Fatalf("%d %+v %s", resp.StatusCode, result, command)
	}
}

func TestLoginRefusesAnEmailThatCouldRewriteTheCommand(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	e := chat.New(chat.Options{Home: t.TempDir()})
	opened := false
	s := New(Options{Token: testToken, Chat: e, OpenTerminal: func(context.Context, string) error { opened = true; return nil }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	for _, body := range []string{`{"email":"--x"}`, `{"email":" -x@example.test"}`, `{"email":"a\rb@example.test"}`, `{"email":"a\u001bb@example.test"}`} {
		req, _ := http.NewRequest("POST", ts.URL+"/api/chat/accounts/default/login", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(raw), "login_command") {
			t.Errorf("%s: %d %s", body, resp.StatusCode, raw)
		}
	}
	if opened {
		t.Error("a terminal opened for a refused email")
	}
}
