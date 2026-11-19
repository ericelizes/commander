package claudeacct

import (
	"os/exec"
	"syscall"
	"time"
)

// StopGrace is how long a claude child has to exit after SIGTERM before it is
// killed.
const StopGrace = 10 * time.Second

// Graceful makes cmd (from exec.CommandContext) end with SIGTERM when its
// context ends, and SIGKILL only StopGrace later. Claude Code rotates its OAuth
// refresh token on every refresh and, on SIGTERM, saves a refresh it has in
// flight before it exits. A SIGKILL between the server's rotation and that save
// loses the new token, and the account is signed out at a later refresh
// ("OAuth session expired and could not be refreshed"). Codex rotates its
// tokens the same way.
func Graceful(cmd *exec.Cmd) *exec.Cmd {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = StopGrace
	return cmd
}
