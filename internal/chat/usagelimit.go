package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// UsageLimit is a Claude plan limit a role's turn stopped at.
type UsageLimit struct {
	Text     string    `json:"text"`                // the CLI's message, e.g. "You've hit your limit · resets 3pm"
	ResetsAt int64     `json:"resets_at,omitempty"` // unix seconds, when the CLI said
	Runtime  string    `json:"runtime"`             // claude or codex
	Account  string    `json:"account,omitempty"`   // the Claude account that ran out
	At       time.Time `json:"at"`

	fromText bool // the CLI's own error message (not only a rate_limit_event)
}

// usageLimitRe matches the CLI's limit messages ("You've hit your limit ·
// resets 3pm", "Claude AI usage limit reached|<epoch>", "spend limit reached").
var usageLimitRe = regexp.MustCompile(`(?i)hit your .*limit|usage limit|limit reached`)

// usageLimitOf reads the two usage-limit signals of Claude Code's stream-json
// (verified against 2.1.308): a rate_limit_event whose rate_limit_info.status
// is "rejected" with no overage taking over, and the synthetic assistant
// message the CLI writes for the 429, which carries error "rate_limit" (or
// "billing_error" for a spend cap) and the human text. nil = neither.
func usageLimitOf(ev Event) *UsageLimit {
	switch ev.Kind {
	case KindRateLimit:
		var rl struct {
			Status        string `json:"status"`
			ResetsAt      int64  `json:"resetsAt"`
			Type          string `json:"rateLimitType"`
			OverageStatus string `json:"overageStatus"`
			UsingOverage  bool   `json:"isUsingOverage"`
		}
		if json.Unmarshal(ev.RateLimit, &rl) != nil || rl.Status != "rejected" || rl.UsingOverage ||
			rl.OverageStatus == "allowed" || rl.OverageStatus == "allowed_warning" {
			return nil
		}
		text := "usage limit reached"
		if rl.Type != "" {
			text += " (" + rl.Type + ")"
		}
		return &UsageLimit{Text: text, ResetsAt: rl.ResetsAt}
	case KindText:
		if (ev.Error == "rate_limit" || ev.Error == "billing_error") && usageLimitRe.MatchString(ev.Text) {
			return &UsageLimit{Text: ev.Text, fromText: true}
		}
	}
	return nil
}

// observeLimit notes a usage-limit signal in the current turn.
func (c *chatRole) observeLimit(ev Event) {
	u := usageLimitOf(ev)
	if u == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev := c.turnLimit; prev != nil {
		if u.ResetsAt == 0 {
			u.ResetsAt = prev.ResetsAt
		}
		if !u.fromText && prev.fromText {
			u.Text, u.fromText = prev.Text, true
		}
	}
	c.turnLimit = u
}

// settleLimit decides at a turn's result whether it stopped at a usage limit:
// the CLI's limit message, or a rejected rate_limit_event and an error result.
// A limited turn shows a notice (Status.UsageLimit carries the switch options);
// a successful turn clears the last one. With auto-switch on, the role moves to
// the next logged-in account with usage left and continues.
func (e *Engine) settleLimit(c *chatRole, p *process, ev Event) {
	failed := ev.Result != nil && ev.Result.IsError
	c.mu.Lock()
	u := c.turnLimit
	c.turnLimit = nil
	if u == nil || (!u.fromText && !failed) {
		if !failed {
			c.limit = nil
		}
		c.mu.Unlock()
		return
	}
	u.Runtime, u.At = p.runtime(), e.opts.Now()
	who := "Codex"
	if u.Runtime == RuntimeClaude {
		u.Account = accountName(p.spec.Account)
		who = "Claude account " + u.Account
	}
	c.limit = u
	c.mu.Unlock()

	until := u.ResetsAt
	if until == 0 {
		until = u.At.Add(time.Hour).Unix() // unknown reset: skip the account for an hour
	}
	e.opts.Usage.MarkOut(p.runtime(), p.spec.Account, c.role, until, u.At)

	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "usage_limit",
		Text: fmt.Sprintf("%s hit its usage limit: %s", who, u.Text)})
	switch {
	case isManagerRole(c.role):
		go e.moveManager(c)
	case u.Runtime == RuntimeClaude && AutoSwitch(e.opts.Home, c.role):
		go e.autoSwitch(c, u.Account)
	}
}

// NextAccount is the logged-in Claude account after from (in list order,
// wrapping) that shares no login with from or with any account reported "out"
// (usage is reported per profile, and profiles on one login run out together);
// "" when there is none.
func (e *Engine) NextAccount(from string) string {
	now := e.opts.Now()
	return NextAccountWith(e.opts.Home, from, e.opts.Usage.All(now), now)
}

// NextAccountWith is NextAccount over a usage map (keyed by UsageKey), for
// callers without an Engine: a spawn reads ReadUsage.
func NextAccountWith(home, from string, usage map[string]Usage, now time.Time) string {
	accounts := claudeacct.List(home)
	start := 0
	for i, a := range accounts {
		if a.Name == from {
			start = i
		}
	}
	spent := []claudeacct.Account{accounts[start]}
	for _, a := range accounts {
		if u := usage[UsageKey(RuntimeClaude, a.Name)]; u.Out(now) {
			spent = append(spent, a)
		}
	}
	for i := 1; i < len(accounts); i++ {
		a := accounts[(start+i)%len(accounts)]
		if a.Name != from && a.LoggedIn && !sharesLogin(a, spent) {
			return a.Name
		}
	}
	return ""
}

// sharesLogin reports whether a is one of spent or on the same login as one.
func sharesLogin(a claudeacct.Account, spent []claudeacct.Account) bool {
	for _, s := range spent {
		if a.Name == s.Name || claudeacct.SameIdentity(a, s) {
			return true
		}
	}
	return false
}

// SetUsageMoveHook sets Options.OnUsageMove after construction.
func (e *Engine) SetUsageMoveHook(f func(role, text string)) { e.opts.OnUsageMove = f }

func (e *Engine) autoSwitch(c *chatRole, from string) {
	next := e.NextAccount(from)
	if next == "" {
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "usage_limit_switch",
			Text: "Auto-switch: no other logged-in Claude account has usage left."})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := e.SetAccount(ctx, c.role, next, e.moveOptions(c.role)); err != nil {
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "usage_limit_switch",
			Text: fmt.Sprintf("Auto-switch to Claude account %s failed: %v", next, err)})
		return
	}
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "usage_limit_switch",
		Text: fmt.Sprintf("Switched from Claude account %s to %s after its usage limit.", from, next)})
}

func isManagerRole(role string) bool { return strings.HasPrefix(role, "manager-") }

// moveOptions continue a limited turn; a Manager is also told to read its
// handover again, since the limit may have stopped it before it did.
func (e *Engine) moveOptions(role string) SwitchOptions {
	o := SwitchOptions{Continue: true}
	if isManagerRole(role) {
		o.Message = ContinueMessage + "; first re-read your handover and unread mail (cmdr internal-inbox --agent " + role + ")"
	}
	return o
}

func accountName(a string) string {
	if a == "" {
		return claudeacct.Default
	}
	return a
}
