package ceilings

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime/debug"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/opencode"
)

// lineReader is the minimal surface the consumer needs from a stream source.
// *bufio.Scanner satisfies this in production via lineScannerAdapter.
type lineReader interface {
	// Scan advances to the next line. Returns false on EOF or scanner error.
	Scan() bool
	// Bytes returns the latest line (without trailing newline).
	Bytes() []byte
	// Err returns the underlying scanner error (nil on EOF).
	Err() error
	// Close releases the underlying process/file handle.
	Close() error
}

// scannerLineReader adapts *bufio.Scanner + an io.Closer to lineReader.
type scannerLineReader struct {
	scanner *bufio.Scanner
	closer  io.Closer
}

func (s *scannerLineReader) Scan() bool    { return s.scanner.Scan() }
func (s *scannerLineReader) Bytes() []byte { return s.scanner.Bytes() }
func (s *scannerLineReader) Err() error    { return s.scanner.Err() }
func (s *scannerLineReader) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

// dockerLogsSource is the production EventSource. It shells out to
// `docker logs -f --since=0s <containerID>` for code-type runs.
//
// Note on --since=0s: this is a defensive flag. Without it, very fast
// operators could emit step_finish before the watchdog wires up its consumer
// and we'd miss the early cost. --since=0s requests "everything from now"
// which docker treats as "from container start". The combination with -f
// then tails new lines indefinitely.
type dockerLogsSource struct{}

func (dockerLogsSource) Open(ctx context.Context, st *runState) (lineReader, error) {
	if st.isKnowledge {
		// Host operators (knowledge-type and host-runtime code) pipe their
		// output into a fifo via tmux pipe-pane at spawn time; tmuxPaneID is
		// the session, so the fifo path is derived (see runState.fifoPath).
		fifo := st.fifoPath()
		if fifo == "" {
			return nil, errors.New("ceilings: host run missing fifo path")
		}
		cmd := exec.CommandContext(ctx, "tail", "-F", fifo)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("ceilings: tail stdout pipe: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("ceilings: tail start: %w", err)
		}
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		return &scannerLineReader{scanner: sc, closer: &cmdCloser{cmd: cmd, pipe: stdout}}, nil
	}
	if st.containerID == "" {
		return nil, errors.New("ceilings: code run missing containerID")
	}
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--since=0s", st.containerID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ceilings: docker logs stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ceilings: docker logs start: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	// 4MB max line — opencode emits compact JSON but tool_result events can
	// embed large blobs. Same buffer the spawn capture uses.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &scannerLineReader{scanner: sc, closer: &cmdCloser{cmd: cmd, pipe: stdout}}, nil
}

// cmdCloser kills the underlying process AND drains its stdout pipe so the
// goroutine exits cleanly when the watchdog tears down.
type cmdCloser struct {
	cmd  *exec.Cmd
	pipe io.ReadCloser
}

func (c *cmdCloser) Close() error {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.pipe != nil {
		_ = c.pipe.Close()
	}
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
	return nil
}

// consume is the per-operator goroutine entry. It runs `recover` around the
// inner loop so a panic in one operator's parse path can't take down the
// whole watchdog. Panics restart the inner loop once; persistent panics give
// up after one retry to avoid infinite spin.
func (w *Watchdog) consume(ctx context.Context, st *runState) {
	const maxRestarts = 1
	restarts := 0
	for {
		err := w.consumeLoop(ctx, st)
		if ctx.Err() != nil {
			// Watchdog shutting down — exit cleanly.
			return
		}
		if err == nil {
			// Stream closed cleanly: the operator exited. Mark the run as
			// "exited" (NOT killed-ceiling) and unregister.
			markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := w.store.MarkClosed(markCtx, st.runID, "exited"); err != nil {
				log.Printf("ceilings: mark exited run=%d: %v", st.runID, err)
			}
			cancel()
			w.Unregister(st.runID)
			return
		}
		if restarts >= maxRestarts {
			log.Printf("ceilings: consumer run=%d exceeded restart budget; giving up: %v", st.runID, err)
			w.Unregister(st.runID)
			return
		}
		restarts++
		log.Printf("ceilings: consumer run=%d errored, restarting (%d/%d): %v", st.runID, restarts, maxRestarts, err)
	}
}

