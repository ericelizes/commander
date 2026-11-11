package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanningHostRetirementStopsOnlyAttestedLogicalHost(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "detached"}[detached], func(t *testing.T) {
			h := newHarness(t)
			sid := h.role("manager-obsolete")
			status := h.start("manager-obsolete")
			if detached {
				h.e.Close()
				h.e = New(h.opts)
			}
			got, err := h.e.RetirePlanningHost(context.Background(), "manager-obsolete")
			if err != nil || got != sid {
				t.Fatal(got, err)
			}
			if alive(status.PID) {
				t.Fatal("obsolete planner remains alive")
			}
			m := readMeta(filepath.Join(h.home, "chat"), "manager-obsolete")
			if m.Running || m.SessionID != sid {
				t.Fatal("retirement lost retained session", m)
			}
			if _, err = os.Stat(filepath.Join(h.home, "chat", "manager-obsolete.jsonl")); err != nil {
				t.Fatal("lost conversation history", err)
			}
		})
	}
}

func TestPlanningRetirementRefusesUnattestedSessionAndExecution(t *testing.T) {
	h := newHarness(t)
	sid := h.role("manager-unknown")
	os.MkdirAll(filepath.Join(h.home, "chat"), 0700)
	h.procs.set(sid, []Proc{{PID: 123, Command: "claude -p --resume " + sid}})
	if err := writeMeta(filepath.Join(h.home, "chat"), "manager-unknown", meta{SessionID: sid, Running: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.RetirePlanningHost(context.Background(), "manager-unknown"); err == nil {
		t.Fatal("unattested session was retired")
	}
	h.role("operator-protected")
	h.opts.Resolver = ResolverFunc(func(context.Context, string) (Spec, error) { return Spec{}, nil })
	// A host's stamped run identity is independently checked, even if DB custody
	// were to disappear. The native lifecycle tests exercise actual stamped hosts.
	c := &chatRole{e: h.e, role: "operator-protected", proc: &process{spec: Spec{ExecutionRunID: 7}}}
	h.e.mu.Lock()
	h.e.chats["operator-protected"] = c
	h.e.mu.Unlock()
	if _, err := h.e.RetirePlanningHost(context.Background(), "operator-protected"); err == nil {
		t.Fatal("execution accepted for planning retirement")
	}
	h.e.mu.Lock()
	delete(h.e.chats, "operator-protected")
	h.e.mu.Unlock()
}
