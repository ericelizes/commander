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
	// Healthy probes whether an account is logged in and working; a pause also
	// clears once it is seen so (default: `claude auth status`).
	Healthy func(account string) bool
	probed  map[string]time.Time
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

// healthEvery bounds how often an account's health is probed.
const healthEvery = 2 * time.Minute

// DefaultHealthy asks Claude (read-only `claude auth status`) whether the
// account is logged in.
func DefaultHealthy(home string) func(string) bool {
	return func(account string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		v, err := claudeacct.Verify(ctx, home, account)
		return err == nil && v.LoggedIn
	}
}

func (w *AuthWatcher) healthy(home, account string) bool {
	if w.Healthy == nil {
		w.Healthy = DefaultHealthy(home)
	}
	if w.probed == nil {
		w.probed = map[string]time.Time{}
	}
	now := w.now()
	if at, ok := w.probed[account]; ok && now.Sub(at) < healthEvery {
		return false
	}
	w.probed[account] = now
	return w.Healthy(account)
}

// Pass runs one check of every paused account. Every step is idempotent and
// ordered so a failure part way leaves state the next pass finishes: the
// carrier is recorded in the pause before the card is inserted, the card is
// inserted only where none exists, and on recovery the card goes first and the
// pause last.
func (w *AuthWatcher) Pass(ctx context.Context) error {
	home := w.Home
	if home == "" {
		home = agentrt.CommanderHome()
	}
	login := w.Login
	if login == nil {
		login = DefaultLogin(home)
	}
	claimed := map[string]bool{}
	entries := authpause.Active(home)
	for _, e := range entries {
		if e.CardBead != "" {
			claimed[fmt.Sprintf("%d/%s", e.CardProject, e.CardBead)] = true
		}
	}
	for _, e := range entries {
		loggedIn, changed := login(e.Account)
		if !loggedIn && !e.SawLoggedOut {
			if err := authpause.Update(home, e.Account, func(x *authpause.Entry) { x.SawLoggedOut = true }); err != nil {
				return err
			}
			e.SawLoggedOut = true
		}
		healthy := loggedIn && !authpause.Recovered(e, loggedIn, changed, false) && w.healthy(home, e.Account)
		if authpause.Recovered(e, loggedIn, changed, healthy) {
			if e.CardBead != "" {
				// Only the sign-in card: a user's own card on the step stays.
				if err := w.Store.DeleteNeedsYouIfAsk(ctx, e.CardProject, e.CardBead, AuthCardAsk); err != nil {
					return err
				}
			}
			n, err := w.Store.ReleaseAuthWaiters(ctx)
			if err != nil {
				return err
			}
			if _, _, err := authpause.Clear(home, e.Account); err != nil {
				return err
			}
			log.Printf("execution: Claude account %s signed in again; %d step(s) ready", e.Account, n)
			continue
		}
		waiters, err := w.Store.AuthWaiters(ctx)
		if err != nil {
			return err
		}
		if e.CardBead != "" {
			card, err := w.Store.GetNeedsYou(ctx, e.CardProject, e.CardBead)
			if err != nil {
				return err
			}
			if card != nil && card.Ask == AuthCardAsk {
				continue // one card per pause, kept until the sign-in succeeds
			}
			if card == nil {
				// Recorded but never inserted (a failure between the two): finish it.
				if _, err := w.Store.InsertNeedsYouIfAbsent(ctx, w.card(e, e.CardProject, e.CardBead, len(waiters))); err != nil {
					return err
				}
				continue
			}
			// A user's card took the step: give up the claim and pick another.
			delete(claimed, fmt.Sprintf("%d/%s", e.CardProject, e.CardBead))
			if err := authpause.Update(home, e.Account, func(x *authpause.Entry) { x.CardProject, x.CardBead = 0, "" }); err != nil {
				return err
			}
		}
		var carrier *store.AuthWaiter
		for i := range waiters {
			if k := fmt.Sprintf("%d/%s", waiters[i].ProjectID, waiters[i].BeadID); !waiters[i].HasCard && !claimed[k] {
				carrier = &waiters[i]
				break
			}
		}
		if carrier == nil {
			continue // every waiting step already has a card; try again next pass
		}
		claimed[fmt.Sprintf("%d/%s", carrier.ProjectID, carrier.BeadID)] = true
		if err := authpause.Update(home, e.Account, func(x *authpause.Entry) { x.CardProject, x.CardBead = carrier.ProjectID, carrier.BeadID }); err != nil {
			return err
		}
		if _, err := w.Store.InsertNeedsYouIfAbsent(ctx, w.card(e, carrier.ProjectID, carrier.BeadID, len(waiters))); err != nil {
			return err
		}
	}
	return nil
}

func (w *AuthWatcher) card(e authpause.Entry, project int64, bead string, waiting int) store.NeedsYou {
	note := fmt.Sprintf("Claude account %q could not authenticate (its login expired or its refresh failed). Dispatch to Claude is paused and no run was moved to another runtime. Sign in again (`claude auth login`, or Settings > Agents). This card clears and work resumes by itself once the sign-in works; %d step(s) are waiting.", e.Account, waiting)
	if e.Reason != "" {
		note += "\n\nLast error: " + e.Reason
	}
	return store.NeedsYou{ProjectID: project, BeadID: bead, Ask: AuthCardAsk, Note: note, Since: w.now().Unix()}
}
