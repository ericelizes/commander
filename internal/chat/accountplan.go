package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Commander treats a chosen account as a preference. At a limit it can use
// any connected account within an explicit runtime pin, retaining preference.
func planChatStart(home, role string, fresh bool, usage map[string]Usage, now time.Time, hooksReady func() bool, order []string) (managerPlan, error) {
	if !IsCommanderRole(role) {
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
	candidates := OrderAccounts(order, accounts)
	// Account order is preference; an explicit runtime pin constrains fallback.
	if pin != "" || (cur.Runtime == RuntimeClaude && want != "default") {
		candidates = append([]Provider{preferred}, candidates...)
	}
	for _, p := range candidates {
		if pin != "" && p.Runtime != pin {
			continue
		}
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
