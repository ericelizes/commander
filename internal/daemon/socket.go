package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// Socket is the TUI-attach surface: a Unix domain socket that speaks a tiny
// JSON-line protocol. One request per line in, one response per line out,
// then the server closes the connection.
//
// Protocol (READ-ONLY for U20 — write surface lands later):
//
//	{"op":"ping"}            → {"ok":true}
//	{"op":"state"}           → {"ok":true,"state":{"operators":N,...}}
//	<anything else>          → {"ok":false,"error":"unknown op: ..."}
//
// Why so narrow: the daemon is the source of truth for live in-memory state,
// but mutations (spawn, decline, etc.) all need argument validation that
// belongs in the TUI/Spawner pipeline. Exposing them over the socket would
// fork the validation surface. We keep U20 strictly observational.
type Socket struct {
	path     string
	listener net.Listener
	onState  func() State

	// connWg tracks active accepted-connection goroutines so Close can wait
	// for them to drain before returning.
	connWg sync.WaitGroup

	// closeOnce guards the listener close + socket-file unlink so Close is
	// idempotent.
	closeOnce sync.Once
}

// NewSocket binds a Unix domain listener at path. A stale socket file from a
// previous (crashed) cmdrd run is unlinked first — launchd / systemd will
// have skipped cleanup when the parent process died ungracefully.
//
// onState is invoked synchronously inside the connection handler when a
// {"op":"state"} request lands; keep it cheap (it holds the connection open).
func NewSocket(path string, onState func() State) (*Socket, error) {
	// Clean up a stale socket file. ENOENT is fine; other errors mean
	// something we can't recover from (e.g. permission denied, path is a
	// non-socket file we shouldn't clobber). os.Remove returns *PathError
	// wrapping ENOENT; errors.Is handles it cleanly.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("daemon: socket: clean stale %s: %w", path, err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: socket: listen %s: %w", path, err)
	}
	// 0600: socket is per-user, only the owner should connect. matches the
	// am cli's per-user inbox security posture.
	_ = os.Chmod(path, 0o600)
	return &Socket{
		path:     path,
		listener: l,
		onState:  onState,
	}, nil
}

// Serve accepts connections until ctx is cancelled or Close is called. Each
// accepted connection runs handleConn in a goroutine. Returns nil on a clean
// shutdown; a non-nil error indicates an Accept failure that wasn't caused by
// us closing the listener.
func (s *Socket) Serve(ctx context.Context) error {
	// Tear the listener down when ctx cancels so Accept returns. We do this
	// inside Serve (instead of relying on Close) because Run wires Close
	// into shutdown after ctx is already cancelled — without this, Accept
	// would block until external Close ran.
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Closed listener after Close/ctx-cancel: clean exit. Other
			// errors propagate so the caller can log them.
			if errors.Is(err, net.ErrClosed) {
				s.connWg.Wait()
				return nil
			}
			return err
		}
		s.connWg.Add(1)
		go func(c net.Conn) {
			defer s.connWg.Done()
			s.handleConn(c)
		}(conn)
	}
}

// Close unbinds the listener, removes the socket file, and waits for any
// in-flight connection handlers to finish. Idempotent.
func (s *Socket) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.listener != nil {
			err = s.listener.Close()
		}
		// Unlink the socket file even if Close failed — if we leave it
		// around, the next NewSocket has to clean up after us.
		_ = os.Remove(s.path)
	})
	return err
}

// handleConn implements one round of the request/response protocol then
// closes. We deliberately don't keep the connection open for multiple
// requests — TUI attach is short-lived (open, ask, close), and one-shot
// connections eliminate a class of "stuck consumer holding the daemon
// hostage" bugs.
func (s *Socket) handleConn(c net.Conn) {
	defer c.Close()
	// Bound the handshake. A wedged client shouldn't tie up a daemon
	// goroutine forever.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))

	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}

	var req struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(trimNewline(line), &req); err != nil {
		writeJSON(c, map[string]any{"ok": false, "error": "invalid json"})
		return
	}

	switch req.Op {
	case "ping":
		writeJSON(c, map[string]any{"ok": true})
	case "state":
		var st State
		if s.onState != nil {
			st = s.onState()
		}
		writeJSON(c, map[string]any{"ok": true, "state": st})
	default:
		writeJSON(c, map[string]any{"ok": false, "error": "unknown op: " + req.Op})
	}
}

// trimNewline strips a single trailing \n (and optional \r) so JSON parsing
// doesn't trip on the framing byte. bufio.Reader.ReadBytes includes the
// delimiter in the returned slice.
func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// writeJSON marshals v and writes it to c terminated by a newline. Errors
// here are unrecoverable — we log nothing because the connection is about to
// close anyway.
func writeJSON(c net.Conn, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		return
	}
	buf = append(buf, '\n')
	_, _ = c.Write(buf)
}
