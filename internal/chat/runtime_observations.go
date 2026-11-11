package chat

import (
	"github.com/example-user/commander/internal/store"
	"syscall"
)

// RuntimeObservations describes the processes this engine actually owns. A
// logical planning conversation has RunID zero and is never execution evidence.
// A detached connection is not a process-death observation.
func (e *Engine) RuntimeObservations() []store.NativeRuntimeObservation {
	e.mu.Lock()
	roles := make([]*chatRole, 0, len(e.chats))
	for _, c := range e.chats {
		roles = append(roles, c)
	}
	e.mu.Unlock()
	out := []store.NativeRuntimeObservation{}
	for _, c := range roles {
		c.mu.Lock()
		p := c.proc
		if p != nil && !p.detached && p.pid > 0 {
			select {
			case <-p.done:
				c.mu.Unlock()
				continue
			default:
			}
			if err := syscall.Kill(p.pid, 0); err == nil || err == syscall.EPERM {
				out = append(out, store.NativeRuntimeObservation{Role: c.role, RunID: p.spec.ExecutionRunID, SessionID: p.spec.SessionID, PID: p.pid})
			}
		}
		c.mu.Unlock()
	}
	return out
}
