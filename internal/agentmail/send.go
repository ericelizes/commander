package agentmail

import (
	"context"
	"fmt"
	"strings"
)

// Send invokes:
//
//	am mail send --project <p> --from <f> --to <t> \
//	  --subject <s> --body <b> \
//	  [--importance <imp>] [--ack-required] --json
//
// opts.Importance ∈ {"low","normal","high","urgent"}; empty omits the flag.
// opts.AckRequired toggles --ack-required.
func (c *cmdClient) Send(ctx context.Context, project, from, to, subject, body string, opts SendOptions) error {
	if project == "" {
		return fmt.Errorf("agentmail: Send requires project")
	}
	if from == "" {
		return fmt.Errorf("agentmail: Send requires from")
	}
	if to == "" {
		return fmt.Errorf("agentmail: Send requires to")
	}
	if subject == "" {
		return fmt.Errorf("agentmail: Send requires subject")
	}

	args := []string{
		"mail", "send",
		"--project", project,
		"--from", from,
		"--to", to,
		"--subject", subject,
		"--body", body,
	}
	if opts.Importance != "" {
		args = append(args, "--importance", opts.Importance)
	}
	if opts.AckRequired {
		args = append(args, "--ack-required")
	}
	args = append(args, "--json")

	_, stderr, err := c.exec(ctx, "am", args...)
	if err != nil {
		return fmt.Errorf("am mail send: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}
