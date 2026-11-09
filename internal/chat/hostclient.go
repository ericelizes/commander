package chat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// hostConn is the Engine's end of a chat host socket.
type hostConn struct {
	c  net.Conn
	r  *bufio.Reader
	mu sync.Mutex // writes
}

var errHostGone = errors.New("chat: chat host is gone")

// dialHost connects to role's host and reads its hello. A refused connection
// (no host) returns an error.
func dialHost(sock string) (*hostConn, *hostHello, error) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	hc := &hostConn{c: c, r: bufio.NewReaderSize(c, 1<<20)}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	m, err := hc.read()
	_ = c.SetReadDeadline(time.Time{})
	if err != nil || m.T != "hello" || m.Hello == nil {
		c.Close()
		return nil, nil, fmt.Errorf("chat: bad chat host hello: %v", err)
	}
	return hc, m.Hello, nil
}

func (hc *hostConn) read() (hostMsg, error) {
	line, err := hc.r.ReadBytes('\n')
	if err != nil {
		return hostMsg{}, err
	}
	var m hostMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return hostMsg{}, err
	}
	return m, nil
}

func (hc *hostConn) send(m hostMsg) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if _, err := hc.c.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("%w: %v", errHostGone, err)
	}
	return nil
}

func (hc *hostConn) close() { _ = hc.c.Close() }
