package api

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

func TestDurableWriteAcknowledgementDoesNotWaitForProjection(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		once.Do(func() { close(started) })
		<-release
		close(finished)
		return livedata.Snapshot{}
	}, Write: func(context.Context, string, []string) ([]byte, []byte, error) { return []byte("committed"), nil, nil }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	returned := make(chan beads.DoorResponse, 1)
	go func() {
		_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "fixture-task"}, NoWait: true})
		returned <- out
	}()
	select {
	case out := <-returned:
		if out.Exit != 0 || out.Stdout != "committed" || out.Settled || !out.ProjectionPending || out.Applied != 1 {
			t.Fatalf("%+v", out)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("durable write waited for UI projection")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("projection refresh never started")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("projection did not finish after acknowledgement")
	}
}

func TestAsyncWriteWithoutProjectionIsAlreadySettled(t *testing.T) {
	s := New(Options{Token: testToken, Write: func(context.Context, string, []string) ([]byte, []byte, error) { return []byte("ok"), nil, nil }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "fixture-task"}, NoWait: true})
	if out.Exit != 0 || !out.Settled || out.ProjectionPending {
		t.Fatalf("%+v", out)
	}
}