// consumeLoop is one pass of the event-stream reader. Returns:
//   - nil on clean EOF (operator exited)
//   - non-nil on stream error or panic-recover
//
// A returned non-nil error means the caller should consider restarting.
func (w *Watchdog) consumeLoop(ctx context.Context, st *runState) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("ceilings: panic in consumer run=%d: %v\n%s", st.runID, r, debug.Stack())
		}
	}()

	reader, err := w.source.Open(ctx, st)
	if err != nil {
		return fmt.Errorf("open event source: %w", err)
	}
	defer reader.Close()

	// Bridge ctx cancellation to reader.Close so an in-flight Scan() unblocks
	// when the watchdog tears down (Stop / Unregister). Without this, a
	// blocking source (e.g. `docker logs -f` waiting on the next line) keeps
	// the consumer goroutine pinned past Stop.
	closeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = reader.Close()
		case <-closeDone:
		}
	}()
	defer close(closeDone)

	claudeEvents := agentrt.NewClaudeEventMapper()
	for reader.Scan() {
		// ctx cancellation gives us early-out on Stop/Unregister.
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line := bytes.TrimSpace(reader.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		// Claude Code operators stream a different nd-JSON shape; the mapper
		// translates it into the opencode events handled below.
		if ev, ok := claudeEvents.Map(line); ok {
			w.handleEvent(ctx, st, ev)
			continue
		}
		ev, err := opencode.ParseEvent(line)
		if err != nil {
			// Malformed JSON: log + skip per U10-a "tolerates malformed lines".
			log.Printf("ceilings: skip malformed event run=%d: %v", st.runID, err)
			continue
		}
		w.handleEvent(ctx, st, ev)
	}
	if err := reader.Err(); err != nil {
		return fmt.Errorf("scanner: %w", err)
	}
	return nil
}

// handleEvent dispatches a parsed opencode event to the right ceiling check.
// Only step_finish and error events drive ceiling math — everything else is
// no-op (text, tool_use, tool_result, step_start, future event types).
func (w *Watchdog) handleEvent(ctx context.Context, st *runState, ev opencode.Event) {
	switch ev.Type {
	case "step_finish":
		w.applyStepFinish(ctx, st, ev)
	case "error":
		w.applyError(ctx, st, ev)
	}
}

// applyStepFinish accumulates per-step cost + tokens into runState, persists
// running totals, then evaluates ceilings. A ceiling breach kills the
// operator; pre-emptive halt kills BEFORE the next call so we don't tip over.
//
// cost: 0 events ARE valid (cached/free model paths) — accept as zero.
func (w *Watchdog) applyStepFinish(ctx context.Context, st *runState, ev opencode.Event) {
	tokens := eventTokens(ev)

	st.mu.Lock()
	st.tokensUsed += tokens
	st.usdUsed += ev.Part.Cost
	// step_finish indicates a successful turn — reset the failure counter.
	st.consecutiveFailures = 0
	tokensSnap := st.tokensUsed
	usdSnap := st.usdUsed
	st.mu.Unlock()

	// Persist every event so a commander crash mid-task leaves accurate
	// running totals on disk. The TUI side panel reads from the DB on cold
	// load and from RunUsage on hot.
	if err := w.store.UpdateUsage(ctx, st.runID, tokensSnap, usdSnap); err != nil {
		log.Printf("ceilings: update usage run=%d: %v", st.runID, err)
	}

	if kill, reason := w.checkCeilings(st); kill {
		w.kill(ctx, st, reason)
	}
}

// applyError increments the failure counter on operator-emitted error events
// and triggers a kill at MaxRetries. Increment is persisted via the store so
// a commander restart sees the accumulated failure state.
func (w *Watchdog) applyError(ctx context.Context, st *runState, _ opencode.Event) {
	st.mu.Lock()
	st.consecutiveFailures++
	failures := st.consecutiveFailures
	st.mu.Unlock()

	if _, err := w.store.IncrementFailures(ctx, st.runID); err != nil {
		log.Printf("ceilings: increment failures run=%d: %v", st.runID, err)
	}

	if w.limits.MaxRetries > 0 && failures >= w.limits.MaxRetries {
		w.kill(ctx, st, fmt.Sprintf("retry ceiling: %d consecutive failures", failures))
	}
}
