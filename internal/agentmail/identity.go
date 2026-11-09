package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ExistingAgent is the identity returned by the server's project-scoped whois
// read. No project or agent is created by this lookup.
type ExistingAgent struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	ProjectID      int64   `json:"project_id"`
	RetiredAt      *string `json:"retired_at"`
	DeregisteredAt *string `json:"deregistered_at"`
}

// What a whois read can refuse with. The wording only tells the sender what to
// do next: anything but an exact active identity refuses the send.
var (
	ErrAgentMissing     = errors.New("no existing agent with that exact name")
	ErrAgentInactive    = errors.New("existing agent is retired or deregistered")
	ErrWhoisFailed      = errors.New("could not read the identity")
	ErrWhoisUnsupported = errors.New("mail server cannot verify existing identities")
)

func (c *ServerClient) LookupExistingAgent(ctx context.Context, projectKey, name string) (ExistingAgent, error) {
	var out ExistingAgent
	if projectKey == "" || name == "" {
		return out, fmt.Errorf("agentmail: whois requires projectKey and name")
	}
	text, err := c.call(ctx, "whois", map[string]any{
		"project_key": projectKey, "agent_name": name, "include_recent_commits": false,
	})
	if err != nil {
		return out, fmt.Errorf("agentmail: %w %q: %v", whoisCallError(err), name, err)
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return ExistingAgent{}, fmt.Errorf("agentmail: %w: parse whois result: %v", ErrWhoisUnsupported, err)
	}
	active := out.RetiredAt == nil && out.DeregisteredAt == nil
	switch {
	case out.Name == "" || out.ID <= 0 || out.ProjectID <= 0:
		return ExistingAgent{}, fmt.Errorf("agentmail: %w: whois returned an incomplete identity for %q", ErrWhoisUnsupported, name)
	case out.Name != name:
		return ExistingAgent{}, fmt.Errorf("agentmail: %w %q: whois returned %q", ErrAgentMissing, name, out.Name)
	case !active:
		return ExistingAgent{}, fmt.Errorf("agentmail: %w: %q", ErrAgentInactive, name)
	}
	return out, nil
}

// whoisCallError reads the server's failure text; an unrecognised one is only a
// failed read.
func whoisCallError(err error) error {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "not found in project"), strings.Contains(text, "agent_not_found"):
		return ErrAgentMissing
	case strings.Contains(text, "unknown tool"), strings.Contains(text, "tool not found"), strings.Contains(text, "rpc error -32601"):
		return ErrWhoisUnsupported
	}
	return ErrWhoisFailed
}
