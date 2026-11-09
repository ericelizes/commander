package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

// freshResolver is the harness resolver that can also mint a role a new
// session, as StoreResolver does, and reads the handoff like WithRuntime.
type freshResolver struct{ h *harness }

func (r freshResolver) Resolve(ctx context.Context, role string) (Spec, error) {
	spec, err := r.h.opts.Resolver.Resolve(ctx, role)
	return WithRuntime(spec, r.h.home), err
}

func (r freshResolver) NewSession(_ context.Context, role string) error {
	r.h.ids[role] = agentrt.NewClaudeSessionID()
	return nil
}

func TestFreshStartsANewSessionThatReadsItsPredecessorFirst(t *testing.T) {
	h := newHarness(t)
	old := h.role("manager-a")
	opts := h.opts
	opts.Resolver = freshResolver{h}
	h.e = New(opts)
	h.start("manager-a")
	sub := h.subscribe("manager-a")
	if err := h.e.Send(context.Background(), "manager-a", "remember 42"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
	// The role's conversation log, which the fresh session reads.
	os.MkdirAll(filepath.Join(h.home, "chat"), 0o700)
	os.WriteFile(filepath.Join(h.home, "chat", "manager-a.jsonl"), []byte(`{"kind":"text","text":"42"}`+"\n"), 0o600)

	st, err := h.e.Fresh(context.Background(), "manager-a")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.SessionID == old || st.SessionID != h.ids["manager-a"] {
		t.Fatalf("not a new session: %+v (old %s)", st, old)
	}
	var said string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, _ := os.ReadFile(FindTranscript(h.projects, st.SessionID))
		if said = string(raw); strings.Contains(said, "[fresh]") {
			break
		}
	}
	if !strings.Contains(said, "read your predecessor's conversation at "+filepath.Join(h.home, "chat", "manager-a.jsonl")) || !strings.Contains(said, "[fresh]") {
		t.Fatalf("the first turn does not read the predecessor: %s", said)
	}
	if _, err := os.Stat(HandoffFile(h.home, "manager-a")); !os.IsNotExist(err) {
		t.Errorf("the handoff marker must be used once: %v", err)
	}
}
