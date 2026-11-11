package chat

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// A chat host is a tiny detached process (one per running role) that owns the
// claude child: it holds claude's stdin, journals claude's stdout lines and
// serves them over a unix socket, so the Engine (cmdrd) can restart without
// claude noticing. The host does no interpretation of the stream: the Engine
// normalizes, persists and publishes. Bound executions end after one result;
// the host records actual process exit, never bead completion. Lines the Engine has not acknowledged
// stay in the journal, so a new Engine attaches and picks up where the old one
// stopped.

// HostArg is argv[1] of a chat host process; the binary's main (cmdrd) and test
// binaries call MaybeRunHost first.
const HostArg = "__chat-host"

const (
	// hostExitLinger is how long a host whose claude exited waits for an
	// Engine to collect the exit before it goes away on its own.
	hostExitLinger = 5 * time.Minute
	// hostJournalCap bounds the unacknowledged journal (bytes).
	hostJournalCap = 128 << 20
)

// hostInfo is what the Engine tells the host about the process it starts, so a
// later Engine can adopt it without resolving anything.
type hostInfo struct {
	ExecutionRunID int64     `json:"execution_run_id,omitempty"`
	Role           string    `json:"role"`
	SessionID      string    `json:"session_id"`
	Model          string    `json:"model,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	Resumed        bool      `json:"resumed,omitempty"`
	StartedAt      time.Time `json:"started_at"`
}

// hostConfig is the file passed to the host (argv[2]).
type hostConfig struct {
	InitialPrompt   string   `json:"initial_prompt,omitempty"`
	ExitReceiptPath string   `json:"exit_receipt_path,omitempty"`
	Sock            string   `json:"sock"`
	Binary          string   `json:"binary"`
	Args            []string `json:"args"`
	Cwd             string   `json:"cwd"`
	Info            hostInfo `json:"info"`
}

// hostExit reports how claude ended.
type hostExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

type hostHello struct {
	Info  hostInfo  `json:"info"`
	PID   int       `json:"pid"`
	First int64     `json:"first"`          // idx of the oldest journal line
	Acked int64     `json:"acked"`          // journal lines below this were processed by an Engine
	Done  []string  `json:"done,omitempty"` // control request ids already answered
	Exit  *hostExit `json:"exit,omitempty"`
}

// hostMsg is one socket line, both directions. Op is client -> host, T host -> client.
type hostMsg struct {
	Op    string     `json:"op,omitempty"` // in, close_stdin, signal, ack, bye, quit
	T     string     `json:"t,omitempty"`  // hello, out, exit, kicked
	Line  string     `json:"line,omitempty"`
	Idx   int64      `json:"idx,omitempty"`
	Sig   int        `json:"sig,omitempty"`
	Hello *hostHello `json:"hello,omitempty"`
	Exit  *hostExit  `json:"exit,omitempty"`
}

type journalLine struct {
	idx  int64
	line string
}

type host struct {
	listener net.Listener // retired before publishing exit, so a restart cannot adopt this host
	cfg      hostConfig
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	wmu      sync.Mutex // stdin writes

	mu                sync.Mutex
	cond              *sync.Cond
	journal           []journalLine
	bytes             int
	next              int64 // idx of the next stdout line
	acked             int64
	turnStart         int64 // idx after the last result line: the journal keeps the current turn
	done              map[string]bool
	exited            *hostExit
	quit              bool
	nativeEndOnce     sync.Once
	nativeResultError bool
	client            *hostClientConn
	stderr            tailBuffer
	lock              *os.File // the session lock, dropped when claude is gone
}

type hostClientConn struct {
	c      net.Conn
	closed bool
	done   chan struct{} // closed once serve has handled everything the Engine sent
}

// MaybeRunHost runs the chat host and exits when argv says so; otherwise it
// returns. Call it first thing in main (and in test mains).
func MaybeRunHost() {
	if len(os.Args) < 3 || os.Args[1] != HostArg {
		return
	}
	os.Exit(runHost(os.Args[2]))
}

func runHost(cfgPath string) int {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chat-host:", err)
		return 1
	}
	var cfg hostConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "chat-host:", err)
		return 1
	}
	_ = os.Remove(cfgPath)
	_ = os.Remove(cfg.Sock) // stale file of a dead host; the Engine checked it is dead
	ln, err := net.Listen("unix", cfg.Sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chat-host: listen:", err)
		return 1
	}
	defer ln.Close()

	h := &host{cfg: cfg, done: map[string]bool{}, listener: ln}
	h.cond = sync.NewCond(&h.mu)
	h.startClaude()
	go h.acceptLoop(ln)

	h.mu.Lock()
	var deadline time.Time
	for !h.quit {
		if h.exited != nil && deadline.IsZero() {
			deadline = time.Now().Add(hostExitLinger)
			time.AfterFunc(hostExitLinger+time.Second, func() { h.mu.Lock(); h.cond.Broadcast(); h.mu.Unlock() })
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		h.cond.Wait()
	}
	h.mu.Unlock()
	return 0
}

// startClaude runs claude with fd 3 (the session lock) passed through. A start
// failure is reported to the Engine as an exit.
func (h *host) startClaude() {
	lock := os.NewFile(3, "session-lock")
	h.lock = lock
	cmd := exec.Command(h.cfg.Binary, h.cfg.Args...)
	cmd.Dir = h.cfg.Cwd
	cmd.Env = os.Environ()
	if lock != nil {
		cmd.ExtraFiles = []*os.File{lock} // the claude child holds the session lock too
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = &h.stderr
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	err := errors.Join(err1, err2)
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		h.persistNativeExit(&hostExit{Code: -1, Stderr: err.Error()})
		h.releaseLock()
		h.mu.Lock()
		h.exited = &hostExit{Code: -1, Stderr: err.Error()}
		h.cond.Broadcast()
		h.mu.Unlock()
		return
	}
	h.cmd, h.stdin = cmd, stdin
	go h.pump(stdout)
	if h.cfg.Info.ExecutionRunID > 0 && h.cfg.InitialPrompt != "" {
		// The detached host owns first delivery. A daemon dying between binding a
		// run and sending a chat message cannot leave an idle claimed execution.
		journal, _ := json.Marshal(map[string]any{"type": "commander_execution_start", "session_id": h.cfg.Info.SessionID, "uuid": fmt.Sprintf("execution-start-%d", h.cfg.Info.ExecutionRunID), "message": h.cfg.InitialPrompt})
		h.mu.Lock()
		h.journal = append(h.journal, journalLine{idx: h.next, line: string(journal)})
		h.next++
		h.bytes += len(journal)
		h.cond.Broadcast()
		h.mu.Unlock()
		raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": h.cfg.InitialPrompt}})
		h.writeStdin(string(raw))
	}
}

// pump journals claude's stdout, then reaps it and records the exit.
func (h *host) pump(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		var t struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(sc.Bytes(), &t)
		h.mu.Lock()
		h.journal = append(h.journal, journalLine{idx: h.next, line: line})
		h.bytes += len(line)
		h.next++
		if t.Type == "result" {
			h.turnStart = h.next
		}
		for h.bytes > hostJournalCap && len(h.journal) > 1 {
			h.bytes -= len(h.journal[0].line)
			h.journal = h.journal[1:]
		}
		h.cond.Broadcast()
		h.mu.Unlock()
		if t.Type == "result" && h.cfg.Info.ExecutionRunID > 0 {
			h.finishNativeTurn(sc.Bytes())
		}
	}
	err := h.cmd.Wait()
	ex := &hostExit{Stderr: h.stderr.String()}
	if st := h.cmd.ProcessState; st != nil {
		ex.Code = st.ExitCode()
		if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			ex.Signal = ws.Signal().String()
		}
	} else if err != nil {
		ex.Code = -1
	}
	h.persistNativeExit(ex)
	h.releaseLock()
	h.mu.Lock()
	h.exited = ex
	h.cond.Broadcast()
	h.mu.Unlock()
}

// releaseLock unlocks the session so a restart finds it free at once, even if
// a grandchild of claude still holds the descriptor.
func (h *host) releaseLock() {
	if h.lock != nil {
		_ = syscall.Flock(int(h.lock.Fd()), syscall.LOCK_UN)
		_ = h.lock.Close()
		h.lock = nil
	}
}

func (h *host) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go h.serve(c)
	}
}

// serve handles one Engine connection. A newer connection replaces an older
// one (the old cmdrd is draining).
func (h *host) serve(c net.Conn) {
	defer c.Close()
	cc := &hostClientConn{c: c, done: make(chan struct{})}
	defer close(cc.done)
	enc := json.NewEncoder(c)
	h.mu.Lock()
	old := h.client
	if old != nil {
		old.closed = true
		_ = json.NewEncoder(old.c).Encode(hostMsg{T: "kicked"})
	}
	h.client = cc
	h.mu.Unlock()
	if old != nil {
		// An Engine that let go (Close) sent its last ack just before hanging
		// up and it may still be unread: take it before the hello reports what
		// was acked. A live old Engine never hangs up, so this wait is bounded.
		select {
		case <-old.done:
		case <-time.After(300 * time.Millisecond):
		}
		_ = old.c.Close()
	}
	h.mu.Lock()
	hello := &hostHello{Info: h.cfg.Info, Acked: h.acked, Exit: nil}
	if h.cmd != nil && h.cmd.Process != nil {
		hello.PID = h.cmd.Process.Pid
	}
	for id := range h.done {
		hello.Done = append(hello.Done, id)
	}
	cursor := h.next
	if len(h.journal) > 0 {
		cursor = h.journal[0].idx
	}
	hello.First = cursor
	h.mu.Unlock()
	if err := enc.Encode(hostMsg{T: "hello", Hello: hello}); err != nil {
		return
	}

	go h.stream(cc, enc, cursor)

	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var m hostMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		h.command(m)
	}
	h.mu.Lock()
	cc.closed = true
	if h.client == cc {
		h.client = nil
	}
	h.cond.Broadcast()
	h.mu.Unlock()
}

// stream sends journal lines from cursor on, then the exit once claude is done.
func (h *host) stream(cc *hostClientConn, enc *json.Encoder, cursor int64) {
	for {
		h.mu.Lock()
		for !cc.closed && !h.quit && cursor >= h.next && h.exited == nil {
			h.cond.Wait()
		}
		if cc.closed || h.quit {
			h.mu.Unlock()
			return
		}
		var batch []hostMsg
		if len(h.journal) > 0 {
			cursor = max(cursor, h.journal[0].idx) // pruned past the cursor (journal cap): skip ahead
			for _, jl := range h.journal[cursor-h.journal[0].idx:] {
				batch = append(batch, hostMsg{T: "out", Idx: jl.idx, Line: jl.line})
			}
		}
		exit := h.exited
		h.mu.Unlock()
		for _, m := range batch {
			if enc.Encode(m) != nil {
				return
			}
		}
		cursor += int64(len(batch))
		if exit != nil && len(batch) == 0 {
			// Retire the socket before the Engine releases the role. Stop/Start
			// must not reconnect to this exiting host. Closing the listener also
			// unlinks it exactly once; deferred cleanup cannot unlink its successor.
			if h.listener != nil {
				_ = h.listener.Close()
			}
			_ = enc.Encode(hostMsg{T: "exit", Exit: exit})
			// Keep the connection for bye; wait until closed.
			h.mu.Lock()
			for !cc.closed && !h.quit {
				h.cond.Wait()
			}
			h.mu.Unlock()
			return
		}
	}
}

func (h *host) command(m hostMsg) {
	switch m.Op {
	case "in":
		h.writeStdin(m.Line)
	case "close_stdin":
		if h.stdin != nil {
			h.wmu.Lock()
			_ = h.stdin.Close()
			h.wmu.Unlock()
		}
	case "signal":
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Signal(syscall.Signal(m.Sig))
		}
	case "ack":
		h.mu.Lock()
		if m.Idx > h.acked {
			h.acked = m.Idx
		}
		h.pruneLocked()
		h.mu.Unlock()
	case "bye": // the Engine collected the exit (or wants the host gone)
		h.mu.Lock()
		if h.exited != nil {
			h.quit = true
		}
		h.cond.Broadcast()
		h.mu.Unlock()
	case "quit": // end claude and the host
		go h.kill()
	}
}

// pruneLocked drops acknowledged lines of finished turns.
func (h *host) pruneLocked() {
	limit := min(h.acked, h.turnStart)
	n := 0
	for n < len(h.journal) && h.journal[n].idx < limit {
		h.bytes -= len(h.journal[n].line)
		n++
	}
	if n > 0 {
		h.journal = append([]journalLine(nil), h.journal[n:]...)
	}
}

func (h *host) writeStdin(line string) {
	if h.stdin == nil {
		return
	}
	var env struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(line), &env) == nil && env.Type == "control_response" && env.Response.RequestID != "" {
		h.mu.Lock()
		h.done[env.Response.RequestID] = true
		h.mu.Unlock()
	}
	h.wmu.Lock()
	_, _ = h.stdin.Write([]byte(line + "\n"))
	h.wmu.Unlock()
}

// kill ends claude (stdin close, SIGTERM, SIGKILL) and then the host.
func (h *host) kill() {
	if h.cmd == nil || h.cmd.Process == nil {
		h.mu.Lock()
		h.quit = true
		h.cond.Broadcast()
		h.mu.Unlock()
		return
	}
	if h.stdin != nil {
		h.wmu.Lock()
		_ = h.stdin.Close()
		h.wmu.Unlock()
	}
	for _, sig := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
		if sig != 0 {
			_ = h.cmd.Process.Signal(sig)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			h.mu.Lock()
			gone := h.exited != nil
			h.mu.Unlock()
			if gone {
				h.mu.Lock()
				h.quit = true
				h.cond.Broadcast()
				h.mu.Unlock()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// hostDir is where host sockets live: <home>/chat-hosts.
func hostDir(home string) string { return filepath.Join(home, "chat-hosts") }

// hostSock is <home>/chat-hosts/<role>.sock. Unix socket paths top out around
// 104 bytes, so a long home or role falls back to a hashed name in /tmp.
func hostSock(home, role string) string {
	path := filepath.Join(hostDir(home), role+".sock")
	if len(path) <= 100 {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return filepath.Join("/tmp", "cmdr-chat-"+hex.EncodeToString(sum[:8])+".sock")
}

func netListenUnix(path string) (*net.UnixListener, error) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
}
