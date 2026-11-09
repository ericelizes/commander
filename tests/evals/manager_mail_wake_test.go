package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wake"
	"github.com/example-user/commander/internal/wsmgr"
)

type wakeNativeRecorder struct{ started []string }

func (n *wakeNativeRecorder) Start(_ context.Context, role string) error {
	n.started = append(n.started, role)
	return nil
}
func (*wakeNativeRecorder) Fresh(string) bool                          { return false }
func (*wakeNativeRecorder) Send(context.Context, string, string) error { return nil }

type wakeInboxFixture struct{ at time.Time }

func (m wakeInboxFixture) FetchInbox(_ context.Context, _, name string, _ time.Time, _ agentmail.InboxOptions) ([]agentmail.Mail, error) {
	return []agentmail.Mail{{ID: name, From: "operator-fixture", Subject: "Completed result", SentAt: m.at}}, nil
}

type managerWakeFixture struct {
	manager   *wsmgr.Manager
	delivered []string
}

func (d *managerWakeFixture) Deliver(ctx context.Context, role, _ string) error {
	if err := d.manager.EnsureRole(ctx, role); err != nil {
		return err
	}
	d.delivered = append(d.delivered, role)
	return nil
}

func TestManagerMailWakeScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/manager-mail-wake.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Stale string `json:"stale_role"`
		Root  string `json:"ready_root"`
		Ready string `json:"ready_role"`
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dir := t.TempDir()
	pid, err := st.InsertProject(ctx, store.Project{Name: "p", Path: dir, Type: "code"})
	if err != nil {
		t.Fatal(err)
	}
	bc := &beads.MemClient{EdgeParentsOnly: true}
	bc.Seed(beads.Bead{ID: sc.Root, Title: "Ready workstream", Status: "open", Labels: []string{"kind:objective"}})
	native := &wakeNativeRecorder{}
	manager := &wsmgr.Manager{Store: st, Beads: bc, Reg: &agentmail.MockRegistrar{NextName: "ReadyFox"}, AmKey: "/fixture", Native: native}
	if _, err = manager.Prepare(ctx, wsmgr.Target{Project: store.Project{ID: pid, Name: "p", Path: dir}, BeadID: sc.Root}); err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertAmAgent(ctx, "/fixture", sc.Stale, "StaleFox"); err != nil {
		t.Fatal(err)
	}
	globalScans := 0
	manager.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) { return nil, nil }
	manager.Fresh = func(ctx context.Context) ([]tui.NumberedWorkstream, error) {
		globalScans++
		<-ctx.Done()
		return nil, ctx.Err()
	}
	delivery := &managerWakeFixture{manager: manager}
	w := wake.New(wakeInboxFixture{at: time.Now()}, st, delivery, "/fixture", time.Second)
	// The old route lookup spends this entire deadline on the stale role,
	// preventing the ready role from starting in the same poll.
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	w.Poll(bounded)
	if globalScans != 0 || len(native.started) != 1 || native.started[0] != sc.Ready || len(delivery.delivered) != 1 || delivery.delivered[0] != sc.Ready {
		t.Fatalf("stale mail blocked the ready Manager: scans=%d started=%v delivered=%v", globalScans, native.started, delivery.delivered)
	}
	if err = manager.EnsureRole(ctx, sc.Stale); !errors.Is(err, wsmgr.ErrInvalidRoute) {
		t.Fatalf("stale role was not rejected: %v", err)
	}
}
