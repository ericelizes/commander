package gates

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/store"
)

// CheckTimeout bounds one auto gate's command.
var CheckTimeout = 2 * time.Minute

// maxEvidence is the most output kept as a gate's evidence (the tail: that is where failures are).
const maxEvidence = 2000

// RunFn runs an auto gate's command in dir and returns its combined output.
type RunFn func(ctx context.Context, dir, command string) (string, error)

// RunShell is the real RunFn: sh -c with CheckTimeout.
func RunShell(ctx context.Context, dir, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	// Own process group: on timeout the whole tree dies, so a grandchild
	// holding the output pipe cannot keep Run blocked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", CheckTimeout)
	}
	return out.String(), err
}

// TrimOutput keeps the tail of a command's output as evidence.
func TrimOutput(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxEvidence {
		return "…" + string(r[len(r)-maxEvidence:])
	}
	return s
}

// EvalAuto runs an auto gate's command and turns the outcome into a gate status and evidence.
func EvalAuto(ctx context.Context, run RunFn, dir, command string) (status, evidence string) {
	output, runErr := run(ctx, dir, command)
	status, evidence = store.GatePassed, TrimOutput(output)
	if runErr != nil {
		status = store.GateFailed
		evidence = strings.TrimSpace(TrimOutput(output) + "\n" + runErr.Error())
	}
	if evidence == "" {
		evidence = "exit 0, no output"
	}
	return status, evidence
}
