package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HandoffFile marks a role just switched from Codex back to Claude: its next
// Claude message first reads the conversation log (the Claude session itself
// has not seen the Codex turns).
func HandoffFile(home, role string) string { return filepath.Join(home, "chat", role+".handoff") }

// ValidRuntime reports whether rt names an agent CLI a chat can run on.
func ValidRuntime(rt string) bool { return rt == RuntimeClaude || rt == RuntimeCodex }

// WriteRuntime records role's runtime. A change of runtime also sets up the
// handoff: to Codex, a fresh thread (it reads the whole log, Claude turns
// included); to Claude, a handoff marker for the first message.
func WriteRuntime(home, role, rt string) error {
	if err := WriteEffectiveRuntime(home, role, rt); err != nil {
		return err
	}
	if err := os.WriteFile(RuntimePinFile(home, role), []byte(rt+"\n"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(RuntimeAutoFile(home, role))
	return nil
}

// RuntimePinFile stores explicit intent separately from the effective runtime.
func RuntimePinFile(home, role string) string {
	return filepath.Join(home, "chat", role+".runtime-pin")
}

// ReadRuntimePin returns only supported, explicitly recorded intent.
func ReadRuntimePin(home, role string) string {
	b, err := os.ReadFile(RuntimePinFile(home, role))
	rt := strings.TrimSpace(string(b))
	if err != nil || !ValidRuntime(rt) {
		return ""
	}
	return rt
}

// WriteEffectiveRuntime records the selected run and its handoff without changing intent.
func WriteEffectiveRuntime(home, role, rt string) error {
	if !ValidRole(role) {
		return fmt.Errorf("chat: invalid role %q", role)
	}
	if !ValidRuntime(rt) {
		return fmt.Errorf("chat: unknown runtime %q (claude or codex)", rt)
	}
	prev := ReadRuntime(home, role)
	if err := os.MkdirAll(filepath.Join(home, "chat"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(RuntimeFile(home, role), []byte(rt+"\n"), 0o600); err != nil {
		return err
	}
	if prev == rt {
		return nil
	}
	if rt == RuntimeCodex {
		_ = os.Remove(HandoffFile(home, role))
		if err := os.Remove(CodexThreadFile(home, role)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if !fileExists(filepath.Join(home, "chat", role+".jsonl")) {
		return nil
	}
	return os.WriteFile(HandoffFile(home, role), nil, 0o600)
}

// SetRuntime switches role to rt and, when it is running, restarts it on the
// new CLI. A stopped role only records the choice for its next start. A turn in
// flight (or a pending approval) refuses with ErrBusy: the restart would cut it.
func (e *Engine) SetRuntime(ctx context.Context, role, rt string, o SwitchOptions) (Status, error) {
	p, err := e.idleProcess(role, "runtime")
	if err != nil {
		return Status{}, err
	}
	if err := WriteRuntime(e.opts.Home, role, rt); err != nil {
		return Status{}, err
	}
	if p == nil || p.spec.Runtime == rt || (p.spec.Runtime == "" && rt == RuntimeClaude) {
		return e.Status(role)
	}
	return e.restart(ctx, role, o)
}
