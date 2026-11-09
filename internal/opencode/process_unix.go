//go:build unix

package opencode

import (
	"os/exec"
	"syscall"
)

// newProcessGroup returns a SysProcAttr that puts the child in its own
// process group so the whole subtree can be signaled together.
func newProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGTERM to the process group of cmd (or to cmd
// itself if process-group setup didn't take).
func killProcessGroup(cmd *exec.Cmd) error {
	return killProcessGroupSignal(cmd, syscall.SIGTERM)
}

func killProcessGroupSignal(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Negative pid = signal the process group.
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		// Fall back to single-process signaling if Setpgid wasn't honored.
		return cmd.Process.Signal(sig)
	}
	return nil
}
