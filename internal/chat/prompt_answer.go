package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/harddeny"
)

// Permission prompts never freeze a chat. The Commander and Operators have
// them off: cmdrd answers each tool prompt itself at once (allowed unless the
// hard deny-list refuses it). Any prompt still left for the user (a question,
// a plan, a role not covered) is declined after PromptTimeout. On 2026-11-17
// the Commander waited 11 hours on one Bash approval nobody was there to give.

// DefaultPromptTimeout is how long a prompt left for the user may wait: 10
// minutes for the Commander, 15 for everyone else. CMDR_PROMPT_TIMEOUT (a Go
// duration, e.g. "20m"; "0" = never) overrides both.
func DefaultPromptTimeout(role string) time.Duration {
	if v := strings.TrimSpace(os.Getenv("CMDR_PROMPT_TIMEOUT")); v != "" {
		if v == "0" {
			return 0
		}
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	if IsCommanderRole(role) {
		return 10 * time.Minute
	}
	return 15 * time.Minute
}

// AutoAnswered is the default AutoAnswer: the Commander and the Operators.
func AutoAnswered(role string) bool {
	return IsCommanderRole(role) || strings.HasPrefix(role, "operator-")
}

func (e *Engine) autoAnswers(role string, cr *ControlRequest) bool {
	if e.opts.AutoAnswer == nil || !e.opts.AutoAnswer(role) {
		return false
	}
	return cr.ToolName != "AskUserQuestion" && cr.ToolName != "ExitPlanMode"
}

// answerAuto answers a tool prompt for an auto-answered role: allow, or deny
// with the hard deny-list's reason. The PreToolUse hook already enforced the
// deploy rule (it knows the step), so it is not repeated here.
func (e *Engine) answerAuto(c *chatRole, p *process, cr *ControlRequest) {
	input := cr.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	resp := map[string]any{"behavior": "allow", "updatedInput": input}
	decision, msg := "allow", "answered by cmdrd: permission prompts are off for this role"
	if cmd := bashCommand(cr); cmd != "" {
		ctx := harddeny.Context{DeployAllowed: true, MainlineAllowed: true}
		if strings.HasPrefix(c.role, "operator-") {
			ctx.Worktree = p.spec.Cwd
		}
		if v := harddeny.Check(cmd, ctx); v.Denied() {
			resp = map[string]any{"behavior": "deny", "message": v.Message()}
			decision, msg = "deny", v.Message()
		}
	}
	if err := p.write(controlSuccess(cr.RequestID, resp)); err != nil {
		// Could not answer: leave it for the user, with the timeout as the backstop.
		p.pmu.Lock()
		p.pending[cr.RequestID] = cr
		p.pmu.Unlock()
		e.armPromptTimeout(c, p, cr)
		return
	}
	c.publish(Event{Kind: KindResolved, Source: SourceEngine, Request: &Request{ID: cr.RequestID, ToolName: cr.ToolName, Decision: decision, Message: msg}})
}

func bashCommand(cr *ControlRequest) string {
	if cr.ToolName != "Bash" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(cr.Input, &in)
	return in.Command
}

// armPromptTimeout declines cr if it is still unanswered after the role's timeout.
func (e *Engine) armPromptTimeout(c *chatRole, p *process, cr *ControlRequest) {
	if e.opts.PromptTimeout == nil {
		return
	}
	d := e.opts.PromptTimeout(c.role)
	if d <= 0 {
		return
	}
	id := cr.RequestID
	time.AfterFunc(d, func() { e.expirePrompt(c, p, id, d) })
}

func (e *Engine) expirePrompt(c *chatRole, p *process, id string, d time.Duration) {
	p.pmu.Lock()
	cr, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.pmu.Unlock()
	if !ok {
		return // answered, cancelled or ended in time
	}
	c.mu.Lock()
	live := c.proc == p
	c.mu.Unlock()
	if !live {
		return
	}
	msg := fmt.Sprintf("Nobody answered this %s prompt for %s, so cmdrd declined it. Rerun the command in the foreground "+
		"without background loops, or record a blocker on your step and carry on.", cr.ToolName, d)
	if cr.ToolName == "AskUserQuestion" || cr.ToolName == "ExitPlanMode" {
		msg = fmt.Sprintf("Nobody answered this for %s, so cmdrd declined it. Carry on with your best judgement, "+
			"and put it to the user as a card if it still matters.", d)
	}
	if err := p.write(controlSuccess(id, map[string]any{"behavior": "deny", "message": msg})); err != nil {
		if e.opts.Logf != nil {
			e.opts.Logf("chat: %s: decline unanswered prompt %s: %v", c.role, id, err)
		}
		return
	}
	c.publish(Event{Kind: KindResolved, Source: SourceEngine, Request: &Request{ID: id, ToolName: cr.ToolName, Decision: "deny", Message: msg}})
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Text: fmt.Sprintf("cmdrd declined a %s prompt nobody answered for %s.", cr.ToolName, d)})
}
