package layout

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EnsureManagerChatPane opens the project's manager window if it is missing
// (without selecting it or attaching) and returns the chat pane id.
func (d *Director) EnsureManagerChatPane(ctx context.Context, projectName, projectPath string) (string, error) {
	if err := d.LaunchLead(ctx, projectName, projectPath, false); err != nil {
		return "", err
	}
	windowName, ok, err := d.projectManagerWindow(ctx, projectName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("manager window for %q not found after launch", projectName)
	}
	return d.chatPane(ctx, windowName)
}

// EnsureOperatorChatPane is EnsureManagerChatPane for an operator's `op-<flat>`
// window.
func (d *Director) EnsureOperatorChatPane(ctx context.Context, taskBeadID, projectName, projectPath string) (string, error) {
	if err := d.LaunchOperator(ctx, taskBeadID, projectName, projectPath, false); err != nil {
		return "", err
	}
	return d.chatPane(ctx, "op-"+flattenName(taskBeadID))
}

// CommanderChatPane returns the chat pane of the root Commander window. It does
// not create the window: if the cockpit isn't running there is nobody to wake.
func (d *Director) CommanderChatPane(ctx context.Context) (string, error) {
	windowName, ok, err := d.rootWindow(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("commander window is not open")
	}
	return d.chatPane(ctx, windowName)
}

// OperatorWindowOpen reports whether the operator's window already exists.
func (d *Director) OperatorWindowOpen(ctx context.Context, taskBeadID string) (bool, error) {
	return d.WindowExists(ctx, CommanderSessionName, "op-"+flattenName(taskBeadID))
}

// ChatPaneOption is the tmux pane option set to "1" on a window's chat pane.
const ChatPaneOption = "@cmdr_chat"

// chatPaneOf is chatPane for a window addressed by id instead of name.
func (d *Director) chatPaneOf(ctx context.Context, windowTarget string) (string, error) {
	return d.chatPaneTarget(ctx, windowTarget)
}

// chatPane returns the window's tagged chat pane. Windows created before the
// tag existed had the chat first, so fall back to the first pane.
func (d *Director) chatPane(ctx context.Context, windowName string) (string, error) {
	return d.chatPaneTarget(ctx, CommanderSessionName+":"+windowName)
}

func (d *Director) chatPaneTarget(ctx context.Context, windowName string) (string, error) {
	stdout, stderr, err := d.r.Run(ctx, "list-panes", "-t", windowName, "-F", "#{pane_id} #{"+ChatPaneOption+"}")
	if err != nil {
		return "", fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	first := ""
	for _, line := range nonEmptyLines(string(stdout)) {
		id, tag, _ := strings.Cut(strings.TrimSpace(line), " ")
		if first == "" {
			first = id
		}
		if tag == "1" {
			return id, nil
		}
	}
	if first == "" {
		return "", fmt.Errorf("window %q has no panes", windowName)
	}
	return first, nil
}

// SendToPane types text into paneID literally, then presses Enter. The two
// steps are separate (with a short gap) so chat TUIs don't swallow the Enter
// as part of a paste.
func (d *Director) SendToPane(ctx context.Context, paneID, text string) error {
	if _, stderr, err := d.r.Run(ctx, "send-keys", "-t", paneID, "-l", text); err != nil {
		return fmt.Errorf("tmux send-keys: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	time.Sleep(300 * time.Millisecond)
	if _, stderr, err := d.r.Run(ctx, "send-keys", "-t", paneID, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// FlattenName exposes the window/session-file name flattening so other
// packages can map a role like operator-<flat> back to its window.
func FlattenName(s string) string { return flattenName(s) }

// ClearInput empties the chat's input line (Ctrl-U).
func (d *Director) ClearInput(ctx context.Context, paneID string) error {
	if _, stderr, err := d.r.Run(ctx, "send-keys", "-t", paneID, "C-u"); err != nil {
		return fmt.Errorf("tmux send-keys C-u: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// TypeInto types text into the pane without pressing Enter.
func (d *Director) TypeInto(ctx context.Context, paneID, text string) error {
	if _, stderr, err := d.r.Run(ctx, "send-keys", "-t", paneID, "-l", text); err != nil {
		return fmt.Errorf("tmux send-keys: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}
