package ceilings

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
)

// fakeRun returns an AgentRun fixture with code-type fields populated. The
// returned run is suitable for direct Register without needing a real DB.
func fakeRun(id int64) store.AgentRun {
	return store.AgentRun{
		ID:                id,
		ProjectID:         42,
		TaskBeadID:        "cmdr-task-1",
		AgentName:         "operator-feat-x",
		OpencodeSessionID: "ses_xyz",
		ContainerID:       sql.NullString{String: "container-abc", Valid: true},
		SpawnTime:         time.Now().Unix(),
		Status:            "running",
	}
}

// newTestWatchdog wires a Watchdog around fakes + injectable Limits. Returns
// the watchdog plus the fake source for test scripting.
func newTestWatchdog(t *testing.T, fs *fakeStore, limits Limits) (*Watchdog, *fakeSource) {
	t.Helper()
	src := newFakeSource()
	w := NewWatchdog(fs, &docker.MockClient{}, &opencode.MockClient{}, &beads.MockClient{}, &agentmail.MockClient{}, limits)
	w.SetEventSource(src)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(w.Stop)
	return w, src
}

// waitFor polls until cond returns true or the deadline elapses. Tests use
// this to await goroutine-side effects without sleeping arbitrary amounts.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor: %s", msg)
}

const stepFinishLine = `{"type":"step_finish","timestamp":1,"sessionID":"ses_xyz","part":{"cost":0.10,"tokens":{"total":1500,"input":1000,"output":500}}}`

func TestConsumer_AccumulatesTokensAndUSD(t *testing.T) {
	fs := newFakeStore()
	w, src := newTestWatchdog(t, fs, DefaultLimits())

	r := fakeRun(1)
	src.installReader(r.ID, newFakeLineReader([]string{
		stepFinishLine,
		stepFinishLine,
	}, true))

	w.Register(r, "myproj", "/abs/myproj")

	waitFor(t, func() bool {
		return fs.callsCount("UpdateUsage") >= 2
	}, "UpdateUsage called twice")

	tokens, usd, ok := w.RunUsage(r.ID)
	if !ok {
		t.Fatalf("expected run still registered after 2 step_finish events")
	}
	if tokens != 3000 {
		t.Fatalf("tokens: want 3000 got %d", tokens)
	}
	if usd < 0.199 || usd > 0.201 {
		t.Fatalf("usd: want ~0.20 got %f", usd)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.tokens[r.ID] != 3000 || fs.usd[r.ID] < 0.199 {
		t.Fatalf("store snapshot wrong: tokens=%d usd=%f", fs.tokens[r.ID], fs.usd[r.ID])
	}
}

func TestConsumer_TolerateMalformedJSON(t *testing.T) {
	fs := newFakeStore()
	w, src := newTestWatchdog(t, fs, DefaultLimits())

	r := fakeRun(2)
	src.installReader(r.ID, newFakeLineReader([]string{
		`{not json`,
		`{"type":"step_start"}`,
		stepFinishLine,
	}, true))

	w.Register(r, "p", "/p")

	waitFor(t, func() bool {
		return fs.callsCount("UpdateUsage") >= 1
	}, "step_finish handled despite malformed neighbours")

	if _, _, ok := w.RunUsage(r.ID); !ok {
		t.Fatalf("operator should not be killed by malformed JSON")
	}
}

func TestConsumer_EOFMarksExited(t *testing.T) {
	fs := newFakeStore()
	w, src := newTestWatchdog(t, fs, DefaultLimits())

	r := fakeRun(3)
	// blockAfter=false → after the line, Scan returns false (EOF).
	src.installReader(r.ID, newFakeLineReader([]string{stepFinishLine}, false))

	w.Register(r, "p", "/p")

	waitFor(t, func() bool {
		return fs.callsContains("MarkClosed:exited")
	}, "MarkClosed(exited) called on stream EOF")

	if _, _, ok := w.RunUsage(r.ID); ok {
		t.Fatalf("expected unregister after exited")
	}
}

func TestConsumer_ErrorEventIncrementsFailures(t *testing.T) {
	fs := newFakeStore()
	limits := DefaultLimits()
	limits.MaxRetries = 99 // don't trip retry kill
	w, src := newTestWatchdog(t, fs, limits)

	r := fakeRun(4)
	src.installReader(r.ID, newFakeLineReader([]string{
		`{"type":"error"}`,
		`{"type":"error"}`,
	}, true))

	w.Register(r, "p", "/p")

	waitFor(t, func() bool {
		return fs.callsCount("IncrementFailures") >= 2
	}, "IncrementFailures called twice")
}

func TestConsumer_StepFinishResetsFailures(t *testing.T) {
	fs := newFakeStore()
	limits := DefaultLimits()
	limits.MaxRetries = 99
	w, src := newTestWatchdog(t, fs, limits)

	r := fakeRun(5)
	src.installReader(r.ID, newFakeLineReader([]string{
		`{"type":"error"}`,
		`{"type":"error"}`,
		stepFinishLine,
	}, true))

	w.Register(r, "p", "/p")

	waitFor(t, func() bool {
		return fs.callsCount("UpdateUsage") >= 1 && fs.callsCount("IncrementFailures") >= 2
	}, "saw both errors and a step_finish")

	// After the success, consecutive failures should reset to 0 in runState.
	v, _ := w.runs.Load(r.ID)
	st := v.(*runState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.consecutiveFailures != 0 {
		t.Fatalf("step_finish should reset failures to 0; got %d", st.consecutiveFailures)
	}
}
