package daemon

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

func TestManagerHandoffReminderRetainsCustodyAcrossRestart(t *testing.T) {
	for _, ref := range []string{beads.ManagerDispatchWait, beads.ManagerStartWait, beads.ManagerCloseWait} {
		t.Run(ref, func(t *testing.T) {
			ctx := context.Background()
			wait := beads.Wait{Kind: beads.WaitUntil, Since: epoch, Until: epoch.Add(time.Minute), Ref: ref}
			m := &beads.MemClient{}
			m.Seed(beads.Bead{ID: "p-ws.1", Status: "open", Labels: []string{"kind:objective", "owner:manager-p-ws", wait.Label()}})
			var sent []sentMail
			makeWatcher := func() *waitWatcher {
				w := watcher(&beads.MockClient{}, nil, &sent, epoch.Add(3*time.Minute))
				w.beads = m
				return w
			}
			w := makeWatcher()
			originalMail := w.mail
			w.mail = func(context.Context, string, string, string) error { return fmt.Errorf("mail offline") }
			w.pollUntil(ctx)
			b, _ := m.Show(ctx, "/p", "p-ws.1")
			if !slices.Contains(b.Labels, wait.Label()) {
				t.Fatal("mail failure lost custody")
			}
			w.mail = originalMail
			w.pollUntil(ctx)
			makeWatcher().pollUntil(ctx)
			if len(sent) != 1 {
				t.Fatalf("restart remailed accepted reminder: %+v", sent)
			}
			b, _ = m.Show(ctx, "/p", "p-ws.1")
			if !slices.Contains(b.Labels, wait.Label()) {
				t.Fatal("deadline cleared custody before an agent accepted it")
			}
		})
	}
}

// A "Manager to start" receipt on a step a gate was linked in front of after
// it was made is no move of the Manager's: at its deadline cmdrd clears it and
// mails nobody. Unblocked, the same receipt still reminds.
func TestStartReceiptBehindALaterGateIsClearedNotMailed(t *testing.T) {
	ctx := context.Background()
	wait := beads.Wait{Kind: beads.WaitUntil, Since: epoch, Until: epoch.Add(time.Minute), Ref: beads.ManagerStartWait}
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "p-ws.1", Status: "open", Labels: []string{"kind:objective", wait.Label()}})
	m.Seed(beads.Bead{ID: "p-ws.2", Status: "open", Labels: []string{"kind:objective", "gate", "approver:user"}})
	if err := m.DependOn(ctx, "/p", "p-ws.1", "p-ws.2"); err != nil {
		t.Fatal(err)
	}
	var sent []sentMail
	w := watcher(&beads.MockClient{}, nil, &sent, epoch.Add(3*time.Minute))
	w.beads = m
	w.pollUntil(ctx)
	if len(sent) != 0 {
		t.Fatalf("mailed the Manager about a step behind an open gate: %+v", sent)
	}
	b, _ := m.Show(ctx, "/p", "p-ws.1")
	if slices.Contains(b.Labels, wait.Label()) {
		t.Fatalf("receipt left on a blocked step: %v", b.Labels)
	}

	// The gate passed: a receipt on the now-ready step reminds as before.
	if err := m.Close(ctx, "/p", "p-ws.2"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetLabel(ctx, "/p", "p-ws.1", wait.Label(), true); err != nil {
		t.Fatal(err)
	}
	w.pollUntil(ctx)
	if len(sent) != 1 {
		t.Fatalf("ready step's receipt did not remind: %+v", sent)
	}
}
