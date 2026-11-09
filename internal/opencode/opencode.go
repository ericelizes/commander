package opencode

import (
	"context"
	"errors"
)

// ErrSessionNotFound is returned by Run when --session points at a ses_-prefixed
// ID that the opencode server does not know about. Callers can sentinel-check
// this with errors.Is to fall back to capture-then-resume.
var ErrSessionNotFound = errors.New("opencode: session not found")

// RunOptions are the optional flags forwarded to `opencode run`. Zero values
// mean "do not pass the flag". This mirrors the verified flag table in
// share/contracts.md.
type RunOptions struct {
	// Model in provider/model format (e.g. "anthropic/claude-sonnet-4-5").
	// Empty string means rely on agent default.
	Model string
	// AttachURL points at a running `opencode serve` (e.g. http://localhost:9997).
	// Empty means spawn-per-operator (the conservative U7 default).
	AttachURL string
	// Dir sets opencode's working directory via --dir.
	Dir string
	// Variant selects a model variant via --variant.
	Variant string
	// Title sets the session title via --title (only meaningful on first run).
	Title string
	// Files attach files to the message via --file (repeated).
	Files []string
	// Continue resumes the most-recent session via --continue. Mutually
	// exclusive with a non-empty sessionID arg to Run.
	Continue bool
	// Fork forks the session when continuing via --fork.
	Fork bool
	// Thinking displays thinking blocks via --thinking.
	Thinking bool
	// SkipPermissions passes --dangerously-skip-permissions. Only used inside
	// the Docker operator sandbox.
	SkipPermissions bool
}

// RunResult summarizes a completed `opencode run` invocation.
//
// Live consumption of the event stream happens through Events while the run
// is in progress; RunResult is what's available *after* the channel closes.
//
// SessionID is the captured-or-resumed ID. If the caller passed a non-empty
// sessionID to Run, this echoes that. If the caller passed empty (first run),
// this is the ID captured from the first event that carried one.
//
// FinalText is the concatenation of every `text` event's part.text — useful
// for terse non-streaming callers (operator chat tail uses the live channel).
//
// TotalCostUSD and TotalTokens are accumulated across every step_finish event
// (Tokens = sum of Input + Output, matching the watchdog's billing math).
type RunResult struct {
	SessionID    string
	FinalText    string
	TotalCostUSD float64
	TotalTokens  int
	// Events is the live stream consumed during the run. After Run returns,
	// the channel is closed and any unread events have already been drained
	// into the accumulators above.
	Events <-chan Event
}

// Stats is the (mostly-raw) output of `opencode stats`. It's text-only by
// design — opencode does not emit JSON for stats — so this is display-only,
// never used for ceiling enforcement (U10 reads step_finish events instead).
type Stats struct {
	// Raw is the full stdout text from opencode stats, verbatim.
	Raw string
	// TotalSpendUSD is best-effort: parsed from the raw text via a trivial
	// regex if a "$X.YZ" total is present. Zero when parsing fails.
	TotalSpendUSD float64
	// DaysBack and Project echo the invocation flags so callers can render
	// "stats for last N days on <project>" without re-tracking.
	DaysBack int
	Project  string
}

// Server is a handle to a running `opencode serve` process. Shutdown sends
// SIGTERM to the process group and waits up to ctx for clean exit.
type Server interface {
	Addr() string
	Shutdown(ctx context.Context) error
}

// Client is the in-process facade over the opencode CLI.
//
// Phase 1 NewSession is just Run with an empty sessionID — the contract
// (capture-then-resume) makes a separate "create session" method redundant
// and would actively mislead callers into pre-minting IDs.
type Client interface {
	// Run invokes `opencode run`. If sessionID is empty, the run creates
	// a new session and the captured ID is returned in RunResult.SessionID
	// (taken from the first nd-JSON event that carries one). If sessionID
	// is non-empty, --session <sessionID> is passed and the run resumes.
	//
	// Events are streamed on RunResult.Events as they're parsed off stdout.
	// The channel is buffered (64) so a slow consumer doesn't stall the
	// underlying pipe; once the run completes the channel is closed.
	//
	// Returns ErrSessionNotFound when --session points at a missing ID.
	Run(ctx context.Context, sessionID, agent, prompt string, opts RunOptions) (*RunResult, error)

	// Stats invokes `opencode stats --days N [--project <p>]`. Output is
	// text — see Stats.Raw — with best-effort total-spend extraction. This
	// is display-only; never consume Stats for ceiling enforcement.
	Stats(ctx context.Context, project string, daysBack int) (*Stats, error)

	// Serve starts `opencode serve --port <port>` as a background process.
	// The returned Server's Addr is "http://localhost:<port>". Shutdown
	// terminates the process group.
	Serve(ctx context.Context, port int) (Server, error)
}
