package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Commander runs on the first account in its order (store.AgentAccounts) that
// has usage left. A person's explicit choice in the chat comes first while it
// has usage, and is kept across a move.
func planChatStart(home, role string, fresh bool, usage map[string]Usage, now time.Time, hooksReady func() bool, order []string) (managerPlan, error) {
	if !IsCommanderRole(role) {
		return planManagerStart(home, role, fresh, usage, now, hooksReady)
	}
	pin := ManagerPin(home, role)
	want := ReadAccountIntent(home, role)
	if want == "" {
		want = ReadAccount(home, role)
	}
	accounts := Accounts(home, usage, now)
	var candidates []Provider
	switch {
	case pin == RuntimeCodex:
		candidates = append(candidates, Provider{Runtime: RuntimeCodex})
	case pin == RuntimeClaude || fileExists(AccountFile(home, role)):
		candidates = append(candidates, Provider{Runtime: RuntimeClaude, Account: want})
	}
	candidates = append(candidates, OrderAccounts(order, accounts)...)
	for _, p := range candidates {
		if !AccountAvailable(p, accounts, now, hooksReady) {
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
