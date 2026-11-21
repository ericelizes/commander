package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

type fakeReported struct{ all []store.ReportedRun }

func (f fakeReported) ReportedRuns(context.Context) ([]store.ReportedRun, error) { return f.all, nil }

func TestReportedRunIsEndedAfterTheGraceOnly(t *testing.T) {
	f := fakeReported{all: []store.ReportedRun{{ProjectID: 1, BeadID: "b", RunID: 3, Agent: "operator-x", ReportedAt: 1000}}}
	var stopped []string
	clock := time.Unix(1000, 0).Add(ReportGrace - time.Second)
	c := executionReported{store: f, now: func() time.Time { return clock },
		stop: func(_ context.Context, role string) error { stopped = append(stopped, role); return nil }}
	c.pass(context.Background())
	if len(stopped) != 0 {
		t.Fatalf("ended inside the grace: %v", stopped)
	}
	clock = clock.Add(time.Second)
	c.pass(context.Background())
	if len(stopped) != 1 || stopped[0] != "operator-x" {
		t.Fatalf("stopped: %v", stopped)
	}
}
