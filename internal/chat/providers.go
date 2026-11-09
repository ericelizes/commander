package chat

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// AccountView is one provider account as the app and `cmdr account list`
// show it: each Claude account, plus Codex (Name "codex").
type AccountView struct {
	Name        string `json:"name"`
	Runtime     string `json:"runtime"`
	LoggedIn    bool   `json:"logged_in"`
	Email       string `json:"email,omitempty"`
	AccountUUID string `json:"account_uuid,omitempty"`
	DuplicateOf string `json:"duplicate_of,omitempty"`
	Dir         string `json:"dir,omitempty"`
	Usage       *Usage `json:"usage,omitempty"` // latest report, as of now; nil = none yet
}

// Accounts lists the Claude accounts (default first) and Codex with their
// latest usage (from usage, keyed by UsageKey).
func Accounts(home string, usage map[string]Usage, now time.Time) []AccountView {
	var out []AccountView
	for _, a := range claudeacct.List(home) {
		v := AccountView{Name: a.Name, Runtime: RuntimeClaude, LoggedIn: a.LoggedIn, Email: a.Email, Dir: a.Dir, AccountUUID: a.AccountUUID, DuplicateOf: a.DuplicateOf}
		if u, ok := usage[UsageKey(RuntimeClaude, a.Name)]; ok {
			u = u.Current(now)
			v.Usage = &u
		}
		out = append(out, v)
	}
	codex := AccountView{Name: RuntimeCodex, Runtime: RuntimeCodex, Dir: CodexHome(), LoggedIn: fileExists(filepath.Join(CodexHome(), "auth.json"))}
	codex.Email, codex.AccountUUID = codexIdentity(filepath.Join(CodexHome(), "auth.json"))
	if u, ok := usage[RuntimeCodex]; ok {
		u = u.Current(now)
		codex.Usage = &u
	}
	return append(out, codex)
}

// Provider is what a role runs on.
type Provider struct {
	Runtime string `json:"runtime"`
	Account string `json:"account,omitempty"` // Claude account; "" on Codex
}

// Providers maps every role that recorded a runtime or account (chats and
// spawned Operators) to what it runs on.
func Providers(home string) map[string]Provider {
	out := map[string]Provider{}
	entries, _ := os.ReadDir(filepath.Join(home, "chat"))
	for _, e := range entries {
		n := e.Name()
		role := strings.TrimSuffix(strings.TrimSuffix(n, ".runtime"), ".account")
		if role == n || !ValidRole(role) {
			continue
		}
		out[role] = ProviderOf(home, role)
	}
	return out
}

// ProviderOf is role's recorded runtime and account.
func ProviderOf(home, role string) Provider {
	if rt := ReadRuntime(home, role); rt == RuntimeCodex {
		return Provider{Runtime: rt}
	}
	return Provider{Runtime: RuntimeClaude, Account: ReadAccount(home, role)}
}

// Home is the engine's COMMANDER_HOME.
func (e *Engine) Home() string { return e.opts.Home }

// codexIdentity reads display claims only. They are unverified local metadata,
// never authorization, and no token or credential is returned to callers.
func codexIdentity(path string) (string, string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var auth struct {
		Tokens struct {
			IDToken   string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return "", ""
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 {
		return "", auth.Tokens.AccountID
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", auth.Tokens.AccountID
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", auth.Tokens.AccountID
	}
	return claims.Email, auth.Tokens.AccountID
}
