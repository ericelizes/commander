package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/diskguard"
	"github.com/example-user/commander/internal/store"
)

type fakeDiskProjects []store.Project

func (f fakeDiskProjects) ListProjects(context.Context) ([]store.Project, error) { return f, nil }

type fakeDiskRuns struct {
	runs []store.AgentRun
	err  error
}

func (f fakeDiskRuns) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	return f.runs, f.err
}

// fakeDisks makes every path under a known prefix read as the given usage.
func fakeDisks(t *testing.T, usage map[string]*diskguard.Usage) {
	t.Helper()
	orig := diskguard.Stat
	t.Cleanup(func() { diskguard.Stat = orig })
	diskguard.Stat = func(p string) (diskguard.Usage, error) {
		for prefix, u := range usage {
			if strings.HasPrefix(p, prefix) {
				out := *u
				out.Path = p
				return out, nil
			}
		}
		return diskguard.Usage{}, errors.New("no such disk")
	}
}

func TestDiskWatcherWarnsRefusesAndClears(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	var alerts []string
	h.alert = func(key, detail string, _ time.Time) error { alerts = append(alerts, key+": "+detail); return nil }
	root := &diskguard.Usage{Mount: "/", Dev: 1, UsedPct: 50, Free: 40 << 30}
	data := &diskguard.Usage{Mount: "/data", Dev: 2, UsedPct: 50, Free: 40 << 30}
	fakeDisks(t, map[string]*diskguard.Usage{"/srv/example": root, "/data": data})
	w := &diskWatcher{home: "/srv/example/.commander", health: h,
		projects: fakeDiskProjects{{Name: "lab", Path: "/data/lab", RepoPath: "/srv/example/code/lab"}}}

	w.check(context.Background())
	now = now.Add(healthGrace + time.Second)
	w.check(context.Background())
	if got := h.list(); len(got) != 0 {
		t.Fatalf("half-full disks raised %v", got)
	}

	data.UsedPct = 85
	w.check(context.Background())
	now = now.Add(healthGrace + time.Second)
	w.check(context.Background())
	h.alertDue()
	got := h.list()
	if len(got) != 1 || got[0].Key != "disk:/data" || !strings.Contains(got[0].Detail, "disk /data is 85% full") || len(alerts) != 1 {
		t.Fatalf("85%% disk: problems %v, alerts %v", got, alerts)
	}

	data.UsedPct = 96
	w.check(context.Background())
	now = now.Add(healthGrace + time.Second)
	w.check(context.Background())
	h.alertDue()
	got = h.list()
	if len(got) != 1 || got[0].Key != "disk-full:/data" || !strings.Contains(got[0].Detail, "new Operators are refused") || len(alerts) != 2 {
		t.Fatalf("96%% disk: problems %v, alerts %v", got, alerts)
	}

	data.UsedPct = 40
	w.check(context.Background())
	if got := h.list(); len(got) != 0 {
		t.Fatalf("a disk back under the line still listed: %v", got)
	}
}

func TestDiskWatcherSweepsOnlyLeftoversOfItsOwnRoot(t *testing.T) {
	home := t.TempDir()
	root := diskguard.TmpRoot(home)
	now := time.Now()
	old := now.Add(-2 * tmpLeftoverAge)
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, old, old)
		return p
	}
	live := mk(diskguard.RunTmpName("op-lab-live"))
	gone := mk(diskguard.RunTmpName("op-lab-gone"))
	runs := fakeDiskRuns{runs: []store.AgentRun{{AgentName: "operator-op-lab-live", TmuxPaneID: sql.NullString{String: "op-lab-live", Valid: true}}}}

	// An unreadable run list touches nothing.
	(&diskWatcher{home: home, runs: fakeDiskRuns{err: errors.New("db locked")}, now: func() time.Time { return now }}).sweepTmp(context.Background())
	if _, err := os.Stat(gone); err != nil {
		t.Fatalf("swept with no run list: %v", err)
	}

	(&diskWatcher{home: home, runs: runs, now: func() time.Time { return now }}).sweepTmp(context.Background())
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("an ended run's old scratch kept: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("a running run's scratch removed: %v", err)
	}
}
