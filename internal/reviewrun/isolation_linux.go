//go:build linux

package reviewrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// reviewIsolation owns a fresh cgroup, so setsid cannot escape review cleanup.
func reviewIsolation(ctx context.Context, program string, args []string) (string, []string, func() error, error) {
	runner, err := exec.LookPath("systemd-run")
	if err != nil {
		return "", nil, nil, fmt.Errorf("bounded Linux review execution requires systemd-run --user: %w", err)
	}
	control, err := exec.LookPath("systemctl")
	if err != nil {
		return "", nil, nil, err
	}
	deadline, _ := ctx.Deadline()
	seconds := int(time.Until(deadline).Seconds()) + 5
	if seconds < 5 {
		seconds = 5
	}
	unit := fmt.Sprintf("commander-review-%d-%d.scope", os.Getpid(), time.Now().UnixNano())
	wrapped := []string{"--user", "--scope", "--quiet", "--unit=" + unit, "--property=TasksMax=256", "--property=RuntimeMaxSec=" + strconv.Itoa(seconds), "--", program}
	wrapped = append(wrapped, args...)
	cleanup := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// --scope leaves detached children in this exact unit even after the
		// foreground command exits. Stop that unit, never the agent's service.
		out, err := exec.CommandContext(stopCtx, control, "--user", "stop", unit).CombinedOutput()
		if err != nil {
			// A fully empty transient scope is already collected by systemd.
			probe := exec.CommandContext(stopCtx, control, "--user", "show", unit, "-p", "LoadState", "--value")
			if b, e := probe.Output(); e == nil && string(b) == "not-found\n" {
				return nil
			}
			return fmt.Errorf("review scope cleanup: %w: %s", err, out)
		}
		return nil
	}
	return runner, wrapped, cleanup, nil
}
