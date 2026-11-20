package execution

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/authpause"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/store"
)

// AuthCardAsk is the one card a Claude login failure raises for the user.
const AuthCardAsk = "Sign in to Claude again"

// AuthWatcher keeps the Claude sign-in pause honest: it raises exactly one
// Needs-you card while an account is paused (never one per step), and when a
// sign-in succeeds it clears the card and the pause and makes the waiting
// steps claimable at once.
type AuthWatcher struct {
	Store *store.Store
	Home  string
	// Login reports an account's login and when its credentials last changed.
	Login func(account string) (loggedIn bool, credChanged time.Time)
	Now   func() time.Time
}

// DefaultLogin reads the account's credentials as Claude Code keeps them.
func DefaultLogin(home string) func(string) (bool, time.Time) {
	return func(account string) (bool, time.Time) {
		claudeacct.ForgetProbes()
		a := claudeacct.Get(home, account)
		var mt time.Time
		if info, err := os.Stat(filepath.Join(claudeacct.ConfigDir(home, account), ".credentials.json")); err == nil {
			mt = info.ModTime()
		}
		return a.LoggedIn, mt
	}
}

func (w *AuthWatcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Pass runs one check of every paused account.
func (w *AuthWatcher) Pass(ctx context.Context) error {
	home := w.Home
	if home == "" {
		home = agentrt.CommanderHome()
	}
	login := w.Login
	if login == nil {
		login = DefaultLogin(home)
	}
	for _, e := range authpause.Active(home) {
		loggedIn, changed := login(e.Account)
		if !loggedIn && !e.SawLoggedOut {
			if err := authpause.Update(home, e.Account, func(x *authpause.Entry) { x.SawLoggedOut = true }); err != nil {
				return err
			}
			e.SawLoggedOut = true
		}
		if authpause.Recovered(e, loggedIn, changed) {
			if e.CardBead != "" {
				if err := w.Store.DeleteNeedsYou(ctx, e.CardProject, e.CardBead); err != nil {
					return err
				}
			}
			if _, _, err := authpause.Clear(home, e.Account); err != nil {
				return err
			}
			n, err := w.Store.ReleaseAuthWaiters(ctx)
			if err != nil {
				return err
			}
			log.Printf("execution: Claude account %s signed in again; %d step(s) ready", e.Account, n)
			continue
		}
		if e.CardBead != "" {
			continue // one card per pause, kept until the sign-in succeeds
		}
		waiters, err := w.Store.AuthWaiters(ctx)
		if err != nil || len(waiters) == 0 {
			if err != nil {
				return err
			}
			continue
		}
		carrier := waiters[0]
		note := fmt.Sprintf("Claude account %q could not authenticate (its login expired or its refresh failed). Dispatch to Claude is paused and no run was moved to another runtime. Sign in again (`claude auth login`, or Settings > Agents). This card clears and work resumes by itself once the sign-in works; %d step(s) are waiting.", e.Account, len(waiters))
		if e.Reason != "" {
			note += "\n\nLast error: " + e.Reason
		}
		if err := w.Store.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: carrier.ProjectID, BeadID: carrier.BeadID, Ask: AuthCardAsk, Note: note, Since: w.now().Unix()}); err != nil {
			return err
		}
		if err := authpause.Update(home, e.Account, func(x *authpause.Entry) { x.CardProject, x.CardBead = carrier.ProjectID, carrier.BeadID }); err != nil {
			return err
		}
	}
	return nil
}
