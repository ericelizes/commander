package chat

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// authFailRe matches the CLI's signed-out messages ("Failed to authenticate:
// OAuth session expired and could not be refreshed", a 401, "not logged in").
var authFailRe = regexp.MustCompile(`(?i)failed to authenticate|oauth session expired|could not be refreshed|invalid api key|not logged in|please run /login|\b401\b`)

// authFailure reports whether ev says the account is signed out.
func authFailure(ev Event) bool {
	if ev.Kind != KindText && ev.Kind != KindResult {
		return false
	}
	if ev.Error == "authentication_failed" {
		return true
	}
	text := ev.Text
	if ev.Result != nil && ev.Result.IsError {
		text += " " + ev.Result.Text
	}
	return (ev.Error != "" || (ev.Result != nil && ev.Result.IsError)) && authFailRe.MatchString(text)
}

// observeAuth notes a signed-out signal in the current turn.
func (c *chatRole) observeAuth(ev Event) {
	if authFailure(ev) {
		c.mu.Lock()
		c.turnAuth = true
		c.mu.Unlock()
	}
}

// authOut remembers accounts a turn found signed out, so a move never lands
// on one (until its login is renewed, which rewrites its credentials).
type authOut struct {
	mu  sync.Mutex
	out map[string]time.Time
}

func (a *authOut) mark(name string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.out == nil {
		a.out = map[string]time.Time{}
	}
	a.out[name] = at
}

func (a *authOut) has(name string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	at, ok := a.out[name]
	return ok && now.Sub(at) < 6*time.Hour
}

// nextSignedIn is the first logged-in Claude account after from (list order,
// wrapping) that no turn found signed out and that is not on from's login.
func nextSignedIn(home, from string, out func(string) bool) string {
	accounts := claudeacct.List(home)
	start := 0
	for i, a := range accounts {
		if a.Name == from {
			start = i
		}
	}
	var spent []claudeacct.Account
	if len(accounts) > 0 {
		spent = append(spent, accounts[start])
	}
	for i := 1; i < len(accounts); i++ {
		a := accounts[(start+i)%len(accounts)]
		if a.Name != from && a.LoggedIn && !out(a.Name) && !sharesLogin(a, spent) {
			return a.Name
		}
	}
	return ""
}

// settleAuth handles a Commander or Manager turn that failed because its
// Claude account is signed out: once per failure it moves the role to another
// logged-in account and continues the turn there, and reports the signed-out
// account (OnAuthFailure) so the user is asked to sign it in again. It reports
// whether the turn was such a failure.
func (e *Engine) settleAuth(c *chatRole, p *process, ev Event) bool {
	c.mu.Lock()
	signedOut := c.turnAuth
	c.turnAuth = false
	c.mu.Unlock()
	failed := ev.Result != nil && ev.Result.IsError
	if !signedOut || !failed || p.runtime() != RuntimeClaude || p.spec.ExecutionRunID > 0 || !isManagedChatRole(c.role) {
		return false
	}
	from := accountName(p.spec.Account)
	now := e.opts.Now()
	e.signedOut.mark(from, now)
	next := nextSignedIn(e.opts.Home, from, func(name string) bool { return e.signedOut.has(name, now) })
	if e.opts.OnAuthFailure != nil {
		e.opts.OnAuthFailure(c.role, from, next)
	}
	if next == "" {
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "auth_failure",
			Text: fmt.Sprintf("Claude account %s is signed out and no other Claude account is logged in.", from)})
		return true
	}
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "auth_failure",
		Text: fmt.Sprintf("Claude account %s is signed out; moving to %s and continuing.", from, next)})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		o := SwitchOptions{Continue: true, Message: AuthContinueMessage}
		if _, err := e.SetAccount(ctx, c.role, next, o); err != nil {
			c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "auth_failure",
				Text: fmt.Sprintf("Moving to Claude account %s failed: %v", next, err)})
		}
	}()
	return true
}

// AuthContinueMessage is the first message after a move made to get past a signed-out account.
const AuthContinueMessage = "[switch] your last turn failed because its Claude account was signed out, so this chat moved to another account; continue where you left off"
