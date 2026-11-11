package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// FreshMessage starts a fresh session's first turn: the handoff note before it
// has the agent read its predecessor's conversation.
const FreshMessage = "[fresh] You are a fresh session of this role with today's prompt. Read your predecessor's conversation, then carry on with what was in flight; say in one line what you picked up."

// sessionMinter gives a role a brand-new session id (StoreResolver).
type sessionMinter interface {
	NewSession(ctx context.Context, role string) error
}

// Fresh restarts role as a brand-new session (new context, today's prompt)
// that first reads its predecessor's conversation log. A turn in flight (or a
// pending approval) refuses with ErrBusy: the restart would cut it.
func (e *Engine) Fresh(ctx context.Context, role string) (Status, error) {
	if e.opts.Resolver != nil {
		spec, err := e.opts.Resolver.Resolve(ctx, role)
		if err != nil {
			return Status{}, err
		}
		if spec.ExecutionRunID > 0 {
			return Status{}, errors.New("execution session is bound to its current run; resume its validated checkpoint instead")
		}
	}

	minter, ok := e.opts.Resolver.(sessionMinter)
	if !ok {
		return Status{}, errors.New("chat: this engine cannot start fresh sessions")
	}
	if _, err := e.idleProcess(role, "to a fresh session"); err != nil {
		return Status{}, err
	}
	if err := e.Stop(ctx, role); err != nil && !errors.Is(err, ErrNotRunning) {
		return Status{}, err
	}
	if err := minter.NewSession(ctx, role); err != nil {
		return Status{}, err
	}
	// The new session reads the role's log first: Codex through a fresh thread
	// (no thread file), Claude through the handoff marker.
	if err := os.Remove(CodexThreadFile(e.opts.Home, role)); err != nil && !os.IsNotExist(err) {
		return Status{}, err
	}
	if ReadRuntime(e.opts.Home, role) == RuntimeClaude && fileExists(filepath.Join(e.opts.Home, "chat", role+".jsonl")) {
		if err := os.WriteFile(HandoffFile(e.opts.Home, role), nil, 0o600); err != nil {
			return Status{}, err
		}
	}
	if _, err := e.Start(ctx, role, StartOptions{}); err != nil {
		return Status{}, err
	}
	if err := e.send(role, FreshMessage, "mail"); err != nil {
		return Status{}, err
	}
	return e.Status(role)
}
