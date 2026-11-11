package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// RetirePlanningHost is called only while Store fences ownership/run writes.
// It refuses executions and unattested pre-host processes. History is retained.
func (e *Engine) RetirePlanningHost(ctx context.Context, role string) (string, error) {
	if !ValidRole(role) {
		return "", errors.New("invalid planning runtime identity")
	}
	e.mu.Lock()
	c := e.chats[role]
	e.mu.Unlock()
	if c != nil {
		c.mu.Lock()
		p := c.proc
		if p != nil && p.spec.ExecutionRunID > 0 {
			c.mu.Unlock()
			return "", errors.New("cannot retire an execution host")
		}
		c.mu.Unlock()
	}
	hc, hello, err := dialHost(hostSock(e.opts.Home, role))
	if err != nil {
		if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED) {
			return "", err
		}
		m := readMeta(e.logDir(), role)
		if c != nil {
			c.mu.Lock()
			live := c.proc != nil
			c.mu.Unlock()
			if live {
				return "", errors.New("attached runtime has no attested host")
			}
		}
		if m.ExecutionRunID > 0 {
			return "", errors.New("execution metadata requires custody reconciliation")
		}
		if m.SessionID != "" {
			procs, scanErr := e.opts.Procs(ctx, m.SessionID)
			if scanErr != nil {
				return "", scanErr
			}
			if len(procs) > 0 {
				return "", errors.New("session still has an unattested process")
			}
		}
		m.Running = false
		if err = os.MkdirAll(e.logDir(), 0700); err != nil {
			return "", err
		}
		if err = writeMeta(e.logDir(), role, m); err != nil {
			return "", err
		}
		if m.SessionID != "" {
			return m.SessionID, nil
		}
		return "absent", nil
	}
	defer hc.close()
	if hello.Info.Role != role || hello.Info.SessionID == "" || hello.Info.ExecutionRunID != 0 {
		return "", errors.New("host does not attest an obsolete logical planning session")
	}
	if c != nil {
		c.mu.Lock()
		p := c.proc
		matching := p != nil && p.spec.ExecutionRunID == 0 && p.spec.SessionID == hello.Info.SessionID
		c.mu.Unlock()
		if p != nil && !matching {
			return "", errors.New("attached runtime identity changed")
		}
		if matching {
			if err = e.Stop(ctx, role); err != nil {
				return "", err
			}
			return hello.Info.SessionID, nil
		}
	}
	m := readMeta(e.logDir(), role)
	if m.SessionID != "" && m.SessionID != hello.Info.SessionID {
		return "", errors.New("retained planning session differs from live host")
	}
	if err = hc.send(hostMsg{Op: "quit"}); err != nil {
		return "", err
	}
	if err = e.waitGone(ctx, hello.PID); err != nil {
		return "", fmt.Errorf("retire planning host: %w", err)
	}
	m.SessionID = hello.Info.SessionID
	m.Running = false
	if err = writeMeta(e.logDir(), role, m); err != nil {
		return "", err
	}
	return hello.Info.SessionID, nil
}
