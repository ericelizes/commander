package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
)

// A stand-in for each CLI: claude reads a code and logs in only on the right
// one, codex prints a device code and finishes when told, and both record
// logouts. The real ones are what cmdrd runs; these pin how it drives them.
const fakeClaude = `#!/bin/sh
case "$1 $2" in
"auth login")
  echo "Opening browser to sign in..."
  echo "If the browser didn't open, visit: https://claude.example/oauth?code=true"
  printf "Paste code here if prompted > "
  read code
  if [ "$code" = "good-code" ]; then
    echo '{"claudeAiOauth":{"accessToken":"t"}}' > "$CLAUDE_CONFIG_DIR/.credentials.json"
    exit 0
  fi
  echo "Login failed"
  exit 1;;
"auth logout")
  echo "$CLAUDE_CONFIG_DIR" >> "$FAKE_LOG"
  rm -f "$CLAUDE_CONFIG_DIR/.credentials.json"
  exit 0;;
esac
exit 2
`
const fakeCodex = `#!/bin/sh
case "$1" in
login)
  echo "1. Open this link in your browser and sign in to your account"
  echo "   https://auth.example/codex/device"
  echo "2. Enter this one-time code (expires in 15 minutes)"
  echo "   1KOA-1GG2L"
  while [ ! -e "$FAKE_LOG.finish" ]; do sleep 0.05; done
  exit 0;;
logout)
  echo codex-logout >> "$FAKE_LOG"
  exit 0;;
esac
exit 2
`

func signinServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"claude": fakeClaude, "codex": fakeCodex} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", log)
	home := t.TempDir()
	if _, err := claudeacct.Create(home, "second"); err != nil {
		t.Fatal(err)
	}
	e := chat.New(chat.Options{Home: home})
	s := New(Options{Token: "test", Chat: e})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, home, log
}

func signinCall(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+"/api/chat/accounts/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestSigninHandsTheLinkToTheAppAndFinishesWithTheCode(t *testing.T) {
	srv, home, _ := signinServer(t)
	code, out := signinCall(t, srv, "POST", "second/signin", "")
	if code != 200 || out["url"] != "https://claude.example/oauth?code=true" {
		t.Fatal(code, out)
	}
	if code, out = signinCall(t, srv, "POST", "second/signin/code", `{"code":"good-code"}`); code != 200 {
		t.Fatal(code, out)
	}
	if !claudeacct.Get(home, "second").LoggedIn {
		t.Fatal("the account is not logged in after a good code")
	}
	if code, _ = signinCall(t, srv, "POST", "second/signin/code", `{"code":"good-code"}`); code != http.StatusConflict {
		t.Fatal("a finished sign-in took another code:", code)
	}
}

func TestSigninRefusesABadCodeInPlainWords(t *testing.T) {
	srv, home, _ := signinServer(t)
	signinCall(t, srv, "POST", "second/signin", "")
	code, out := signinCall(t, srv, "POST", "second/signin/code", `{"code":"wrong"}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "didn't work") {
		t.Fatal(code, out)
	}
	if claudeacct.Get(home, "second").LoggedIn {
		t.Fatal("logged in on a bad code")
	}
	signinCall(t, srv, "POST", "second/signin", "")
	if code, out = signinCall(t, srv, "POST", "second/signin/code", `{"code":"two words"}`); code != http.StatusBadRequest {
		t.Fatal("a code with spaces reached the command:", code, out)
	}
	signinCall(t, srv, "DELETE", "second/signin", "")
}

func TestCodexSigninGivesItsDeviceCodeAndFinishesByItself(t *testing.T) {
	srv, _, log := signinServer(t)
	code, out := signinCall(t, srv, "POST", "codex/signin", "")
	if code != 200 || out["url"] != "https://auth.example/codex/device" || out["user_code"] != "1KOA-1GG2L" {
		t.Fatal(code, out)
	}
	if _, st := signinCall(t, srv, "GET", "codex/signin", ""); st["state"] != "waiting" {
		t.Fatal(st)
	}
	if err := os.WriteFile(log+".finish", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, st := signinCall(t, srv, "GET", "codex/signin", "")
		if st["state"] == "done" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSignoutRemovesOnlyThatAccountsLogin(t *testing.T) {
	srv, home, log := signinServer(t)
	dir := claudeacct.Dir(home, "second")
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"t"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := signinCall(t, srv, "POST", "second/signout", ""); code != 200 {
		t.Fatal(code, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(raw)) != dir {
		t.Fatalf("logout ran against %q, want %q (%v)", raw, dir, err)
	}
	if code, _ := signinCall(t, srv, "POST", "missing/signout", ""); code != http.StatusNotFound {
		t.Fatal("signed out an account that does not exist:", code)
	}
}
