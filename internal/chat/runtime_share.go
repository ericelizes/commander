package chat

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
)

// RuntimeAutoFile marks role's runtime file as written by a share (a new
// Manager's draw, `cmdr runtime rebalance`) rather than by a person.
func RuntimeAutoFile(home, role string) string {
	return filepath.Join(home, "chat", role+".runtime-auto")
}

// RuntimePinned reports whether a person chose role's runtime: its runtime
// file exists without the auto marker. Every explicit switch (`cmdr runtime
// <role> ...`, the chat menu) goes through WriteRuntime, which drops the
// marker; Managers switched by hand before shares existed have none either.
// A pinned Manager is never rebalanced. Its Operators do not inherit its runtime.
func RuntimePinned(home, role string) bool {
	if strings.HasPrefix(role, "operator-") {
		return ReadRuntimePin(home, role) != ""
	}
	return fileExists(RuntimeFile(home, role)) && !fileExists(RuntimeAutoFile(home, role))
}

// WriteAutoRuntime is WriteRuntime for a share's choice: role stays unpinned.
func WriteAutoRuntime(home, role, rt string) error {
	if err := WriteEffectiveRuntime(home, role, rt); err != nil {
		return err
	}
	if err := os.Remove(RuntimePinFile(home, role)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return MarkRuntimeAuto(home, role)
}

// MarkRuntimeAuto marks role's current runtime as a share's choice.
func MarkRuntimeAuto(home, role string) error {
	return os.WriteFile(RuntimeAutoFile(home, role), nil, 0o600)
}

// dropDrawnCodex moves a Manager a share drew onto Codex back to Claude when no
// share asks for Codex any more (unset now means 0), at its next start. A
// person's choice (no auto marker) stays. Only a start that spawns calls it: a
// surviving Codex host is adopted as Codex, its handoff untouched. Reports
// whether it switched the role.
func dropDrawnCodex(home, role string) bool {
	if !fileExists(RuntimeAutoFile(home, role)) || ReadRuntime(home, role) != RuntimeCodex || agentrt.CodexManagerShare() > 0 {
		return false
	}
	return WriteAutoRuntime(home, role, RuntimeClaude) == nil
}

// CodexByShare is the deterministic draw behind the runtime shares: id (a task
// bead, a workstream) hashes to a point in [0,1), so the same id always lands
// on the same side and about share of all ids go to Codex.
func CodexByShare(id string, share float64) bool {
	if share <= 0 || id == "" {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return float64(h.Sum32())/(1<<32) < share
}

// AssignNewManagerRuntime picks a never-started Manager's runtime from the
// Manager share (deterministic per workstream id) unless one is already set.
// Codex only while it runs hooks (hooksReady).
func AssignNewManagerRuntime(home, role, workstreamID string, share float64, hooksReady func() bool) error {
	if fileExists(RuntimeFile(home, role)) {
		return nil
	}
	rt := RuntimeClaude
	if CodexByShare(workstreamID, share) && hooksReady() {
		rt = RuntimeCodex
	}
	return WriteAutoRuntime(home, role, rt)
}
