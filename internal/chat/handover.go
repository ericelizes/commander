package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// cockpitSession is the tmux session the cockpit lives in (layout.CommanderSessionName).
const cockpitSession = "commander"

// HandoverOptions guards Handover and PopOut.
type HandoverOptions struct {
	// Confirm must be true: both stop a running Claude.
	Confirm bool
	// Force hands over even when the TUI looks mid-turn (that turn is lost).
	Force bool
	// Wait bounds how long to wait for the old owner to exit; default 15s.
	Wait time.Duration
}

// PlaceholderMarker starts the command of a chat pane whose chat was handed
// over to the native engine. layout.SoftRestart leaves such panes alone: it
// must never restart the TUI claude on a session the engine owns.
const PlaceholderMarker = "CMDR_NATIVE_CHAT="

// placeholderCmd keeps the role's chat pane (and its window layout) alive
// while the chat runs natively.
func placeholderCmd(role string) string {
	msg := "This chat (" + role + ") now runs natively in the Commander app.\\nPop it out from the app to get the terminal chat back.\\n"
	return PlaceholderMarker + role + " sh -c 'printf \"" + msg + "\"; exec tail -f /dev/null'"
}

// chatPane finds role's chat pane in the cockpit: the @cmdr_chat pane whose
// start command names the agent (-n <agent>), else (workstream Managers) the
// chat pane of the window tagged with the bead, else (commander) window 0's.
func (e *Engine) chatPane(ctx context.Context, spec Spec) (pane, startCmd string, err error) {
	out, stderr, err := e.opts.Tmux.Run(ctx, "list-panes", "-s", "-t", cockpitSession, "-F",
		"#{pane_id}|#{@cmdr_chat}|#{@cmdr_ws}|#{window_name}|#{pane_start_command}")
	if err != nil {
		return "", "", fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var byWS, byName string
	var byWSCmd, byNameCmd string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "|", 5)
		if len(f) < 5 || f[1] != "1" {
			continue
		}
		if strings.Contains(" "+f[4]+" ", " -n "+spec.Agent+" ") {
			return f[0], f[4], nil
		}
		if bead, ok := strings.CutPrefix(spec.Role, "manager-"); ok && f[2] == bead && byWS == "" {
			byWS, byWSCmd = f[0], f[4]
		}
		if spec.Role == "commander" && f[2] == "" && f[3] == "commander" && byName == "" {
			byName, byNameCmd = f[0], f[4]
		}
	}
	if byWS != "" {
		return byWS, byWSCmd, nil
	}
	if byName != "" {
		return byName, byNameCmd, nil
	}
	return "", "", fmt.Errorf("chat: no cockpit chat pane for %s", spec.Role)
}

// Handover moves role from its tmux TUI chat to a native (headless) process on
// the same session id: it checks the TUI is between turns, replaces the pane's
// claude with a placeholder (tmux respawn-pane -k, the same way a soft restart
// stops chats), waits for that claude to exit, then Starts headless with
// --resume. Requires AllowHandover and Confirm.
func (e *Engine) Handover(ctx context.Context, role string, o HandoverOptions) (Status, error) {
	if !e.opts.AllowHandover || e.opts.Tmux == nil || !o.Confirm {
		return Status{}, ErrHandover
	}
	if e.Running(role) {
		return e.Status(role)
	}
	spec, err := e.opts.Resolver.Resolve(ctx, role)
	if err != nil {
		return Status{}, err
	}
	pane, _, err := e.chatPane(ctx, spec)
	if err != nil {
		return Status{}, err
	}
	if !o.Force {
		text, _, err := e.opts.Tmux.Run(ctx, "capture-pane", "-p", "-t", pane, "-S", "-30")
		if err != nil {
			return Status{}, fmt.Errorf("tmux capture-pane: %w", err)
		}
		if st, _ := livedata.ClassifyPane(string(text)); st == store.AgentWorking {
			return Status{}, errors.New("chat: the terminal chat is mid-turn; wait for it to finish or pass force")
		}
	}
	if _, stderr, err := e.opts.Tmux.Run(ctx, "respawn-pane", "-k", "-t", pane, placeholderCmd(role)); err != nil {
		return Status{}, fmt.Errorf("tmux respawn-pane: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	if err := e.waitReleased(ctx, spec.SessionID, o.Wait); err != nil {
		return Status{}, err
	}
	return e.Start(ctx, role, StartOptions{})
}

// waitReleased polls until no process outside the engine has the session open.
func (e *Engine) waitReleased(ctx context.Context, id string, wait time.Duration) error {
	if wait <= 0 {
		wait = 15 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		procs, err := e.opts.Procs(ctx, id)
		if err != nil {
			return err
		}
		owned := foreignOwner(id, procs, e.ourPIDs())
		if owned == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("chat: old owner did not exit: %w", owned)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// PopOut is the reverse of Handover: it stops the native process (waiting for
// it to exit and release the lock) and restarts the interactive claude in the
// role's cockpit pane on the same session id.
func (e *Engine) PopOut(ctx context.Context, role string, o HandoverOptions) error {
	if !e.opts.AllowHandover || e.opts.Tmux == nil || !o.Confirm {
		return ErrHandover
	}
	spec, err := e.opts.Resolver.Resolve(ctx, role)
	if err != nil {
		return err
	}
	pane, _, err := e.chatPane(ctx, spec)
	if err != nil {
		return err
	}
	if c, _, err := e.live(role); err == nil {
		c.mu.Lock()
		busy := c.tracker.turn
		c.mu.Unlock()
		if busy && !o.Force {
			return errors.New("chat: a turn is in progress; interrupt it or pass force")
		}
		if err := e.Stop(ctx, role); err != nil {
			return err
		}
	}
	if err := e.waitReleased(ctx, spec.SessionID, o.Wait); err != nil {
		return err
	}
	// A pane runs on default, whichever account the engine last ran this session on.
	if _, err := claudeacct.CarryTranscript(e.opts.Home, e.opts.ProjectsDir, spec.SessionID, claudeacct.Default, claudeacct.TmuxConfigDir(ctx, e.opts.Tmux.Run)); err != nil {
		return err
	}
	cmd := agentrt.ClaudeInteractiveCmd(e.opts.Binary, spec.Agent, spec.Model, spec.SessionID)
	if _, stderr, err := e.opts.Tmux.Run(ctx, "respawn-pane", "-k", "-t", pane, cmd); err != nil {
		return fmt.Errorf("tmux respawn-pane: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	c, _ := e.chat(role)
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "popped_out", Text: "The chat moved back to the terminal (" + pane + ")."})
	return nil
}
