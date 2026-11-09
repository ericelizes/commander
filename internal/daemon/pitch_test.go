package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

func TestPitchWatcherMailsTheManagerOncePerAsk(t *testing.T) {
	gate := livedata.NoPitch{Project: "p", BeadID: "p-ws.9", Title: "Greenlight merging the fix", Workstream: "WS", ManagerRole: "manager-p-ws", Gate: true}
	snap := livedata.Snapshot{NoPitch: []livedata.NoPitch{gate}}
	var sent []sentMail
	var bodies []string
	w := newPitchWatcher(t.TempDir(), func() (livedata.Snapshot, bool) { return snap, true }, func(_ context.Context, to, subject, body string) error {
		sent = append(sent, sentMail{to, subject})
		bodies = append(bodies, body)
		return nil
	})
	w.now = func() time.Time { return time.Unix(1_000, 0) }
	ctx := context.Background()
	w.pass(ctx)
	if len(sent) != 0 || len(w.Asked()) != 0 {
		t.Fatalf("mailed on first sight (the Manager often writes the card right away): %+v", sent)
	}
	w.pass(ctx)
	w.pass(ctx)
	if len(sent) != 1 || sent[0].to != "manager-p-ws" {
		t.Fatalf("want exactly one mail to the Manager, got %+v", sent)
	}
	for _, want := range []string{"p-ws.9", "internal-set-objective --project p --bead p-ws.9 --state needs-user --decision", `"approves": true`} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("mail lacks %q:\n%s", want, bodies[0])
		}
	}
	if got := w.Asked()["p:p-ws.9"]; got != 1_000 {
		t.Fatalf("asked at = %d, want the mail's time", got)
	}
	// Pitched, then pitchless again: forgotten, so mailed again.
	w.latest = func() (livedata.Snapshot, bool) { return livedata.Snapshot{}, true }
	w.pass(ctx)
	if len(w.Asked()) != 0 {
		t.Fatalf("a pitched ask is forgotten: %+v", w.Asked())
	}
	w.latest = func() (livedata.Snapshot, bool) { return snap, true }
	w.pass(ctx)
	w.pass(ctx)
	if len(sent) != 2 {
		t.Fatalf("want a second mail, got %d", len(sent))
	}
}

func TestPitchWatcherSkipsDegradedAndRetriesFailedMail(t *testing.T) {
	snap := livedata.Snapshot{NoPitch: []livedata.NoPitch{{Project: "p", BeadID: "p-ws.1", ManagerRole: "manager-p-ws"}}}
	fail := true
	var sent int
	w := newPitchWatcher(t.TempDir(), func() (livedata.Snapshot, bool) {
		s := snap
		s.Degraded = &livedata.SnapDegraded{Reason: "x"}
		return s, true
	}, func(context.Context, string, string, string) error {
		if fail {
			return errors.New("down")
		}
		sent++
		return nil
	})
	for range 3 {
		w.pass(context.Background())
	}
	w.latest = func() (livedata.Snapshot, bool) { return snap, true }
	w.pass(context.Background())
	w.pass(context.Background())
	if len(w.Asked()) != 0 {
		t.Fatal("a failed mail is not an ask")
	}
	fail = false
	w.pass(context.Background())
	w.pass(context.Background())
	if sent != 1 {
		t.Fatalf("want one mail once the server is back, got %d", sent)
	}
}

