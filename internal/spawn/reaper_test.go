package spawn

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/store"
)

func TestReaper_StopsOrphans(t *testing.T) {
	fs := newFakeStore()
	// Pre-seed one tracked container; the other will be an orphan.
	if _, err := fs.InsertAgentRun(context.Background(), store.AgentRun{
		ProjectID:         1,
		TaskBeadID:        "bd-1",
		AgentName:         "operator-a",
		OpencodeSessionID: "ses_a",
		ContainerID:       sql.NullString{String: "tracked-1", Valid: true},
		SpawnTime:         time.Now().Unix(),
		Status:            "running",
	}); err != nil {
		t.Fatal(err)
	}

	dk := &docker.MockClient{
		ListResponse: []docker.ContainerInfo{
			{ID: "tracked-1", Status: "running", Labels: map[string]string{"commander.project": "p1"}},
			{ID: "orphan-1", Status: "running", Labels: map[string]string{"commander.project": "p1"}},
		},
	}
	r := NewReaper(fs, dk)
	r.now = func() time.Time { return time.Unix(1700000000, 0) }

	killed, err := r.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(killed) != 1 || killed[0] != "orphan-1" {
		t.Errorf("killed: got %v want [orphan-1]", killed)
	}

	// docker.Stop should have been called exactly for the orphan.
	stopCalls := 0
	for _, c := range dk.Calls {
		if c == "Stop(id=orphan-1)" {
			stopCalls++
		}
		if c == "Stop(id=tracked-1)" {
			t.Errorf("tracked container should not be stopped: %v", dk.Calls)
		}
	}
	if stopCalls != 1 {
		t.Errorf("expected 1 Stop call for orphan, got %d (%v)", stopCalls, dk.Calls)
	}

	// cost_event recorded.
	if len(fs.costEvents) != 1 {
		t.Fatalf("cost_events: got %d want 1", len(fs.costEvents))
	}
	if fs.costEvents[0].EventType != "orphan-reaped" {
		t.Errorf("event_type: got %q want orphan-reaped", fs.costEvents[0].EventType)
	}
}

func TestReaper_NoContainers(t *testing.T) {
	fs := newFakeStore()
	dk := &docker.MockClient{ListResponse: nil}
	r := NewReaper(fs, dk)

	killed, err := r.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(killed) != 0 {
		t.Errorf("killed: got %v want []", killed)
	}
	for _, c := range dk.Calls {
		if c[:4] == "Stop" {
			t.Errorf("Stop should not have been called: %v", dk.Calls)
		}
	}
}

func TestReaper_ListError(t *testing.T) {
	fs := newFakeStore()
	dk := &docker.MockClient{ListErr: errors.New("docker daemon down")}
	r := NewReaper(fs, dk)
	if _, err := r.Reap(context.Background()); err == nil {
		t.Error("expected error from list failure")
	}
}

func TestReaper_NonRunningRowTreatedAsOrphan(t *testing.T) {
	// A container exists with a corresponding row, but the row is not "running"
	// (e.g. a prior MarkClosed call). Reaper should kill the container.
	fs := newFakeStore()
	if _, err := fs.InsertAgentRun(context.Background(), store.AgentRun{
		ProjectID:         1,
		TaskBeadID:        "bd-1",
		AgentName:         "operator-a",
		OpencodeSessionID: "ses_a",
		ContainerID:       sql.NullString{String: "stale-1", Valid: true},
		SpawnTime:         time.Now().Unix(),
		Status:            "killed-ceiling",
	}); err != nil {
		t.Fatal(err)
	}

	dk := &docker.MockClient{
		ListResponse: []docker.ContainerInfo{
			{ID: "stale-1", Labels: map[string]string{"commander.project": "p1"}},
		},
	}
	r := NewReaper(fs, dk)

	killed, err := r.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(killed) != 1 || killed[0] != "stale-1" {
		t.Errorf("killed: got %v want [stale-1]", killed)
	}
}
