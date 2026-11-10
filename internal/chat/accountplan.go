package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// Commander treats a chosen account as a preference. At a limit it can use
// any connected account, regardless of provider, and retains that preference.
func planChatStart(home, role string, fresh bool, usage map[string]Usage, now time.Time, hooksReady func() bool) (managerPlan, error) {
	if role != "commander" {
		return planManagerStart(home, role, fresh, usage, now, hooksReady)
	}
	cur := ProviderOf(home, role)
	pin := ManagerPin(home, role)
	want := ReadAccountIntent(home, role)
	if want == "" {
		want = ReadAccount(home, role)
	}
	preferred := cur
	if pin != "" {
		preferred.Runtime = pin
	}
	preferred.Account = want
	accounts := Accounts(home, usage, now)
	available := func(p Provider) bool {
		for _, a := range accounts {
			if a.Runtime != p.Runtime || (p.Runtime == RuntimeClaude && a.Name != p.Account) {
				continue
			}
			if !a.LoggedIn || a.DuplicateOf != "" || (a.Usage != nil && a.Usage.Out(now)) {
				return false
			}
			if p.Runtime == RuntimeCodex {
				return hooksReady()
			}
			// Separate profiles for one login share the same usage limit.
			for _, b := range accounts {
				if b.Runtime == RuntimeClaude && b.Usage != nil && b.Usage.Out(now) &&
					claudeacct.SameIdentity(claudeacct.Account{LoggedIn: a.LoggedIn, Email: a.Email, AccountUUID: a.AccountUUID}, claudeacct.Account{LoggedIn: b.LoggedIn, Email: b.Email, AccountUUID: b.AccountUUID}) {
					return false
				}
			}
			return true
		}
		return false
	}
	candidates := []Provider{preferred, cur}
	for _, a := range accounts {
		candidates = append(candidates, Provider{Runtime: a.Runtime, Account: a.Name})
	}
	for _, p := range candidates {
		if !available(p) {
			continue
		}
		plan := managerPlan{runtime: p.Runtime, pin: pin, leave: "account unavailable"}
		if p.Runtime == RuntimeClaude {
			plan.account = p.Account
			if p.Account != want {
				plan.intent = want
			}
		}
		return plan, nil
	}
	return managerPlan{}, fmt.Errorf("%w for commander: connect an account or wait for its usage to reset", ErrNoUsage)
}

func codexConnected() bool {
	raw, err := os.ReadFile(filepath.Join(CodexHome(), "auth.json"))
	if err != nil {
		return false
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
		} `json:"tokens"`
	}
	return json.Unmarshal(raw, &auth) == nil && (auth.APIKey != "" || auth.Tokens.AccessToken != "" || auth.Tokens.IDToken != "")
}
