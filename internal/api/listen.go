package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultPort is used when the config sets no api.port.
const DefaultPort = 8787

// portAttempts is how many ports after the configured one are tried.
const portAttempts = 20

// EnsureToken returns the API token from <home>/api-token, creating a random
// one (mode 0600) when the file is missing or empty.
func EnsureToken(home string) (string, error) {
	path := filepath.Join(home, "api-token")
	if raw, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(raw)); tok != "" {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", err
			}
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, os.Chmod(path, 0o600) // WriteFile keeps the mode of an existing file
}

// reusePortListen binds addr with SO_REUSEPORT. A restarting cmdrd then takes
// the port while the previous one is still draining (instead of drifting to
// the next port and rewriting api-url under every client), and the old one
// closing its listener leaves the new one serving. Only sockets that set
// SO_REUSEPORT themselves can share the port, so an unrelated program on it
// still reports EADDRINUSE and moves us on.
func reusePortListen(addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(context.Background(), "tcp", addr)
}

// listen binds 127.0.0.1:port, moving to the next port while it is taken.
func listen(port int) (net.Listener, error) {
	var err error
	for p := port; p < port+portAttempts; p++ {
		var ln net.Listener
		ln, err = reusePortListen(fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("api: no free port in %d-%d: %w", port, port+portAttempts-1, err)
}

// Start serves s on loopback (port 0 = DefaultPort), writes the chosen base
// URL to <home>/api-url, and keeps the snapshot fresh until ctx is done. It
// returns the URL once the listener is bound.
func Start(ctx context.Context, home string, port int, s *Server) (string, error) {
	if port == 0 {
		port = DefaultPort
	}
	ln, err := listen(port)
	if err != nil {
		return "", err
	}
	base := "http://" + ln.Addr().String()
	if err := os.WriteFile(filepath.Join(home, "api-url"), []byte(base+"\n"), 0o644); err != nil {
		_ = ln.Close()
		return "", err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx }, // ends WebSockets on shutdown
	}
	go s.Poll(ctx)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "api: serve:", err)
		}
	}()
	return base, nil
}
