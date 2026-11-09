package agentmail

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Inbox invokes:
//
//	am mail inbox --project <p> --agent <a> --since <ISO-8601> \
//	  [--limit <n>] [--include-bodies] --json
//
// since is formatted via iso8601() — RFC3339 UTC. Duration strings like
// "24h" are NOT accepted by am (verified discrepancy from earlier plan).
//
// opts.Limit <= 0 omits --limit. opts.IncludeBodies toggles --include-bodies.
func (c *cmdClient) Inbox(ctx context.Context, project, agent string, since time.Time, opts InboxOptions) ([]Mail, error) {
	if project == "" {
		return nil, fmt.Errorf("agentmail: Inbox requires project")
	}
	if agent == "" {
		return nil, fmt.Errorf("agentmail: Inbox requires agent")
	}

	args := []string{
		"mail", "inbox",
		"--project", project,
		"--agent", agent,
		"--since", iso8601(since),
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	if opts.IncludeBodies {
		args = append(args, "--include-bodies")
	}
	args = append(args, "--json")

	stdout, stderr, err := c.exec(ctx, "am", args...)
	if err != nil {
		return nil, fmt.Errorf("am mail inbox: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return parseMailJSON(stdout)
}

// CheckInbox invokes:
//
//	am check-inbox --project <p> --agent <a> --rate-limit 0 --json
//
// CRITICAL: --rate-limit 0 is ALWAYS passed. am's default is 120s, which
// makes polling-style usage from commander unusable. check_inbox_test.go
// has a regression guard.
func (c *cmdClient) CheckInbox(ctx context.Context, project, agent string) ([]Mail, error) {
	if project == "" {
		return nil, fmt.Errorf("agentmail: CheckInbox requires project")
	}
	if agent == "" {
		return nil, fmt.Errorf("agentmail: CheckInbox requires agent")
	}

	args := []string{
		"check-inbox",
		"--project", project,
		"--agent", agent,
		"--rate-limit", "0",
		"--json",
	}

	stdout, stderr, err := c.exec(ctx, "am", args...)
	if err != nil {
		return nil, fmt.Errorf("am check-inbox: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return parseMailJSON(stdout)
}

// parseMailJSON tolerates both shapes am may emit:
//   - bare JSON array: [{...}, {...}]
//   - envelope object with a "mail" or "messages" array
//
// Empty stdout is treated as an empty result (am returns no output when
// there's nothing new on a check-inbox poll).
func parseMailJSON(stdout []byte) ([]Mail, error) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return nil, nil
	}

	// Try array shape first.
	if strings.HasPrefix(trimmed, "[") {
		var mail []Mail
		if err := json.Unmarshal([]byte(trimmed), &mail); err != nil {
			return nil, fmt.Errorf("am: parse mail array: %w", err)
		}
		return mail, nil
	}

	// Otherwise envelope.
	var envelope struct {
		Mail     []Mail `json:"mail"`
		Messages []Mail `json:"messages"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, fmt.Errorf("am: parse mail envelope: %w", err)
	}
	if envelope.Mail != nil {
		return envelope.Mail, nil
	}
	return envelope.Messages, nil
}
