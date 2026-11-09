package ceilings

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
)

// newKillHarness builds a watchdog wired with mock clients we can introspect
// directly (NOT via the EventSource path — kill is exercised by calling
// w.kill on a synthesized runState).
func newKillHarness(t *testing.T) (*Watchdog, *fakeStore, *docker.MockClient, *beads.MockClient, *agentmail.MockClient) {
	t.Helper()
	fs := newFakeStore()
	dm := &docker.MockClient{}
	bm := &beads.MockClient{}
	am := &agentmail.MockClient{}
	w := NewWatchdog(fs, dm, &opencode.MockClient{}, bm, am, DefaultLimits())
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(w.Stop)
	return w, fs, dm, bm, am
}

func TestKill_FullChain_CodeRun(t *testing.T) {
	w, fs, dm, bm, am := newKillHarness(t)

	st := &runState{
		runID:       7,
		containerID: "c-7",
		projectID:   42,
		projectName: "myproj",
		projectPath: "/abs/myproj",
		taskBeadID:  "cmdr-task-7",
		agentName:   "operator-feat",
		spawnTime:   time.Now(),
		tokensUsed:  150_000,
		usdUsed:     4.20,
	}
	w.runs.Store(st.runID, st)

	w.kill(context.Background(), st, "usd ceiling: $5.01 >= $5.00")

	// docker.Stop called once.
	stopCalls := 0
	for _, c := range dm.Calls {
		if strings.HasPrefix(c, "Stop(") {
			stopCalls++
		}
	}
	if stopCalls != 1 {
		t.Fatalf("docker.Stop calls: want 1 got %d (%v)", stopCalls, dm.Calls)
	}

	// am.Send to lead-myproj.
	if len(am.Calls) != 1 {
		t.Fatalf("am.Send calls: want 1 got %d (%v)", len(am.Calls), am.Calls)
	}
	if !strings.Contains(am.Calls[0], "->lead-myproj/") {
		t.Fatalf("am to addressee wrong: %s", am.Calls[0])
	}
	if !strings.Contains(am.Calls[0], "imp=urgent") {
		t.Fatalf("am importance wrong: %s", am.Calls[0])
	}

	// bd set-state cmdr=blocked.
	if len(bm.Calls) != 1 {
		t.Fatalf("bd set-state calls: want 1 got %d (%v)", len(bm.Calls), bm.Calls)
	}
	if !strings.Contains(bm.Calls[0], "cmdr=blocked") {
		t.Fatalf("bd dimension wrong: %s", bm.Calls[0])
	}

	// MarkClosed killed-ceiling.
	if !fs.callsContains("MarkClosed:killed-ceiling") {
		t.Fatalf("expected MarkClosed:killed-ceiling, got %v", fs.calls)
	}

	// cost_event ceiling-hit row.
	if !fs.callsContains("InsertCostEvent:ceiling-hit") {
		t.Fatalf("expected InsertCostEvent:ceiling-hit, got %v", fs.calls)
	}
	if len(fs.costEvents) != 1 {
		t.Fatalf("cost_events count: want 1 got %d", len(fs.costEvents))
	}
	ev := fs.costEvents[0]
	if ev.AgentName != "operator-feat" {
		t.Fatalf("cost_event agent: %q", ev.AgentName)
	}
	if ev.DeltaUSD < 4.19 || ev.DeltaUSD > 4.21 {
		t.Fatalf("cost_event delta_usd: %f", ev.DeltaUSD)
	}
	if ev.DeltaTokens != 150_000 {
		t.Fatalf("cost_event delta_tokens: %d", ev.DeltaTokens)
	}
	if !ev.Reason.Valid || !strings.Contains(ev.Reason.String, "usd ceiling") {
		t.Fatalf("cost_event reason: %+v", ev.Reason)
	}

	// Run is unregistered.
	if _, _, ok := w.RunUsage(st.runID); ok {
		t.Fatalf("expected run unregistered after kill")
	}
}

func TestKill_Idempotent(t *testing.T) {
	w, fs, dm, _, am := newKillHarness(t)

	st := &runState{
		runID:       8,
		containerID: "c-8",
		projectID:   1,
		projectName: "p",
		projectPath: "/p",
		taskBeadID:  "bd-1",
		agentName:   "operator-x",
		spawnTime:   time.Now(),
	}
	w.runs.Store(st.runID, st)

	// Two concurrent kills → only one chain runs.
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			w.kill(context.Background(), st, "first reason")
			done <- struct{}{}
		}()
	}
	<-done
	<-done

	stopCalls := 0
	for _, c := range dm.Calls {
		if strings.HasPrefix(c, "Stop(") {
			stopCalls++
		}
	}
	if stopCalls != 1 {
		t.Fatalf("docker.Stop should be called exactly once; got %d", stopCalls)
	}
	if len(am.Calls) != 1 {
		t.Fatalf("am.Send should be called exactly once; got %d", len(am.Calls))
	}
	if fs.callsCount("MarkClosed:killed-ceiling") != 1 {
		t.Fatalf("MarkClosed should be called exactly once; got %d", fs.callsCount("MarkClosed:killed-ceiling"))
	}
	if fs.callsCount("InsertCostEvent:ceiling-hit") != 1 {
		t.Fatalf("InsertCostEvent should be called exactly once")
	}
}

func TestKill_KnowledgeRunSkipsDockerStop(t *testing.T) {
	w, _, dm, _, _ := newKillHarness(t)

	st := &runState{
		runID:       9,
		isKnowledge: true,
		// Empty tmuxPaneID — kill should log + skip without calling tmux.
		projectID:   1,
		projectName: "p",
		projectPath: "/p",
		taskBeadID:  "bd-9",
		agentName:   "operator-knowledge",
		spawnTime:   time.Now(),
	}
	w.runs.Store(st.runID, st)
	w.kill(context.Background(), st, "test")

	for _, c := range dm.Calls {
		if strings.HasPrefix(c, "Stop(") {
			t.Fatalf("knowledge kill must NOT call docker.Stop; got %q", c)
		}
	}
}
