package livedata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

// A presentation callback can outlive the watch's three-second read budget.
// State remains readable both during that presentation and after it finishes.
func TestPostgresWatchReadsDuringSnapshotPresentation(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/watch-state-isolation.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		HoldMS int `json:"hold_ms"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil || scenario.HoldMS <= 3000 {
		t.Fatalf("invalid scenario: %s", raw)
	}
	st, _ := pgtest.Open(t)
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "watch-fixture", Path: t.TempDir(), Type: "knowledge", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	src := &Source{Deps: Deps{Store: st}, OnSnapshot: func(*Snapshot) { close(entered); <-release }}
	snapshotResult := make(chan Snapshot, 1)
	go func() { defer close(done); snapshotResult <- src.Snapshot(context.Background()) }()
	<-entered
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); <-done }()
	timer := time.AfterFunc(time.Duration(scenario.HoldMS)*time.Millisecond, unblock)
	defer timer.Stop()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	src.last.finished(errors.New("a different presentation load failed"))
	snap := src.WatchSnapshot(ctx)
	if snap.Degraded != nil {
		t.Fatalf("readable database reported unavailable during presentation: %+v", snap.Degraded)
	}
	if len(snap.Projects) != 1 {
		t.Fatalf("missing current project: %+v", snap)
	}
	if time.Since(start) >= 3*time.Second {
		t.Fatal("watch waited for snapshot presentation")
	}
	unblock()
	<-done
	if snapshot := <-snapshotResult; snapshot.Degraded != nil {
		t.Fatalf("API snapshot degraded: %+v", snapshot.Degraded)
	}
	if snap := src.WatchSnapshot(context.Background()); snap.Degraded != nil {
		t.Fatalf("unavailable after presentation: %+v", snap.Degraded)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if snap := src.WatchSnapshot(context.Background()); snap.Degraded == nil {
		t.Fatal("closed database reported healthy")
	}
}
