package opencode

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Serve starts `opencode serve --port <port>` as a background process and
// returns a Server handle. The caller MUST call Shutdown to release resources;
// the process is started in its own process group so Shutdown can SIGTERM the
// whole group cleanly.
//
// Serve does not block waiting for the port to bind — that's a higher-level
// readiness concern (callers can poll the addr or use --attach which retries).
// If readiness is ever required here, add a port-poll loop on Addr().
func (c *execClient) Serve(ctx context.Context, port int) (Server, error) {
	args := []string{"serve", "--port", strconv.Itoa(port)}

	// We deliberately do NOT pass ctx into the command: cancelling ctx after
	// Serve returns would silently kill the server. Lifecycle is tied to
	// Shutdown() instead.
	cmd := exec.Command(c.binary, args...)
	cmd.SysProcAttr = newProcessGroup()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode serve: start: %w", err)
	}

	srv := &serveServer{
		addr: fmt.Sprintf("http://localhost:%d", port),
		cmd:  cmd,
		done: make(chan error, 1),
	}
	go func() {
		srv.done <- cmd.Wait()
	}()

	return srv, nil
}

type serveServer struct {
	addr string
	cmd  *exec.Cmd
	done chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

func (s *serveServer) Addr() string { return s.addr }

func (s *serveServer) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		// SIGTERM the whole process group. opencode-serve may spawn helpers;
		// signaling the group catches them too.
		if err := killProcessGroup(s.cmd); err != nil {
			s.shutdownErr = fmt.Errorf("opencode serve: signal: %w", err)
			return
		}

		select {
		case <-s.done:
			// Process exited; ignore non-zero exit (SIGTERM produces one).
		case <-ctx.Done():
			// Hard kill on timeout.
			_ = killProcessGroupSignal(s.cmd, syscall.SIGKILL)
			s.shutdownErr = ctx.Err()
		case <-time.After(5 * time.Second):
			_ = killProcessGroupSignal(s.cmd, syscall.SIGKILL)
		}
	})
	return s.shutdownErr
}
