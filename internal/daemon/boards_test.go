package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/livedata"
)

func TestWithBoards_WritesEachRolesBoardAndStampsItsVersion(t *testing.T) {
	home := t.TempDir()
	snap := livedata.Snapshot{Display: &livedata.Display{Workstreams: []livedata.BoardRow{
		{Key: "p:a", N: 1, Title: "Alpha", ManagerRole: "manager-a", Kind: "stalled", Text: "Stalled"},
		{Key: "p:b", N: 2, Title: "Beta", ManagerRole: "manager-b", Kind: "working", Text: "Working"},
	}}}
	src := withBoards(home, func(context.Context) livedata.Snapshot { return snap }, nil)
	got := src(context.Background())
	if got.BoardVersions["commander"] != 1 || got.BoardVersions["manager-a"] != 1 {
		t.Fatalf("versions %v", got.BoardVersions)
	}
	f, err := boardturn.Read(home, "manager-a", time.Now())
	if err != nil || len(f.Entries) != 1 || f.Entries[0].Line() != "#1 Alpha: Stalled (100% done, no gates)" {
		t.Fatalf("manager-a board %+v %v", f, err)
	}
	all, _ := boardturn.Read(home, "commander", time.Now())
	if len(all.Entries) != 2 {
		t.Fatalf("commander board %+v", all)
	}

	// A degraded snapshot writes nothing, so the files go stale.
	_ = os.RemoveAll(filepath.Join(home, "board"))
	snap.Degraded = &livedata.SnapDegraded{Reason: "store down"}
	src(context.Background())
	if _, err := os.Stat(filepath.Join(home, "board")); !os.IsNotExist(err) {
		t.Fatalf("degraded snapshot wrote boards: %v", err)
	}
}
