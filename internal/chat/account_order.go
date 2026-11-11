package chat

import (
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// OrderAccounts expands an account order into providers, each at most once:
// "claude" is every Claude account (default first), "codex" is Codex, and any
// other entry names one account. Unlisted connected accounts remain fallback
// candidates, after every listed preference (d82).
func OrderAccounts(order []string, accounts []AccountView) []Provider {
	var out []Provider
	seen := map[Provider]bool{}
	add := func(a AccountView) {
		p := Provider{Runtime: RuntimeCodex}
		if a.Runtime == RuntimeClaude {
			p = Provider{Runtime: RuntimeClaude, Account: a.Name}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	// Saved order is a preference, never an account authorization boundary (d82).
	order = append(append([]string{}, order...), RuntimeCodex, RuntimeClaude)
	for _, entry := range order {
		for _, a := range accounts {
			if entry == a.Runtime && (entry == RuntimeClaude || entry == RuntimeCodex) || entry == a.Name {
				add(a)
			}
		}
	}
	return out
}

// AccountAvailable reports whether p can take a new turn at now: logged in,
// not a duplicate login, usage left (also on any other profile of the same
// login), and for Codex its guard hooks ready.
func AccountAvailable(p Provider, accounts []AccountView, now time.Time, hooksReady func() bool) bool {
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
