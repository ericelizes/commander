// Package agentmail is a thin wrapper around the `am` (Agent Mail) CLI.
//
// Every shape invoked here is documented in share/contracts.md. Drift from
// that contract should fail integration tests, not silently break.
//
// Two contract gotchas guarded by this package and its tests:
//
//   - `--program opencode-ai` (NOT `claude-code`), verified by integration tests.
//   - `--rate-limit 0` is ALWAYS passed to `am check-inbox`. Default is 120s,
//     which makes polling-style usage unusable.
//   - `--since` on `am mail inbox` requires ISO-8601 (RFC3339 UTC), NOT
//     duration strings like "24h ago".
package agentmail

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// Mail is the minimal projection of an am message commander cares about.
type Mail struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body"`
	Importance string    `json:"importance"`
	SentAt     time.Time `json:"sent_at"`
}

// Agent is the minimal projection of an am agent.
type Agent struct {
	Name    string `json:"name"`
	Program string `json:"program"`
	Model   string `json:"model"`
}

// SendOptions controls optional flags on `am mail send`.
//
// Importance is one of {"low", "normal", "high", "urgent"}. Empty string
// means "do not pass --importance" (am picks its default).
//
// NoAutoContact (server client only) sends auto_contact_if_blocked:false, so a
// recipient with no approved contact refuses the send instead of the server
// approving the link itself. Unset leaves that choice to the server.
type SendOptions struct {
	Importance    string
	AckRequired   bool
	NoAutoContact bool
}

// InboxOptions controls optional flags on `am mail inbox`.
//
// Limit <= 0 means "do not pass --limit". IncludeBodies toggles
// --include-bodies (without it, am returns subjects/metadata only).
type InboxOptions struct {
	Limit         int
	IncludeBodies bool
}

// Client is the surface commander code depends on. The MockClient in mock.go
// satisfies this for downstream tests.
type Client interface {
	// Register an agent in <project>. Idempotent: re-registering the same
	// name updates rather than duplicating. program MUST be "opencode-ai"
	// for commander (see U1 / D7).
	Register(ctx context.Context, project, program, model, name string) error

	// Send a message in <project> from <from> to <to>.
	Send(ctx context.Context, project, from, to, subject, body string, opts SendOptions) error

	// Inbox returns messages received by <agent> in <project> at or after
	// <since>. <since> is formatted as ISO-8601 (RFC3339 UTC) — duration
	// strings like "24h" do NOT work.
	Inbox(ctx context.Context, project, agent string, since time.Time, opts InboxOptions) ([]Mail, error)

	// CheckInbox is the polling form: am check-inbox always passes
	// --rate-limit 0 (default 120s makes polling unusable).
	CheckInbox(ctx context.Context, project, agent string) ([]Mail, error)

	// ListAgents returns every agent registered in <project>.
	ListAgents(ctx context.Context, project string) ([]Agent, error)
}

// execFunc abstracts process execution so tests can capture argv without
// running a real am binary. Matches the U5-a/U5-b convention (see
// internal/beads).
type execFunc func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// cmdClient is the real Client backed by the am binary on PATH.
type cmdClient struct {
	exec execFunc
}

// New returns a Client that shells out to `am`.
func New() Client {
	return &cmdClient{exec: defaultExec}
}

// defaultExec runs name+args via os/exec, capturing stdout and stderr separately.
func defaultExec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// iso8601 formats t as RFC3339 in UTC, the shape am's --since flag accepts.
//
// Verified in share/contracts.md (am section): "--since requires ISO-8601,
// not duration strings". The reference shell helper uses `date -u +%FT%TZ`
// which produces e.g. 2026-05-09T12:34:56Z — this matches.
func iso8601(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
