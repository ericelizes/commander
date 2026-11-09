package opencode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// New returns the production Client backed by the real `opencode` binary on PATH.
func New() Client {
	return &execClient{binary: "opencode"}
}

// NewWithBinary lets tests / install-time overrides point at a different
// opencode binary path (e.g. a fake script on $PATH).
func NewWithBinary(path string) Client {
	return &execClient{binary: path}
}

// execClient is the production Client. It shells out to `opencode` and parses
// the nd-JSON stream as it arrives.
type execClient struct {
	binary string
	// commandFn is overridable for tests; production uses exec.CommandContext.
	commandFn func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func (c *execClient) cmd(ctx context.Context, args ...string) *exec.Cmd {
	if c.commandFn != nil {
		return c.commandFn(ctx, c.binary, args...)
	}
	return exec.CommandContext(ctx, c.binary, args...)
}

// eventsBufferSize keeps the nd-JSON pipe from blocking when the caller is
// slow. 64 events is enough headroom for ~a second of normal opencode output
// without forcing an unbounded queue.
const eventsBufferSize = 64

func (c *execClient) Run(ctx context.Context, sessionID, agent, prompt string, opts RunOptions) (*RunResult, error) {
	args := buildRunArgs(sessionID, agent, prompt, opts)

	cmd := c.cmd(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode run: stdout pipe: %w", err)
	}
	// Capture stderr separately for error reporting. opencode writes
	// NotFoundError and other diagnostics here.
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode run: start: %w", err)
	}

	out := make(chan Event, eventsBufferSize)
	result := &RunResult{
		SessionID: sessionID,
		Events:    out,
	}

	// finalText accumulator + accumulators are owned by the goroutine that
	// reads stdout. We synchronize via a wait group so Run blocks until the
	// stream is fully drained and the process has exited.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(out)
		streamEvents(stdout, out, result)
	}()

	wg.Wait()

	waitErr := cmd.Wait()
	if waitErr != nil {
		stderr := stderrBuf.String()
		if isSessionNotFound(stderr) {
			return result, ErrSessionNotFound
		}
		return result, fmt.Errorf("opencode run: %w (stderr: %s)", waitErr, strings.TrimSpace(stderr))
	}

	return result, nil
}

// streamEvents reads nd-JSON lines off r, parses each, accumulates into
// result, and forwards to out. Malformed lines are skipped silently — opencode
// occasionally interleaves non-JSON banner output that's safe to drop.
func streamEvents(r io.Reader, out chan<- Event, result *RunResult) {
	scanner := bufio.NewScanner(r)
	// Tokens (and especially text events) can be large; bump the buffer well
	// above bufio's 64KiB default to avoid ErrTooLong on long step outputs.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var finalText strings.Builder
	for scanner.Scan() {
		line := scanner.Bytes()
		// Drop empty / non-JSON lines (banner text). Cheap byte-prefix check.
		trimmed := trimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		ev, err := ParseEvent(trimmed)
		if err != nil {
			// Malformed object — skip rather than abort the run.
			continue
		}

		// Capture session ID on the first event that carries one when the
		// caller didn't pass --session.
		if result.SessionID == "" && ev.SessionID != "" {
			result.SessionID = ev.SessionID
		}

		switch ev.Type {
		case "text":
			finalText.WriteString(ev.Part.Text)
		case "step_finish":
			result.TotalCostUSD += ev.Part.Cost
			result.TotalTokens += ev.Part.Tokens.Input + ev.Part.Tokens.Output
		}

		out <- ev
	}
	result.FinalText = finalText.String()
}

// trimSpace trims leading/trailing ASCII whitespace without allocating.
func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// buildRunArgs assembles the argv for `opencode run`, mirroring the verified
// flag table in share/contracts.md. Order is stable for snapshot-friendly tests.
func buildRunArgs(sessionID, agent, prompt string, opts RunOptions) []string {
	args := []string{"run", "--format", "json"}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	if agent != "" {
		args = append(args, "--agent", agent)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.AttachURL != "" {
		args = append(args, "--attach", opts.AttachURL)
	}
	if opts.Dir != "" {
		args = append(args, "--dir", opts.Dir)
	}
	if opts.Variant != "" {
		args = append(args, "--variant", opts.Variant)
	}
	if opts.Title != "" {
		args = append(args, "--title", opts.Title)
	}
	for _, f := range opts.Files {
		args = append(args, "--file", f)
	}
	if opts.Continue {
		args = append(args, "--continue")
	}
	if opts.Fork {
		args = append(args, "--fork")
	}
	if opts.Thinking {
		args = append(args, "--thinking")
	}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, prompt)
	return args
}

// isSessionNotFound matches the verified opencode error string for a missing
// --session ID. Substring match keeps us tolerant of opencode's prefix wording
// drifting between point releases.
func isSessionNotFound(stderr string) bool {
	return strings.Contains(stderr, "NotFoundError") &&
		strings.Contains(stderr, "Session not found")
}
