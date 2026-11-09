package chat

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountsProjectsCodexIdentityWithoutChangingBindings(t *testing.T) {
	user, home, codex := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CODEX_HOME", codex)
	profile(t, user, home, "default", "one@example.test", "one")
	profile(t, user, home, "second", "two@example.test", "two")
	os.MkdirAll(filepath.Join(home, "chat"), 0700)
	binding := filepath.Join(home, "chat", "manager-example.account")
	os.WriteFile(binding, []byte("second\n"), 0600)
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"three@example.test"}`)) + ".signature"
	auth := `{"tokens":{"id_token":"` + token + `","account_id":"three"}}`
	authPath := filepath.Join(codex, "auth.json")
	os.WriteFile(authPath, []byte(auth), 0600)
	got := Accounts(home, nil, time.Now())
	last := got[len(got)-1]
	if last.Email != "three@example.test" || last.AccountUUID != "three" || !last.LoggedIn || last.Runtime != RuntimeCodex {
		t.Fatalf("identity not projected")
	}
	if got[0].Name != "default" || got[1].Name != "second" {
		t.Fatal("profile keys changed")
	}
	raw, _ := os.ReadFile(authPath)
	if string(raw) != auth {
		t.Fatal("credential changed")
	}
	raw, _ = os.ReadFile(binding)
	if string(raw) != "second\n" || ProviderOf(home, "manager-example").Account != "second" {
		t.Fatal("binding changed")
	}
}

func TestCodexIdentityMissingOrMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	for _, raw := range []string{"", "{", `{ "OPENAI_API_KEY":"invented" }`, `{"tokens":{"id_token":"bad"}}`, `{"tokens":{"id_token":"a.!.b"}}`, `{"tokens":{"id_token":"a.bm90anNvbg.b"}}`} {
		os.WriteFile(path, []byte(raw), 0600)
		email, id := codexIdentity(path)
		if email != "" || id != "" {
			t.Fatal("malformed identity projected")
		}
	}
}