func TestPitchWatcherStaleCardGeneration(t *testing.T) {
	card := livedata.NeedsYou{Project: "p", BeadID: "p-1", Workstream: "W", Ask: "Ship it", Since: 1, SpecHash: "new", Basis: &store.Basis{Spec: "old", Gen: 1}}
	snap := livedata.Snapshot{NeedsYou: []livedata.NeedsYou{card}, Workstreams: []livedata.SnapWorkstream{{Project: "p", BeadID: "p-w", Title: "W", ManagerRole: "manager-p-w", Status: "open"}}}
	fail, sent := true, 0
	w := newPitchWatcher(t.TempDir(), func() (livedata.Snapshot, bool) { return snap, true }, func(_ context.Context, to, subject, body string) error {
		if fail {
			return errors.New("offline")
		}
		sent++
		if to != "manager-p-w" || !strings.Contains(subject, "[stale card] p-1") {
			t.Fatalf("wrong recipient/subject: %s %s", to, subject)
		}
		for _, verb := range []string{"--project p --bead p-1 --card-check still-right --reason", "--state needs-user --decision", "--status in-progress", "--push-reason", "--push-reset"} {
			if !strings.Contains(body, verb) {
				t.Errorf("missing %s", verb)
			}
		}
		return nil
	})
	w.now = func() time.Time { return time.Unix(100, 0) }
	ctx := context.Background()
	snap.Degraded = &livedata.SnapDegraded{Reason: "offline"}
	w.pass(ctx)
	snap.Degraded = nil
	w.pass(ctx)
	fail = false
	w.pass(ctx)
	w.pass(ctx)
	if sent != 1 {
		t.Fatalf("retry/once: %d", sent)
	}
	snap.NeedsYou[0].SpecHash = "old"
	w.pass(ctx)
	snap.NeedsYou[0].SpecHash = "new"
	w.pass(ctx)
	if sent != 1 {
		t.Fatalf("same generation mailed again: %d", sent)
	}
	snap.NeedsYou[0].Basis = &store.Basis{Spec: "old", Gen: 2}
	w.pass(ctx)
	if sent != 2 {
		t.Fatalf("new generation not mailed: %d", sent)
	}
	snap.NeedsYou = nil
	w.pass(ctx)
	if sent != 2 {
		t.Fatal("withdrawn card mailed")
	}
}

func TestPitchWatcherStaleRestartPruneAndMultiple(t *testing.T) {
	home := t.TempDir()
	card := livedata.NeedsYou{Project: "p", BeadID: "p-1", Workstream: "W", Ask: "Q", PushCount: 2, Basis: &store.Basis{Gen: 1, Raised: 10}}
	other := card
	other.BeadID = "p-2"
	snap := livedata.Snapshot{NeedsYou: []livedata.NeedsYou{card, other}, Workstreams: []livedata.SnapWorkstream{{Project: "p", Title: "W", ManagerRole: "manager-p-w", Status: "open"}}}
	sent := 0
	latest := func() (livedata.Snapshot, bool) { return snap, true }
	mail := func(_ context.Context, _, subject, _ string) error {
		if !strings.Contains(subject, "Pushed back 2 times") {
			t.Fatalf("not push stale: %s", subject)
		}
		sent++
		return nil
	}
	w := newPitchWatcher(home, latest, mail)
	w.pass(context.Background())
	if sent != 2 {
		t.Fatalf("two cards: %d", sent)
	}
	w = newPitchWatcher(home, latest, mail)
	w.pass(context.Background())
	if sent != 2 {
		t.Fatalf("restart remailed: %d", sent)
	}
	snap.NeedsYou[0].Basis = &store.Basis{Gen: 1, Raised: 20}
	w.pass(context.Background())
	if sent != 3 {
		t.Fatalf("rebaseline not mailed: %d", sent)
	}
	snap.NeedsYou = nil
	w.pass(context.Background())
	w = newPitchWatcher(home, latest, mail)
	if len(w.stale) != 0 {
		t.Fatalf("withdrawn keys persisted: %v", w.stale)
	}
}

func TestPitchWatcherStaleMissingManagerLogged(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	snap := livedata.Snapshot{NeedsYou: []livedata.NeedsYou{{Project: "p", BeadID: "p-1", Ask: "Q", PushCount: 2}}}
	w := newPitchWatcher(t.TempDir(), func() (livedata.Snapshot, bool) { return snap, true }, func(context.Context, string, string, string) error { t.Fatal("mailed without role"); return nil })
	w.pass(context.Background())
	if !strings.Contains(logs.String(), "no Manager role for p:p-1") {
		t.Fatalf("missing diagnostic: %s", logs.String())
	}
}
