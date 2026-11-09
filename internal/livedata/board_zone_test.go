package livedata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/example-user/commander/internal/config"
)

// zoneSnap has one row waiting until `at` and one parked step until `parked`.
func zoneSnap(at, parked int64) Snapshot {
	return Snapshot{Workstreams: []SnapWorkstream{
		{Project: "p", BeadID: "w", Title: "Wait", ManagerRole: "manager-w", Steps: []SnapStep{
			{BeadID: "w.1", Title: "Reconcile", Status: "open", State: "not-started", Waiting: "reconcile", WaitUntil: at, WaitUntilAs: "reconcile"},
		}},
		{Project: "p", BeadID: "v", Title: "Park", ManagerRole: "manager-v", Steps: []SnapStep{
			{BeadID: "v.1", Title: "Later", Status: "deferred", State: "not-started", ParkedUntil: parked},
		}},
	}}
}

func TestBoardZoneFormatsWaitTimes(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	defer SetBoardZone(nil)
	at := time.Date(2026, 10, 28, 23, 4, 0, 0, time.UTC).Unix() // 7:04 PM in New York
	for _, c := range []struct {
		loc  *time.Location
		want string
		now  int64
	}{
		{ny, "Waiting for reconcile · 07:04 PM", at - 25*60},
		{ny, "Waiting for reconcile · Wed 07:04 PM", at - 86400},
		{time.UTC, "Waiting for reconcile · 11:04 PM", at - 25*60},
	} {
		SetBoardZone(c.loc)
		d := Board(zoneSnap(at, at), c.now)
		var all []string
		all = append(all, BoardLines(d, c.now, "commander")...)
		for _, e := range BoardEntries(d, c.now, "commander") {
			all = append(all, e.Text)
		}
		for _, r := range Rows(Snapshot{Display: &d}, c.now) {
			all = append(all, r.Line.Text)
		}
		for _, w := range d.Workstreams {
			if w.Workflow != nil {
				all = append(all, WorkflowLines(w.Workflow)...)
			}
		}
		joined := strings.Join(all, "\n")
		if !strings.Contains(joined, c.want) {
			t.Errorf("%s now=%d: no %q in\n%s", c.loc, c.now, c.want, joined)
		}
		if strings.Contains(joined, "@1") || !strings.Contains(joined, "Parked until") {
			t.Errorf("%s: raw mark or missing parked line in\n%s", c.loc, joined)
		}
	}
}

// 2026-11-01 is the US fall-back day: 01:30 happens twice and the day is 25 hours.
func TestBoardZoneDSTBoundary(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	d := func(h, m int) int64 { return time.Date(2026, 11, 1, h, m, 0, 0, ny).Unix() }
	if got := clock(d(23, 30), d(0, 10), ny); got != "11:30 PM" {
		t.Fatalf("same day across the repeated hour: %q", got)
	}
	// 3 AM EST is 8:00 UTC, 25h after midnight EDT: still the same New York day.
	if got := clock(time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Unix(), d(0, 10), ny); got != "03:00 AM" {
		t.Fatalf("after the shift: %q", got)
	}
	if got := clock(d(0, 10), d(0, 10)-86400, ny); got != "Sun 12:10 AM" {
		t.Fatalf("day before: %q", got)
	}
}

func TestLoadBoardZone(t *testing.T) {
	defer SetBoardZone(nil)
	home := t.TempDir()
	write := func(body string) { _ = os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644) }
	write("[user]\ntimezone = \"America/New_York\"\n")
	LoadBoardZone(home)
	if BoardZone().String() != "America/New_York" {
		t.Fatalf("zone %v", BoardZone())
	}
	write("[user]\ntimezone = \"Mars/Olympus\"\n")
	LoadBoardZone(home)
	if BoardZone() != time.Local {
		t.Fatalf("invalid name must fall back, got %v", BoardZone())
	}
	if _, err := (config.User{Timezone: "Mars/Olympus"}).Location(); err == nil {
		t.Fatal("want an error for an invalid zone")
	}
}
