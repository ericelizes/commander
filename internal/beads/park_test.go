package beads

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func parkMem(b Bead) *MemClient {
	m := &MemClient{}
	m.Seed(b)
	return m
}

func TestParkUnparkRestoresStatusAndKeepsMarkers(t *testing.T) {
	ctx := context.Background()
	m := parkMem(Bead{ID: "w.1", Status: "in_progress", Labels: []string{"gate", "needs-user", "approver:user"}})
	until := time.Unix(2_000_000, 0)
	if err := Park(ctx, m, "d", "w.1", "manager-w", "CI red", until); err != nil {
		t.Fatal(err)
	}
	b, _ := m.Show(ctx, "d", "w.1")
	for _, l := range []string{ParkedLabel, "parked-from:in_progress", "parked-until:2000000", "needs-user", "gate"} {
		if !slices.Contains(b.Labels, l) {
			t.Errorf("missing %q in %v", l, b.Labels)
		}
	}
	if b.Status != StatusDeferred || ParkedUntil(b.Labels) != 2_000_000 {
		t.Fatalf("parked: %+v", b)
	}
	cs, _ := m.Comments(ctx, "d", "w.1")
	if len(cs) != 1 || !strings.Contains(cs[0].Text, "parked by manager-w until 1970-") || !strings.Contains(cs[0].Text, "CI red") {
		t.Fatalf("comment: %+v", cs)
	}
	if ok, err := Unpark(ctx, m, "d", "w.1", "manager-w"); err != nil || !ok {
		t.Fatalf("unpark: %v %v", ok, err)
	}
	b, _ = m.Show(ctx, "d", "w.1")
	if b.Status != "in_progress" || slices.Contains(b.Labels, ParkedLabel) || ParkedUntil(b.Labels) != 0 || !slices.Contains(b.Labels, "needs-user") {
		t.Fatalf("unparked: %+v", b)
	}
	for _, l := range b.Labels {
		if strings.HasPrefix(l, ParkedFromPrefix) {
			t.Errorf("left %q", l)
		}
	}
}

func TestParkIdempotent(t *testing.T) {
	ctx := context.Background()
	m := parkMem(Bead{ID: "w.1", Status: "open", Labels: []string{"gate", "owner:user"}})
	until := time.Unix(2_000_000, 0)
	for i := 0; i < 2; i++ {
		if err := Park(ctx, m, "d", "w.1", "commander", "", until); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := m.Show(ctx, "d", "w.1")
	froms := 0
	for _, l := range b.Labels {
		if strings.HasPrefix(l, ParkedFromPrefix) {
			froms++
			if l != "parked-from:open" {
				t.Errorf("second park rewrote the previous status: %q", l)
			}
		}
	}
	if froms != 1 {
		t.Fatalf("labels %v", b.Labels)
	}
	if cs, _ := m.Comments(ctx, "d", "w.1"); len(cs) != 1 {
		t.Fatalf("a repeat park must not comment again: %+v", cs)
	}
	if ok, _ := Unpark(ctx, m, "d", "w.1", "commander"); !ok {
		t.Fatal("first unpark changed nothing")
	}
	if ok, err := Unpark(ctx, m, "d", "w.1", "commander"); ok || err != nil {
		t.Fatalf("second unpark: %v %v", ok, err)
	}
	if b, _ = m.Show(ctx, "d", "w.1"); b.Status != "open" {
		t.Fatalf("status %s", b.Status)
	}
}

func TestParkClosedRefused(t *testing.T) {
	m := parkMem(Bead{ID: "w.1", Status: "closed"})
	if err := Park(context.Background(), m, "d", "w.1", "commander", "", time.Time{}); err == nil {
		t.Fatal("parked a closed bead")
	}
}

func TestParkUntilForms(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for in, want := range map[string]time.Time{"2h": now.Add(2 * time.Hour), "3d": now.Add(72 * time.Hour), "2026-11-01T10:00:00Z": time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)} {
		got, err := ParseParkUntil(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseParkUntil("soon", now); err == nil {
		t.Error("accepted soon")
	}
}

func TestParkStaleLabelsClearedOnUnparkAndNotReused(t *testing.T) {
	ctx := context.Background()
	// An open bead left with stale labels by an Unpark that died half way.
	m := parkMem(Bead{ID: "w.1", Status: "open", Labels: []string{"parked", "parked-from:in_progress", "parked-until:5"}})
	if err := Park(ctx, m, "d", "w.1", "manager-w", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	b, _ := m.Show(ctx, "d", "w.1")
	if !slices.Contains(b.Labels, "parked-from:open") || slices.Contains(b.Labels, "parked-from:in_progress") || ParkedUntil(b.Labels) != 0 {
		t.Fatalf("stale label reused: %v", b.Labels)
	}
	if ok, err := Unpark(ctx, m, "d", "w.1", "manager-w"); err != nil || !ok {
		t.Fatalf("unpark: %v %v", ok, err)
	}
	b, _ = m.Show(ctx, "d", "w.1")
	if b.Status != "open" || len(b.Labels) != 0 {
		t.Fatalf("after unpark: %+v", b)
	}
}

func TestParkRejectsPastUntil(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	if _, err := ParseParkUntil(now.Add(-time.Hour).UTC().Format(time.RFC3339), now); err == nil || !strings.Contains(err.Error(), "past") {
		t.Fatalf("past accepted: %v", err)
	}
	if _, err := ParseParkUntil(now.Add(time.Hour).UTC().Format(time.RFC3339), now); err != nil {
		t.Fatal(err)
	}
}

func TestUnparkReadsALabelAnOlderBuildWroteWithTheWholeHeader(t *testing.T) {
	ctx := context.Background()
	for from, want := range map[string]string{"P2 · IN_PROGRESS": "in_progress", "P2 · OPEN": "open", "P1 · BLOCKED": "blocked", "P2 · CLOSED": "open", "garbage": "open"} {
		m := parkMem(Bead{ID: "w.1", Status: StatusDeferred, Labels: []string{ParkedLabel, ParkedFromPrefix + from}})
		if ok, err := Unpark(ctx, m, "d", "w.1", "cmdrd"); err != nil || !ok {
			t.Fatalf("%q: unpark: %v %v", from, ok, err)
		}
		b, _ := m.Show(ctx, "d", "w.1")
		if b.Status != want || len(b.Labels) != 0 {
			t.Errorf("%q: after unpark: %+v, want status %q and no labels", from, b, want)
		}
	}
}
